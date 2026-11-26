package fingerprint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
)

const (
	installMarkerPrefix = "marker-v1:"
	// markerTimestampKey is when pnpm last pruned node_modules: it changes on every install.
	markerTimestampKey = "prunedAt"
)

// HashInstallMarker hashes a package manager's node_modules marker (pnpm's .modules.yaml,
// npm's .package-lock.json, yarn's state files) by content that describes what is installed.
// A JSON marker is hashed with its keys sorted and the install timestamp removed; a YAML one
// with the timestamp line removed. It returns false when the file cannot be read, and the
// caller hashes it like any other file.
func HashInstallMarker(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) == nil {
		delete(doc, markerTimestampKey)
		if canonical, err := canonicalizeJSON(doc); err == nil {
			return digestMarker(canonical), true
		}
	}
	return digestMarker(dropTimestampLines(data)), true
}

// canonicalizeJSON re-encodes a document with every object's keys in sorted order.
func canonicalizeJSON(doc map[string]json.RawMessage) ([]byte, error) {
	generic := make(map[string]any, len(doc))
	for key, raw := range doc {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		generic[key] = value
	}
	return json.Marshal(generic)
}

func dropTimestampLines(data []byte) []byte {
	var kept [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte(markerTimestampKey+":")) {
			kept = append(kept, line)
		}
	}
	return bytes.Join(kept, []byte("\n"))
}

func digestMarker(content []byte) string {
	sum := sha256.Sum256(content)
	return installMarkerPrefix + hex.EncodeToString(sum[:])
}
