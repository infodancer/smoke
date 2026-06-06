// Command smolder is the consumer half of the smoke system: it applies
// sustained low heat — probing a running server's routes (smolder run) and
// gating route coverage (smolder gate) — against a manifest produced by the
// smoke library.
//
//	smolder run  --base https://pr-42.oldschoolgamers.org [--manifest routes.json] [--target preview|live]
//	smolder gate --manifest routes.json [--mode warn|fail]
//
// The manifest source is a file path or an http(s) URL. For `run`, when
// --manifest is omitted it is fetched from <base>/_smoke/manifest (the
// non-prod introspection endpoint). For `run --target live`, pass the
// committed routes.json explicitly, since production does not expose the
// endpoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/infodancer/smoke"
)

// newFlagSet builds a subcommand flag set that prints usage and exits on error.
func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ExitOnError)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "gate":
		err = gateCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "smolder: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "smolder:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `smolder — route smoke testing

usage:
  smolder run  --base URL [--manifest SRC] [--target preview|live] [--concurrency N] [--timeout DUR]
  smolder gate --manifest SRC [--mode warn|fail]

SRC is a file path or an http(s) URL. For "run", --manifest defaults to
<base>/_smoke/manifest.
`)
}

func runCmd(args []string) error {
	fs := newFlagSet("run")
	base := fs.String("base", "", "base URL of the server to probe (required)")
	manifestSrc := fs.String("manifest", "", "manifest file path or URL (default <base>/_smoke/manifest)")
	target := fs.String("target", "preview", "preview (run all) or live (ReadOnly only)")
	concurrency := fs.Int("concurrency", 0, "max in-flight requests (0 = default)")
	timeout := fs.Duration("timeout", 0, "per-request timeout (0 = default 15s)")
	cookie := fs.String("cookie", "", "Cookie header for authenticated probes, e.g. 'osg_session=<jwt>'")
	includeWrites := fs.Bool("include-writes", false, "also probe Mutating routes (requires --cookie for auth-gated writes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" {
		return fmt.Errorf("run: --base is required")
	}
	tgt, err := parseTarget(*target)
	if err != nil {
		return err
	}
	src := *manifestSrc
	if src == "" {
		src = trimSlash(*base) + "/_smoke/manifest"
	}
	manifest, err := loadManifest(src)
	if err != nil {
		return err
	}

	rep, err := smoke.Run(context.Background(), manifest, smoke.RunOptions{
		BaseURL:       *base,
		Target:        tgt,
		Concurrency:   *concurrency,
		Timeout:       *timeout,
		Cookie:        *cookie,
		IncludeWrites: *includeWrites,
	})
	if err != nil {
		return err
	}

	for _, r := range rep.Results {
		if r.Outcome == smoke.Fail {
			fmt.Printf("%-4s %-6s %s -> %d  %s\n", r.Outcome, r.Method, r.Pattern, r.Status, r.Reason)
		}
	}
	pass, fail, skip := rep.Counts()
	fmt.Printf("\nsmolder run %s: %d passed, %d failed, %d skipped (%d routes)\n",
		*target, pass, fail, skip, len(rep.Results))
	if fail > 0 {
		return fmt.Errorf("%d route(s) failed", fail)
	}
	return nil
}

func gateCmd(args []string) error {
	fs := newFlagSet("gate")
	manifestSrc := fs.String("manifest", "", "manifest file path or URL (required)")
	mode := fs.String("mode", "warn", "warn (report, exit 0) or fail (exit non-zero)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestSrc == "" {
		return fmt.Errorf("gate: --manifest is required")
	}
	if *mode != "warn" && *mode != "fail" {
		return fmt.Errorf("gate: --mode must be warn or fail, got %q", *mode)
	}
	manifest, err := loadManifest(*manifestSrc)
	if err != nil {
		return err
	}

	incomplete := manifest.Incomplete()
	if len(incomplete) == 0 {
		fmt.Printf("smolder gate: all %d routes covered\n", len(manifest.Routes))
		return nil
	}
	fmt.Fprintf(os.Stderr, "smolder gate: %d route(s) lack smoke coverage:\n", len(incomplete))
	for _, r := range incomplete {
		fmt.Fprintf(os.Stderr, "  %-6s %s  (add smoke.Example for its path params, or smoke.Skip with a reason)\n",
			r.EffectiveMethod(), r.Pattern)
	}
	if *mode == "fail" {
		return fmt.Errorf("%d uncovered route(s)", len(incomplete))
	}
	fmt.Fprintln(os.Stderr, "(warn mode: not failing)")
	return nil
}

func parseTarget(s string) (smoke.Target, error) {
	switch s {
	case "preview":
		return smoke.Preview, nil
	case "live":
		return smoke.Live, nil
	default:
		return 0, fmt.Errorf("invalid --target %q (want preview or live)", s)
	}
}

// loadManifest reads a manifest from an http(s) URL or a file path.
func loadManifest(src string) (smoke.Manifest, error) {
	var data []byte
	if isURL(src) {
		client := &http.Client{Timeout: 20 * time.Second}
		resp, err := client.Get(src)
		if err != nil {
			return smoke.Manifest{}, fmt.Errorf("fetch manifest %s: %w", src, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return smoke.Manifest{}, fmt.Errorf("fetch manifest %s: status %d", src, resp.StatusCode)
		}
		if data, err = io.ReadAll(resp.Body); err != nil {
			return smoke.Manifest{}, fmt.Errorf("read manifest %s: %w", src, err)
		}
	} else {
		var err error
		if data, err = os.ReadFile(src); err != nil {
			return smoke.Manifest{}, fmt.Errorf("read manifest %s: %w", src, err)
		}
	}
	return smoke.ParseManifest(data)
}

func isURL(s string) bool {
	return len(s) > 7 && (s[:7] == "http://" || (len(s) > 8 && s[:8] == "https://"))
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
