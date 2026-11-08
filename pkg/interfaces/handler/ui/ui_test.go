package ui

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestDictLanguagesHaveTheSameKeys reads the embedded app.js and checks that
// every language in DICT has exactly the keys of DICT.en, so a string added
// for one language cannot silently fall back to English (or to its key) in
// another.
func TestDictLanguagesHaveTheSameKeys(t *testing.T) {
	src, err := static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	langs, err := dictKeys(string(src))
	if err != nil {
		t.Fatal(err)
	}
	en, ok := langs["en"]
	if !ok || len(en) == 0 {
		t.Fatalf("DICT.en not found or empty: %v", langs)
	}
	if len(langs) < 2 {
		t.Fatalf("only %d language(s) in DICT", len(langs))
	}
	for lang, keys := range langs {
		if dup := duplicates(keys); len(dup) > 0 {
			t.Errorf("DICT.%s: duplicate keys %v", lang, dup)
		}
		if missing := minus(en, keys); len(missing) > 0 {
			t.Errorf("DICT.%s lacks %v", lang, missing)
		}
		if extra := minus(keys, en); len(extra) > 0 {
			t.Errorf("DICT.%s has keys DICT.en lacks: %v", lang, extra)
		}
	}
}

// TestUIStringsAreInDict checks that every key the static HTML names
// (data-i18n, data-i18n-html, data-i18n-label) and every literal t('key') call in app.js
// exists in DICT.en.
func TestUIStringsAreInDict(t *testing.T) {
	js, err := static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := static.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	langs, err := dictKeys(string(js))
	if err != nil {
		t.Fatal(err)
	}
	var used []string
	for _, attr := range []string{`data-i18n="`, `data-i18n-html="`, `data-i18n-label="`} {
		used = append(used, between(string(html), attr, `"`)...)
	}
	for _, m := range tCall.FindAllStringSubmatch(string(js), -1) {
		used = append(used, m[1])
	}
	if len(used) == 0 {
		t.Fatal("found no i18n keys in use")
	}
	if missing := minus(used, langs["en"]); len(missing) > 0 {
		t.Errorf("keys used but not in DICT.en: %v", missing)
	}
}

func TestDictKeysParser(t *testing.T) {
	src := "const x = 1;\nconst DICT = {\n" +
		"  en: { 'a.b': 'x: y', c: (n) => `n: ${n} ${`inner: ${'}'}`}`, // d: comment\n" +
		"    /* e: block */ \"f\": [1, {g: 2}], h: \"q\\\" i: \" },\n" +
		"  ja: { 'a.b': 'z', c: 1 },\n};"
	got, err := dictKeys(src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.b", "c", "f", "h"}; !slices.Equal(got["en"], want) {
		t.Errorf("en = %v, want %v", got["en"], want)
	}
	if want := []string{"a.b", "c"}; !slices.Equal(got["ja"], want) {
		t.Errorf("ja = %v, want %v", got["ja"], want)
	}
}

// tCall is a call of the t() helper with a literal key, not a method that
// happens to end in t (URLSearchParams.get('lang')).
var tCall = regexp.MustCompile(`(?:^|[^\w.$])t\('([^']+)'`)

// dictKeys returns the keys of each language object in `const DICT = {...}`.
// It is a small scanner, not a JavaScript parser: enough for an object of
// objects whose values are strings, template literals and arrow functions.
func dictKeys(src string) (map[string][]string, error) {
	start := strings.Index(src, "const DICT = {")
	if start < 0 {
		return nil, fmt.Errorf("const DICT not found")
	}
	s := &scanner{src: src, i: start + len("const DICT = ")}
	out := map[string][]string{}
	err := s.object(func(lang string) error {
		s.space()
		if s.peek() != '{' {
			return fmt.Errorf("DICT.%s is not an object literal", lang)
		}
		return s.object(func(key string) error {
			out[lang] = append(out[lang], key)
			return s.value()
		})
	})
	return out, err
}

type scanner struct {
	src string
	i   int
}

func (s *scanner) peek() byte {
	if s.i >= len(s.src) {
		return 0
	}
	return s.src[s.i]
}

// space skips white space and comments.
func (s *scanner) space() {
	for s.i < len(s.src) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(s.src[s.i])):
			s.i++
		case strings.HasPrefix(s.src[s.i:], "//"):
			if n := strings.IndexByte(s.src[s.i:], '\n'); n >= 0 {
				s.i += n
			} else {
				s.i = len(s.src)
			}
		case strings.HasPrefix(s.src[s.i:], "/*"):
			if n := strings.Index(s.src[s.i+2:], "*/"); n >= 0 {
				s.i += n + 4
			} else {
				s.i = len(s.src)
			}
		default:
			return
		}
	}
}

// object reads `{ key: value, ... }`, calling each for every key with the
// scanner placed after the colon; each must consume the value.
func (s *scanner) object(each func(key string) error) error {
	s.space()
	if s.peek() != '{' {
		return fmt.Errorf("expected { at %d", s.i)
	}
	s.i++
	for {
		s.space()
		if s.peek() == '}' {
			s.i++
			return nil
		}
		key, err := s.key()
		if err != nil {
			return err
		}
		s.space()
		if s.peek() != ':' {
			return fmt.Errorf("expected : after %q at %d", key, s.i)
		}
		s.i++
		if err := each(key); err != nil {
			return err
		}
		s.space()
		switch s.peek() {
		case ',':
			s.i++
		case '}':
		default:
			return fmt.Errorf("expected , or } after %q at %d", key, s.i)
		}
	}
}

func (s *scanner) key() (string, error) {
	switch c := s.peek(); {
	case c == '\'' || c == '"':
		from := s.i + 1
		if err := s.str(c); err != nil {
			return "", err
		}
		return s.src[from : s.i-1], nil
	case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		from := s.i
		for s.i < len(s.src) && (s.src[s.i] == '_' || s.src[s.i] == '$' || s.src[s.i] == '.' ||
			s.src[s.i] >= 'a' && s.src[s.i] <= 'z' || s.src[s.i] >= 'A' && s.src[s.i] <= 'Z' || s.src[s.i] >= '0' && s.src[s.i] <= '9') {
			s.i++
		}
		return s.src[from:s.i], nil
	default:
		return "", fmt.Errorf("unexpected %q at %d", c, s.i)
	}
}

// value skips one property value: everything up to the , or } that closes
// it at nesting depth 0.
func (s *scanner) value() error {
	depth := 0
	for s.i < len(s.src) {
		s.space()
		switch c := s.peek(); c {
		case '\'', '"':
			if err := s.str(c); err != nil {
				return err
			}
		case '`':
			if err := s.template(); err != nil {
				return err
			}
		case '(', '[', '{':
			depth++
			s.i++
		case ')', ']':
			depth--
			s.i++
		case '}':
			if depth == 0 {
				return nil
			}
			depth--
			s.i++
		case ',':
			if depth == 0 {
				return nil
			}
			s.i++
		default:
			s.i++
		}
	}
	return fmt.Errorf("unterminated value")
}

// str skips a '...' or "..." literal, the scanner on its opening quote.
func (s *scanner) str(q byte) error {
	for s.i++; s.i < len(s.src); s.i++ {
		switch s.src[s.i] {
		case '\\':
			s.i++
		case q:
			s.i++
			return nil
		}
	}
	return fmt.Errorf("unterminated string")
}

// template skips a `...` literal including ${...} substitutions, which may
// hold further strings and templates.
func (s *scanner) template() error {
	for s.i++; s.i < len(s.src); {
		switch {
		case s.src[s.i] == '\\':
			s.i += 2
		case s.src[s.i] == '`':
			s.i++
			return nil
		case strings.HasPrefix(s.src[s.i:], "${"):
			s.i++ // onto the {, which value() counts as depth 1
			if err := s.substitution(); err != nil {
				return err
			}
		default:
			s.i++
		}
	}
	return fmt.Errorf("unterminated template")
}

func (s *scanner) substitution() error {
	depth := 0
	for s.i < len(s.src) {
		switch c := s.src[s.i]; c {
		case '\'', '"':
			if err := s.str(c); err != nil {
				return err
			}
		case '`':
			if err := s.template(); err != nil {
				return err
			}
		case '{':
			depth++
			s.i++
		case '}':
			depth--
			s.i++
			if depth == 0 {
				return nil
			}
		default:
			s.i++
		}
	}
	return fmt.Errorf("unterminated substitution")
}

// between returns every substring of s found between open and the next end.
func between(s, open, end string) []string {
	var out []string
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return out
		}
		s = s[i+len(open):]
		j := strings.Index(s, end)
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j+len(end):]
	}
}

// minus returns the elements of a not in b, sorted.
func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}

func duplicates(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if seen[k] && !slices.Contains(out, k) {
			out = append(out, k)
		}
		seen[k] = true
	}
	return out
}

// TestLogoIsTheAppIcon keeps one brand mark: docs/logo.svg (the chart icon,
// the CNCF Landscape logo) is the icon the UI ships, byte for byte, and is a
// plain vector (no embedded raster, no text that depends on a font).
func TestLogoIsTheAppIcon(t *testing.T) {
	icon, err := static.ReadFile("static/icon.svg")
	if err != nil {
		t.Fatal(err)
	}
	logo, err := os.ReadFile("../../../../docs/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(icon, logo) {
		t.Error("docs/logo.svg differs from the UI icon (pkg/interfaces/handler/ui/static/icon.svg)")
	}
	plainVector(t, "docs/logo.svg", logo)
}

// TestWordmarkCarriesTheLogo: the wordmarks (the mark with the name, for the
// CNCF Landscape) draw the logo's own paths, unchanged, and the name as
// outlines, so they cannot drift from the icon or depend on a font.
func TestWordmarkCarriesTheLogo(t *testing.T) {
	logo, err := os.ReadFile("../../../../docs/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	marks := svgPath.FindAll(logo, -1)
	if len(marks) == 0 {
		t.Fatal("no paths in docs/logo.svg")
	}
	for _, name := range []string{"docs/wordmark.svg", "docs/wordmark-on-dark.svg"} {
		wm, err := os.ReadFile("../../../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		plainVector(t, name, wm)
		for _, m := range marks {
			if !bytes.Contains(wm, m) {
				t.Errorf("%s does not draw the logo path %.60s...: regenerate it with scripts/wordmark.py", name, m)
			}
		}
		if n := len(svgPath.FindAll(wm, -1)); n != len(marks)+1 {
			t.Errorf("%s has %d paths, want the logo's %d and one for the name", name, n, len(marks))
		}
	}
}

var svgPath = regexp.MustCompile(`<path fill="#[0-9a-f]{6}" d="[^"]+"/>`)

// plainVector checks an SVG has a viewBox and no embedded raster or text
// that depends on a font.
func plainVector(t *testing.T, name string, b []byte) {
	t.Helper()
	var doc struct {
		XMLName xml.Name
		ViewBox string `xml:"viewBox,attr"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil || doc.XMLName.Local != "svg" || doc.ViewBox == "" {
		t.Errorf("%s: not an SVG with a viewBox: %v %+v", name, err, doc)
	}
	for _, banned := range []string{"<image", "<text", "base64,"} {
		if bytes.Contains(b, []byte(banned)) {
			t.Errorf("%s contains %q", name, banned)
		}
	}
}

// TestHeaderShowsTheWordmark: the title in the header is the wordmark (the
// logo with the name), not text alone.
func TestHeaderShowsTheWordmark(t *testing.T) {
	html, err := static.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	title := between(string(html), `<div class="title">`, `</div>`)
	if len(title) == 0 {
		t.Fatal("no header title in index.html")
	}
	if !strings.Contains(title[0], `<h1><img class="wordmark" src="wordmark.svg" alt="kubeglance"`) {
		t.Errorf("header title is not the wordmark:\n%s", title[0])
	}
}

// TestUIWordmarkIsTheWordmark: the header's wordmark draws the same logo and
// name as docs/wordmark.svg (only the name's colour follows the theme), so
// the UI cannot drift from the brand. scripts/wordmark.py writes both.
func TestUIWordmarkIsTheWordmark(t *testing.T) {
	ui, err := static.ReadFile("static/wordmark.svg")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := os.ReadFile("../../../../docs/wordmark.svg")
	if err != nil {
		t.Fatal(err)
	}
	plainVector(t, "static/wordmark.svg", ui)
	d := regexp.MustCompile(` d="[^"]+"`)
	want, got := d.FindAll(docs, -1), d.FindAll(ui, -1)
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("static/wordmark.svg has %d paths, docs/wordmark.svg %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("path %d differs from docs/wordmark.svg: regenerate with scripts/wordmark.py", i)
		}
	}
}
