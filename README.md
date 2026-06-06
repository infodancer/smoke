# smoke / smolder

Black-box, pure-HTTP route smoke testing: verify that every URL a server
serves actually serves, against a real running instance. A supplement to unit
tests that catches the wiring / dependency-injection / migration / config
regressions that fakes miss — the class of bug where every unit test is green
but the deployed handler 500s.

- **`smoke`** (this package, imported by the host) — a router-agnostic registry
  of route specs and the probe runner. The host registers its routes through
  the registry (a drop-in `*http.ServeMux` wrap, or a manual registry) and
  exposes the resulting manifest.
- **`smolder`** (`cmd/smolder`) — the CLI that consumes a manifest and applies
  the heat: probes a base URL (`smolder run`) and gates route coverage
  (`smolder gate`).

See [DESIGN.md](DESIGN.md) for the full rationale and model.

## Quick shape

```go
mux := smoke.NewMux()                                  // wraps *http.ServeMux, records a spec per route
mux.HandleFunc("GET /widgets/{id}", h, smoke.Example("id", "42"))
mux.HandleFunc("POST /widgets", create, smoke.Write()) // mutating; deferred / skipped by default
// ... serve via mux; expose mux.Registry().Manifest() as JSON for smolder
```

```
smolder run  --base https://preview.example.com [--target preview|live] [--include-writes --cookie 'sess=…']
smolder gate --manifest routes.json [--mode warn|fail]
```

- Routes carry an `Effect` (ReadOnly / Mutating). The runner is reads-only by
  default; `live` runs only ReadOnly routes; writes run only with
  `--include-writes` (and a `--cookie` credential for auth-gated ones).
- The default expectation is "200–399, never 5xx"; set an exact status per route
  when needed. Incomplete (un-exampled) and skipped routes are never failed by
  the runner — coverage is the gate's job.

## Status

Extracted from [old-school-gamers/org](https://github.com/old-school-gamers/org)
(PRs #299 / #300), where it was incubated against a real ~160-route surface.
The package is host-agnostic; host-specific pieces (fixture seeding, auth-cookie
acquisition, CI wiring) live in the consuming repo.

Private during initial review; pure standard library, no dependencies.
