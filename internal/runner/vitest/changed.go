package vitest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// changedFiles lists the files `vitest --changed [since]` treats as changed, the way Vitest's
// git provider does: the files changed since the merge base with since (when given), the
// staged ones, and the modified or untracked ones. Paths are absolute.
func changedFiles(ctx context.Context, projRoot, since string) ([]string, error) {
	top, err := gitLines(ctx, projRoot, "rev-parse", "--show-toplevel")
	if err != nil || len(top) != 1 {
		return nil, fmt.Errorf("no git repository at %s: %v", projRoot, err)
	}
	root := top[0]
	cmds := [][]string{
		{"diff", "--cached", "--name-only", "-z"},
		{"ls-files", "--other", "--modified", "--exclude-standard", "-z"},
	}
	if since != "" {
		cmds = append([][]string{{"diff", "--name-only", "-z", since + "...HEAD"}}, cmds...)
	}
	seen := map[string]bool{}
	var out []string
	for _, args := range cmds {
		names, err := gitLines(ctx, root, args...)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			abs := filepath.Join(root, filepath.FromSlash(name))
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out, nil
}

// gitLines runs git in dir and splits its output on NUL or newline, dropping empty entries.
func gitLines(ctx context.Context, dir string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.FieldsFunc(string(raw), func(r rune) bool { return r == 0 || r == '\n' }), nil
}
