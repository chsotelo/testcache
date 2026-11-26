package config

import (
	"path/filepath"
	"strings"
)

// MatchGlob reports whether relPath (slash-separated or OS path) matches pattern.
// Supports '*', '?', and '**' path segments.
func MatchGlob(pattern, relPath string) bool {
	pat := filepath.ToSlash(strings.TrimPrefix(pattern, "./"))
	p := filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	if pat == "" {
		return false
	}
	patParts := strings.Split(pat, "/")
	pathParts := strings.Split(p, "/")
	return matchSegments(patParts, pathParts)
}

func matchSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchSegments(pat[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], path[1:])
}

// IsExcluded reports whether relPath matches any of the exclude glob patterns.
func IsExcluded(patterns []string, relPath string) bool {
	for _, pat := range patterns {
		if MatchGlob(pat, relPath) {
			return true
		}
	}
	return false
}
