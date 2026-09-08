package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// errIsDirectory is resolve()'s internal signal that a key names a directory
// (root itself included) rather than an object. It never leaves this file:
// each public method maps it to whatever "this is not an object" means for
// that operation — Get/Exists treat it exactly like a missing key, Put/Delete
// simply propagate it as the hard error it already is.
var errIsDirectory = errors.New("storage: key names a directory, not an object")

// ErrPresignNotSupported is returned by PresignGet/PresignPut on a backend
// with no notion of a signed, directly-fetchable URL — the filesystem backend
// today, since a local path is never itself web-reachable. It is a distinct,
// checkable error rather than a fabricated file:// URL or an empty string,
// because a future caller (attachments, P2.3) that wants presigned upload/
// download links needs to be able to detect "this backend cannot do that" and
// require an object-storage backend instead — at configuration time, not by
// discovering a broken link at runtime.
var ErrPresignNotSupported = errors.New("storage: presigned URLs are not supported by this backend")

// FSProvider implements Provider over a directory on the local filesystem. It
// is the DEFAULT backend (see FromEnv) — S3Provider is the opt-in for a
// multi-node or cloud deployment that cannot share a local disk.
//
// Every key is resolved to a path under root and validated to stay inside it
// before any file operation runs (see resolve): a key is untrusted input the
// moment it crosses this seam, so "../../etc/passwd", an absolute path, and a
// symlink that resolves outside root are all refused rather than clamped —
// clamping would let two different-looking keys collide on one file.
type FSProvider struct{ root string }

// compile-time guarantees both Provider implementations satisfy the one seam.
var (
	_ Provider = (*FSProvider)(nil)
	_ Provider = (*S3Provider)(nil)
)

// NewFSProvider creates root (and any missing parent directories) if it does
// not already exist, then returns a Provider rooted there. Creating eagerly
// means a misconfigured root (unwritable, a path that collides with a file)
// fails at startup rather than on the first Put a workspace happens to make.
func NewFSProvider(root string) (*FSProvider, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("storage: filesystem root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve root %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root %q: %w", abs, err)
	}
	// Resolved ONCE, to its real (symlink-free) path, so every later
	// containment check in resolve() compares real-to-real. Without this, a
	// root that itself sits behind a symlink (e.g. macOS's /tmp -> /private/tmp)
	// would make resolve()'s own EvalSymlinks check reject every legitimate
	// key, because the resolved leaf would no longer share the UNresolved
	// root as a string prefix.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve root %q: %w", abs, err)
	}
	return &FSProvider{root: resolved}, nil
}

// Name returns "fs", identifying this Provider implementation.
func (p *FSProvider) Name() string { return "fs" }

// Put writes body to the path key resolves to, creating any missing parent
// directories and overwriting whatever was already there — matching S3's
// PutObject, which replaces an existing key rather than erroring on one.
func (p *FSProvider) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	path, err := p.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("storage: create parent directory for %q: %w", key, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("storage: open %q for write: %w", key, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		return fmt.Errorf("storage: write %q: %w", key, err)
	}
	return nil
}

// Get opens the file key resolves to. A missing key is ErrNotFound, matching
// S3Provider.Get, so a caller can handle one error for either backend.
//
// ContentType and ETag are left zero on the returned metadata: a filesystem
// has no notion of either, and guessing one (from the extension, say) would
// let a caller mistake a fabricated value for one the backend actually
// recorded, the same reasoning PresignGet's error exists for.
func (p *FSProvider) Get(_ context.Context, key string) (io.ReadCloser, *ObjectMetadata, error) {
	path, err := p.resolve(key)
	if errors.Is(err, errIsDirectory) {
		// A directory (root included, via a "." key) was never Put, and
		// os.Open would happily hand back a directory-typed *os.File whose
		// Read fails later — far too late to be a useful error. Treated
		// identically to a plain missing key.
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("storage: open %q: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("storage: stat %q: %w", key, err)
	}
	return f, &ObjectMetadata{Key: key, Size: info.Size(), LastModified: info.ModTime()}, nil
}

// Delete removes the file key resolves to. Deleting an already-absent key is
// a no-op, matching S3's DeleteObject (which does not error on a missing
// key) — Delete is idempotent on both backends.
func (p *FSProvider) Delete(_ context.Context, key string) error {
	path, err := p.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}

// Exists reports whether key names a file under root.
func (p *FSProvider) Exists(_ context.Context, key string) (bool, error) {
	path, err := p.resolve(key)
	if errors.Is(err, errIsDirectory) {
		// A directory is not an object, and Exists reports "no object here"
		// the same way it does for a plain missing key: false, no error.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("storage: stat %q: %w", key, err)
	}
	return true, nil
}

// PresignGet always fails: see ErrPresignNotSupported.
func (p *FSProvider) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

// PresignPut always fails: see ErrPresignNotSupported.
func (p *FSProvider) PresignPut(context.Context, string, string, time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

// resolve turns a caller-supplied key into an absolute path guaranteed to sit
// inside root, or an error if it would not: an absolute key, a "../" that
// climbs out (however many segments it uses — filepath.Join collapses them
// before the containment check runs, so there is no depth this can evade at),
// a symlink that resolves outside root, or a key that names a directory
// (root itself included — see errIsDirectory).
//
// The symlink check walks up from the resolved path to the deepest ancestor
// that currently exists and resolves symlinks from there: a fresh Put target
// has no leaf to stat, but every directory ABOVE it that already exists is
// somewhere a symlink could have been planted pointing outside root, and
// filepath.EvalSymlinks simply fails (rather than reporting anything useful)
// on a path whose final component is absent.
//
// TOCTOU note: resolve validates containment and hands back a path; every
// caller then performs a SEPARATE os.Open/os.Remove/os.OpenFile against that
// path afterward. A symlink swapped into place in the gap between the two
// (root, or an ancestor already checked here, replaced by one pointing
// outside root) could still let an operation escape — this function does not
// close that window, and closing it portably would need OS-specific
// primitives (Linux's openat2/RESOLVE_BENEATH; nothing equivalent exists on
// every platform this seam runs on), which is out of scope here. It matters
// only under a materially different threat model than "an untrusted key from
// an API caller": it requires an attacker with concurrent WRITE access to
// the storage root's filesystem, at which point this package is not the
// weakest link.
func (p *FSProvider) resolve(key string) (string, error) {
	if key == "" {
		return "", errors.New("storage: key must not be empty")
	}
	if filepath.IsAbs(key) {
		return "", fmt.Errorf("storage: key %q must be a relative path", key)
	}
	joined := filepath.Join(p.root, key)
	if !withinRoot(p.root, joined) {
		return "", fmt.Errorf("storage: key %q escapes the storage root", key)
	}

	existing := joined
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Nothing on this branch exists yet, so nothing on it could be a
			// symlink either; the textual containment check above is final.
			return joined, nil
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("storage: resolve key %q: %w", key, err)
	}
	if !withinRoot(p.root, resolved) {
		return "", fmt.Errorf("storage: key %q resolves outside the storage root", key)
	}

	// A key naming a directory is not a valid object identifier: "." collapses
	// to root exactly (withinRoot above permits path == root — that check is
	// about ESCAPE, not about what kind of thing the path names), and a key
	// can just as easily land on a directory an earlier Put created only as an
	// intermediate prefix (Put("a/b.txt") creates directory "a" as a side
	// effect). Checked explicitly rather than left to os.Open/os.Remove:
	// os.Open succeeds on a directory and only fails on the eventual Read,
	// long after this call reports success, and os.Remove SUCCEEDS on an
	// EMPTY directory — which the storage root always is on a freshly
	// provisioned deployment — so leaving this unchecked would let a "."
	// key delete the storage root itself.
	//
	// Stat, not Lstat: this must catch a key whose own leaf is a symlink
	// TO a directory too (already proven to resolve inside root by the
	// EvalSymlinks check above), not just a plain directory entry.
	if joined == p.root {
		return "", errIsDirectory
	}
	if info, err := os.Stat(joined); err == nil && info.IsDir() {
		return "", errIsDirectory
	}
	return joined, nil
}

// withinRoot reports whether path is root itself or a descendant of it. Both
// arguments are cleaned first so a caller never has to.
func withinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
