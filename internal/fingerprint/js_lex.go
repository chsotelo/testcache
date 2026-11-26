package fingerprint

import "bytes"

type lexMode uint8

const (
	modeCode lexMode = iota
	modeJSXTag
	modeJSXChildren
)

type frameKind uint8

const (
	frameParen frameKind = iota
	frameBracket
	frameBrace
	frameTemplateExpr
	frameJSXExpr
	frameJSXElem
)

type tokKind uint8

const (
	tokStart tokKind = iota
	tokExprOp
	tokExprEnd
	tokDot
)

type lexFrame struct {
	kind       frameKind
	isCtrl     bool
	isBlockHdr bool
	isBlock    bool
	tagName    string
	returnMode lexMode
}

type jsLexer struct {
	src       []byte
	pos       int
	canJSX    bool
	mode      lexMode
	stack     []lexFrame
	out       bytes.Buffer
	trivia    triviaState
	prevTok   tokKind
	stmtHdr   string
	errReason string
	// lineStarts are the output offsets of the words that open a top-level line.
	lineStarts []int
}

func normalizeJS(src []byte, canJSX bool) NormalizeResult {
	l := jsLexer{
		src:     src,
		canJSX:  canJSX,
		mode:    modeCode,
		prevTok: tokStart,
	}
	l.out.Grow(len(src))
	l.scanHashbang()
	for l.pos < len(l.src) && l.errReason == "" {
		switch l.mode {
		case modeCode:
			l.stepCode()
		case modeJSXTag:
			l.stepJSXTag()
		case modeJSXChildren:
			l.stepJSXChildren()
		}
	}
	if l.errReason != "" {
		return NormalizeResult{Data: src, OK: false, Reason: l.errReason}
	}
	if l.mode != modeCode || len(l.stack) > 0 {
		return NormalizeResult{Data: src, OK: false, Reason: "unclosed delimiter or jsx element"}
	}
	return NormalizeResult{Data: l.out.Bytes(), OK: true, lineStarts: l.lineStarts}
}

func (l *jsLexer) fail(reason string) {
	if l.errReason == "" {
		l.errReason = reason
	}
}

func (l *jsLexer) scanHashbang() {
	if len(l.src) >= 2 && l.src[0] == '#' && l.src[1] == '!' {
		end := scanLineEnd(l.src, 2)
		l.trivia.emit(&l.out, l.src[0:end], false)
		l.pos = end
	}
}

func (l *jsLexer) stepCode() {
	if next, hasNL, ok := matchJSWhitespace(l.src, l.pos); ok {
		l.trivia.addWhitespace(l.src[l.pos:next], hasNL)
		l.pos = next
		return
	}
	b := l.src[l.pos]
	if b == '/' && l.stepSlash() {
		return
	}
	if b == '\'' || b == '"' {
		l.stepString(b)
		return
	}
	if b == '`' {
		l.scanTemplateFrom(l.pos, true)
		return
	}
	if b == '<' && l.canJSX && l.prevTok != tokExprEnd && l.stepMaybeJSX() {
		return
	}
	if l.stepDelimiter(b) {
		return
	}
	if isJSIdentStart(b) || b >= 0x80 {
		l.stepIdent()
		return
	}
	if isASCIIDigit(b) || (b == '.' && l.pos+1 < len(l.src) && isASCIIDigit(l.src[l.pos+1])) {
		l.stepNumber()
		return
	}
	l.stepOperator()
}

func (l *jsLexer) stepSlash() bool {
	if l.pos+1 < len(l.src) {
		nextB := l.src[l.pos+1]
		if nextB == '/' {
			end := scanLineEnd(l.src, l.pos+2)
			l.emitOrStripComment(l.src[l.pos:end], l.src[l.pos+2:end], false)
			l.pos = end
			return true
		}
		if nextB == '*' {
			end, ok := scanBlockComment(l.src, l.pos)
			if !ok {
				l.fail("unterminated block comment")
				return true
			}
			body := l.src[l.pos+2 : end-2]
			l.emitOrStripComment(l.src[l.pos:end], body, containsNewline(body))
			l.pos = end
			return true
		}
	}
	if l.prevTok == tokExprEnd {
		end := l.pos + 1
		if end < len(l.src) && l.src[end] == '=' {
			end++
		}
		l.emitCodeTok(l.src[l.pos:end], tokExprOp, "")
		l.pos = end
		return true
	}
	end, ok := scanJSRegex(l.src, l.pos)
	if !ok {
		l.fail("unterminated regex literal")
		return true
	}
	l.emitCodeTok(l.src[l.pos:end], tokExprEnd, "")
	l.pos = end
	return true
}

func (l *jsLexer) emitOrStripComment(full, body []byte, hasNL bool) {
	if isMeaningfulComment(body) {
		l.trivia.emit(&l.out, full, false)
		return
	}
	l.trivia.addComment(hasNL)
}

func (l *jsLexer) emitCodeTok(tok []byte, kind tokKind, hdr string) {
	l.trivia.emit(&l.out, tok, false)
	l.prevTok = kind
	l.stmtHdr = hdr
}

func (l *jsLexer) stepString(quote byte) {
	end, ok := scanJSString(l.src, l.pos, quote)
	if !ok {
		l.fail("unterminated string literal")
		return
	}
	l.emitCodeTok(l.src[l.pos:end], tokExprEnd, "")
	l.pos = end
}

func (l *jsLexer) scanTemplateFrom(start int, hasOpenTick bool) {
	i := start + 1
	if !hasOpenTick {
		i = start
	}
	for i < len(l.src) {
		b := l.src[i]
		if b == '\\' {
			i = advanceEscape(l.src, i)
			continue
		}
		if b == '`' {
			end := i + 1
			l.emitCodeTok(l.src[start:end], tokExprEnd, "")
			l.pos = end
			return
		}
		if b == '$' && i+1 < len(l.src) && l.src[i+1] == '{' {
			end := i + 2
			l.emitCodeTok(l.src[start:end], tokStart, "")
			l.stack = append(l.stack, lexFrame{kind: frameTemplateExpr})
			l.pos = end
			return
		}
		i++
	}
	l.fail("unterminated template literal")
}

func (l *jsLexer) stepDelimiter(b byte) bool {
	switch b {
	case '(':
		l.stack = append(l.stack, lexFrame{
			kind:       frameParen,
			isCtrl:     isCtrlHeaderWord(l.stmtHdr),
			isBlockHdr: l.stmtHdr == "switch" || l.stmtHdr == "catch",
		})
		l.emitCodeTok(l.src[l.pos:l.pos+1], tokExprOp, "")
		l.pos++
		return true
	case ')':
		return l.closeParen()
	case '[':
		l.stack = append(l.stack, lexFrame{kind: frameBracket})
		l.emitCodeTok(l.src[l.pos:l.pos+1], tokExprOp, "")
		l.pos++
		return true
	case ']':
		if len(l.stack) == 0 || l.stack[len(l.stack)-1].kind != frameBracket {
			l.fail("mismatched closing bracket")
			return true
		}
		l.stack = l.stack[:len(l.stack)-1]
		l.emitCodeTok(l.src[l.pos:l.pos+1], tokExprEnd, "")
		l.pos++
		return true
	case '{':
		isBlk := l.prevTok == tokStart || l.prevTok == tokExprEnd
		l.stack = append(l.stack, lexFrame{kind: frameBrace, isBlock: isBlk})
		l.emitCodeTok(l.src[l.pos:l.pos+1], tokStart, "")
		l.pos++
		return true
	case '}':
		return l.closeBrace()
	}
	return false
}

func (l *jsLexer) closeParen() bool {
	if len(l.stack) == 0 || l.stack[len(l.stack)-1].kind != frameParen {
		l.fail("mismatched closing paren")
		return true
	}
	top := l.stack[len(l.stack)-1]
	l.stack = l.stack[:len(l.stack)-1]
	nextKind := tokExprEnd
	if top.isCtrl || top.isBlockHdr {
		nextKind = tokStart
	}
	l.emitCodeTok(l.src[l.pos:l.pos+1], nextKind, "")
	l.pos++
	return true
}

func (l *jsLexer) closeBrace() bool {
	if len(l.stack) == 0 {
		l.fail("unmatched closing brace")
		return true
	}
	top := l.stack[len(l.stack)-1]
	switch top.kind {
	case frameBrace:
		l.stack = l.stack[:len(l.stack)-1]
		nextKind := tokExprEnd
		if top.isBlock {
			nextKind = tokStart
		}
		l.emitCodeTok(l.src[l.pos:l.pos+1], nextKind, "")
		l.pos++
	case frameTemplateExpr:
		l.stack = l.stack[:len(l.stack)-1]
		l.scanTemplateFrom(l.pos, false)
	case frameJSXExpr:
		l.stack = l.stack[:len(l.stack)-1]
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+1], false)
		l.mode = top.returnMode
		l.stmtHdr = ""
		l.pos++
	default:
		l.fail("mismatched closing brace")
	}
	return true
}
