package fingerprint

import (
	"bytes"
	"strings"
)

// docTags are JSDoc and TypeScript tags that document code for people and type checkers.
// They change nothing at run time, unlike pragmas such as @vitest-environment, @jsx,
// @jsxImportSource or @vite-ignore, which stay meaningful along with any tag not listed here.
var docTags = map[string]bool{
	"param": true, "arg": true, "argument": true, "returns": true, "return": true,
	"type": true, "typedef": true, "template": true, "typeparam": true, "property": true,
	"prop": true, "callback": true, "example": true, "deprecated": true, "see": true,
	"link": true, "linkcode": true, "linkplain": true, "throws": true, "exception": true,
	"description": true, "desc": true, "summary": true, "remarks": true, "default": true,
	"defaultvalue": true, "since": true, "version": true, "author": true, "todo": true,
	"private": true, "public": true, "protected": true, "internal": true, "readonly": true,
	"override": true, "abstract": true, "async": true, "constant": true, "const": true,
	"enum": true, "function": true, "func": true, "method": true, "class": true,
	"constructor": true, "extends": true, "augments": true, "implements": true,
	"interface": true, "member": true, "memberof": true, "module": true, "namespace": true,
	"name": true, "inheritdoc": true, "ignore": true, "kind": true, "static": true,
	"this": true, "yields": true, "yield": true, "fires": true, "emits": true, "event": true,
	"listens": true, "requires": true, "file": true, "fileoverview": true, "overview": true,
	"global": true, "inner": true, "instance": true, "access": true, "alias": true,
	"satisfies": true, "import": true, "category": true, "beta": true, "alpha": true,
	"experimental": true, "sealed": true, "virtual": true, "copyright": true,
	"ts-check": true, "ts-nocheck": true, "ts-ignore": true, "ts-expect-error": true,
}

// hashDirectives follow a '#' and change what runs: tree-shaking annotations and the source
// map URLs that stack traces are mapped through. Any other '#' is text ("see #123").
var hashDirectives = []string{"__", " sourceMappingURL", "sourceMappingURL", " sourceURL", "sourceURL"}

// isMeaningfulJSComment reports whether a JS/TS comment may change what the code does: it is
// a legal comment ("/*! ... */"), it carries a tag that is not a documentation tag, or a
// '#__' annotation or source map directive. Documentation comments, however many tags they
// hold, are stripped like any other comment.
func isMeaningfulJSComment(body []byte) bool {
	if len(body) > 0 && body[0] == '!' {
		return true
	}
	return hasNonDocTag(body) || hasHashDirective(body)
}

func hasNonDocTag(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '@' {
			continue
		}
		// "name@example.com" is an address, not a tag.
		if i > 0 && isTagByte(body[i-1]) {
			continue
		}
		j := i + 1
		for j < len(body) && isTagByte(body[j]) {
			j++
		}
		if j == i+1 || !docTags[strings.ToLower(string(body[i+1:j]))] {
			return true
		}
		i = j - 1
	}
	return false
}

func hasHashDirective(body []byte) bool {
	for i := bytes.IndexByte(body, '#'); i >= 0; {
		rest := body[i+1:]
		for _, d := range hashDirectives {
			if bytes.HasPrefix(rest, []byte(d)) {
				return true
			}
		}
		next := bytes.IndexByte(rest, '#')
		if next < 0 {
			return false
		}
		i += next + 1
	}
	return false
}

func isTagByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_'
}
