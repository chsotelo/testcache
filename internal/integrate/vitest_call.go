package integrate

import "strings"

type commandWord string

const (
	wordVitest   commandWord = "vitest"
	wordRun      commandWord = "run"
	wordRunFlag  commandWord = "--run"
	wordCrossEnv commandWord = "cross-env"
	wordRtk      commandWord = "rtk"
	wordProxy    commandWord = "proxy"
	// localBinSuffix is how a direct path to the project's Vitest ends.
	localBinSuffix = "node_modules/.bin/vitest"
)

// vitestCall is a simple command recognised as a direct Vitest invocation.
type vitestCall struct {
	// assignments are the NAME=value words in front of the command, cross-env's included.
	assignments []string
	// launcherStart and vitestEnd delimit, in the source, the words that tc replaces:
	// from the launcher (npx, pnpm exec, ...) up to and including "vitest".
	launcherStart, vitestEnd int
	// args are the words after "vitest".
	args []shellWord
	// impliedRun is true for `rtk vitest`, which always runs once.
	impliedRun bool
	// viaRtk is true when the command goes through rtk (`rtk proxy ...`, `rtk vitest`).
	viaRtk bool
}

// launcherForms lists the ways a command starts Vitest directly; the last word is "vitest".
func launcherForms() [][]string {
	vitest := string(wordVitest)
	return [][]string{
		{vitest},
		{"npx", vitest},
		{"pnpm", "exec", vitest},
		{"pnpm", vitest},
		{"yarn", vitest},
	}
}

// isAssignment reports whether the word is a NAME=value environment assignment.
func isAssignment(w shellWord) bool {
	name, _, found := strings.Cut(w.text, "=")
	if !found || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isLetter := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		if !isLetter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// findVitestCall recognises `[NAME=v ...] [cross-env NAME=v ...] [rtk proxy] <launcher> vitest
// [args]` at the start of a simple command. Every word up to "vitest" must be plain.
func findVitestCall(words []shellWord) (vitestCall, bool) {
	var call vitestCall
	i := skipAssignments(words, 0, &call)
	if i < len(words) && words[i].plain && words[i].text == string(wordCrossEnv) {
		i = skipAssignments(words, i+1, &call)
	}
	if hasWordsAt(words, i, []string{string(wordRtk), string(wordProxy)}) {
		call.viaRtk = true
		i += 2
	} else if hasWordsAt(words, i, []string{string(wordRtk), string(wordVitest)}) {
		call.viaRtk, call.impliedRun = true, true
		return fillVitestCall(call, words, i, 2), true
	}
	if i < len(words) && words[i].plain && isLocalBinPath(words[i].text) {
		return fillVitestCall(call, words, i, 1), true
	}
	for _, form := range launcherForms() {
		if hasWordsAt(words, i, form) {
			return fillVitestCall(call, words, i, len(form)), true
		}
	}
	return vitestCall{}, false
}

func skipAssignments(words []shellWord, i int, call *vitestCall) int {
	for i < len(words) && words[i].plain && isAssignment(words[i]) {
		call.assignments = append(call.assignments, words[i].text)
		i++
	}
	return i
}

func fillVitestCall(call vitestCall, words []shellWord, at, launcherLen int) vitestCall {
	call.launcherStart = words[at].start
	call.vitestEnd = words[at+launcherLen-1].end
	call.args = words[at+launcherLen:]
	return call
}

func hasWordsAt(words []shellWord, at int, want []string) bool {
	if at+len(want) > len(words) {
		return false
	}
	for j, w := range want {
		if !words[at+j].plain || words[at+j].text != w {
			return false
		}
	}
	return true
}

func isLocalBinPath(text string) bool {
	return text == localBinSuffix || strings.HasSuffix(text, "/"+localBinSuffix)
}

// isSingleRun reports whether the call runs the tests once and exits: `vitest run ...` or
// `vitest --run ...`, and nothing that turns it into watch, UI or another subcommand.
func (c vitestCall) isSingleRun() bool {
	hasRun := c.impliedRun
	for i, arg := range c.args {
		if !arg.plain {
			continue
		}
		name, _, _ := strings.Cut(arg.text, "=")
		switch {
		case name == "--watch" || name == "-w" || name == "--ui":
			return false
		case i == 0 && isOtherSubcommand(arg.text):
			return false
		case (i == 0 && arg.text == string(wordRun)) || arg.text == string(wordRunFlag):
			hasRun = true
		}
	}
	return hasRun
}

// isOtherSubcommand reports whether the first argument selects a Vitest subcommand other than run.
func isOtherSubcommand(text string) bool {
	switch text {
	case "watch", "dev", "bench", "list", "typecheck", "related", "init":
		return true
	}
	return false
}
