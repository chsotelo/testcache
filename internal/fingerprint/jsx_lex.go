package fingerprint

func (l *jsLexer) stepMaybeJSX() bool {
	res := tryParseJSXStart(l.src, l.pos)
	switch res.kind {
	case jsxNotJSX:
		return false
	case jsxAmbiguous:
		l.fail("ambiguous jsx tag")
		return true
	case jsxFragment:
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+2], false)
		l.stack = append(l.stack, lexFrame{kind: frameJSXElem, returnMode: l.mode})
		l.mode = modeJSXChildren
		l.pos += 2
		return true
	case jsxOpenTag:
		return l.enterJSXOpenTag(res)
	default:
		return false
	}
}

func (l *jsLexer) enterJSXOpenTag(res jsxStartResult) bool {
	l.trivia.emit(&l.out, l.src[l.pos:res.afterName], false)
	l.pos = res.afterName
	if res.typeArgsEnd > res.typeArgsStart {
		norm := normalizeJS(l.src[res.typeArgsStart:res.typeArgsEnd], false)
		if !norm.OK {
			l.fail("ambiguous jsx generic type args")
			return true
		}
		l.trivia.emit(&l.out, norm.Data, false)
		l.pos = res.typeArgsEnd
	}
	l.stack = append(l.stack, lexFrame{
		kind:       frameJSXElem,
		tagName:    res.tagName,
		returnMode: l.mode,
	})
	l.mode = modeJSXTag
	return true
}

func (l *jsLexer) stepJSXTag() {
	if next, hasNL, ok := matchJSWhitespace(l.src, l.pos); ok {
		l.trivia.addWhitespace(l.src[l.pos:next], hasNL)
		l.pos = next
		return
	}
	b := l.src[l.pos]
	if b == '/' && l.stepJSXTagSlash() {
		return
	}
	if b == '>' {
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+1], false)
		l.mode = modeJSXChildren
		l.pos++
		return
	}
	if b == '"' || b == '\'' {
		l.stepJSXAttrString(b)
		return
	}
	if b == '{' {
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+1], false)
		l.stack = append(l.stack, lexFrame{kind: frameJSXExpr, returnMode: modeJSXTag})
		l.mode = modeCode
		l.prevTok = tokStart
		l.stmtHdr = ""
		l.pos++
		return
	}
	if b == '<' {
		l.fail("unexpected < in jsx opening tag")
		return
	}
	end := scanJSXTagAtomEnd(l.src, l.pos)
	l.trivia.emit(&l.out, l.src[l.pos:end], false)
	l.pos = end
}

func (l *jsLexer) stepJSXTagSlash() bool {
	if l.pos+1 >= len(l.src) {
		l.fail("unterminated jsx tag")
		return true
	}
	nb := l.src[l.pos+1]
	if nb == '>' {
		if len(l.stack) == 0 || l.stack[len(l.stack)-1].kind != frameJSXElem {
			l.fail("unexpected /> without jsx element")
			return true
		}
		top := l.stack[len(l.stack)-1]
		l.stack = l.stack[:len(l.stack)-1]
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+2], false)
		l.pos += 2
		l.restoreAfterJSXElem(top.returnMode)
		return true
	}
	if nb == '/' {
		end := scanLineEnd(l.src, l.pos+2)
		l.emitOrStripComment(l.src[l.pos:end], l.src[l.pos+2:end], false)
		l.pos = end
		return true
	}
	if nb == '*' {
		end, ok := scanBlockComment(l.src, l.pos)
		if !ok {
			l.fail("unterminated comment in jsx tag")
			return true
		}
		body := l.src[l.pos+2 : end-2]
		l.emitOrStripComment(l.src[l.pos:end], body, containsNewline(body))
		l.pos = end
		return true
	}
	l.fail("unexpected / in jsx opening tag")
	return true
}

func (l *jsLexer) stepJSXAttrString(quote byte) {
	i := l.pos + 1
	for i < len(l.src) && l.src[i] != quote {
		i++
	}
	if i >= len(l.src) {
		l.fail("unterminated jsx attribute string")
		return
	}
	l.trivia.emit(&l.out, l.src[l.pos:i+1], false)
	l.pos = i + 1
}

func (l *jsLexer) stepJSXChildren() {
	if next, hasNL, ok := matchJSWhitespace(l.src, l.pos); ok {
		l.trivia.addWhitespace(l.src[l.pos:next], hasNL)
		l.pos = next
		return
	}
	b := l.src[l.pos]
	if b == '{' && l.skipCommentOnlyJSXChild() {
		return
	}
	if b == '{' {
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+1], false)
		l.stack = append(l.stack, lexFrame{kind: frameJSXExpr, returnMode: modeJSXChildren})
		l.mode = modeCode
		l.prevTok = tokStart
		l.stmtHdr = ""
		l.pos++
		return
	}
	if b == '<' {
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '/' {
			l.scanJSXClosingTag()
			return
		}
		l.enterNestedJSX()
		return
	}
	end := scanJSXTextEnd(l.src, l.pos)
	l.trivia.emit(&l.out, l.src[l.pos:end], false)
	l.pos = end
}

// skipCommentOnlyJSXChild drops a `{/* ... */}` child that is alone on its line next to a
// tag or an expression. JSX removes whitespace that contains a line break, so that child and
// its braces render nothing either way. Between two pieces of text it stays: the lines of one
// text are joined into a single child, and the comment splits them into two.
func (l *jsLexer) skipCommentOnlyJSXChild() bool {
	if !l.trivia.hasNewline {
		return false
	}
	end, hasNL, ok := scanCommentOnlyBraces(l.src, l.pos)
	if !ok {
		return false
	}
	next, nl, ok := matchJSWhitespace(l.src, end)
	if !ok || !nl {
		return false
	}
	if !l.afterJSXTagOrExpr() && !beforeJSXTagOrExpr(l.src, next) {
		return false
	}
	l.trivia.addComment(hasNL)
	l.pos = end
	return true
}

// JSX text cannot hold '>' or '}': output ending in one of them ends with a tag or an
// expression, not with text.
func (l *jsLexer) afterJSXTagOrExpr() bool {
	out := l.out.Bytes()
	if len(out) == 0 {
		return false
	}
	last := out[len(out)-1]
	return last == '>' || last == '}'
}

func beforeJSXTagOrExpr(src []byte, pos int) bool {
	return pos < len(src) && (src[pos] == '<' || src[pos] == '{')
}

// scanCommentOnlyBraces matches `{` comments and whitespace `}` at pos, where every comment
// can be stripped, and returns the offset after the closing brace.
func scanCommentOnlyBraces(src []byte, pos int) (end int, hasNL bool, ok bool) {
	i := pos + 1
	for i < len(src) {
		if next, nl, isWS := matchJSWhitespace(src, i); isWS {
			hasNL = hasNL || nl
			i = next
			continue
		}
		switch {
		case src[i] == '}':
			return i + 1, hasNL, true
		case i+1 < len(src) && src[i] == '/' && src[i+1] == '*':
			cEnd, closed := scanBlockComment(src, i)
			if !closed || isMeaningfulJSComment(src[i+2:cEnd-2]) {
				return 0, false, false
			}
			hasNL = hasNL || containsNewline(src[i+2:cEnd-2])
			i = cEnd
		case i+1 < len(src) && src[i] == '/' && src[i+1] == '/':
			cEnd := scanLineEnd(src, i+2)
			if isMeaningfulJSComment(src[i+2 : cEnd]) {
				return 0, false, false
			}
			i = cEnd
		default:
			return 0, false, false
		}
	}
	return 0, false, false
}

func (l *jsLexer) enterNestedJSX() {
	res := tryParseJSXStart(l.src, l.pos)
	switch res.kind {
	case jsxFragment:
		l.trivia.emit(&l.out, l.src[l.pos:l.pos+2], false)
		l.stack = append(l.stack, lexFrame{kind: frameJSXElem, returnMode: l.mode})
		l.mode = modeJSXChildren
		l.pos += 2
	case jsxOpenTag:
		l.enterJSXOpenTag(res)
	default:
		l.fail("ambiguous nested jsx element")
	}
}

func (l *jsLexer) scanJSXClosingTag() {
	if len(l.stack) == 0 || l.stack[len(l.stack)-1].kind != frameJSXElem {
		l.fail("unexpected jsx closing tag")
		return
	}
	top := l.stack[len(l.stack)-1]
	l.trivia.emit(&l.out, l.src[l.pos:l.pos+2], false)
	l.pos += 2
	if !l.consumeJSXTrivia() {
		return
	}
	tagName := ""
	if l.pos < len(l.src) && l.src[l.pos] != '>' {
		nameEnd := scanJSXTagNameEnd(l.src, l.pos)
		if nameEnd == l.pos {
			l.fail("invalid jsx closing tag name")
			return
		}
		tagName = string(l.src[l.pos:nameEnd])
		l.trivia.emit(&l.out, l.src[l.pos:nameEnd], false)
		l.pos = nameEnd
		if !l.consumeJSXTrivia() {
			return
		}
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '>' || tagName != top.tagName {
		l.fail("mismatched jsx closing tag")
		return
	}
	l.trivia.emit(&l.out, l.src[l.pos:l.pos+1], false)
	l.pos++
	l.stack = l.stack[:len(l.stack)-1]
	l.restoreAfterJSXElem(top.returnMode)
}

func (l *jsLexer) consumeJSXTrivia() bool {
	for l.pos < len(l.src) {
		if next, hasNL, ok := matchJSWhitespace(l.src, l.pos); ok {
			l.trivia.addWhitespace(l.src[l.pos:next], hasNL)
			l.pos = next
			continue
		}
		if l.pos+1 < len(l.src) && l.src[l.pos] == '/' && l.src[l.pos+1] == '/' {
			end := scanLineEnd(l.src, l.pos+2)
			l.emitOrStripComment(l.src[l.pos:end], l.src[l.pos+2:end], false)
			l.pos = end
			continue
		}
		if l.pos+1 < len(l.src) && l.src[l.pos] == '/' && l.src[l.pos+1] == '*' {
			end, ok := scanBlockComment(l.src, l.pos)
			if !ok {
				l.fail("unterminated comment in jsx closing tag")
				return false
			}
			body := l.src[l.pos+2 : end-2]
			l.emitOrStripComment(l.src[l.pos:end], body, containsNewline(body))
			l.pos = end
			continue
		}
		break
	}
	return true
}

func (l *jsLexer) restoreAfterJSXElem(retMode lexMode) {
	l.mode = retMode
	if retMode == modeCode {
		l.prevTok = tokExprEnd
		l.stmtHdr = ""
	}
}
