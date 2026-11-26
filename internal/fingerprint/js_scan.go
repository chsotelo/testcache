package fingerprint

import "unicode/utf8"

func (l *jsLexer) stepIdent() {
	end := scanJSIdentEnd(l.src, l.pos)
	word := string(l.src[l.pos:end])
	kind, hdr := classifyJSIdent(word, l.prevTok == tokDot, l.stmtHdr)
	l.markTopLevelLine()
	l.emitCodeTok(l.src[l.pos:end], kind, hdr)
	l.pos = end
}

// markTopLevelLine records where the word about to be written lands in the output when it
// opens a line outside every bracket: the only place a type-only statement is looked for.
func (l *jsLexer) markTopLevelLine() {
	if len(l.stack) != 0 || l.mode != modeCode {
		return
	}
	switch {
	case !l.trivia.hasTokenPrev:
		l.lineStarts = append(l.lineStarts, l.out.Len())
	case l.trivia.hasNewline:
		l.lineStarts = append(l.lineStarts, l.out.Len()+1)
	}
}

func classifyJSIdent(word string, afterDot bool, prevHdr string) (tokKind, string) {
	if afterDot {
		return tokExprEnd, ""
	}
	switch word {
	case "if", "while", "for", "with", "switch", "catch":
		return tokExprOp, word
	case "await":
		if prevHdr == "for" {
			return tokExprOp, "for-await"
		}
		return tokExprOp, ""
	case "else", "do", "try", "finally":
		return tokStart, ""
	case "return", "throw", "case", "default", "delete", "void", "typeof",
		"yield", "new", "in", "instanceof", "export", "break", "continue":
		return tokExprOp, ""
	default:
		return tokExprEnd, ""
	}
}

func isCtrlHeaderWord(w string) bool {
	return w == "if" || w == "while" || w == "for" || w == "with" || w == "for-await"
}

func (l *jsLexer) stepNumber() {
	end := scanJSNumberEnd(l.src, l.pos)
	l.emitCodeTok(l.src[l.pos:end], tokExprEnd, "")
	l.pos = end
}

func (l *jsLexer) stepOperator() {
	end, kind := scanJSOperator(l.src, l.pos, l.prevTok)
	l.emitCodeTok(l.src[l.pos:end], kind, "")
	l.pos = end
}

func scanJSOperator(src []byte, pos int, prev tokKind) (int, tokKind) {
	rem := src[pos:]
	if len(rem) >= 3 && (string(rem[:3]) == "..." || string(rem[:3]) == "===" ||
		string(rem[:3]) == "!==" || string(rem[:3]) == ">>>") {
		if len(rem) >= 4 && rem[3] == '=' && string(rem[:3]) == ">>>" {
			return pos + 4, tokExprOp
		}
		return pos + 3, tokExprOp
	}
	if len(rem) >= 3 && (string(rem[:3]) == "<<=" || string(rem[:3]) == ">>=" ||
		string(rem[:3]) == "**=" || string(rem[:3]) == "&&=" ||
		string(rem[:3]) == "||=" || string(rem[:3]) == "??=") {
		return pos + 3, tokExprOp
	}
	return scanShortOperator(src, pos, prev)
}

func scanShortOperator(src []byte, pos int, prev tokKind) (int, tokKind) {
	b := src[pos]
	var b2 byte
	if pos+1 < len(src) {
		b2 = src[pos+1]
	}
	if b == '?' && b2 == '.' && !(pos+2 < len(src) && isASCIIDigit(src[pos+2])) {
		return pos + 2, tokDot
	}
	if b == '=' && b2 == '>' {
		return pos + 2, tokStart
	}
	if (b == '+' && b2 == '+') || (b == '-' && b2 == '-') {
		if prev == tokExprEnd {
			return pos + 2, tokExprEnd
		}
		return pos + 2, tokExprOp
	}
	if isTwoCharOp(b, b2) {
		return pos + 2, tokExprOp
	}
	if b == '.' {
		return pos + 1, tokDot
	}
	if b == ';' {
		return pos + 1, tokStart
	}
	if b == '!' && prev == tokExprEnd {
		return pos + 1, tokExprEnd
	}
	return pos + 1, tokExprOp
}

func isTwoCharOp(a, b byte) bool {
	if b == '=' && (a == '=' || a == '!' || a == '<' || a == '>' || a == '+' ||
		a == '-' || a == '*' || a == '%' || a == '&' || a == '|' || a == '^') {
		return true
	}
	return (a == '&' && b == '&') || (a == '|' && b == '|') || (a == '?' && b == '?') ||
		(a == '*' && b == '*') || (a == '<' && b == '<') || (a == '>' && b == '>')
}

func matchJSWhitespace(src []byte, pos int) (next int, hasNL bool, ok bool) {
	i := pos
	for i < len(src) {
		b := src[i]
		if isASCIIWhitespace(b) {
			if b == '\n' || b == '\r' {
				hasNL = true
			}
			i++
			continue
		}
		if b == 0xe2 && i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
			hasNL = true
			i += 3
			continue
		}
		break
	}
	return i, hasNL, i > pos
}

func scanLineEnd(src []byte, pos int) int {
	i := pos
	for i < len(src) {
		b := src[i]
		if b == '\n' || b == '\r' {
			return i
		}
		if b == 0xe2 && i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
			return i
		}
		i++
	}
	return i
}

func scanJSString(src []byte, pos int, quote byte) (int, bool) {
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

func scanJSRegex(src []byte, pos int) (int, bool) {
	i := pos + 1
	inClass := false
	for i < len(src) {
		b := src[i]
		if b == '\n' || b == '\r' || (b == 0xe2 && i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9)) {
			return 0, false
		}
		if b == '\\' {
			if i+1 >= len(src) || src[i+1] == '\n' || src[i+1] == '\r' {
				return 0, false
			}
			i += 2
			continue
		}
		if b == '[' {
			inClass = true
		} else if b == ']' {
			inClass = false
		} else if b == '/' && !inClass {
			i++
			for i < len(src) && isJSIdentCont(src[i]) {
				i++
			}
			return i, true
		}
		i++
	}
	return 0, false
}

func scanJSIdentEnd(src []byte, pos int) int {
	i := pos
	for i < len(src) {
		b := src[i]
		if isJSIdentCont(b) || b == '\\' {
			i++
			continue
		}
		if b >= 0x80 {
			_, sz := utf8.DecodeRune(src[i:])
			i += sz
			continue
		}
		break
	}
	return i
}

func scanJSNumberEnd(src []byte, pos int) int {
	i := pos
	for i < len(src) {
		b := src[i]
		if isASCIIDigit(b) || isASCIIAlpha(b) || b == '_' || b == '.' {
			if (b == 'e' || b == 'E') && i+1 < len(src) && (src[i+1] == '+' || src[i+1] == '-') {
				i += 2
				continue
			}
			i++
			continue
		}
		break
	}
	return i
}

func isJSIdentStart(b byte) bool {
	return isASCIIAlpha(b) || b == '_' || b == '$' || b == '\\'
}

func isJSIdentCont(b byte) bool {
	return isASCIIAlpha(b) || isASCIIDigit(b) || b == '_' || b == '$'
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
