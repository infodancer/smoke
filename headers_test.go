package smoke_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/infodancer/smoke"
)

const goodPP = "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()"

// secureHeaders are the strict defaults a site gets from its header middleware.
func secureHeaders() map[string]string {
	return map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Permissions-Policy":        goodPP,
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
		"Content-Security-Policy":   "default-src 'self'",
	}
}

// serverSending answers every path 200 with the headers chosen per path.
func serverSending(t *testing.T, perPath map[string]map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range perPath[r.URL.Path] {
			w.Header().Set(k, v)
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runOne(t *testing.T, srv *httptest.Server, m smoke.Manifest, opts smoke.RunOptions) smoke.Result {
	t.Helper()
	opts.BaseURL = srv.URL
	rep, err := smoke.Run(context.Background(), m, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) == 0 {
		t.Fatal("no results")
	}
	return rep.Results[0]
}

func oneRoute(opts ...smoke.Option) *smoke.Registry {
	reg := smoke.New()
	reg.AddPattern("GET /a", opts...)
	return reg
}

func TestDefaultBaselinePassesStrictHeaders(t *testing.T) {
	srv := serverSending(t, map[string]map[string]string{"/a": secureHeaders()})
	if res := runOne(t, srv, oneRoute().Manifest(), smoke.RunOptions{}); res.Outcome != smoke.Pass {
		t.Errorf("strict headers: %v %s", res.Outcome, res.Reason)
	}
}

func TestDefaultBaselineFailsMissingHeaders(t *testing.T) {
	srv := serverSending(t, nil)
	res := runOne(t, srv, oneRoute().Manifest(), smoke.RunOptions{})
	if res.Outcome != smoke.Fail {
		t.Fatalf("a server with no security headers must fail by default, got %v", res.Outcome)
	}
	for _, name := range []string{"X-Frame-Options", "Permissions-Policy", "Strict-Transport-Security"} {
		if !strings.Contains(res.Reason, name) {
			t.Errorf("reason %q does not name %s", res.Reason, name)
		}
	}
}

func TestDefaultBaselineRequiresSomeCSP(t *testing.T) {
	h := secureHeaders()
	delete(h, "Content-Security-Policy")
	srv := serverSending(t, map[string]map[string]string{"/a": h})
	res := runOne(t, srv, oneRoute().Manifest(), smoke.RunOptions{})
	if res.Outcome != smoke.Fail || !strings.Contains(res.Reason, "Content-Security-Policy") {
		t.Errorf("no CSP must fail: %v %q", res.Outcome, res.Reason)
	}
	// Report-only counts: it is the rollout state.
	h["Content-Security-Policy-Report-Only"] = "default-src 'self'"
	srv2 := serverSending(t, map[string]map[string]string{"/a": h})
	if res := runOne(t, srv2, oneRoute().Manifest(), smoke.RunOptions{}); res.Outcome != smoke.Pass {
		t.Errorf("report-only CSP should satisfy the baseline: %v %s", res.Outcome, res.Reason)
	}
}

func TestLoosenedHeaderFailsUnderDefault(t *testing.T) {
	h := secureHeaders()
	h["Permissions-Policy"] = "camera=(self), " + goodPP // a superset is not the policy
	srv := serverSending(t, map[string]map[string]string{"/a": h})
	res := runOne(t, srv, oneRoute().Manifest(), smoke.RunOptions{})
	if res.Outcome != smoke.Fail || !strings.Contains(res.Reason, "Permissions-Policy") {
		t.Errorf("loosened policy must fail on exact match: %v %q", res.Outcome, res.Reason)
	}
}

func TestRouteOverrideReplacesOnlyThatHeaderOnThatRoute(t *testing.T) {
	vtt := secureHeaders()
	vtt["Permissions-Policy"] = "camera=(self), microphone=(self)"
	srv := serverSending(t, map[string]map[string]string{"/a": vtt, "/b": secureHeaders()})

	reg := smoke.New()
	reg.AddPattern("GET /a", smoke.ExpectHeaders(map[string]string{"Permissions-Policy": "camera=(self), microphone=(self)"}))
	reg.AddPattern("GET /b")
	rep, err := smoke.Run(context.Background(), reg.Manifest(), smoke.RunOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Outcome != smoke.Pass {
			t.Errorf("%s: %v %s", r.Pattern, r.Outcome, r.Reason)
		}
	}

	// The same loosened value on a route that did not declare it must fail.
	srv2 := serverSending(t, map[string]map[string]string{"/a": vtt, "/b": vtt})
	rep, _ = smoke.Run(context.Background(), reg.Manifest(), smoke.RunOptions{BaseURL: srv2.URL})
	for _, r := range rep.Results {
		switch r.Pattern {
		case "/a":
			if r.Outcome != smoke.Pass {
				t.Errorf("/a: %v %s", r.Outcome, r.Reason)
			}
		case "/b":
			if r.Outcome != smoke.Fail {
				t.Errorf("/b sends a loosened policy it never declared and must fail, got %v", r.Outcome)
			}
		}
	}
}

func TestSiteBaselineInManifestReplacesDefault(t *testing.T) {
	srv := serverSending(t, map[string]map[string]string{"/a": {"X-Frame-Options": "SAMEORIGIN"}})
	reg := oneRoute()
	reg.ExpectHeaders(map[string]string{"X-Frame-Options": "SAMEORIGIN"})
	m := reg.Manifest()
	if res := runOne(t, srv, m, smoke.RunOptions{}); res.Outcome != smoke.Pass {
		t.Errorf("site baseline: %v %s", res.Outcome, res.Reason)
	}
	// It survives a trip through the committed JSON.
	data, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := smoke.ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if res := runOne(t, srv, m2, smoke.RunOptions{}); res.Outcome != smoke.Pass {
		t.Errorf("after round trip: %v %s", res.Outcome, res.Reason)
	}
	if !strings.Contains(string(data), "expect_headers") {
		t.Errorf("manifest JSON does not record the baseline: %s", data)
	}
}

func TestRunOptionsHeadersBeatManifest(t *testing.T) {
	srv := serverSending(t, map[string]map[string]string{"/a": {"X-Frame-Options": "DENY"}})
	reg := oneRoute()
	reg.ExpectHeaders(map[string]string{"X-Frame-Options": "SAMEORIGIN"})
	res := runOne(t, srv, reg.Manifest(), smoke.RunOptions{Headers: map[string]string{"X-Frame-Options": "DENY"}})
	if res.Outcome != smoke.Pass {
		t.Errorf("RunOptions.Headers should win: %v %s", res.Outcome, res.Reason)
	}
}

func TestHeaderChecksCanBeTurnedOffExplicitly(t *testing.T) {
	srv := serverSending(t, nil)
	res := runOne(t, srv, oneRoute().Manifest(), smoke.RunOptions{HeaderChecksOff: true})
	if res.Outcome != smoke.Pass {
		t.Errorf("HeaderChecksOff: %v %s", res.Outcome, res.Reason)
	}
}

func TestBaselineHeadersReturnsACopy(t *testing.T) {
	a := smoke.BaselineHeaders()
	a["X-Frame-Options"] = "ALLOWALL"
	if smoke.BaselineHeaders()["X-Frame-Options"] != "DENY" {
		t.Error("BaselineHeaders aliases package state")
	}
	if a["Permissions-Policy"] != goodPP {
		t.Errorf("baseline Permissions-Policy = %q", a["Permissions-Policy"])
	}
}

func TestExpectHeadersRejectsBadInput(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"empty name":     {"": "x"},
		"empty value":    {"X-Frame-Options": ""},
		"control char":   {"X-Frame-Options": "DENY\r\nX: y"},
		"no headers":     {},
		"non-token name": {"Bad Name": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("ExpectHeaders(%v) did not panic", m)
				}
			}()
			smoke.ExpectHeaders(m)(&smoke.RouteSpec{})
		})
	}
}

func TestHeaderNamesAreCaseInsensitive(t *testing.T) {
	srv := serverSending(t, map[string]map[string]string{"/a": {"X-Frame-Options": "SAMEORIGIN"}})
	reg := oneRoute()
	reg.ExpectHeaders(map[string]string{"x-frame-options": "SAMEORIGIN"})
	if res := runOne(t, srv, reg.Manifest(), smoke.RunOptions{}); res.Outcome != smoke.Pass {
		t.Errorf("lowercase name: %v %s", res.Outcome, res.Reason)
	}
}
