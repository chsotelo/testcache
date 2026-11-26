package fingerprint

import (
	"bytes"
	"path/filepath"
	"strings"
)

type sourceExt string

const (
	extJS  sourceExt = ".js"
	extJSX sourceExt = ".jsx"
	extMJS sourceExt = ".mjs"
	extCJS sourceExt = ".cjs"
	extTS  sourceExt = ".ts"
	extTSX sourceExt = ".tsx"
	extMTS sourceExt = ".mts"
	extCTS sourceExt = ".cts"
	extCSS sourceExt = ".css"
)

const cssCommentSep = "/**/"

// NormalizeResult holds the output of comment/whitespace normalization for a source file.
type NormalizeResult struct {
	Data   []byte
	OK     bool
	Reason string
	// lineStarts are the offsets in Data of the words that open a top-level line.
	lineStarts []int
}

// IsNormalizableExt reports whether path has a JS/TS/JSX/CSS extension eligible for normalization.
func IsNormalizableExt(path string) bool {
	ext := sourceExt(strings.ToLower(filepath.Ext(path)))
	switch ext {
	case extJS, extJSX, extMJS, extCJS, extTS, extTSX, extMTS, extCTS, extCSS:
		return true
	default:
		return false
	}
}

func isJSXCapableExt(ext sourceExt) bool {
	switch ext {
	case extJS, extJSX, extMJS, extCJS, extTSX:
		return true
	default:
		return false
	}
}

// NormalizeSource strips non-meaningful comments and normalizes multi-line whitespace
// for JS/TS/JSX/CSS files, and drops the top-level type-only statements of TypeScript files.
// If lexing fails or state is ambiguous, OK is false.
func NormalizeSource(path string, src []byte) NormalizeResult {
	ext := sourceExt(strings.ToLower(filepath.Ext(path)))
	if ext == extCSS {
		return normalizeCSS(src)
	}
	if !IsNormalizableExt(path) {
		return NormalizeResult{Data: src, OK: false, Reason: "unsupported extension"}
	}
	res := normalizeJS(src, isJSXCapableExt(ext))
	if res.OK && isTypeScriptExt(ext) {
		res.Data = stripTypeStatements(res.Data, res.lineStarts)
	}
	return res
}

type triviaState struct {
	buf          []byte
	hasAny       bool
	hasComment   bool
	hasNewline   bool
	hasSpace     bool
	hasTokenPrev bool
}

func (t *triviaState) addWhitespace(ws []byte, hasNL bool) {
	t.hasAny = true
	t.hasSpace = true
	if hasNL {
		t.hasNewline = true
		t.buf = t.buf[:0]
		return
	}
	if !t.hasComment {
		t.buf = append(t.buf, ws...)
	}
}

func (t *triviaState) addComment(hasNL bool) {
	t.hasAny = true
	t.hasComment = true
	if hasNL {
		t.hasNewline = true
	}
	t.buf = t.buf[:0]
}

func (t *triviaState) flush(out *bytes.Buffer, isCSS bool) {
	if !t.hasAny {
		return
	}
	if t.hasTokenPrev {
		switch {
		case t.hasNewline:
			out.WriteByte('\n')
		case t.hasComment:
			if isCSS && !t.hasSpace {
				out.WriteString(cssCommentSep)
			} else {
				out.WriteByte(' ')
			}
		default:
			out.Write(t.buf)
		}
	}
	t.resetRun()
}

func (t *triviaState) resetRun() {
	t.buf = t.buf[:0]
	t.hasAny = false
	t.hasComment = false
	t.hasNewline = false
	t.hasSpace = false
}

func (t *triviaState) emit(out *bytes.Buffer, tok []byte, isCSS bool) {
	t.flush(out, isCSS)
	out.Write(tok)
	t.hasTokenPrev = true
}

func isMeaningfulComment(body []byte) bool {
	if len(body) > 0 && body[0] == '!' {
		return true
	}
	return bytes.IndexByte(body, '@') >= 0 || bytes.IndexByte(body, '#') >= 0
}

func containsNewline(data []byte) bool {
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b == '\n' || b == '\r' {
			return true
		}
		if b == 0xe2 && i+2 < len(data) && data[i+1] == 0x80 && (data[i+2] == 0xa8 || data[i+2] == 0xa9) {
			return true
		}
	}
	return false
}

func isASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\v' || b == '\f' || b == '\r' || b == '\n'
}

func matchASCIIWhitespace(src []byte, pos int) (next int, hasNL bool) {
	i := pos
	for i < len(src) && isASCIIWhitespace(src[i]) {
		if src[i] == '\n' || src[i] == '\r' {
			hasNL = true
		}
		i++
	}
	return i, hasNL
}

func normalizeCSS(src []byte) NormalizeResult {
	var out bytes.Buffer
	out.Grow(len(src))
	var trivia triviaState
	pos := 0
	for pos < len(src) {
		b := src[pos]
		if isASCIIWhitespace(b) {
			next, hasNL := matchASCIIWhitespace(src, pos)
			trivia.addWhitespace(src[pos:next], hasNL)
			pos = next
			continue
		}
		if b == '/' && pos+1 < len(src) && src[pos+1] == '*' {
			end, ok := scanBlockComment(src, pos)
			if !ok {
				return NormalizeResult{Data: src, OK: false, Reason: "unterminated css comment"}
			}
			body := src[pos+2 : end-2]
			if isMeaningfulComment(body) {
				trivia.emit(&out, src[pos:end], true)
			} else {
				trivia.addComment(containsNewline(body))
			}
			pos = end
			continue
		}
		next, handled, errReason := scanCSSSpecialOrToken(src, pos)
		if errReason != "" {
			return NormalizeResult{Data: src, OK: false, Reason: errReason}
		}
		if !handled {
			next = scanCSSTokenChunk(src, pos)
		}
		trivia.emit(&out, src[pos:next], true)
		pos = next
	}
	return NormalizeResult{Data: out.Bytes(), OK: true}
}

func scanCSSSpecialOrToken(src []byte, pos int) (next int, handled bool, errReason string) {
	b := src[pos]
	if b == '\'' || b == '"' {
		end, ok := scanCSSString(src, pos, b)
		if !ok {
			return 0, false, "unterminated css string"
		}
		return end, true, ""
	}
	if (b == 'u' || b == 'U') && hasCSSURLPrefix(src, pos) {
		end, ok := scanCSSURL(src, pos)
		if !ok {
			return 0, false, "unterminated or invalid css url"
		}
		return end, true, ""
	}
	return pos, false, ""
}

func scanCSSTokenChunk(src []byte, pos int) int {
	i := pos + 1
	for i < len(src) {
		b := src[i]
		if isASCIIWhitespace(b) || b == '\'' || b == '"' {
			break
		}
		if b == '/' && i+1 < len(src) && src[i+1] == '*' {
			break
		}
		if (b == 'u' || b == 'U') && hasCSSURLPrefix(src, i) {
			break
		}
		i++
	}
	return i
}

func hasCSSURLPrefix(src []byte, pos int) bool {
	if pos+4 > len(src) {
		return false
	}
	if pos > 0 && isCSSIdentByte(src[pos-1]) {
		return false
	}
	return strings.EqualFold(string(src[pos:pos+4]), "url(")
}

func isCSSIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_' || b == '-' || b >= 0x80
}

func scanCSSString(src []byte, pos int, quote byte) (int, bool) {
	i := pos + 1
	for i < len(src) {
		b := src[i]
		if b == quote {
			return i + 1, true
		}
		if b == '\\' {
			i = advanceEscape(src, i)
			continue
		}
		if b == '\n' || b == '\r' {
			return 0, false
		}
		i++
	}
	return 0, false
}

func scanCSSURL(src []byte, pos int) (int, bool) {
	i := pos + 4
	i, _ = matchASCIIWhitespace(src, i)
	if i >= len(src) {
		return 0, false
	}
	if src[i] == '\'' || src[i] == '"' {
		end, ok := scanCSSString(src, i, src[i])
		if !ok {
			return 0, false
		}
		i, _ = matchASCIIWhitespace(src, end)
		if i < len(src) && src[i] == ')' {
			return i + 1, true
		}
		return 0, false
	}
	for i < len(src) {
		b := src[i]
		if b == ')' {
			return i + 1, true
		}
		if b == '\\' {
			if i+1 >= len(src) {
				return 0, false
			}
			i += 2
			continue
		}
		if b == '(' || b == '\'' || b == '"' || b == '\n' || b == '\r' {
			return 0, false
		}
		i++
	}
	return 0, false
}

func scanBlockComment(src []byte, pos int) (int, bool) {
	i := pos + 2
	for i+1 < len(src) {
		if src[i] == '*' && src[i+1] == '/' {
			return i + 2, true
		}
		i++
	}
	return 0, false
}

func advanceEscape(src []byte, backslashPos int) int {
	if backslashPos+1 >= len(src) {
		return len(src)
	}
	if src[backslashPos+1] == '\r' && backslashPos+2 < len(src) && src[backslashPos+2] == '\n' {
		return backslashPos + 3
	}
	return backslashPos + 2
}
