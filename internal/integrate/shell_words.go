// Package integrate recognises Vitest single-run commands in package scripts and in Claude
// Code Bash calls, so they can be served by tc without changing any repository.
package integrate

import "strings"

// shellWord is one word of a shell command.
type shellWord struct {
	// text is the word with its quotes removed.
	text string
	// start and end delimit the word in the source command.
	start, end int
	// plain is true when text is exactly what the shell would pass on: no expansion, glob,
	// redirection or tilde is involved.
	plain bool
}

// shellSegment is one simple command: the words between two control operators.
type shellSegment struct {
	words []shellWord
}

// shellScan is the result of splitting a command line.
type shellScan struct {
	segments []shellSegment
	// operators counts the control operators (&&, ||, ;, |, &, newline) found.
	operators int
}

const (
	// unsupportedChars make a command too complex to reason about: substitutions, groups.
	unsupportedChars = "`(){}"
	// expansionChars make a word depend on the shell: variables, globs, redirections.
	expansionChars = "$*?[<>~"
	shellBlanks    = " \t"
)

type shellScanner struct {
	src   string
	pos   int
	scan  shellScan
	words []shellWord
}

// splitShellCommand splits src into simple commands and words. It reports false for anything
// it does not model (substitutions, subshells, groups, comments, heredocs, open quotes), in
// which case the command must be left alone.
func splitShellCommand(src string) (shellScan, bool) {
	sc := &shellScanner{src: src}
	for sc.pos < len(src) {
		c := src[sc.pos]
		switch {
		case strings.IndexByte(shellBlanks, c) >= 0:
			sc.pos++
		case c == '#':
			return shellScan{}, false
		case sc.isAtOperator():
			sc.closeSegment()
			sc.scan.operators++
			sc.skipOperator()
		default:
			if !sc.readWord() {
				return shellScan{}, false
			}
		}
	}
	sc.closeSegment()
	return sc.scan, true
}

func (sc *shellScanner) closeSegment() {
	if len(sc.words) > 0 {
		sc.scan.segments = append(sc.scan.segments, shellSegment{words: sc.words})
		sc.words = nil
	}
}

// isAtOperator reports whether a control operator starts here. An ampersand that belongs to
// a redirection (2>&1, &>file) is not one.
func (sc *shellScanner) isAtOperator() bool {
	switch sc.src[sc.pos] {
	case ';', '|', '\n':
		return true
	case '&':
		return !sc.isNextByte('>')
	}
	return false
}

func (sc *shellScanner) isNextByte(c byte) bool {
	return sc.pos+1 < len(sc.src) && sc.src[sc.pos+1] == c
}

func (sc *shellScanner) skipOperator() {
	c := sc.src[sc.pos]
	sc.pos++
	if sc.pos < len(sc.src) && (sc.src[sc.pos] == c || (c == '|' && sc.src[sc.pos] == '&')) && c != '\n' {
		sc.pos++
	}
}

// readWord consumes one word starting at pos. It reports false on an unsupported construct.
func (sc *shellScanner) readWord() bool {
	word := shellWord{start: sc.pos, plain: true}
	var text strings.Builder
	for sc.pos < len(sc.src) {
		c := sc.src[sc.pos]
		if strings.IndexByte(shellBlanks, c) >= 0 || c == ';' || c == '|' || c == '\n' {
			break
		}
		if c == '&' && !sc.isRedirectAmpersand() {
			break
		}
		if !sc.readWordPart(&text, &word) {
			return false
		}
	}
	word.text, word.end = text.String(), sc.pos
	sc.words = append(sc.words, word)
	return true
}

// isRedirectAmpersand reports whether the ampersand at pos is part of >& or &>.
func (sc *shellScanner) isRedirectAmpersand() bool {
	return sc.isNextByte('>') || (sc.pos > 0 && sc.src[sc.pos-1] == '>')
}

// readWordPart consumes one quoted span or one unquoted character of the current word.
func (sc *shellScanner) readWordPart(text *strings.Builder, word *shellWord) bool {
	c := sc.src[sc.pos]
	switch {
	case c == '\'':
		return sc.readSingleQuoted(text)
	case c == '"':
		return sc.readDoubleQuoted(text, word)
	case c == '\\':
		if sc.pos+1 >= len(sc.src) || sc.src[sc.pos+1] == '\n' {
			return false
		}
		text.WriteByte(sc.src[sc.pos+1])
		sc.pos += 2
		return true
	case strings.IndexByte(unsupportedChars, c) >= 0:
		return false
	case c == '<' && sc.isNextByte('<'):
		// A heredoc body is not a command line.
		return false
	}
	if strings.IndexByte(expansionChars, c) >= 0 || c == '&' {
		word.plain = false
	}
	text.WriteByte(c)
	sc.pos++
	return true
}

func (sc *shellScanner) readSingleQuoted(text *strings.Builder) bool {
	closing := strings.IndexByte(sc.src[sc.pos+1:], '\'')
	if closing < 0 {
		return false
	}
	text.WriteString(sc.src[sc.pos+1 : sc.pos+1+closing])
	sc.pos += closing + 2
	return true
}

func (sc *shellScanner) readDoubleQuoted(text *strings.Builder, word *shellWord) bool {
	sc.pos++
	for sc.pos < len(sc.src) {
		c := sc.src[sc.pos]
		switch c {
		case '"':
			sc.pos++
			return true
		case '`':
			return false
		case '$':
			word.plain = false
		case '\\':
			if sc.pos+1 < len(sc.src) && strings.IndexByte("\"\\$`", sc.src[sc.pos+1]) >= 0 {
				sc.pos++
				c = sc.src[sc.pos]
			}
		}
		text.WriteByte(c)
		sc.pos++
	}
	return false
}
