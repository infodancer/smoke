# smoke / smolder

Black-box, pure-HTTP route smoke testing for Go web servers: verify that every
URL your server *says* it serves actually serves, by probing a real running
instance. A supplement to unit tests that catches the regressions fakes miss —
wiring, dependency injection, migrations, config, reverse-proxy — the class of
bug where every unit test is green but the deployed handler returns a 500.

Two pieces, one module, **no third-party dependencies** (standard library only):

- **`smoke`** — imported by your server. A router-agnostic registry of route
  specs that records every route as it's registered (a drop-in `*http.ServeMux`
  wrapper, or a manual registry) and emits a JSON manifest.
- **`smolder`** (`cmd/smolder`) — the CLI. Probes a base URL (`smolder run`)
  and gates coverage (`smolder gate`) from that manifest.

See [DESIGN.md](DESIGN.md) for the full rationale and data model.

## Install

```sh
go get github.com/infodancer/smoke          # library
go install github.com/infodancer/smoke/cmd/smolder@latest   # CLI
```

Or vendor it and run the CLI via a Go 1.24 tool directive (no separate install):

```sh
go get -tool github.com/infodancer/smoke/cmd/smolder
go tool smolder gate --manifest routes.json --mode fail
```

## Register routes

Wrap your mux; every registration records a spec. Add an `Example` for path
params, mark writes, and the rest is inferred:

```go
mux := smoke.NewMux() // wraps *http.ServeMux, records a spec per route

mux.HandleFunc("GET /health", health)                                  // covered: no params
mux.HandleFunc("GET /widgets/{id}", show, smoke.Example("id", "42"))   // covered: example param
mux.HandleFunc("GET /account", dashboard, smoke.AuthRequired())        // probed only with a session
mux.HandleFunc("POST /widgets", create, smoke.Write())                 // mutating; skipped by default
mux.HandleFunc("GET /admin/", adminUI, smoke.Status(403))              // asserts an exact status

http.ListenAndServe(":8080", mux) // *smoke.Mux is an http.Handler

// Expose the manifest for smolder (gate it to non-prod — it enumerates routes):
http.HandleFunc("GET /_smoke/manifest", func(w http.ResponseWriter, r *http.Request) {
    json, _ := mux.Registry().Manifest().MarshalJSON()
    w.Header().Set("Content-Type", "application/json")
    w.Write(json)
})
```

`smoke.New()` gives a bare registry if you don't use `http.ServeMux` — call
`reg.Add(method, path, opts...)` from wherever your router registers, and serve
`reg.Manifest()`.

## Probe and gate

```sh
# Probe a running server. Default expectation: 2xx/3xx (never a 5xx).
smolder run --base https://preview.example.com

# Run only ReadOnly routes against production (no mutations).
smolder run --base https://example.com --target live

# Include writes, authenticated (acquire the cookie out of band).
smolder run --base https://preview.example.com --include-writes --cookie 'session=…'

# Coverage gate: every route needs an example, a skip, or a write/auth marker.
smolder gate --manifest routes.json --mode fail   # warn | fail
```

## Model

- **Effect** — `ReadOnly` (default for GET/HEAD) or `Mutating`. The runner is
  reads-only by default; `--target live` runs only ReadOnly routes; Mutating
  routes run only with `--include-writes`.
- **Expectation** — default is `2xx/3xx`. A 4xx on a route you deliberately
  probe is *not* a pass (it means you didn't reach what you meant to test), so
  the cases are explicit: `Status(code)` to assert an exact code, `AuthRequired`
  for reads probed only with a session, `Skip(reason)` for routes that aren't
  GET-probeable. Only a 5xx is the default failure class.
- **Coverage gate** — a route is "covered" when it has the examples it needs
  (or a skip). New routes fail `gate --mode fail` until covered, so adding a
  route forces a smoke spec.
- **Manifest** — the JSON contract between the two halves: the union of every
  route and its completeness, stable-sorted so a committed copy diffs cleanly.
- **Labels** — `Label(key, value)` records arbitrary tags on a route, carried
  through the manifest untouched. smoke never interprets them; they let another
  tool ride the same route manifest instead of building its own recorder (e.g. a
  sitemap coverage gate reading the `sitemap` key). Labels don't affect coverage.

## License

Apache 2.0 — see [LICENSE](LICENSE).
