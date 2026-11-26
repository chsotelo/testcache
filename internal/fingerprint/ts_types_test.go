package fingerprint

import "testing"

func normalizedTS(t *testing.T, src string) string {
	t.Helper()
	res := NormalizeSource("mod.ts", []byte(src))
	if !res.OK {
		t.Fatalf("normalize failed: %s\n%s", res.Reason, src)
	}
	return string(res.Data)
}

func TestTypeOnlyStatementsDoNotChangeTheHash(t *testing.T) {
	const code = "import { a } from \"./a\";\nexport const x = a + 1;"
	cases := map[string]string{
		"import type named":       "import type { A, B } from \"./types\";\n" + code,
		"import type default":     "import type A from \"./types\";\n" + code,
		"import type namespace":   "import type * as T from \"./types\"\n" + code,
		"import type multiline":   "import type {\n  A,\n  B,\n} from \"./types\";\n" + code,
		"export type list":        code + "\nexport type { A } from \"./types\";",
		"export type star":        code + "\nexport type * from \"./types\";",
		"alias":                   "type A = string;\n" + code,
		"alias without semicolon": "type A = string\n" + code,
		"exported alias":          "export type A = components[\"schemas\"][\"A\"];\n" + code,
		"union on several lines":  "export type A =\n  | \"x\"\n  | \"y\";\n" + code,
		"object type":             "type A = {\n  a: string;\n  b: () => void;\n};\n" + code,
		"generic on many lines":   "type A = Map<\n  string,\n  number\n>;\n" + code,
		"conditional":             "type A<T> = T extends string\n  ? 1\n  : 2;\n" + code,
		"arrow on two lines":      "type F = (a: number) =>\n  void\n" + code,
		"interface":               "interface A {\n  a: string;\n}\n" + code,
		"exported interface":      "export interface A<T extends { k: 1 }> extends B {\n  a: T;\n}\n" + code,
		"interface with defaults": "interface A<T extends { k: 1 } = { k: 1 }, U = {} | null> extends B {\n  a: T;\n}\n" + code,
		"between statements":      "import { a } from \"./a\";\ntype A = 1;\nexport const x = a + 1;",
		"at the end":              code + "\ntype A = { a: 1 }[\"a\"]",
		"brace in a string":       "type A = \"}\" | '{';\n" + code,
	}
	want := normalizedTS(t, code)
	for name, src := range cases {
		if got := normalizedTS(t, src); got != want {
			t.Errorf("%s: type-only statement changed the normalized source\n got: %q\nwant: %q", name, got, want)
		}
	}
}

func TestCodeNextToTypesStillChangesTheHash(t *testing.T) {
	pairs := map[string][2]string{
		"statement after alias without semicolon": {"type A = string\nconst x = 1", "type A = string\nconst x = 2"},
		"call after alias without semicolon":      {"type A = string\n(function () { run(1) })()", "type A = string\n(function () { run(2) })()"},
		"statement on the interface line":         {"interface A {} const x = 1;", "interface A {} const x = 2;"},
		"statement on the alias line":             {"type A = 1; const x = 1;", "type A = 1; const x = 2;"},
		"default import named type":               {"import type from \"./a\";", "import type from \"./b\";"},
		"default import named type with list":     {"import type, { a } from \"./a\";", "import type, { b } from \"./a\";"},
		"value import":                            {"import { a } from \"./a\";", "import { a } from \"./b\";"},
		"inline type import keeps the module":     {"import { type A } from \"./a\";", "import { type A } from \"./b\";"},
		"variable named type":                     {"let type = 1;\ntype = 2;", "let type = 1;\ntype = 3;"},
		"enum":                                    {"export enum E { A = 1 }", "export enum E { A = 2 }"},
		"type inside a function":                  {"function f() {\n  type A = 1;\n  return 1;\n}", "function f() {\n  type A = 1;\n  return 2;\n}"},
		"type text inside a template":             {"const s = `\ntype A = 1;\n`;", "const s = `\ntype A = 2;\n`;"},
		"export of a value":                       {"export { a } from \"./a\";", "export { a } from \"./b\";"},
	}
	for name, p := range pairs {
		if normalizedTS(t, p[0]) == normalizedTS(t, p[1]) {
			t.Errorf("%s: a change in code was normalized away\n%q\n%q", name, p[0], p[1])
		}
	}
}

func TestTypeStatementsAreKeptInJavaScript(t *testing.T) {
	a := NormalizeSource("mod.js", []byte("type A = 1;\nconst x = 1;"))
	b := NormalizeSource("mod.js", []byte("const x = 1;"))
	if string(a.Data) == string(b.Data) {
		t.Fatal("a .js file must be hashed as written")
	}
}
