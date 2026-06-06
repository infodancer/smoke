package smoke_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/infodancer/smoke"
)

func TestParsePatternAndParams(t *testing.T) {
	reg := smoke.New()
	reg.AddPattern("GET /bestiary/{slug}/{$}", smoke.Example("slug", "goblin"))
	reg.AddPattern("/health")
	reg.AddPattern("POST /api/treasure/generate")

	specs := reg.Specs()
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3", len(specs))
	}

	// Method parsed out of the pattern; effect defaulted from method.
	bySlug := findSpec(t, specs, "/bestiary/{slug}/{$}")
	if bySlug.Method != "GET" {
		t.Errorf("method = %q, want GET", bySlug.Method)
	}
	if bySlug.Effect != smoke.ReadOnly {
		t.Errorf("GET effect = %v, want ReadOnly", bySlug.Effect)
	}
	if !bySlug.Complete() {
		t.Errorf("route with slug example should be complete")
	}

	health := findSpec(t, specs, "/health")
	if health.Method != "" {
		t.Errorf("methodless route parsed method %q", health.Method)
	}
	if !health.Complete() {
		t.Errorf("parameterless route should be complete")
	}

	gen := findSpec(t, specs, "/api/treasure/generate")
	if gen.Effect != smoke.Mutating {
		t.Errorf("POST effect = %v, want Mutating", gen.Effect)
	}
}

func TestIncompleteDetection(t *testing.T) {
	reg := smoke.New()
	reg.AddPattern("GET /campaign/{slug}/sessions/{id}", smoke.Example("slug", "x")) // missing id
	reg.AddPattern("GET /campaign/{slug}", smoke.Example("slug", "x"))               // complete
	reg.AddPattern("GET /admin/secret", smoke.Skip("admin-only"))                    // exempt

	inc := reg.Manifest().Incomplete()
	if len(inc) != 1 {
		t.Fatalf("got %d incomplete, want 1: %+v", len(inc), inc)
	}
	if inc[0].Pattern != "/campaign/{slug}/sessions/{id}" {
		t.Errorf("incomplete = %q", inc[0].Pattern)
	}
}

func TestExpandPath(t *testing.T) {
	cases := []struct {
		pattern string
		params  map[string]string
		want    string
	}{
		{"/bestiary/{slug}/{$}", map[string]string{"slug": "goblin"}, "/bestiary/goblin/"},
		{"GET /campaign/{slug}/sessions/{id}", map[string]string{"slug": "shadowmaze", "id": "3"}, "/campaign/shadowmaze/sessions/3"},
		{"/health", nil, "/health"},
		{"GET /bestiary/{$}", nil, "/bestiary/"},
	}
	for _, c := range cases {
		reg := smoke.New()
		var opts []smoke.Option
		for k, v := range c.params {
			opts = append(opts, smoke.Example(k, v))
		}
		reg.AddPattern(c.pattern, opts...)
		got, err := reg.Manifest().Routes[0].ExpandPath()
		if err != nil {
			t.Errorf("%s: %v", c.pattern, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.pattern, got, c.want)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	reg := smoke.New()
	reg.AddPattern("GET /bestiary/{slug}/{$}", smoke.Example("slug", "goblin"), smoke.Status(200))
	reg.AddPattern("POST /api/x", smoke.Skip("phase 2"))

	data, err := reg.Manifest().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := smoke.ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Routes) != 2 {
		t.Fatalf("got %d routes", len(parsed.Routes))
	}
	// Sorted by pattern: /api/x before /bestiary/...
	if parsed.Routes[0].Pattern != "/api/x" {
		t.Errorf("not sorted: %q first", parsed.Routes[0].Pattern)
	}
	b := findRoute(t, parsed.Routes, "/bestiary/{slug}/{$}")
	if b.Effect != "read_only" || b.ExpectStatus != 200 || !b.Complete {
		t.Errorf("round-trip lost data: %+v", b)
	}
}

func TestMuxRecordsAndServes(t *testing.T) {
	m := smoke.NewMux()
	m.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	m.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })

	if got := len(m.Registry().Specs()); got != 2 {
		t.Fatalf("registry recorded %d, want 2", got)
	}
	// Still serves.
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/ok", nil))
	if rec.Code != 200 {
		t.Errorf("served %d, want 200", rec.Code)
	}
}

func TestRunAgainstServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
		case "/redir":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/boom":
			w.WriteHeader(500)
		case "/bestiary/goblin/":
			w.WriteHeader(200)
		case "/api/write":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	reg := smoke.New()
	reg.AddPattern("GET /ok")
	reg.AddPattern("GET /redir")                                                // 302 passes default class
	reg.AddPattern("GET /boom")                                                 // 500 fails
	reg.AddPattern("GET /bestiary/{slug}/{$}", smoke.Example("slug", "goblin")) // 200
	reg.AddPattern("POST /api/write", smoke.Mutates())                          // skipped under Live
	reg.AddPattern("GET /needsparam/{id}")                                      // incomplete → skipped

	// Use Run's own client (no redirect-following) so /redir is observed as a
	// 302, which the default class accepts.
	rep, err := smoke.Run(context.Background(), reg.Manifest(), smoke.RunOptions{
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	pass, fail, _ := rep.Counts()
	if fail != 1 {
		t.Errorf("fail = %d, want 1 (/boom); results: %+v", fail, rep.Results)
	}
	if pass != 3 {
		t.Errorf("pass = %d, want 3 (/ok,/redir,/bestiary; the POST is reads-only-skipped); results: %+v", pass, rep.Results)
	}

	// Live target skips the mutating route entirely.
	repLive, err := smoke.Run(context.Background(), reg.Manifest(), smoke.RunOptions{
		BaseURL: srv.URL, Target: smoke.Live,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range repLive.Results {
		if res.Pattern == "/api/write" && res.Outcome != smoke.Skipped {
			t.Errorf("mutating route ran under Live: %+v", res)
		}
	}
}

func TestRunWriteProbing(t *testing.T) {
	var gotCookie, gotBody, gotCT, gotOrigin, gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/write" {
			gotCookie = r.Header.Get("Cookie")
			gotCT = r.Header.Get("Content-Type")
			gotOrigin = r.Header.Get("Origin")
			gotHost = r.Host
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	reg := smoke.New()
	reg.AddPattern("POST /api/write", smoke.Body("application/json", `{"x":1}`))
	m := reg.Manifest()

	// Default run: writes are not probed (reads-only), so the route is skipped.
	rep, _ := smoke.Run(context.Background(), m, smoke.RunOptions{BaseURL: srv.URL})
	if _, _, skip := rep.Counts(); skip != 1 {
		t.Errorf("write should be skipped without IncludeWrites; results=%+v", rep.Results)
	}

	// With IncludeWrites + a cookie: probed, cookie + body + content-type sent.
	rep, _ = smoke.Run(context.Background(), m, smoke.RunOptions{
		BaseURL: srv.URL, IncludeWrites: true, Cookie: "osg_jwt=tok",
	})
	if pass, _, _ := rep.Counts(); pass != 1 {
		t.Fatalf("write should pass with IncludeWrites; results=%+v", rep.Results)
	}
	if gotCookie != "osg_jwt=tok" {
		t.Errorf("cookie = %q, want osg_jwt=tok", gotCookie)
	}
	if gotBody != `{"x":1}` {
		t.Errorf("body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	// Same-origin header sent and matching Host, so a CSRF origin check passes.
	if gotOrigin == "" || gotOrigin != "http://"+gotHost {
		t.Errorf("Origin = %q, want http://%s (same-origin for CSRF)", gotOrigin, gotHost)
	}

	// Live target never probes a mutating route, even with IncludeWrites.
	rep, _ = smoke.Run(context.Background(), m, smoke.RunOptions{
		BaseURL: srv.URL, Target: smoke.Live, IncludeWrites: true, Cookie: "osg_jwt=tok",
	})
	if _, _, skip := rep.Counts(); skip != 1 {
		t.Errorf("Live must not probe writes; results=%+v", rep.Results)
	}
}

func findSpec(t *testing.T, specs []smoke.RouteSpec, pattern string) smoke.RouteSpec {
	t.Helper()
	for _, s := range specs {
		if s.Pattern == pattern {
			return s
		}
	}
	t.Fatalf("spec %q not found", pattern)
	return smoke.RouteSpec{}
}

func findRoute(t *testing.T, routes []smoke.ManifestRoute, pattern string) smoke.ManifestRoute {
	t.Helper()
	for _, r := range routes {
		if r.Pattern == pattern {
			return r
		}
	}
	t.Fatalf("route %q not found", pattern)
	return smoke.ManifestRoute{}
}

func TestAuthRequiredGating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "" {
			w.WriteHeader(401) // unauthenticated
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	reg := smoke.New()
	reg.AddPattern("GET /api/me", smoke.AuthRequired())
	m := reg.Manifest()

	// No credential: the auth-required route is skipped, not failed on the 401.
	rep, _ := smoke.Run(context.Background(), m, smoke.RunOptions{BaseURL: srv.URL})
	if _, fail, skip := rep.Counts(); fail != 0 || skip != 1 {
		t.Errorf("unauth run: fail=%d skip=%d, want 0/1; %+v", fail, skip, rep.Results)
	}
	// With a credential: probed and expected to succeed (2xx/3xx).
	rep, _ = smoke.Run(context.Background(), m, smoke.RunOptions{BaseURL: srv.URL, Cookie: "s=1"})
	if pass, _, _ := rep.Counts(); pass != 1 {
		t.Errorf("authed run: pass=%d, want 1; %+v", pass, rep.Results)
	}
}
