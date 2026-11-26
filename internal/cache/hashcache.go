package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const statModTimeCutoff = 2 * time.Second

// FileHashEntry records a cached SHA-256 digest keyed by file size and mtime.
type FileHashEntry struct {
	Size      int64  `json:"size"`
	MtimeNano int64  `json:"mtimeNano"`
	SHA256    string `json:"sha256"`
}

// FileHashCache accelerates repeated SHA-256 hashing of files by (path, size, mtime).
type FileHashCache struct {
	path    string
	mu      sync.Mutex
	entries map[string]FileHashEntry
	dirty   bool
}

// LoadFileHashCache reads the persisted file-hash cache from path.
func LoadFileHashCache(path string) (*FileHashCache, error) {
	c := &FileHashCache{
		path:    path,
		entries: make(map[string]FileHashEntry),
	}
	var loaded map[string]FileHashEntry
	found, err := readJSON(path, &loaded)
	if err != nil || (found && loaded == nil) {
		// Corrupted or null index is treated as a miss and removed.
		_ = os.Remove(path)
		return c, nil
	}
	if found {
		for k, v := range loaded {
			if k != "" && v.SHA256 != "" {
				c.entries[k] = v
			}
		}
	}
	return c, nil
}

// HashFile returns the SHA-256 hex digest of absPath, reusing cached hashes when
// size and mtime match (and the file is older than the mtime precision cutoff).
func (c *FileHashCache) HashFile(absPath string) (string, error) {
	return c.hashCached(absPath, absPath, func() (string, error) {
		return sha256File(absPath)
	})
}

// HashFileTransformed returns a cached digest for absPath under cacheKeyPrefix,
// computing the digest from file bytes via computeFn on cache miss.
func (c *FileHashCache) HashFileTransformed(absPath, cacheKeyPrefix string, computeFn func([]byte) (string, error)) (string, error) {
	key := cacheKeyPrefix + "\x00" + absPath
	return c.hashCached(absPath, key, func() (string, error) {
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", absPath, err)
		}
		return computeFn(data)
	})
}

func (c *FileHashCache) hashCached(absPath, cacheKey string, computeFn func() (string, error)) (string, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", absPath, err)
	}
	size := info.Size()
	mtime := info.ModTime()
	mtimeNano := mtime.UnixNano()

	c.mu.Lock()
	cached, ok := c.entries[cacheKey]
	c.mu.Unlock()
	if ok && cached.Size == size && cached.MtimeNano == mtimeNano && time.Since(mtime) >= statModTimeCutoff {
		return cached.SHA256, nil
	}

	digest, err := computeFn()
	if err != nil {
		return "", err
	}

	if time.Since(mtime) >= statModTimeCutoff {
		c.mu.Lock()
		c.entries[cacheKey] = FileHashEntry{
			Size:      size,
			MtimeNano: mtimeNano,
			SHA256:    digest,
		}
		c.dirty = true
		c.mu.Unlock()
	}
	return digest, nil
}

// blobKeyPrefix marks the entries that map a git blob id to the digest of that content.
const blobKeyPrefix = "blob\x00"

// LookupBlobDigest returns the digest recorded for a git blob id.
func (c *FileHashCache) LookupBlobDigest(blobID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[blobKeyPrefix+blobID]
	return entry.SHA256, ok && entry.SHA256 != ""
}

// StoreBlobDigest records the digest of the content a git blob id stands for.
func (c *FileHashCache) StoreBlobDigest(blobID, digest string) {
	if blobID == "" || digest == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[blobKeyPrefix+blobID] = FileHashEntry{SHA256: digest}
	c.dirty = true
}

// Save writes updated file-hash cache entries to disk if any changed.
func (c *FileHashCache) Save() error {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	snapshot := make(map[string]FileHashEntry, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	c.dirty = false
	c.mu.Unlock()
	return writeJSONAtomic(c.path, snapshot)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
