package smoke

import (
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
)

// The default, narrow security policy is expressed by two things together: this
// package decides what a probe expects, and github.com/infodancer/web/httpsec
// decides what a site sends. Neither controls the other, and they must stay in
// sync; the site template runs smoke against a server built on httpsec's
// defaults, so drift between them fails a test. Whether to add a restriction or
// loosen one is decided from first principles (what the browser feature does,
// who needs it, what abuse it allows), never because one side says so. A site is
// expected to conform to the baseline unless a specific exception is made for
// that site or route, with the reason written next to it.

// baselineHeaders is the security header set smoke expects on every response
// unless told otherwise. It is deliberately the strict default of a hardened
// site, so a route that does not state an exception is held to it. Values are
// matched exactly: a loosened policy is a failure, not a near miss.
//
// A Content-Security-Policy is also required under the baseline, as either
// Content-Security-Policy or Content-Security-Policy-Report-Only, but its value
// is the site's own and is not pinned here.
var baselineHeaders = map[string]string{
	"X-Content-Type-Options":    "nosniff",
	"X-Frame-Options":           "DENY",
	"Referrer-Policy":           "strict-origin-when-cross-origin",
	"Permissions-Policy":        "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()",
	"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
}

// BaselineHeaders returns a copy of the headers smoke expects by default.
func BaselineHeaders() map[string]string { return maps.Clone(baselineHeaders) }

// ExpectHeaders is a route option: the route must send these headers, name to
// exact value, in place of the site's value for each named header. Every other
// header keeps the site expectation, so a narrow exception is stated narrowly.
// Names are case-insensitive. It panics on an invalid name or value, at
// registration time, so a typo cannot silently weaken a check.
func ExpectHeaders(m map[string]string) Option {
	clean := cleanHeaders(m)
	return func(s *RouteSpec) {
		if s.ExpectHeaders == nil {
			s.ExpectHeaders = map[string]string{}
		}
		maps.Copy(s.ExpectHeaders, clean)
	}
}

// ExpectHeaders sets the site-wide expectation recorded in the manifest. It
// replaces the built-in baseline wholesale for this site, so state every header
// the site sends; a header left out is then not checked. Use it for a site whose
// default differs from the baseline (a site-wide exception). A run's
// RunOptions.Headers overrides it.
func (r *Registry) ExpectHeaders(m map[string]string) {
	clean := cleanHeaders(m)
	r.mu.Lock()
	r.headers = clean
	r.mu.Unlock()
}

// cleanHeaders validates a header map and canonicalizes its names.
func cleanHeaders(m map[string]string) map[string]string {
	if len(m) == 0 {
		panic("smoke: ExpectHeaders needs at least one header")
	}
	out := make(map[string]string, len(m))
	for name, value := range m {
		if !validHeaderName(name) {
			panic(fmt.Sprintf("smoke: invalid header name %q", name))
		}
		if strings.TrimSpace(value) == "" {
			panic(fmt.Sprintf("smoke: empty value for header %s", name))
		}
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				panic(fmt.Sprintf("smoke: control character in value for header %s", name))
			}
		}
		out[http.CanonicalHeaderKey(name)] = strings.TrimSpace(value)
	}
	return out
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// headerExpectation is what one run expects before any route override.
type headerExpectation struct {
	off        bool
	values     map[string]string
	requireCSP bool // the built-in baseline: some CSP header must be present
}

// resolveHeaders picks the site-level expectation: RunOptions.Headers, else the
// manifest's, else the built-in baseline.
func resolveHeaders(m Manifest, opts RunOptions) headerExpectation {
	switch {
	case opts.HeaderChecksOff:
		return headerExpectation{off: true}
	case len(opts.Headers) > 0:
		return headerExpectation{values: cleanHeaders(opts.Headers)}
	case len(m.Headers) > 0:
		return headerExpectation{values: cleanHeaders(m.Headers)}
	default:
		return headerExpectation{values: BaselineHeaders(), requireCSP: true}
	}
}

// checkHeaders compares a response to the expectation overlaid with the
// route's own, returning one line per problem, sorted.
func checkHeaders(got http.Header, site headerExpectation, route ManifestRoute) []string {
	if site.off {
		return nil
	}
	want := maps.Clone(site.values)
	maps.Copy(want, route.ExpectHeaders)
	var problems []string
	for name, value := range want {
		have := strings.TrimSpace(got.Get(name))
		switch {
		case have == "":
			problems = append(problems, fmt.Sprintf("%s missing, want %q", name, value))
		case have != value:
			problems = append(problems, fmt.Sprintf("%s = %q, want %q", name, have, value))
		}
	}
	if site.requireCSP && got.Get("Content-Security-Policy") == "" && got.Get("Content-Security-Policy-Report-Only") == "" {
		problems = append(problems, "Content-Security-Policy missing (or -Report-Only)")
	}
	sort.Strings(problems)
	return problems
}
