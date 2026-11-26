package fingerprint

import "bytes"

type jsxStartKind uint8

const (
	jsxNotJSX jsxStartKind = iota
	jsxFragment
	jsxOpenTag
	jsxAmbiguous
)

type jsxStartResult struct {
	kind          jsxStartKind
	tagName       string
	afterName     int
	typeArgsStart int
	typeArgsEnd   int
}

func tryParseJSXStart(src []byte, pos int) jsxStartResult {
	if pos+1 >= len(src) {
		return jsxStartResult{kind: jsxAmbiguous}
	}
	if src[pos+1] == '>' {
		return jsxStartResult{kind: jsxFragment}
	}
	nameEnd := scanJSXTagNameEnd(src, pos+1)
	if nameEnd == pos+1 {
		return jsxStartResult{kind: jsxAmbiguous}
	}
	tagName := string(src[pos+1 : nameEnd])
	return classifyAfterJSXName(src, tagName, nameEnd)
}

func classifyAfterJSXName(src []byte, tagName string, nameEnd int) jsxStartResult {
	after, ok := skipPreviewTrivia(src, nameEnd)
	if !ok || after >= len(src) {
		return jsxStartResult{kind: jsxAmbiguous}
	}
	b := src[after]
	switch {
	case b == '/' && after+1 < len(src) && src[after+1] == '>':
		return jsxStartResult{kind: jsxOpenTag, tagName: tagName, afterName: nameEnd}
	case b == ',' || b == '=':
		return jsxStartResult{kind: jsxNotJSX}
	case b == '>':
		return classifySingleGT(src, tagName, nameEnd, after)
	case b == '<':
		return classifyJSXTypeArgs(src, tagName, nameEnd, after)
	case b == '{':
		return jsxStartResult{kind: jsxOpenTag, tagName: tagName, afterName: nameEnd}
	case isJSXIdentStart(src, after):
		return classifyJSXAttrOrExtends(src, tagName, nameEnd, after)
	default:
		return jsxStartResult{kind: jsxAmbiguous}
	}
}

func classifySingleGT(src []byte, tagName string, nameEnd, gtPos int) jsxStartResult {
	if hasClosingJSXTag(src, gtPos+1, tagName) {
		return jsxStartResult{kind: jsxOpenTag, tagName: tagName, afterName: nameEnd}
	}
	next, ok := skipPreviewTrivia(src, gtPos+1)
	if ok && next < len(src) && src[next] == '(' {
		return jsxStartResult{kind: jsxNotJSX}
	}
	return jsxStartResult{kind: jsxAmbiguous}
}

func classifyJSXAttrOrExtends(src []byte, tagName string, nameEnd, attrPos int) jsxStartResult {
	const kwExtends = "extends"
	if attrPos+len(kwExtends) <= len(src) && string(src[attrPos:attrPos+len(kwExtends)]) == kwExtends {
		afterKw := attrPos + len(kwExtends)
		if afterKw == len(src) || !isJSXIdentPart(src[afterKw]) {
			next, ok := skipPreviewTrivia(src, afterKw)
			if !ok || next >= len(src) {
				return jsxStartResult{kind: jsxAmbiguous}
			}
			if (src[next] == '/' && next+1 < len(src) && src[next+1] == '>') || src[next] == '=' {
				return jsxStartResult{kind: jsxOpenTag, tagName: tagName, afterName: nameEnd}
			}
			if !hasClosingJSXTag(src, afterKw, tagName) {
				return jsxStartResult{kind: jsxNotJSX}
			}
			return jsxStartResult{kind: jsxAmbiguous}
		}
	}
	return jsxStartResult{kind: jsxOpenTag, tagName: tagName, afterName: nameEnd}
}

func classifyJSXTypeArgs(src []byte, tagName string, nameEnd, ltPos int) jsxStartResult {
	end, ok := scanBalancedTypeArgs(src, ltPos)
	if !ok {
		return jsxStartResult{kind: jsxAmbiguous}
	}
	next, ok := skipPreviewTrivia(src, end)
	if !ok || next >= len(src) {
		return jsxStartResult{kind: jsxAmbiguous}
	}
	b := src[next]
	if (b == '/' && next+1 < len(src) && src[next+1] == '>') || b == '>' || b == '{' || isJSXIdentStart(src, next) {
		return jsxStartResult{
			kind:          jsxOpenTag,
			tagName:       tagName,
			afterName:     nameEnd,
			typeArgsStart: ltPos,
			typeArgsEnd:   end,
		}
	}
	return jsxStartResult{kind: jsxAmbiguous}
}

type typeArgScanner struct {
	src   []byte
	depth int
	paren int
	brack int
	brace int
}

func scanBalancedTypeArgs(src []byte, ltPos int) (int, bool) {
	s := typeArgScanner{src: src, depth: 1}
	i := ltPos + 1
	for i < len(src) {
		if next, ok := skipTypeArgLiteralOrComment(src, i); ok {
			if next < 0 {
				return 0, false
			}
			i = next
			continue
		}
		if src[i] == '=' && i+1 < len(src) && src[i+1] == '>' {
			i += 2
			continue
		}
		done, valid := s.stepByte(src[i])
		if !valid {
			return 0, false
		}
		if done {
			return i + 1, true
		}
		i++
	}
	return 0, false
}

func (s *typeArgScanner) stepByte(b byte) (done, valid bool) {
	switch b {
	case '(':
		s.paren++
	case ')':
		s.paren--
	case '[':
		s.brack++
	case ']':
		s.brack--
	case '{':
		s.brace++
	case '}':
		s.brace--
	case '<':
		if s.paren == 0 && s.brack == 0 && s.brace == 0 {
			s.depth++
		}
	case '>':
		if s.paren == 0 && s.brack == 0 && s.brace == 0 {
			s.depth--
			if s.depth == 0 {
				return true, true
			}
		}
	}
	return false, s.paren >= 0 && s.brack >= 0 && s.brace >= 0
}

func skipTypeArgLiteralOrComment(src []byte, pos int) (int, bool) {
	b := src[pos]
	if b == '\'' || b == '"' {
		end, ok := scanJSString(src, pos, b)
		if !ok {
			return -1, true
		}
		return end, true
	}
	if b == '`' {
		return skipSimpleTemplate(src, pos), true
	}
	if b == '/' && pos+1 < len(src) {
		if src[pos+1] == '/' {
			return scanLineEnd(src, pos+2), true
		}
		if src[pos+1] == '*' {
			end, ok := scanBlockComment(src, pos)
			if !ok {
				return -1, true
			}
			return end, true
		}
	}
	return pos, false
}

func skipSimpleTemplate(src []byte, pos int) int {
	i := pos + 1
	for i < len(src) {
		if src[i] == '\\' {
			i = advanceEscape(src, i)
			continue
		}
		if src[i] == '$' && i+1 < len(src) && src[i+1] == '{' {
			return -1
		}
		if src[i] == '`' {
			return i + 1
		}
		i++
	}
	return -1
}

func skipPreviewTrivia(src []byte, pos int) (int, bool) {
	i := pos
	for i < len(src) {
		if next, _, ok := matchJSWhitespace(src, i); ok {
			i = next
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '/' {
			i = scanLineEnd(src, i+2)
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
			end, ok := scanBlockComment(src, i)
			if !ok {
				return 0, false
			}
			i = end
			continue
		}
		break
	}
	return i, true
}

func hasClosingJSXTag(src []byte, from int, tagName string) bool {
	target := []byte("</")
	pos := from
	for pos < len(src) {
		idx := bytes.Index(src[pos:], target)
		if idx < 0 {
			return false
		}
		cand := pos + idx + 2
		next, ok := skipPreviewTrivia(src, cand)
		if ok && next+len(tagName) <= len(src) && string(src[next:next+len(tagName)]) == tagName {
			after := next + len(tagName)
			afterSkip, ok2 := skipPreviewTrivia(src, after)
			if ok2 && afterSkip < len(src) && src[afterSkip] == '>' {
				return true
			}
		}
		pos = cand
	}
	return false
}

func scanJSXTagNameEnd(src []byte, pos int) int {
	if !isJSXIdentStart(src, pos) {
		return pos
	}
	i := scanJSXIdentEnd(src, pos)
	for i < len(src) && (src[i] == '.' || src[i] == ':') {
		if i+1 >= len(src) || !isJSXIdentStart(src, i+1) {
			break
		}
		i = scanJSXIdentEnd(src, i+1)
	}
	return i
}

func scanJSXIdentEnd(src []byte, pos int) int {
	i := pos
	for i < len(src) && isJSXIdentPart(src[i]) {
		i++
	}
	return i
}

func isJSXIdentStart(src []byte, pos int) bool {
	if pos >= len(src) {
		return false
	}
	b := src[pos]
	return isASCIIAlpha(b) || b == '_' || b == '$' || b >= 0x80
}

func isJSXIdentPart(b byte) bool {
	return isASCIIAlpha(b) || isASCIIDigit(b) || b == '_' || b == '$' || b == '-' || b >= 0x80
}

func scanJSXTagAtomEnd(src []byte, pos int) int {
	i := pos + 1
	for i < len(src) {
		b := src[i]
		if isASCIIWhitespace(b) || b == '/' || b == '>' || b == '{' || b == '<' || b == '"' || b == '\'' {
			break
		}
		i++
	}
	return i
}

func scanJSXTextEnd(src []byte, pos int) int {
	i := pos + 1
	for i < len(src) {
		b := src[i]
		if isASCIIWhitespace(b) || b == '{' || b == '<' {
			break
		}
		if b == 0xe2 && i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
			break
		}
		i++
	}
	return i
}
