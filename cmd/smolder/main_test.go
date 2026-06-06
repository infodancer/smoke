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
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	good := writeManifest(t, completeManifest)
	if err := runCmd([]string{"--base", srv.URL, "--manifest", good}); err != nil {
		t.Errorf("run against healthy server should pass: %v", err)
	}

	bad := writeManifest(t, `{"routes":[{"method":"GET","pattern":"/missing","effect":"read_only","complete":true}]}`)
	if err := runCmd([]string{"--base", srv.URL, "--manifest", bad}); err == nil {
		t.Errorf("run should fail when a route 404s")
	}
}
