# smoke / smolder — black-box route smoke testing

Status: **approved design, not yet implemented** (2026-06-05)
Owner: Matthew Hunter

This document records the design agreed for a black-box HTTP smoke-testing
system, so the decisions survive the gap before implementation. It is the
source of truth for the build; update it when the design changes.

---

## 1. Motivation

OSG has extensive unit tests, but they run against fakes and in-process wiring.
They do **not** catch regressions in the seam between a working handler and a
working *deployment*: dependency wiring, DI, migrations, reverse-proxy config,
the `FROM scratch` image missing a file, timezone data, etc.

The triggering example: the treasure generator returned **502 in production**
while every unit test was green. The store's own tests passed because they
built a pool-backed store; the bug was that `routes()` built the generator's
store **without** the pgx pool, so the first query nil-panicked at request time
(an un-recovered handler panic → closed connection → Cloudflare 502). Nothing
exercised the *wired* route end-to-end against a real server + database.

We want a **black-box, pure-HTTP** check that every URL the site serves
actually serves — run against the live per-PR preview server (and, in a
read-only mode, against production). It is a **supplement** to the unit tests,
not a replacement, and explicitly a black-box test: no internal imports, just
HTTP against a running server.

## 2. Goals / non-goals

**Goals**
- Verify every served route returns its expected status class (never 5xx)
  against a real running server — the faithful round trip is the point.
- Make the route list **self-maintaining**: derived from the router, never a
  hand-kept list that rots.
- **Gate** new routes: shipping a handler without smoke coverage is itself a
  failure (configurable warn vs hard-fail).
- Run against the **live site read-only** with no mutations — not
  create-then-delete, simply never touching mutating routes.
- Be a **reusable** library, not OSG-specific — other infodancer/web modules
  should be able to register their own specs.

**Non-goals (for now)**
- Full correctness testing. "Serves" is the floor; richer per-route assertions
  (body contains, JSONPath, schema) are an additive extension, not v1.
- Authenticated journeys / driving the real OIDC flow (see §9).
- Load testing (that is k6's job, a different axis).
- Replacing unit tests.

## 3. The two pieces

The system splits into a **producer** and a **consumer** of one artifact (the
manifest). They live in **one repo, two packages**, so the manifest schema
cannot skew between them.

- **`smoke`** (imported) — builds and owns the list of things to test: the
  spec registry, `RouteSpec`, and router integration. Produces the manifest.
- **`smolder`** (run, `cmd/smolder`) — consumes a manifest and applies the
  heat: probes a base URL, runs the offline gate, reports. The name is
  deliberate: *smolder* is the sustained low application of heat — the constant
  CI cadence (every PR) and eventually a steady run against live — where we
  watch for smoke.

Repo: **`github.com/infodancer/smoke`** (end state). See §11 for the
incubate-in-OSG-then-extract plan and the naming rationale.

## 4. Route model — specs, not a URL list

A flat URL list cannot express param substitution, expected status, side-effect
class, or future body assertions, and would be outgrown immediately. The unit
of the system is a **`RouteSpec`**, roughly:

```
RouteSpec {
    Method        string              // GET, POST, ...
    Pattern       string              // "/bestiary/{slug}/{$}"
    Effect        Effect              // ReadOnly | Mutating (default by method)
    ExampleParams map[string]string   // {"slug": "goblin"} from known seed data
    ExpectStatus  int                 // 0 = default (see below)
    Skip          string              // non-empty = explicitly exempted, with reason
    Complete      bool                // false = route recorded but no spec → gate target
    // Optional, additive later: Assertions []Assertion (body contains, JSONPath...)
}
```

**Default expected status:** unset (`ExpectStatus == 0`) means **anything but
5xx**. A black-box probe of a real route surface legitimately hits 4xx — auth
gates (401/403), method gates (405), missing optional params (400/404); all mean
the handler ran and responded. Only a 5xx is the failure class this catches (the
panic/wiring/DI regression, e.g. the treasure 502). Set `ExpectStatus` per route
where a specific code matters (e.g. a public page must be 200). (Revised from the
original "200/3xx only" after the first live run showed 4xx is normal and
pervasive for unauthenticated probes of auth/method-gated routes.)

**Effect class** decides what is safe against live:
- `ReadOnly` — safe against live production (default for GET/HEAD).
- `Mutating` — preview/test only (default for POST/PUT/PATCH/DELETE, or the
  rare side-effecting GET, which must override the default explicitly).

This is a *declared property of the route*, not a global flag the runner hopes
to honor, so the runner can mechanically filter the live-safe subset.

## 5. The manifest — the one contract

The manifest is the **union of actual routes and their spec-completeness
state**. Integration (§6) records *every* route; routes lacking a spec are
emitted with `Complete: false`. This single artifact drives everything:

- `smolder run` probes it.
- `smolder gate` reads it and flags `Complete: false` routes — **offline**, no
  server needed, because the spec-less routes are present in the manifest.

Two transports, mapped onto the two run targets (§8):

1. **`GET /_smoke/manifest`** — interrogate a running non-prod binary; emits the
   recorded specs as JSON. Authoritative for *that build*; cannot drift from
   what the server serves. **Gated to non-prod** by env/token — route
   enumeration is an information-disclosure surface and must never be exposed on
   production.
2. **Committed `routes.json`** — generated from the registry, committed to the
   repo. Drives live-prod runs (prod won't expose the endpoint) and gives the
   PR-diff signal ("3 routes added").

Kept honest like the existing sqlc gate: a CI step regenerates the manifest
(host-side `go generate`, since regeneration needs the in-process router) and
fails if it differs from the committed `routes.json`.

## 6. Router integration — agnostic core, pluggable depth

The spec registry is **pure data, router-independent**. Integration is a thin,
pluggable layer because *automatic drift-gating requires knowing the actual
route set*, and that capability is router-specific. Three depths:

1. **Wrap** — `smoke` intercepts registration; handler + spec recorded in one
   call. **Required** for the stdlib `http.ServeMux`, which exposes no
   introspection API (`net/http` has no `mux.Routes()`). **This is what OSG
   uses.**
2. **Harvest** — for routers that expose their tree (chi `Walk`/`Routes()`,
   gorilla/mux `Walk`, echo `Routes()`): read the route list after the fact and
   match it against separately-declared specs. This is the integration that
   does **not disturb existing routing** — a chi user changes none of their
   `r.Get(...)` calls, adds one harvest call plus spec declarations.
   **Deferred** — not built until the package is in its own repo and there is a
   real second consumer to prove its shape (see §11).
3. **Manual** — exotic/unknown routers: declare specs only. Gets the manifest
   and the runner, but no automatic drift detection (nothing can enumerate the
   real routes). Graceful, documented degradation.

Honest framing: **compatibility is universal; automatic drift-gating exists
wherever a wrap or harvest exists** (stdlib + chi/mux/echo cover nearly
everyone) and degrades to manifest+runner elsewhere.

**Mounted modules (search, faq, …):** when a module is converted to register
through `smoke`, its specs flow up to the host registry at its mount prefix
(`registry.Mount("/faq", …)`) — registration *is* the manifest, no drift. Until
a module is converted, the host declares a coarse subtree spec (`/faq/` → 200).
Module conversion happens after extraction (§11), not in phase 1.

## 7. The gate

Two checks, both reading the one manifest:

- **Spec-completeness / drift** — every recorded route is `Complete` (has an
  example-or-skip) or it fails. This is "you added a route without a test."
- **Spec validity** — example params resolve, expected status sane, etc.

Strictness is a flag, easy to flip, runnable in either mode:

- `smolder gate --mode warn` → prints the uncovered routes, exit 0.
- `smolder gate --mode fail` → exit non-zero.

Usage:
- **Pre-push hook in `warn`** — prints which URLs need smoke tests as a prompt
  for future work; never blocks the push. (The hook regenerates the manifest
  host-side first, then gates, so it sees freshly-added routes.)
- **GitHub CI in `fail`** — hard gate.

The gate is a `smolder` subcommand (not a `go test`) so strictness lives in one
flag and `go test ./...` stays about unit behavior.

## 8. Run targets and idempotency

- `smolder run --base <url> --target preview` (default) — run everything;
  mutations allowed because the per-PR DB is ephemeral and torn down after the
  PR, so accumulated rows are acceptable (no rollback/cleanup needed).
- `smolder run --base <url> --target live` — run **only** `ReadOnly` specs. No
  creates, no deletes, nothing to clean up — a filter, not a create/teardown
  dance. **Phase 1 is entirely GET → entirely ReadOnly → live-safe from day
  one.** Phase-2 mutations simply never enter the live set.

Live mode also implies **politeness**: low concurrency and a recognizable
User-Agent (e.g. `smolder/live`) so the traffic is filterable out of
analytics/Loki and won't trip rate limits. Live runs read the committed
`routes.json` (prod doesn't expose `/_smoke/manifest`).

## 9. Authentication (phase 2+, deferred)

Many routes are auth-gated (`/account/*`, `/campaign/{slug}/manage|edit|new`).
Phase 1 does **not** authenticate: unauthenticated, the *correct* response is a
302-to-login or 401, and asserting that class still proves the route is wired.
The treasure 502 fails this; a login redirect passes it.

Authenticated testing is the real phase-2 wall (not POST/PUT). Options — drive
the real OIDC flow against webauth (heavy, flaky) or a **preview-only**
session-minting backdoor (a security surface: env-gated, never in the prod
image). Deferred and to be decided deliberately.

## 10. Phasing

- **Phase 1** — GET / ReadOnly only. Expected-status-class assertions
  (200/3xx, never 5xx). The wrap integration for OSG's stdlib mux, the
  registry, the manifest (endpoint + committed JSON + drift check), the gate
  (warn default), `smolder run` + `smolder gate`, and CI wiring into
  pr-preview.yml after the preview is healthy.
- **Phase 2** — POST/PUT/PATCH with sample payloads and result assertions;
  richer body/JSONPath assertions; the authentication story.
- **Later** — extract to `infodancer/smoke`; harvest adapters; convert the
  web modules to register their own specs.

## 11. Repo placement, incubation, naming

**End state:** its own repo, `github.com/infodancer/smoke` — a small, focused,
dependency-light module consumed by OSG and by the infodancer/web modules. Not
folded into `infodancer/web`: this is testing infra *for* mountable features, a
different concern, and a consumer wanting only smoke should not pull all of
`web`.

**Incubate in-tree first.** While `RouteSpec`, options, effect classes, and the
manifest format are still molten, a published module would mean a cross-repo
dance on every change. So:

1. Build at `server/internal/smoke` (library) + `server/cmd/smolder` (CLI) in
   OSG, developed against OSG's real ~158-route surface — the only honest test
   of whether the API is right.
2. **Hard rule from line one:** the package imports nothing from OSG.
   Dependency points one way — OSG → smoke, never the reverse. Seed examples
   and wiring live in OSG and are passed in.
3. When the API settles, lift the package verbatim to
   `github.com/infodancer/smoke`, rewrite OSG's import path, then convert the
   web modules. Because the core is OSG-agnostic, extraction is `git mv` + an
   import-path rewrite, not a redesign. `internal/` is kept deliberately as a
   forcing function: nothing can depend on it until it is published on purpose.

**Naming.** `smoke` is the library you import to *declare* what to test; the
GitHub prefix (`infodancer/smoke`) resolves any ambiguity with the generic
"smoke test" concept, and `smoke.Router` / `smoke.RouteSpec` read well at the
call site. `smolder` is the CLI that *applies sustained low heat* — the
constant CI cadence and the eventual steady run against live — which is where
"smolder" fits better than "smoke."

## 12. Open questions

- Exact shape of the wrap API for stdlib (`smoke.Mux` delegating to
  `*http.ServeMux`, recording each `Handle`/`HandleFunc`), and the fluent
  options for specs.
- Where seed example values are sourced (constants in OSG wiring vs read from
  the seed loader) so they cannot drift from what boot actually seeds.
- Manifest generation entry point (`go generate` target vs a `smolder dump`
  subcommand that imports the host — the latter conflicts with smolder being
  host-agnostic, so likely a host-side `go generate`).
- Whether the drift CI check and the gate are one CI step or two.
