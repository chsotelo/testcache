package fingerprint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustNormEqual(t *testing.T, path, a, b string) {
	t.Helper()
	ra := NormalizeSource(path, []byte(a))
	rb := NormalizeSource(path, []byte(b))
	if !ra.OK || !rb.OK {
		t.Fatalf("expected OK for %s: ra=(%v,%q) rb=(%v,%q)", path, ra.OK, ra.Reason, rb.OK, rb.Reason)
	}
	if string(ra.Data) != string(rb.Data) {
		t.Fatalf("expected equal normalized output for %s:\nA: %q\nB: %q", path, string(ra.Data), string(rb.Data))
	}
}

func mustNormDiff(t *testing.T, path, a, b string) {
	t.Helper()
	ra := NormalizeSource(path, []byte(a))
	rb := NormalizeSource(path, []byte(b))
	if ra.OK && rb.OK && string(ra.Data) == string(rb.Data) {
		t.Fatalf("expected DIFFERENT normalized output for %s, both gave:\n%q", path, string(ra.Data))
	}
}

func TestNormalizeCommentOnlyEditsAndExtensions(t *testing.T) {
	exts := []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}
	for _, ext := range exts {
		p := "src/mod" + ext
		mustNormEqual(t, p,
			"export const x = 1 + 2;\nexport function f(a) { return a; }\n",
			"// header comment\nexport const x = 1 + /* inline */ 2;\n\nexport function f(a) { return a; } // tail\n",
		)
		mustNormEqual(t, p,
			"const a = 1;\nconst b = 2;",
			"/* multi\n   line */\nconst a = 1;\r\n  \t \nconst b = 2;\n// eof\n",
		)
	}
	mustNormEqual(t, "styles/app.css",
		".btn {\ncolor: red;\n}",
		"/* top */\n.btn {\n  /* mid */\n  color: red;\n}\n/* bottom */",
	)
}

func TestNormalizeStringsTemplatesAndRegexes(t *testing.T) {
	p := "src/test.ts"
	// Strings untouched.
	mustNormDiff(t, p, `const s = "// not a comment";`, `const s = "// edited";`)
	mustNormDiff(t, p, `const s = '/* a */';`, `const s = '/* b */';`)
	mustNormDiff(t, p, "const s = \"a\\\nb\";", "const s = \"a\\\nc\";")

	// Template literals including nested ${...} to arbitrary depth.
	mustNormDiff(t, p,
		"const t = `a /* keep */ ${`b ${x /* c */} c`} d`;",
		"const t = `a /* diff */ ${`b ${x /* c */} c`} d`;",
	)
	mustNormDiff(t, p,
		"const t = `a ${`b ${`c ${y}`}`} d`;",
		"const t = `a ${`b ${`c ${z}`}`} d`;",
	)
	mustNormEqual(t, p,
		"const t = `a ${ `b ${ { k: 1 }.k }`} d`;",
		"const t = `a ${/* c1 */ `b ${/* c2 */ { k: 1 }.k /* c3 */}`} d`;",
	)
	mustNormEqual(t, p,
		"const t = `a ${/* x */ `b ${/* y */ { k: 1 }.k /* z */}`} d`;",
		"const t = `a ${/* c1 */ `b ${/* c2 */ { k: 1 }.k /* c3 */}`} d`;",
	)
	mustNormDiff(t, p,
		"const t = `\\${notExpr} \\` // keep`;",
		"const t = `\\${notExpr} \\` // diff`;",
	)

	// Regex literals vs division.
	mustNormDiff(t, p, `const r = /foo\/bar/gi;`, `const r = /foo\/baz/gi;`)
	mustNormDiff(t, p, `const r = /[/]a/g;`, `const r = /[/]b/g;`)
	mustNormEqual(t, p, `const x = a / b / c;`, `const x = a /* c1 */ / b / /* c2 */ c;`)
	mustNormEqual(t, p, `if (ok) /re/.test(s);`, `if (ok) /* c */ /re/.test(s);`)
	mustNormDiff(t, p, `if (ok) /re1/.test(s);`, `if (ok) /re2/.test(s);`)
	mustNormEqual(t, p, `while (ok) /re/.test(s);`, `while (ok) /* c */ /re/.test(s);`)
	mustNormEqual(t, p, `for (;;) /re/.test(s);`, `for (;;) /* c */ /re/.test(s);`)
	mustNormEqual(t, p, `const x = (a + b) / 2;`, `const x = (a + b) /* c */ / 2;`)
	mustNormEqual(t, p, `const x = obj.return / 2;`, `const x = obj.return /* c */ / 2;`)
	mustNormEqual(t, p, `const x = val! / 2;`, `const x = val! /* c */ / 2;`)
	mustNormDiff(t, p, `const x = !/a/.test(s);`, `const x = !/b/.test(s);`)
	mustNormEqual(t, p, `if (a) { b(); } /re/.test(s);`, `if (a) { b(); } /* c */ /re/.test(s);`)
}

func TestNormalizeJSXAndTSXRules(t *testing.T) {
	for _, p := range []string{"src/Comp.tsx", "src/Comp.jsx", "src/Comp.js"} {
		// JSX text keeps // and /* verbatim.
		mustNormDiff(t, p, `export const C = () => <a>http://x</a>;`, `export const C = () => <a>http://y</a>;`)
		mustNormDiff(t, p, `export const C = () => <a>/* keep */</a>;`, `export const C = () => <a>/* diff */</a>;`)
		// Single-line JSX whitespace is kept byte-exact.
		mustNormDiff(t, p, `export const C = () => <div>a  b</div>;`, `export const C = () => <div>a b</div>;`)
		// Multi-line JSX whitespace collapses to \n.
		mustNormEqual(t, p,
			"export const C = () => (\n  <div>\n    <span>a</span>\n  </div>\n);",
			"export const C = () => (\n  <div>\n  <span>a</span>\n  </div>\n);",
		)
		// Comments inside JSX expression containers and JSX opening tags are stripped.
		mustNormEqual(t, p,
			`export const C = () => <div id="a">{ 1 + 2 }</div>;`,
			`export const C = () => <div /* c1 */ id="a">{/* c2 */ 1 + 2 /* c3 */}</div>;`,
		)
		mustNormEqual(t, p,
			`export const C = () => <div /* x */ id="a">{/* y */ 1 + 2 /* z */}</div>;`,
			`export const C = () => <div /* c1 */ id="a">{/* c2 */ 1 + 2 /* c3 */}</div>;`,
		)
		// Quotes and backticks inside JSX children are literal text.
		mustNormDiff(t, p,
			"export const C = () => <div>don't \"stop\" `a`</div>;",
			"export const C = () => <div>don't \"stop\" `b`</div>;",
		)
		// Fragments and nested JSX inside attributes.
		mustNormEqual(t, p,
			`export const C = () => <><span title="http://a">{ <b />}</span></>;`,
			`export const C = () => <><span /* c */ title="http://a">{/* d */ <b />}</span></>;`,
		)
		mustNormDiff(t, p,
			`export const C = () => <>http://frag1</>;`,
			`export const C = () => <>http://frag2</>;`,
		)
	}
}

func TestNormalizeTSXGenericsAndAttrs(t *testing.T) {
	// TSX generics & generic JSX components.
	mustNormEqual(t, "src/Gen.tsx",
		"export const id = <T,>(x: T): T => x;\nexport const el = <Select< Item> v={1} />;",
		"export const id = <T,>(x: T): T => /* c */ x;\nexport const el = <Select</* c */ Item> /* a */ v={1} />;",
	)
	mustNormEqual(t, "src/Gen2.tsx",
		"export const fn: <T>(a: T) => T = (a) => a;\nexport const g = <T extends object>(x: T) => x;",
		"export const fn: <T>(a: T) => T = (a) => /* c */ a;\nexport const g = <T extends object>(x: T) => /* c */ x;",
	)
	// JSX attribute string does not treat \" as an escape.
	mustNormDiff(t, "src/Attr.tsx",
		`export const A = () => <div title="\" />;`,
		`export const A = () => <div title="a\" />;`,
	)
}

func TestNormalizeMeaningfulCommentsAndHashbang(t *testing.T) {
	meaningful := []string{
		"// @vitest-environment jsdom\nexport const a = 1;",
		"/* @jsxImportSource preact */\nexport const a = 1;",
		"export const m = import(/* @vite-ignore */ path);",
		"export const b = /*#__PURE__*/ make();",
		"export const b = /*@__PURE__*/ make();",
		"/*#__NO_SIDE_EFFECTS__*/ export function pure() {}",
		"export const a = 1;\n//# sourceMappingURL=a.js.map",
		"/*! legal */\nexport const a = 1;",
		"//! legal\nexport const a = 1;",
		"//@ sourceMappingURL=a.js.map\nexport const a = 1;",
		"// @jsx h\nexport const a = 1;",
		"/** @param x\n * @custom-plugin-flag */\nexport const a = 1;",
	}
	base := "export const a = 1;"
	for _, src := range meaningful {
		mustNormDiff(t, "src/a.ts", base, src)
	}
	mustNormDiff(t, "src/env.test.ts",
		"// @vitest-environment jsdom\nexport const a = 1;",
		"// @vitest-environment node\nexport const a = 1;",
	)
	mustNormEqual(t, "bin/cli.js",
		"#!/usr/bin/env node\nconsole.log(1);",
		"#!/usr/bin/env node\n// comment after hashbang\nconsole.log(1);",
	)
	mustNormDiff(t, "bin/cli.js",
		"#!/usr/bin/env node\nconsole.log(1);",
		"#!/usr/bin/env bun\nconsole.log(1);",
	)
}

func TestNormalizeASICases(t *testing.T) {
	p := "src/asi.js"
	mustNormDiff(t, p, "a\n++b", "a++\nb")
	mustNormDiff(t, p, "a\n/* c */\n++b", "a /* c */ ++b")
	mustNormDiff(t, p, "return\n/* c */\nx", "return /* c */ x")
	mustNormDiff(t, p, "return/*\n*/x", "return/* */x")
	mustNormDiff(t, p, "throw\n/* c */\nerr", "throw /* c */ err")
	mustNormEqual(t, p, "return\nx", "return\n/* comment */\nx")
	mustNormEqual(t, p, "a\n++b", "a\n/* comment */\n++b")
}

func TestNormalizeCSSRules(t *testing.T) {
	p := "styles/main.css"
	mustNormEqual(t, p, "div .foo { color: red; }", "div /* c */ .foo { color: red; }")
	mustNormDiff(t, p, "div/*c*/.foo { color: red; }", "div .foo { color: red; }")
	mustNormDiff(t, p, "div/*c*/.foo { color: red; }", "div.foo { color: red; }")
	mustNormEqual(t, p, "div/*c1*/.foo { color: red; }", "div/*c2*/.foo { color: red; }")
	mustNormDiff(t, p, `.a { content: "/* keep */"; }`, `.a { content: "/* diff */"; }`)
	mustNormDiff(t, p, `.a { background: url(/* a */); }`, `.a { background: url(/* b */); }`)
	mustNormDiff(t, p, `.a { background: url("/* a */"); }`, `.a { background: url("/* b */"); }`)
	mustNormDiff(t, p, "/*! keep */ .a {}", "/*! diff */ .a {}")
	mustNormDiff(t, p, "/* @layer base */ .a {}", "/* @layer app */ .a {}")
	mustNormDiff(t, p, "/* #id */ .a {}", "/* #other */ .a {}")
}

func TestNormalizeFallbacksAndVerboseLogging(t *testing.T) {
	invalid := []struct {
		name string
		src  string
	}{
		{"unterminated_string.ts", `const s = "unterminated`},
		{"unterminated_comment.ts", `const x = 1; /* open`},
		{"unterminated_regex.ts", `const r = /abc`},
		{"unterminated_template.ts", "const t = `open ${1}"},
		{"mismatched_jsx.tsx", `export const A = () => <div></span>;`},
		{"unclosed_jsx.tsx", `export const A = () => <div>hello;`},
		{"unterminated_css.css", `.a { /* open`},
	}
	dir := t.TempDir()
	for _, tc := range invalid {
		p := filepath.Join(dir, tc.name)
		if err := os.WriteFile(p, []byte(tc.src), 0o644); err != nil {
			t.Fatal(err)
		}
		var logs []string
		h := NewHasher(dir, nil, nil)
		h.Configure(HasherOptions{
			NormalizeHash: true,
			VerboseLog: func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
		})
		got, err := h.HashDependency(p)
		if err != nil {
			t.Fatalf("%s error: %v", tc.name, err)
		}
		if !strings.HasPrefix(got, rawHashPrefix) {
			t.Fatalf("%s expected raw fallback %s, got %q", tc.name, rawHashPrefix, got)
		}
		if len(logs) != 1 || !strings.Contains(logs[0], tc.name) {
			t.Fatalf("%s expected verbose fallback log, got %v", tc.name, logs)
		}
	}
}

func TestNormalizeNonSourceAndDisabledSwitch(t *testing.T) {
	dir := t.TempDir()
	// Non-normalizable extension keeps raw hash.
	jsonPath := filepath.Join(dir, "data.json")
	if err := os.WriteFile(jsonPath, []byte(`{"a": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHasher(dir, nil, nil)
	gotJSON, err := h.HashDependency(jsonPath)
	if err != nil || !strings.HasPrefix(gotJSON, rawHashPrefix) {
		t.Fatalf("expected the content hash for json, got %q err=%v", gotJSON, err)
	}

	// NormalizeHash=false disables normalization.
	tsPath := filepath.Join(dir, "off.ts")
	_ = os.WriteFile(tsPath, []byte("export const a = 1;"), 0o644)
	h.Configure(HasherOptions{NormalizeHash: false})
	h1, _ := h.HashDependency(tsPath)
	_ = os.WriteFile(tsPath, []byte("export const a = 1; // comment"), 0o644)
	h2, _ := h.HashDependency(tsPath)
	if h1 == h2 {
		t.Fatalf("expected NormalizeHash=false to treat comment edit as changed")
	}
}

// Documentation comments change nothing at run time, whatever tags they hold: editing them
// keeps the cache. Type-checker directives only matter to --typecheck, which is not cached.
func TestNormalizeDocumentationCommentsAreStripped(t *testing.T) {
	base := "export function cn(a) { return a; }"
	for _, src := range []string{
		"/**\n * Merges class names.\n * @param {...any} a class values\n * @returns {string}\n */\nexport function cn(a) { return a; }",
		"// @ts-check\nexport function cn(a) { return a; }",
		"// @ts-expect-error\nexport function cn(a) { return a; }",
		"/** @type {import('./x').T} @see https://example.com/a#b */\nexport function cn(a) { return a; }",
		"// ask name@example.com, see issue #123\nexport function cn(a) { return a; }",
		"/** @deprecated use cx. @since 1.2 @example cn('a') */\nexport function cn(a) { return a; }",
	} {
		mustNormEqual(t, "src/utils.js", base, src)
	}
	// CSS keeps the conservative rule.
	mustNormDiff(t, "styles/a.css", ".a { color: red; }", "/* @param x */\n.a { color: red; }")
}

// A {/* comment */} child alone on its line renders nothing: adding or removing one keeps the
// cache. Next to text on the same line it separates two text children, so it still counts.
func TestNormalizeCommentOnlyJSXChildren(t *testing.T) {
	for _, p := range []string{"src/Table.jsx", "src/Table.tsx", "src/Table.js"} {
		mustNormEqual(t, p,
			"export const T = () => (\n  <div>\n    <span>a</span>\n  </div>\n);",
			"export const T = () => (\n  <div>\n    {/* Table container */}\n    <span>a</span>\n    {/* multi\n       line */}\n    {// line comment\n    }\n  </div>\n);",
		)
		mustNormDiff(t, p,
			"export const T = () => <div>foo  bar</div>;",
			"export const T = () => <div>foo {/* c */} bar</div>;",
		)
		mustNormDiff(t, p,
			"export const T = () => (\n  <div>\n    <span>a</span>\n  </div>\n);",
			"export const T = () => (\n  <div>\n    {/* @vite-ignore */}\n    <span>a</span>\n  </div>\n);",
		)
		// Between two lines of one text it splits that text into two children.
		mustNormDiff(t, p,
			"export const T = () => (\n  <p>\n    first line\n    second line\n  </p>\n);",
			"export const T = () => (\n  <p>\n    first line\n    {/* c */}\n    second line\n  </p>\n);",
		)
		// Next to text on one side and a tag on the other it renders nothing.
		mustNormEqual(t, p,
			"export const T = () => (\n  <p>\n    first line\n    <b>x</b>\n  </p>\n);",
			"export const T = () => (\n  <p>\n    first line\n    {/* c */}\n    <b>x</b>\n  </p>\n);",
		)
		// An expression is not a comment.
		mustNormDiff(t, p,
			"export const T = () => (\n  <div>\n    <span>a</span>\n  </div>\n);",
			"export const T = () => (\n  <div>\n    {/* c */ x}\n    <span>a</span>\n  </div>\n);",
		)
	}
}
