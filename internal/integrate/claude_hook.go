package integrate

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner/vitest"
)

const (
	hookEventPreToolUse = "PreToolUse"
	hookToolBash        = "Bash"
	hookCommandField    = "command"
	// maxHookInputBytes bounds what the hook reads from stdin.
	maxHookInputBytes = 4 << 20
	changeDirCommand  = "cd"
	// tcVitestCommand replaces the launcher and "vitest" of a rewritten command.
	tcVitestCommand = "tc vitest"
)

// hookInput is the part of the PreToolUse payload the hook reads.
type hookInput struct {
	ToolName  string          `json:"tool_name"`
	Cwd       string          `json:"cwd"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// hookOutput is the documented PreToolUse response that replaces the tool input.
type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName string          `json:"hookEventName"`
	UpdatedInput  json.RawMessage `json:"updatedInput"`
}

// HookRewriter rewrites Bash commands that run Vitest once so that they go through tc.
type HookRewriter struct {
	// HasVitest reports whether a project directory resolves a Vitest binary.
	HasVitest func(dir string) bool
}

// NewHookRewriter returns a rewriter that checks the file system for Vitest.
func NewHookRewriter() HookRewriter {
	return HookRewriter{HasVitest: func(dir string) bool { return vitest.LocateBinary(dir) != "" }}
}

// RunClaudeHook reads a PreToolUse payload from stdin and, when the Bash command runs Vitest
// once, writes the response that swaps in `tc vitest`. In every other case it writes nothing,
// which leaves the tool call untouched.
func (h HookRewriter) RunClaudeHook(stdin io.Reader, stdout io.Writer) error {
	if config.IsDisabled() {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxHookInputBytes))
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("parse hook input: %w", err)
	}
	if in.ToolName != hookToolBash {
		return nil
	}
	updated, changed := h.rewriteToolInput(in)
	if !changed {
		return nil
	}
	out, err := json.Marshal(hookOutput{
		HookSpecificOutput: hookSpecificOutput{HookEventName: hookEventPreToolUse, UpdatedInput: updated},
	})
	if err != nil {
		return fmt.Errorf("encode hook output: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "%s\n", out)
	return err
}

// rewriteToolInput returns the Bash tool input with its command rewritten. updatedInput
// replaces the whole input object, so every other field is passed back as it came.
func (h HookRewriter) rewriteToolInput(in hookInput) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(in.ToolInput, &fields); err != nil {
		return nil, false
	}
	var command string
	if err := json.Unmarshal(fields[hookCommandField], &command); err != nil {
		return nil, false
	}
	rewritten, changed := h.RewriteCommand(command, in.Cwd)
	if !changed {
		return nil, false
	}
	encoded, err := json.Marshal(rewritten)
	if err != nil {
		return nil, false
	}
	fields[hookCommandField] = encoded
	updated, err := json.Marshal(fields)
	return updated, err == nil
}

// textEdit replaces src[start:end] with text.
type textEdit struct {
	start, end int
	text       string
}

// RewriteCommand returns command with every direct Vitest single run replaced by
// `TC_INVOKED_BY=claude-hook tc vitest ...`. Only the launcher is replaced; arguments, pipes,
// redirections and neighbouring commands stay byte for byte. It reports false when nothing
// changes: no such call, a construct it does not model, or a directory without Vitest.
func (h HookRewriter) RewriteCommand(command, cwd string) (string, bool) {
	if cwd == "" || !strings.Contains(command, string(wordVitest)) {
		return command, false
	}
	scan, ok := splitShellCommand(command)
	if !ok {
		return command, false
	}
	var edits []textEdit
	dir, dirKnown := cwd, true
	for _, seg := range scan.segments {
		if seg.words[0].plain && seg.words[0].text == changeDirCommand {
			dir, dirKnown = followChangeDir(dir, dirKnown, seg.words)
			continue
		}
		call, found := findVitestCall(seg.words)
		if !found || !call.isSingleRun() || !dirKnown || !h.HasVitest(dir) {
			continue
		}
		prefix := runlog.EnvInvokedBy + "=" + string(runlog.InvokerClaudeHook) + " "
		edits = append(edits,
			textEdit{start: seg.words[0].start, end: seg.words[0].start, text: prefix},
			textEdit{start: call.launcherStart, end: call.vitestEnd, text: tcVitestCommand})
	}
	if len(edits) == 0 {
		return command, false
	}
	return applyTextEdits(command, edits), true
}

// followChangeDir returns the directory after `cd <dir>`; it is unknown for any other form.
func followChangeDir(dir string, dirKnown bool, words []shellWord) (string, bool) {
	if len(words) != 2 || !words[1].plain || words[1].text == "-" {
		return dir, false
	}
	target := words[1].text
	if filepath.IsAbs(target) {
		return filepath.Clean(target), true
	}
	return filepath.Join(dir, target), dirKnown
}

// applyTextEdits applies edits given in source order.
func applyTextEdits(src string, edits []textEdit) string {
	var b strings.Builder
	last := 0
	for _, e := range edits {
		b.WriteString(src[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.WriteString(src[last:])
	return b.String()
}
