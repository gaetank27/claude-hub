package rag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

// maxCodeFileBytes bounds an indexed project file; larger files (build artifacts,
// minified bundles, data dumps) are skipped.
const maxCodeFileBytes = 512 * 1024

// dirSkip are directories never walked when a project is not a git repo (when it
// is, `git ls-files` already excludes ignored paths).
var dirSkip = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true, "bin": true,
	"vendor": true, ".next": true, "target": true, ".venv": true, "__pycache__": true,
}

// ProjectStats summarizes a project collection's index for the UI.
type ProjectStats struct {
	Files   int
	Chunks  int
	Bytes   int64
	Pending int
}

// ScanProject (re)indexes a project's files into its private collection: it lists
// the git-tracked files (respecting .gitignore), upserts a `code` document per
// file — skipping unchanged ones via a content hash — removes deleted files, and
// enqueues changed/new files to the async worker. Returns the indexed file count,
// total bytes and the git HEAD it scanned. The heavy embed work runs in the worker.
func (ix *Indexer) ScanProject(ctx context.Context, projectDir, collectionID string) (files int, bytesTotal int64, headSHA string, err error) {
	headSHA = gitHead(ctx, projectDir)
	paths, err := listProjectFiles(ctx, projectDir)
	if err != nil {
		return 0, 0, "", err
	}

	existing, err := ix.db.ListRagDocumentsByCollection(collectionID)
	if err != nil {
		return 0, 0, "", err
	}
	byRef := map[string]store.RagDocument{}
	for _, d := range existing {
		if d.SourceType == store.RagSourceCode {
			byRef[d.SourceRef] = d
		}
	}
	seen := map[string]bool{}

	for _, rel := range paths {
		content, ok := readTextFile(filepath.Join(projectDir, rel))
		if !ok {
			continue // binary, too large, or unreadable
		}
		seen[rel] = true
		bytesTotal += int64(len(content))
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])

		if d, exists := byRef[rel]; exists {
			if d.ContentHash != hash {
				_ = ix.db.MarkRagDocumentStale(d.ID, hash, int64(len(content)))
				ix.Enqueue(d.ID)
			}
			continue
		}
		doc := store.RagDocument{
			ID: uuid.NewString(), Title: rel, SourceType: store.RagSourceCode, SourceRef: rel,
			Status: store.RagPending, Enabled: true, Bytes: int64(len(content)),
			CollectionID: collectionID, ContentHash: hash, CreatedAt: time.Now().UTC(),
		}
		if err := ix.db.CreateRagCodeDocument(doc); err == nil {
			ix.Enqueue(doc.ID)
		}
	}
	// Drop documents for files that no longer exist.
	for ref, d := range byRef {
		if !seen[ref] {
			_ = ix.db.DeleteRagDocument(d.ID)
		}
	}
	return len(seen), bytesTotal, headSHA, nil
}

// CollectionStats counts a project collection's documents, embedded chunks, bytes
// and how many documents are still pending/processing (so the scheduler can tell
// when a reindex has settled and the UI can show progress).
func (ix *Indexer) CollectionStats(collectionID string) (ProjectStats, error) {
	docs, err := ix.db.ListRagDocumentsByCollection(collectionID)
	if err != nil {
		return ProjectStats{}, err
	}
	var st ProjectStats
	for _, d := range docs {
		st.Files++
		st.Bytes += d.Bytes
		st.Chunks += d.ChunkCount
		if d.Status == store.RagPending || d.Status == store.RagProcessing {
			st.Pending++
		}
	}
	return st, nil
}

// gitHead returns the project's current HEAD sha, or "" when it is not a git repo.
func gitHead(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// listProjectFiles returns the project's files relative to dir. It prefers
// `git ls-files` (honoring .gitignore); for a non-git directory it walks the tree
// skipping well-known heavy/build directories.
func listProjectFiles(ctx context.Context, dir string) ([]string, error) {
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z").Output(); err == nil {
		var paths []string
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				paths = append(paths, p)
			}
		}
		return paths, nil
	}
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if dirSkip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, rerr := filepath.Rel(dir, path); rerr == nil {
			paths = append(paths, rel)
		}
		return nil
	})
	return paths, err
}

// readTextFile reads a file if it is a reasonably-sized text file, returning ok=false
// for oversized, unreadable or binary files (a NUL byte in the head marks binary).
func readTextFile(abs string) (content []byte, ok bool) {
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > maxCodeFileBytes {
		return nil, false
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, false
	}
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, false // binary
	}
	return b, true
}
