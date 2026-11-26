package fingerprint

import "bytes"

// A type-only statement leaves nothing in the code that runs, so a change inside one cannot
// change a test result. stripTypeStatements removes the top-level ones: "import type",
// "export type", type aliases and interfaces. Where the end of a statement is unclear it
// removes less, never more: what stays is hashed like any other code.

const (
	kwImport    = "import"
	kwExport    = "export"
	kwType      = "type"
	kwInterface = "interface"
	kwFrom      = "from"
)

type typeStmtKind uint8

const (
	stmtImportType typeStmtKind = iota
	stmtExportType
	stmtTypeAlias
)

func isTypeScriptExt(ext sourceExt) bool {
	switch ext {
	case extTS, extTSX, extMTS, extCTS:
		return true
	default:
		return false
	}
}

// stripTypeStatements removes the type-only statements that begin at one of starts, the
// offsets in src of the words that open a top-level line.
func stripTypeStatements(src []byte, starts []int) []byte {
	var out []byte
	pos := 0
	for _, s := range starts {
		if s < pos {
			continue
		}
		end := typeStatementEnd(src, s)
		if end <= s {
			continue
		}
		out = append(out, src[pos:s]...)
		pos = end
		if pos < len(src) && src[pos] == '\n' {
			pos++
		}
	}
	if pos == 0 {
		return src
	}
	return bytes.TrimRight(append(out, src[pos:]...), "\n")
}

// typeStatementEnd returns where the type-only statement starting at s ends, or s when the
// statement there is not one this file recognizes.
func typeStatementEnd(src []byte, s int) int {
	word, i := readWord(src, s)
	end := -1
	switch word {
	case kwImport:
		if next, j := readSpacedWord(src, i); next == kwType {
			end = typeKeywordEnd(src, j, stmtImportType)
		}
	case kwExport:
		switch next, j := readSpacedWord(src, i); next {
		case kwType:
			end = typeKeywordEnd(src, j, stmtExportType)
		case kwInterface:
			end = interfaceEnd(src, j)
		}
	case kwType:
		end = typeKeywordEnd(src, i, stmtTypeAlias)
	case kwInterface:
		end = interfaceEnd(src, i)
	}
	if end < 0 {
		return s
	}
	return end
}

// typeKeywordEnd handles what follows the word "type" at j: a list ("{ A }", "*") for an
// import or an export, or a name. It returns -1 when "type" is not the keyword here, as in
// `import type from "./type"`.
func typeKeywordEnd(src []byte, j int, kind typeStmtKind) int {
	k := skipInlineSpace(src, j)
	if k >= len(src) {
		return -1
	}
	if src[k] == '{' || src[k] == '*' {
		if kind == stmtTypeAlias {
			return -1
		}
		return (&typeScan{src: src}).end(k)
	}
	name, afterName := readWord(src, k)
	if k == j || name == "" {
		return -1
	}
	if kind == stmtImportType {
		if next, _ := readSpacedWord(src, afterName); name == kwFrom || next != kwFrom {
			return -1
		}
		return (&typeScan{src: src}).end(afterName)
	}
	a := skipInlineSpace(src, afterName)
	if a >= len(src) || (src[a] != '<' && !isAssignAt(src, a)) {
		return -1
	}
	return (&typeScan{src: src}).end(a)
}

func isAssignAt(src []byte, a int) bool {
	return src[a] == '=' && (a+1 >= len(src) || (src[a+1] != '=' && src[a+1] != '>'))
}

func interfaceEnd(src []byte, j int) int {
	k := skipInlineSpace(src, j)
	if name, _ := readWord(src, k); k == j || name == "" {
		return -1
	}
	return (&typeScan{src: src, iface: true}).end(k)
}

func readWord(src []byte, pos int) (string, int) {
	if pos >= len(src) || !isJSIdentStart(src[pos]) {
		return "", pos
	}
	end := scanJSIdentEnd(src, pos)
	return string(src[pos:end]), end
}

// readSpacedWord reads a word separated from pos by at least one space or tab.
func readSpacedWord(src []byte, pos int) (string, int) {
	k := skipInlineSpace(src, pos)
	if k == pos {
		return "", pos
	}
	return readWord(src, k)
}

func skipInlineSpace(src []byte, pos int) int {
	for pos < len(src) && src[pos] != '\n' && isASCIIWhitespace(src[pos]) {
		pos++
	}
	return pos
}

// typeScan walks a type-only statement in normalized source to find its end.
type typeScan struct {
	src         []byte
	depth       int
	prev, prev2 byte
	iface       bool
	bodyClosed  bool
}

// end returns the offset after the statement, or -1 when it cannot tell.
func (t *typeScan) end(from int) int {
	for i := from; i < len(t.src); i++ {
		if next, ok := skipTypeArgLiteralOrComment(t.src, i); ok {
			if next < 0 {
				return -1
			}
			t.note('"')
			i = next - 1
			continue
		}
		c := t.src[i]
		switch {
		case c == '\n':
			if t.depth == 0 && t.endsAtNewline(i) {
				return i
			}
		case isASCIIWhitespace(c):
		case c == ';' && t.depth == 0:
			return i + 1
		default:
			if stop, done := t.step(c, i); done {
				return stop
			}
		}
	}
	if t.depth != 0 || (t.iface && !t.bodyClosed) {
		return -1
	}
	return len(t.src)
}

func (t *typeScan) step(c byte, i int) (int, bool) {
	t.note(c)
	switch c {
	case '(', '[', '{':
		t.depth++
	case ')', ']', '}':
		t.depth--
		if t.depth < 0 {
			return -1, true
		}
		if c == '}' && t.depth == 0 && t.iface {
			return t.afterInterfaceBrace(i + 1)
		}
	}
	return 0, false
}

func (t *typeScan) note(c byte) {
	t.prev2, t.prev = t.prev, c
}

// endsAtNewline reports whether a line break outside every bracket ends the statement. It
// does not when the type cannot stop there ("=", "|", "=>" before it) or when the next line
// opens with something no statement starts with.
func (t *typeScan) endsAtNewline(i int) bool {
	if t.iface {
		return t.bodyClosed
	}
	if bytes.IndexByte([]byte("=|&,<:?"), t.prev) >= 0 || (t.prev == '>' && t.prev2 == '=') {
		return false
	}
	return i+1 >= len(t.src) || bytes.IndexByte([]byte("|&?:>"), t.src[i+1]) < 0
}

// afterInterfaceBrace decides, after a "}" that closes to the top level, whether it closed
// the body. A "}" inside the type parameters is followed by something no statement starts
// with: ">", ",", "=", "|" or "&".
func (t *typeScan) afterInterfaceBrace(j int) (int, bool) {
	t.bodyClosed = true
	k := skipInlineSpace(t.src, j)
	if k >= len(t.src) || t.src[k] == '\n' {
		return k, true
	}
	switch t.src[k] {
	case ';':
		return k + 1, true
	case '>', ',', '=', '|', '&':
		return 0, false
	default:
		return j, true
	}
}
