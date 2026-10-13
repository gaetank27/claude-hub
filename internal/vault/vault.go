// Package vault implements the persistent "safe deposit box" for soft-deleted
// projects. A deleted project is packed into a single self-contained .tar.gz
// (working tree + journals + per-account memory + a JSON manifest) before any
// on-disk destruction, so it can always be fully restored. Archives are never
// removed automatically — only the explicit Delete (the one true destruction).
//
// The package is pure archive I/O: it knows nothing about the store or the
// session manager. The manifest types are self-contained; callers map their own
// domain types onto them.
package vault

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// manifestName is the archive's first entry: a JSON descriptor read on its own
// when listing, without unpacking the (potentially large) payload.
const manifestName = "manifest.json"

// ErrNotFound is returned when an archive name does not exist in the vault.
var ErrNotFound = errors.New("vault archive not found")

// nameRe guards every caller-supplied archive name against path traversal: a
// vault name is a flat "<slug>-<timestamp>.tar.gz" with no separators.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.tar\.gz$`)

// slugSanitize collapses anything outside the safe set into a dash so a project
// slug can be embedded in a filename.
var slugSanitize = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Manifest is the JSON descriptor embedded as the first entry of every archive
// and returned by List/Get for display and by Extract for restore.
type Manifest struct {
	Project   ProjectMeta   `json:"project"`
	Rag       *RagMeta      `json:"rag,omitempty"`
	Sessions  []SessionMeta `json:"sessions"`
	Accounts  []string      `json:"accounts,omitempty"` // account ids whose per-project memory was captured
	DeletedAt time.Time     `json:"deletedAt"`
}

// ProjectMeta carries the project row needed to recreate it on restore.
type ProjectMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Dir       string    `json:"dir"`
	Category  string    `json:"category"`
	Starred   bool      `json:"starred"`
	CreatedAt time.Time `json:"createdAt"`
}

// RagMeta is the project's private RAG scheduling config, restored verbatim.
type RagMeta struct {
	Enabled         bool `json:"enabled"`
	AutoOnChange    bool `json:"autoOnChange"`
	IntervalMinutes int  `json:"intervalMinutes"`
}

// SessionMeta carries a session row plus the basename of its journal inside the
// archive's journals/ directory.
type SessionMeta struct {
	ID              string    `json:"id"`
	State           string    `json:"state"`
	PermissionMode  string    `json:"permissionMode"`
	CreatedAt       time.Time `json:"createdAt"`
	Comment         string    `json:"comment,omitempty"`
	PrURL           string    `json:"prUrl,omitempty"`
	ClaudeAccountID string    `json:"claudeAccountId,omitempty"`
	JournalFile     string    `json:"journalFile,omitempty"`
}

// Source maps a path inside the archive to a filesystem path to copy in. FSPath
// may be a file or a directory (copied recursively under ArchivePath).
type Source struct {
	ArchivePath string
	FSPath      string
}

// Entry is a vault archive as seen from outside: its flat name, byte size on
// disk and decoded manifest.
type Entry struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	Manifest Manifest `json:"manifest"`
}

// Store is a vault rooted at a directory.
type Store struct {
	dir string
}

// New opens (and creates if missing) a vault rooted at dir.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create vault dir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the vault root.
func (s *Store) Dir() string { return s.dir }

// Create writes a new archive for the given slug. The manifest is written first,
// then every source. The tarball is built into a .partial temp file, fsync'd and
// atomically renamed, so a crash mid-write never leaves a half-archive that the
// caller might mistake for a complete one before destroying the original.
func (s *Store) Create(slug string, manifest Manifest, sources []Source) (Entry, error) {
	name := s.uniqueName(slug, manifest.DeletedAt)
	final := filepath.Join(s.dir, name)
	tmp := final + ".partial"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Entry{}, err
	}
	// Best-effort cleanup of the temp file on any failure path.
	committed := false
	defer func() {
		f.Close()
		if !committed {
			os.Remove(tmp)
		}
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Entry{}, err
	}
	if err := writeBytes(tw, manifestName, mb); err != nil {
		return Entry{}, err
	}
	for _, src := range sources {
		if err := addSource(tw, src); err != nil {
			return Entry{}, fmt.Errorf("archive %s: %w", src.ArchivePath, err)
		}
	}
	if err := tw.Close(); err != nil {
		return Entry{}, err
	}
	if err := gz.Close(); err != nil {
		return Entry{}, err
	}
	if err := f.Sync(); err != nil {
		return Entry{}, err
	}
	if err := f.Close(); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return Entry{}, err
	}
	committed = true

	fi, err := os.Stat(final)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Name: name, Size: fi.Size(), Manifest: manifest}, nil
}

// List returns every archive in the vault, most-recently-deleted first. An
// archive whose manifest can't be read is skipped rather than failing the list.
func (s *Store) List() ([]Entry, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*.tar.gz"))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(matches))
	for _, p := range matches {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		m, err := readManifest(p)
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: filepath.Base(p), Size: fi.Size(), Manifest: m})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Manifest.DeletedAt.After(out[j].Manifest.DeletedAt)
	})
	return out, nil
}

// Get returns a single archive's entry.
func (s *Store) Get(name string) (Entry, error) {
	p, err := s.resolve(name)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return Entry{}, err
	}
	m, err := readManifest(p)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Name: name, Size: fi.Size(), Manifest: m}, nil
}

// Extract unpacks the whole archive under destDir and returns its manifest. The
// caller (the manager) then moves working/, journals/ and memory/ into place.
func (s *Store) Extract(name, destDir string) (Manifest, error) {
	p, err := s.resolve(name)
	if err != nil {
		return Manifest{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var manifest Manifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		// Guard against path traversal: every entry must stay under destDir.
		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		if !within(destDir, target) {
			return Manifest{}, fmt.Errorf("unsafe archive entry %q", hdr.Name)
		}
		if hdr.Name == manifestName {
			if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
				return Manifest{}, fmt.Errorf("decode manifest: %w", err)
			}
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return Manifest{}, err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Manifest{}, err
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return Manifest{}, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Manifest{}, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return Manifest{}, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return Manifest{}, err
			}
			out.Close()
		}
	}
	return manifest, nil
}

// Delete permanently removes an archive — the only destructive operation.
func (s *Store) Delete(name string) error {
	p, err := s.resolve(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// --- helpers ---

// resolve validates a caller-supplied name and returns its absolute path, or
// ErrNotFound if it does not exist.
func (s *Store) resolve(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("%w: invalid name %q", ErrNotFound, name)
	}
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return p, nil
}

// uniqueName builds "<slug>-<UTC timestamp>.tar.gz", appending a counter on the
// rare same-second collision.
func (s *Store) uniqueName(slug string, t time.Time) string {
	base := slugSanitize.ReplaceAllString(slug, "-")
	if base == "" {
		base = "project"
	}
	stamp := t.UTC().Format("20060102-150405")
	name := fmt.Sprintf("%s-%s.tar.gz", base, stamp)
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(s.dir, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%s-%d.tar.gz", base, stamp, i)
	}
}

func readManifest(p string) (Manifest, error) {
	f, err := os.Open(p)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		return Manifest{}, err
	}
	if hdr.Name != manifestName {
		return Manifest{}, fmt.Errorf("first entry is %q, want %q", hdr.Name, manifestName)
	}
	var m Manifest
	if err := json.NewDecoder(tr).Decode(&m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func writeBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(b)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// addSource copies a file or directory tree into the archive under src.ArchivePath.
func addSource(tw *tar.Writer, src Source) error {
	info, err := os.Lstat(src.FSPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // best-effort: a missing source (e.g. no memory dir) is fine
		}
		return err
	}
	if !info.IsDir() {
		return addPath(tw, src.ArchivePath, src.FSPath, info)
	}
	return filepath.Walk(src.FSPath, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src.FSPath, p)
		if err != nil {
			return err
		}
		archivePath := src.ArchivePath
		if rel != "." {
			archivePath = path.Join(src.ArchivePath, filepath.ToSlash(rel))
		}
		return addPath(tw, archivePath, p, fi)
	})
}

// addPath writes one filesystem entry (dir, symlink or regular file) to the tar.
func addPath(tw *tar.Writer, archivePath, fsPath string, fi os.FileInfo) error {
	switch {
	case fi.IsDir():
		return tw.WriteHeader(&tar.Header{
			Name:     archivePath + "/",
			Mode:     int64(fi.Mode().Perm()),
			Typeflag: tar.TypeDir,
		})
	case fi.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(fsPath)
		if err != nil {
			return err
		}
		return tw.WriteHeader(&tar.Header{
			Name:     archivePath,
			Mode:     int64(fi.Mode().Perm()),
			Typeflag: tar.TypeSymlink,
			Linkname: link,
		})
	case fi.Mode().IsRegular():
		if err := tw.WriteHeader(&tar.Header{
			Name:     archivePath,
			Mode:     int64(fi.Mode().Perm()),
			Size:     fi.Size(),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return err
		}
		f, err := os.Open(fsPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	default:
		return nil // skip sockets, devices, etc.
	}
}

// within reports whether target is inside root (after cleaning), defending the
// extractor against "../" entries.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
