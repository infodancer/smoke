package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const completeManifest = `{"routes":[
  {"method":"GET","pattern":"/ok","effect":"read_only","complete":true},
  {"method":"GET","pattern":"/redir","effect":"read_only","complete":true}
]}`

const incompleteManifest = `{"routes":[
  {"method":"GET","pattern":"/ok","effect":"read_only","complete":true},
  {"method":"GET","pattern":"/campaign/{slug}","effect":"read_only","complete":false}
]}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGate(t *testing.T) {
	complete := writeManifest(t, completeManifest)
	incomplete := writeManifest(t, incompleteManifest)

	if err := gateCmd([]string{"--manifest", complete, "--mode", "fail"}); err != nil {
		t.Errorf("complete manifest should pass fail-mode gate: %v", err)
	}
	if err := gateCmd([]string{"--manifest", incomplete, "--mode", "warn"}); err != nil {
		t.Errorf("warn mode must never error: %v", err)
	}
	if err := gateCmd([]string{"--manifest", incomplete, "--mode", "fail"}); err == nil {
		t.Errorf("fail mode must error on an uncovered route")
	}
}

func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
		case "/redir":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/boom":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	// good manifest probes only /ok (200) and /redir (302) -- both 2xx/3xx.
	good := writeManifest(t, completeManifest)
	// This server sends no security headers, so a default run fails on them...
	if err := runCmd([]string{"--base", srv.URL, "--manifest", good}); err == nil {
		t.Errorf("run must fail by default when security headers are missing")
	}
	// ...and passes only when the check is explicitly turned off.
	if err := runCmd([]string{"--base", srv.URL, "--manifest", good, "--no-header-checks"}); err != nil {
		t.Errorf("run against healthy server should pass: %v", err)
	}

	bad := writeManifest(t, `{"routes":[{"method":"GET","pattern":"/boom","effect":"read_only","complete":true}]}`)
	if err := runCmd([]string{"--base", srv.URL, "--manifest", bad, "--no-header-checks"}); err == nil {
		t.Errorf("run should fail when a route 5xxs")
	}
}

func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInline(t *testing.T) {
	clean := writeSite(t, map[string]string{"index.html": `<p>ok</p><script src="/a.js" defer></script>`})
	dirty := writeSite(t, map[string]string{
		"index.html":        `<p>ok</p>`,
		"a/index.html":      `<div style="x:y">t</div>`,
		"archive/page.html": `<script>x()</script>`,
	})

	if err := inlineCmd([]string{"--dir", clean, "--mode", "fail"}); err != nil {
		t.Errorf("clean tree must pass fail mode: %v", err)
	}
	if err := inlineCmd([]string{"--dir", dirty, "--mode", "warn"}); err != nil {
		t.Errorf("warn mode must not error: %v", err)
	}
	if err := inlineCmd([]string{"--dir", dirty, "--mode", "fail"}); err == nil {
		t.Error("fail mode must error on findings")
	}
	// Allowing the page that has the script leaves only the style attribute.
	if err := inlineCmd([]string{"--dir", dirty, "--mode", "fail", "--allow", "archive/*.html"}); err == nil {
		t.Error("the style attribute outside the allowed page must still fail")
	}
	both := []string{"--dir", dirty, "--mode", "fail", "--allow", "archive/*.html", "--allow", "a/*.html"}
	if err := inlineCmd(both); err != nil {
		t.Errorf("every dirty page allowed, want pass: %v", err)
	}
	if err := inlineCmd([]string{"--mode", "fail"}); err == nil {
		t.Error("--dir is required")
	}
	if err := inlineCmd([]string{"--dir", clean, "--mode", "bogus"}); err == nil {
		t.Error("a bad --mode must be rejected")
	}
	if err := inlineCmd([]string{"--dir", filepath.Join(clean, "missing"), "--mode", "fail"}); err == nil {
		t.Error("a missing directory must be an error, not a clean pass")
	}
	empty := writeSite(t, map[string]string{"notes.txt": "no pages here"})
	if err := inlineCmd([]string{"--dir", empty, "--mode", "fail"}); err == nil {
		t.Error("a tree with no HTML pages is a misconfigured path, not a clean build")
	}
	if err := inlineCmd([]string{"--dir", clean, "--allow", "["}); err == nil {
		t.Error("a malformed --allow pattern must be rejected")
	}
}
