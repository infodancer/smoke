package smoke

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Target selects which routes to run.
type Target int

const (
	// Preview runs every route — mutations are acceptable because the target
	// is an ephemeral per-PR database.
	Preview Target = iota
	// Live runs only ReadOnly routes — no mutations against production.
	Live
)

// RunOptions configures a probe run.
type RunOptions struct {
	// BaseURL is the server to probe, e.g. "https://pr-42.oldschoolgamers.org".
	BaseURL string
	// Target gates which routes run (Preview: all; Live: ReadOnly only).
	Target Target
	// Concurrency caps in-flight requests (default 8; forced to 2 for Live to
	// stay polite to production).
	Concurrency int
	// Timeout per request (default 15s).
	Timeout time.Duration
	// UserAgent identifies the prober (default "smolder"); Live appends so the
	// traffic is filterable from analytics.
	UserAgent string
	// Cookie, when set, is sent verbatim as the Cookie header on every probe —
	// the authenticated-session credential for write probes (e.g.
	// "osg_session=<jwt>"). Acquired out of band (cmd smokeauth).
	Cookie string
	// IncludeWrites enables probing Mutating routes. Off by default so a
	// reads-only run never fires a write even if a write route lacks a Skip.
	// Requires Cookie for auth-gated writes.
	IncludeWrites bool
	// Client, when set, overrides the default HTTP client (tests inject one).
	Client *http.Client
}

// Outcome is a route's probe result.
type Outcome int

const (
	Pass    Outcome = iota // status matched the expectation
	Fail                   // wrong status, transport error, or 5xx
	Skipped                // skipped (Skip set, incomplete, or filtered by target)
)

func (o Outcome) String() string {
	switch o {
	case Pass:
		return "PASS"
	case Fail:
		return "FAIL"
	default:
		return "SKIP"
	}
}

// Result is the outcome of probing one route.
type Result struct {
	Method  string
	Pattern string
	URL     string
	Status  int
	Outcome Outcome
	Reason  string // why it failed or was skipped
}

// Report aggregates results.
type Report struct {
	Results []Result
}

// Failed returns only the failing results.
func (r Report) Failed() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Outcome == Fail {
			out = append(out, res)
		}
	}
	return out
}

// Counts returns pass/fail/skip totals.
func (r Report) Counts() (pass, fail, skip int) {
	for _, res := range r.Results {
		switch res.Outcome {
		case Pass:
			pass++
		case Fail:
			fail++
		case Skipped:
			skip++
		}
	}
	return
}

// Run probes every route in the manifest against opts.BaseURL and returns a
// report. It never mutates against Live targets: only ReadOnly routes run.
// Incomplete and explicitly-skipped routes are reported as Skipped, not Fail —
// coverage is the gate's job, not the runner's.
func Run(ctx context.Context, m Manifest, opts RunOptions) (Report, error) {
	if opts.BaseURL == "" {
		return Report{}, fmt.Errorf("smoke: BaseURL is required")
	}
	conc := opts.Concurrency
	if conc <= 0 {
		conc = 8
	}
	if opts.Target == Live && conc > 2 {
		conc = 2 // be polite to production
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "smolder"
	}
	if opts.Target == Live {
		ua += "/live"
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			// A 3xx is a valid outcome (e.g. an auth-gated route redirecting to
			// login); don't follow it off-site or into an auth flow.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	base := trimTrailingSlash(opts.BaseURL)

	results := make([]Result, len(m.Routes))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, route := range m.Routes {
		wg.Add(1)
		go func(i int, route ManifestRoute) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = probe(ctx, client, base, ua, route, opts)
		}(i, route)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		if results[i].Pattern != results[j].Pattern {
			return results[i].Pattern < results[j].Pattern
		}
		return results[i].Method < results[j].Method
	})
	return Report{Results: results}, nil
}

func probe(ctx context.Context, client *http.Client, base, ua string, route ManifestRoute, opts RunOptions) Result {
	res := Result{Method: route.EffectiveMethod(), Pattern: route.Pattern}
	mutating := effectFromString(route.Effect) != ReadOnly

	if route.Skip != "" {
		res.Outcome, res.Reason = Skipped, "skip: "+route.Skip
		return res
	}
	if mutating && (opts.Target == Live || !opts.IncludeWrites) {
		res.Outcome, res.Reason = Skipped, "mutating route skipped (reads-only run)"
		return res
	}
	path, err := route.ExpandPath()
	if err != nil {
		res.Outcome, res.Reason = Skipped, "incomplete: "+err.Error()
		return res
	}
	res.URL = base + path

	var body io.Reader
	if route.RequestBody != "" {
		body = strings.NewReader(route.RequestBody)
	}
	req, err := http.NewRequestWithContext(ctx, route.EffectiveMethod(), res.URL, body)
	if err != nil {
		res.Outcome, res.Reason = Fail, "build request: "+err.Error()
		return res
	}
	req.Header.Set("User-Agent", ua)
	// Same-origin header so the request passes a host's CSRF origin check on
	// write probes (a server that compares Origin/Referer host to its own Host).
	// Harmless on safe methods. Derived from the base so it matches whatever
	// host we're actually probing (in-network container or public domain).
	req.Header.Set("Origin", base)
	if route.ContentType != "" {
		req.Header.Set("Content-Type", route.ContentType)
	}
	if opts.Cookie != "" {
		req.Header.Set("Cookie", opts.Cookie)
	}
	// Don't follow redirects automatically — a 3xx is itself a valid outcome
	// and following it can wander off-site or into auth flows.
	resp, err := client.Do(req)
	if err != nil {
		res.Outcome, res.Reason = Fail, "request: "+err.Error()
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	if !statusOK(resp.StatusCode, route.ExpectStatus) {
		res.Outcome = Fail
		if route.ExpectStatus != 0 {
			res.Reason = fmt.Sprintf("status %d, want %d", resp.StatusCode, route.ExpectStatus)
		} else {
			res.Reason = fmt.Sprintf("status %d, want 2xx/3xx", resp.StatusCode)
		}
		return res
	}
	res.Outcome = Pass
	return res
}

// statusOK applies the expectation: exact match when expect is set, else the
// default class (200..399, never 4xx/5xx).
func statusOK(status, expect int) bool {
	if expect != 0 {
		return status == expect
	}
	return status >= 200 && status < 400
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
