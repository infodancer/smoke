package smoke

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func kinds(fs []InlineFinding) []InlineKind {
	var out []InlineKind
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func TestScanInlineFindsWhatAStrictPolicyBlocks(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []InlineKind
	}{
		{"inline script", `<script>go()</script>`, []InlineKind{InlineScript}},
		{"module script", `<script type="module">go()</script>`, []InlineKind{InlineScript}},
		{"js mime script", `<script type="text/javascript">go()</script>`, []InlineKind{InlineScript}},
		{"style element", `<style>.a{color:red}</style>`, []InlineKind{InlineStyleElement}},
		{"style attribute", `<div style="width:3px">x</div>`, []InlineKind{InlineStyleAttr}},
		{"handler", `<button onclick="go()">x</button>`, []InlineKind{InlineHandler}},
		{"uppercase handler", `<BUTTON ONCLICK="go()">x</BUTTON>`, []InlineKind{InlineHandler}},
		{"javascript url", `<a href="javascript:void(0)">x</a>`, []InlineKind{InlineJSURL}},
		{"obfuscated javascript url", "<a href=\" Java\tScript:alert(1)\">x</a>", []InlineKind{InlineJSURL}},
		{"entity javascript url", `<a href="javascript&colon;alert(1)">x</a>`, []InlineKind{InlineJSURL}},
		{"unquoted attributes", `<div style=color:red onclick=go()>x</div>`, []InlineKind{InlineStyleAttr, InlineHandler}},
		{"several on one tag", `<p style="a:b" onmouseover="x()">t</p>`, []InlineKind{InlineStyleAttr, InlineHandler}},
		{"svg style", `<svg><style>.a{fill:red}</style></svg>`, []InlineKind{InlineStyleElement}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(ScanInline([]byte(tc.html)))
			if len(got) != len(tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("kinds = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestScanInlineIgnoresWhatIsNotBlocked(t *testing.T) {
	for name, html := range map[string]string{
		"external script":      `<script src="/a.js" defer></script>`,
		"external with body":   `<script src="/a.js">ignored()</script>`,
		"json-ld":              `<script type="application/ld+json">{"a":1}</script>`,
		"json data":            `<script type='application/json'>{}</script>`,
		"html template":        `<script type="text/html" id="t"><div style="x:y" onclick="z()"></div></script>`,
		"importmap":            `<script type="importmap">{}</script>`,
		"empty script":         `<script></script>`,
		"blank style":          "<style>\n  </style>",
		"stylesheet link":      `<link rel="stylesheet" href="/a.css">`,
		"data attribute":       `<div data-style="x" data-onclick="y" class="onclick style">t</div>`,
		"words in text":        `<p>use style="x" or onclick="y" or javascript:z</p>`,
		"comment":              `<!-- <script>x()</script> <div style="a:b"> -->`,
		"cdata":                `<![CDATA[ <script>x()</script> ]]>`,
		"script mentions tags": `<script src="/a.js"></script><p>ok</p>`,
		"textarea content":     `<textarea><script>x()</script><div style="a:b"></div></textarea>`,
		"title content":        `<title><style>x</style></title>`,
		"normal link":          `<a href="/page?x=javascript:y">x</a>`,
		"end tag attrs":        `<p>t</p style="x">`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := ScanInline([]byte(html)); len(got) != 0 {
				t.Fatalf("flagged clean input: %+v", got)
			}
		})
	}
}

func TestScanInlineParsesTagsNotLookalikes(t *testing.T) {
	// A ">" inside a quoted attribute must not end the tag early, or the style
	// attribute after it would be missed.
	got := ScanInline([]byte(`<a title="a > b" style="x:y">t</a>`))
	if len(got) != 1 || got[0].Kind != InlineStyleAttr {
		t.Fatalf("got %+v", got)
	}
	// The body of a script is raw text: a tag-looking string inside it is not a tag.
	got = ScanInline([]byte(`<script>var s = "<div style='a:b'>";</script>`))
	if len(got) != 1 || got[0].Kind != InlineScript {
		t.Fatalf("got %+v", got)
	}
	// ...and the first closing script tag ends it, whatever the case.
	got = ScanInline([]byte(`<SCRIPT>x()</SCRIPT><p style="a:b">t</p>`))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestScanInlineReportsLinesAndDetail(t *testing.T) {
	got := ScanInline([]byte("<html>\n<body>\n<div style=\"a:b\">\n</div>\n<button onclick=\"x()\">\n"))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Line != 3 || got[1].Line != 5 {
		t.Errorf("lines = %d, %d; want 3, 5", got[0].Line, got[1].Line)
	}
	if !strings.Contains(got[0].Detail, "div") || !strings.Contains(got[1].Detail, "onclick") {
		t.Errorf("details = %q, %q", got[0].Detail, got[1].Detail)
	}
}

func TestAuditInline(t *testing.T) {
	site := fstest.MapFS{
		"index.html":              {Data: []byte(`<p>clean</p>`)},
		"a/index.html":            {Data: []byte(`<div style="x:y">t</div>`)},
		"a/b/index.htm":           {Data: []byte(`<script>x()</script>`)},
		"archive/saved/page.html": {Data: []byte(`<style>.a{}</style><script>y()</script>`)},
		"app.js":                  {Data: []byte(`document.write("<div style='a:b'>")`)},
	}
	rep, err := AuditInline(site, InlineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pages != 4 {
		t.Errorf("Pages = %d, want 4 (only .html and .htm are scanned)", rep.Pages)
	}
	if len(rep.Findings) != 4 {
		t.Fatalf("findings = %+v", rep.Findings)
	}
	if rep.Findings[0].Path != "a/b/index.htm" && rep.Findings[0].Path != "a/index.html" {
		t.Errorf("findings are not ordered by path: %+v", rep.Findings)
	}

	rep, err = AuditInline(site, InlineOptions{Allow: []string{"archive/*/*.html"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pages != 3 || len(rep.Findings) != 2 {
		t.Errorf("after allowing the archive: pages=%d findings=%d, want 3 and 2", rep.Pages, len(rep.Findings))
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "archive/saved/page.html" {
		t.Errorf("Skipped = %v", rep.Skipped)
	}

	if _, err := AuditInline(site, InlineOptions{Allow: []string{"["}}); err == nil {
		t.Error("a malformed allow pattern must be an error, not silently match nothing")
	}
}

func TestInlineReportCounts(t *testing.T) {
	rep := InlineReport{Findings: []InlineFinding{
		{Path: "a.html", Kind: InlineStyleAttr}, {Path: "a.html", Kind: InlineStyleAttr},
		{Path: "b.html", Kind: InlineStyleAttr}, {Path: "b.html", Kind: InlineScript},
	}}
	c := rep.Counts()
	if c[InlineStyleAttr].Findings != 3 || c[InlineStyleAttr].Pages != 2 {
		t.Errorf("style attr = %+v", c[InlineStyleAttr])
	}
	if c[InlineScript].Findings != 1 || c[InlineScript].Pages != 1 {
		t.Errorf("script = %+v", c[InlineScript])
	}
}

// A scan must stay linear in the document size: saved pages run to tens of
// megabytes with many comments, scripts and styles.
func TestScanInlineIsLinear(t *testing.T) {
	var b strings.Builder
	for range 40000 {
		b.WriteString("<!-- c --><p>text</p><script src=\"/a.js\"></script><title>t</title><div class=\"x\">y</div>\n")
	}
	doc := []byte(b.String())
	start := time.Now()
	ScanInline(doc)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("scanning %d bytes took %v; the scanner is not linear", len(doc), d)
	}
}
