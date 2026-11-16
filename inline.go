package smoke

import (
	"fmt"
	"html"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// A strict Content-Security-Policy (script-src and style-src without
// 'unsafe-inline') blocks code and styling that lives in the page itself:
// inline script blocks, inline event-handler attributes, javascript: URLs,
// <style> elements and style attributes. This file finds them in built HTML so
// a build can fail before a policy is enforced against them. It reads source,
// not a running server, so it covers every page of a static build, including
// ones no route probe reaches.
//
// It is a scanner, not a browser: it tokenises the way HTML does (comments,
// raw-text script and style, quoted attributes) but does not build a DOM or
// execute anything. Data blocks that the browser never runs (JSON-LD, a
// text/html template, an import map) are not findings.

// InlineKind names what a strict policy would block.
type InlineKind string

const (
	InlineScript       InlineKind = "inline script"
	InlineStyleElement InlineKind = "style element"
	InlineStyleAttr    InlineKind = "style attribute"
	InlineHandler      InlineKind = "event handler attribute"
	InlineJSURL        InlineKind = "javascript: URL"
)

// InlineKinds lists every kind, in the order reports print them.
var InlineKinds = []InlineKind{InlineScript, InlineStyleElement, InlineStyleAttr, InlineHandler, InlineJSURL}

// InlineFinding is one thing a strict policy would block.
type InlineFinding struct {
	Path   string // slash-separated path inside the audited tree; empty for ScanInline
	Line   int    // 1-based line of the tag
	Kind   InlineKind
	Detail string // the tag, and the attribute where one is involved, e.g. "div style"
}

// urlAttrs are the attributes whose value is a URL the browser may navigate to
// or load, so a javascript: value in them runs script.
var urlAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true,
	"xlink:href": true, "data": true, "poster": true, "background": true,
}

// ScanInline reports what a strict policy would block in one HTML document.
func ScanInline(doc []byte) []InlineFinding {
	s := scanner{src: string(doc), line: 1}
	for s.next() {
	}
	return s.found
}

type scanner struct {
	src   string
	pos   int
	line  int
	lined int // offset up to which newlines are already counted
	found []InlineFinding
}

func (s *scanner) lineAt(off int) int {
	s.line += strings.Count(s.src[s.lined:off], "\n")
	s.lined = off
	return s.line
}

func (s *scanner) add(off int, kind InlineKind, detail string) {
	s.found = append(s.found, InlineFinding{Line: s.lineAt(off), Kind: kind, Detail: detail})
}

// indexFold returns the index of the first case-insensitive occurrence of sub
// (which must be lower case) in s, or -1. It does not copy s: a saved page can be
// tens of megabytes and this runs once per raw-text element.
func indexFold(s, sub string) int {
	if sub == "" {
		return 0
	}
	first, upper := sub[0], sub[0]&^0x20
	for i := 0; i+len(sub) <= len(s); i++ {
		if c := s[i]; c != first && (first < 'a' || first > 'z' || c != upper) {
			continue
		}
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// skipTo moves past the next occurrence of end (lower case, matched
// case-insensitively), or to the end of the input when it never appears, as a
// browser does. It returns the text skipped over.
func (s *scanner) skipTo(end string) (body string) {
	rest := s.src[s.pos:]
	i := indexFold(rest, end)
	if i < 0 {
		s.pos = len(s.src)
		return rest
	}
	s.pos += i + len(end)
	return rest[:i]
}

// next consumes one construct and reports whether input remains.
func (s *scanner) next() bool {
	i := strings.IndexByte(s.src[s.pos:], '<')
	if i < 0 {
		return false
	}
	start := s.pos + i
	s.pos = start + 1
	if s.pos >= len(s.src) {
		return false
	}
	switch c := s.src[s.pos]; {
	case strings.HasPrefix(s.src[s.pos:], "!--"):
		s.pos += 3
		s.skipTo("-->")
	case strings.HasPrefix(s.src[s.pos:], "![CDATA["):
		s.pos += len("![CDATA[")
		s.skipTo("]]>")
	case c == '!' || c == '?' || c == '/':
		s.skipTo(">")
	case isAlpha(c):
		s.startTag(start)
	}
	return true
}

func isAlpha(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

type attr struct{ name, value string }

// startTag parses a start tag whose "<" is at start, records findings for it,
// and consumes the raw-text body of script and style and the text of title and
// textarea, whose contents are not markup.
func (s *scanner) startTag(start int) {
	b := s.src
	n := s.pos
	for n < len(b) && !isSpace(b[n]) && b[n] != '>' && b[n] != '/' {
		n++
	}
	name := strings.ToLower(b[s.pos:n])
	s.pos = n

	var attrs []attr
	for s.pos < len(b) {
		for s.pos < len(b) && (isSpace(b[s.pos]) || b[s.pos] == '/') {
			s.pos++
		}
		if s.pos >= len(b) {
			break
		}
		if b[s.pos] == '>' {
			s.pos++
			break
		}
		a := s.pos
		for s.pos < len(b) && !isSpace(b[s.pos]) && b[s.pos] != '=' && b[s.pos] != '>' && b[s.pos] != '/' {
			s.pos++
		}
		an := strings.ToLower(b[a:s.pos])
		for s.pos < len(b) && isSpace(b[s.pos]) {
			s.pos++
		}
		val := ""
		if s.pos < len(b) && b[s.pos] == '=' {
			s.pos++
			for s.pos < len(b) && isSpace(b[s.pos]) {
				s.pos++
			}
			switch {
			case s.pos < len(b) && (b[s.pos] == '"' || b[s.pos] == '\''):
				q := b[s.pos]
				s.pos++
				v := s.pos
				for s.pos < len(b) && b[s.pos] != q {
					s.pos++
				}
				val = b[v:s.pos]
				if s.pos < len(b) {
					s.pos++
				}
			default:
				v := s.pos
				for s.pos < len(b) && !isSpace(b[s.pos]) && b[s.pos] != '>' {
					s.pos++
				}
				val = b[v:s.pos]
			}
		}
		if an != "" {
			attrs = append(attrs, attr{an, val})
		}
	}

	for _, a := range attrs {
		switch {
		case a.name == "style":
			s.add(start, InlineStyleAttr, name+" style")
		case len(a.name) > 2 && strings.HasPrefix(a.name, "on") && isLetters(a.name[2:]):
			s.add(start, InlineHandler, name+" "+a.name)
		case urlAttrs[a.name] && isJavaScriptURL(a.value):
			s.add(start, InlineJSURL, name+" "+a.name)
		}
	}

	switch name {
	case "script":
		body := s.skipTo("</script")
		s.skipTo(">")
		if hasAttr(attrs, "src") || strings.TrimSpace(body) == "" || !executableScript(attrValue(attrs, "type")) {
			return
		}
		s.add(start, InlineScript, "script")
	case "style":
		body := s.skipTo("</style")
		s.skipTo(">")
		if strings.TrimSpace(body) != "" {
			s.add(start, InlineStyleElement, "style")
		}
	case "title", "textarea":
		s.skipTo("</" + name)
		s.skipTo(">")
	}
}

func isLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isAlpha(s[i]) {
			return false
		}
	}
	return s != ""
}

func hasAttr(attrs []attr, name string) bool {
	for _, a := range attrs {
		if a.name == name {
			return true
		}
	}
	return false
}

func attrValue(attrs []attr, name string) string {
	for _, a := range attrs {
		if a.name == name {
			return a.value
		}
	}
	return ""
}

// isJavaScriptURL reports whether a URL attribute value runs script. Browsers
// decode character references and drop tabs and newlines inside a URL, and
// ignore leading whitespace and control characters, so this does too.
func isJavaScriptURL(v string) bool {
	v = html.UnescapeString(v)
	v = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, v)
	v = strings.TrimLeft(v, " \f\v\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f")
	return len(v) >= 11 && strings.EqualFold(v[:11], "javascript:")
}

// executableScript reports whether a <script type> value is one the browser
// runs. An absent type, "module" and the JavaScript MIME types run; anything
// else is a data block (JSON-LD, a template, an import map).
func executableScript(typ string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch t {
	case "", "module", "text/javascript", "application/javascript", "application/x-javascript",
		"text/ecmascript", "application/ecmascript", "text/jscript", "text/livescript",
		"text/x-javascript", "text/x-ecmascript", "application/x-ecmascript", "text/javascript1.0",
		"text/javascript1.1", "text/javascript1.2", "text/javascript1.3", "text/javascript1.4",
		"text/javascript1.5", "text/jscript1.0":
		return true
	}
	return false
}

// InlineOptions tunes AuditInline.
type InlineOptions struct {
	// Allow lists path.Match patterns, matched against each page's slash path
	// inside the tree, for pages that are skipped (an archived third-party
	// document that cannot be made clean). A page is skipped, not partly
	// allowed; say why next to the pattern where it is set.
	Allow []string
}

// InlineReport is the result of AuditInline.
type InlineReport struct {
	Pages    int             // pages scanned (.html and .htm files, minus Skipped)
	Skipped  []string        // pages matched by an Allow pattern
	Findings []InlineFinding // ordered by path, then line
}

// InlineCount is a per-kind tally.
type InlineCount struct {
	Findings int // occurrences
	Pages    int // distinct pages with at least one
}

// Counts tallies the findings by kind.
func (r InlineReport) Counts() map[InlineKind]InlineCount {
	out := map[InlineKind]InlineCount{}
	seen := map[InlineKind]map[string]bool{}
	for _, f := range r.Findings {
		c := out[f.Kind]
		c.Findings++
		if seen[f.Kind] == nil {
			seen[f.Kind] = map[string]bool{}
		}
		if !seen[f.Kind][f.Path] {
			seen[f.Kind][f.Path] = true
			c.Pages++
		}
		out[f.Kind] = c
	}
	return out
}

// AuditInline scans every .html and .htm file under fsys (for a directory, pass
// os.DirFS(dir)) and reports what a strict policy would block.
func AuditInline(fsys fs.FS, opts InlineOptions) (InlineReport, error) {
	for _, p := range opts.Allow {
		if _, err := path.Match(p, ""); err != nil {
			return InlineReport{}, fmt.Errorf("smoke: bad allow pattern %q: %w", p, err)
		}
	}
	var rep InlineReport
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := strings.ToLower(path.Ext(p)); ext != ".html" && ext != ".htm" {
			return nil
		}
		for _, pat := range opts.Allow {
			if ok, _ := path.Match(pat, p); ok {
				rep.Skipped = append(rep.Skipped, p)
				return nil
			}
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rep.Pages++
		for _, f := range ScanInline(data) {
			f.Path = p
			rep.Findings = append(rep.Findings, f)
		}
		return nil
	})
	if err != nil {
		return InlineReport{}, err
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		if rep.Findings[i].Path != rep.Findings[j].Path {
			return rep.Findings[i].Path < rep.Findings[j].Path
		}
		return rep.Findings[i].Line < rep.Findings[j].Line
	})
	return rep, nil
}
