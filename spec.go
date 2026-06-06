// Package smoke is a router-agnostic registry of HTTP routes and the metadata
// needed to smoke-test them: a black-box, pure-HTTP check that every URL a
// server serves actually serves. It is the producer half of the system; the
// smolder CLI (cmd/smolder) is the consumer that probes a running server and
// runs the gate.
//
// The package imports nothing from any host application — hosts depend on
// smoke, never the reverse — so it can be lifted to its own repo
// (github.com/infodancer/smoke) once the API stabilizes. See DESIGN.md.
package smoke

import (
	"net/url"
	"regexp"
	"strings"
)

// Effect classifies a route's side effects, which decides whether it is safe
// to exercise against a live production site.
type Effect int

const (
	// ReadOnly routes are safe against live production (the default for GET and
	// HEAD). They must not mutate state.
	ReadOnly Effect = iota
	// Mutating routes change state and may only run against an ephemeral
	// target (the per-PR preview). The default for POST/PUT/PATCH/DELETE, and
	// the explicit override for the rare side-effecting GET.
	Mutating
)

func (e Effect) String() string {
	if e == Mutating {
		return "mutating"
	}
	return "read_only"
}

// RouteSpec describes one route and how to smoke it. It is the unit of the
// system; a flat URL list cannot express params, expected status, side-effect
// class, or (later) body assertions.
type RouteSpec struct {
	// Method is the HTTP method ("GET", "POST", ...). Empty means the route
	// was registered without one (matches any method); the runner treats it as
	// GET.
	Method string `json:"method"`
	// Pattern is the path pattern, Go 1.22 ServeMux syntax minus the method —
	// e.g. "/bestiary/{slug}/{$}".
	Pattern string `json:"pattern"`
	// Effect is the side-effect class. Defaults from Method unless overridden.
	Effect Effect `json:"effect"`
	// ExampleParams supplies a concrete value for each path wildcard, drawn
	// from known-present seed data — e.g. {"slug": "goblin"}.
	ExampleParams map[string]string `json:"example_params,omitempty"`
	// ExpectStatus, when non-zero, is the exact status the route must return.
	// Zero means the default class: 200..399, never 5xx (and never 4xx).
	ExpectStatus int `json:"expect_status,omitempty"`
	// Skip, when non-empty, exempts the route from probing and from the gate,
	// recording the reason.
	Skip string `json:"skip,omitempty"`
	// RequestBody is the body sent when probing a write route; empty means no
	// body. ContentType sets its Content-Type header.
	RequestBody string `json:"request_body,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	// AuthRequired marks a route that only responds successfully to an
	// authenticated request. It is skipped in an unauthenticated run (no
	// Cookie) and probed — expecting the default 2xx/3xx — only when a
	// credential is supplied. Use for reads that 401/404 without a session.
	AuthRequired bool `json:"auth_required,omitempty"`
}

// Option mutates a RouteSpec at registration time.
type Option func(*RouteSpec)

// Example sets one path-parameter example value.
func Example(key, value string) Option {
	return func(s *RouteSpec) {
		if s.ExampleParams == nil {
			s.ExampleParams = map[string]string{}
		}
		s.ExampleParams[key] = value
	}
}

// Examples sets several path-parameter example values at once.
func Examples(m map[string]string) Option {
	return func(s *RouteSpec) {
		for k, v := range m {
			Example(k, v)(s)
		}
	}
}

// Status requires an exact response status instead of the default class.
func Status(code int) Option { return func(s *RouteSpec) { s.ExpectStatus = code } }

// Mutates marks the route as state-changing (excluded from live runs).
func Mutates() Option { return func(s *RouteSpec) { s.Effect = Mutating } }

// SafeForLive marks the route ReadOnly explicitly, overriding the
// method-derived default (e.g. a GET that the default would keep ReadOnly is
// unaffected; use this only to document intent or override a prior option).
func SafeForLive() Option { return func(s *RouteSpec) { s.Effect = ReadOnly } }

// Skip exempts the route from probing and the gate, recording why.
func Skip(reason string) Option { return func(s *RouteSpec) { s.Skip = reason } }

// AuthRequired marks a read route that needs a session: skipped without a
// credential, probed (expecting 2xx/3xx) when one is supplied.
func AuthRequired() Option { return func(s *RouteSpec) { s.AuthRequired = true } }

// Body attaches a request body and content type for probing a write route.
// Unlike Write(), it does not skip the route — the route is probed (POSTed)
// when the runner is given write access. Use for writes that have been brought
// under smoke coverage with a known-valid payload against the fixture.
func Body(contentType, body string) Option {
	return func(s *RouteSpec) {
		s.ContentType = contentType
		s.RequestBody = body
	}
}

// Form attaches a urlencoded form body (the common OSG write shape).
func Form(values url.Values) Option {
	return Body("application/x-www-form-urlencoded", values.Encode())
}

// Write marks a state-changing route as deferred to smoke phase 2 (writes need
// auth and request payloads). The route is recorded as Mutating and Skipped, so
// phase-1 reads-only runs never fire it and the coverage gate treats it as
// intentional rather than missing. Grep the skip reason to find the phase-2
// backlog; replace Write() with real coverage when wiring write probes.
func Write() Option {
	return func(s *RouteSpec) {
		s.Effect = Mutating
		s.Skip = "write — smoke phase 2 (auth + payloads)"
	}
}

// defaultEffect returns the side-effect class implied by an HTTP method.
func defaultEffect(method string) Effect {
	switch strings.ToUpper(method) {
	case "", "GET", "HEAD", "OPTIONS":
		return ReadOnly
	default:
		return Mutating
	}
}

// paramPattern matches Go 1.22 ServeMux wildcards: {name} and {name...}. The
// end-of-path anchor {$} is intentionally excluded — it is not a parameter.
var paramPattern = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)(\.\.\.)?\}`)

// PathParams returns the wildcard names in a pattern, in order, without the
// {$} anchor and without the "..." suffix.
func PathParams(pattern string) []string {
	matches := paramPattern.FindAllStringSubmatch(pattern, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// Complete reports whether the spec is testable: explicitly skipped, or every
// path parameter has an example value. A parameterless route is trivially
// complete. Incomplete specs are what the gate flags.
func (s RouteSpec) Complete() bool {
	if s.Skip != "" {
		return true
	}
	for _, p := range PathParams(s.Pattern) {
		if _, ok := s.ExampleParams[p]; !ok {
			return false
		}
	}
	return true
}

// parsePattern splits a Go 1.22 ServeMux registration pattern into its method
// and path. "GET /x" -> ("GET", "/x"); "/x" -> ("", "/x"). A leading token is
// treated as a method only when it is all uppercase ASCII letters followed by
// a space, matching net/http's own grammar closely enough for registration.
func parsePattern(pattern string) (method, path string) {
	if head, rest, found := strings.Cut(pattern, " "); found && isMethod(head) {
		return head, strings.TrimSpace(rest)
	}
	return "", pattern
}

func isMethod(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
