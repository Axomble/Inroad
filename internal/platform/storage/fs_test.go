package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNewFSProviderRejectsEmptyRoot(t *testing.T) {
	if _, err := NewFSProvider(""); err == nil {
		t.Fatal("expected an error for an empty root")
	}
	if _, err := NewFSProvider("   "); err == nil {
		t.Fatal("expected an error for a blank root")
	}
}

func TestFSProviderName(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	if got := p.Name(); got != "fs" {
		t.Fatalf("Name() = %q, want %q", got, "fs")
	}
}

// TestFSProviderPutGetExistsDelete is the round trip the brief calls out
// explicitly: Put -> Get -> Exists -> Delete against a temp directory.
func TestFSProviderPutGetExistsDelete(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	ctx := context.Background()
	key := "workspaces/ws-1/avatars/user-1.jpg"
	content := []byte("fake-image-bytes")

	if err := p.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := p.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected the object to exist")
	}

	body, meta, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
	if meta.Size != int64(len(content)) {
		t.Fatalf("meta.Size = %d, want %d", meta.Size, len(content))
	}

	if err := p.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	exists, err = p.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists after delete: %v", err)
	}
	if exists {
		t.Fatal("expected the object to no longer exist")
	}
}

// A Put on an existing key must overwrite it, matching S3's PutObject.
func TestFSProviderPutOverwritesExistingKey(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	ctx := context.Background()
	if err := p.Put(ctx, "k", bytes.NewReader([]byte("first")), 5, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := p.Put(ctx, "k", bytes.NewReader([]byte("second")), 6, ""); err != nil {
		t.Fatalf("Put (overwrite): %v", err)
	}
	body, _, err := p.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "second" {
		t.Fatalf("content = %q, want %q (the overwrite)", got, "second")
	}
}

// Get on a missing key returns ErrNotFound, matching S3Provider's behaviour so
// a caller can handle one error for either backend.
func TestFSProviderGetMissingKeyReturnsErrNotFound(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	if _, _, err := p.Get(context.Background(), "does/not/exist.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Delete is idempotent: deleting an already-absent key must not error,
// matching S3's DeleteObject.
func TestFSProviderDeleteMissingKeyIsANoOp(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	if err := p.Delete(context.Background(), "never/existed.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// --- the security case: path traversal ---------------------------------

func TestFSProviderRefusesPathTraversal(t *testing.T) {
	root := t.TempDir()
	p, err := NewFSProvider(root)
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	// A real target outside root, so a successful escape would be observable.
	outside := filepath.Join(filepath.Dir(root), "fs-provider-escape-target")
	if err := os.WriteFile(outside, []byte("should never be reachable"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	keys := []string{
		"../../etc/passwd",
		"../fs-provider-escape-target",
		"a/../../fs-provider-escape-target",
	}
	if runtime.GOOS != "windows" {
		keys = append(keys, "/etc/passwd", outside)
	}

	ctx := context.Background()
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if err := p.Put(ctx, key, bytes.NewReader([]byte("x")), 1, ""); err == nil {
				t.Fatalf("Put(%q) succeeded, want it refused", key)
			}
			if _, _, err := p.Get(ctx, key); err == nil {
				t.Fatalf("Get(%q) succeeded, want it refused", key)
			}
			if _, err := p.Exists(ctx, key); err == nil {
				t.Fatalf("Exists(%q) succeeded, want it refused", key)
			}
			if err := p.Delete(ctx, key); err == nil {
				t.Fatalf("Delete(%q) succeeded, want it refused", key)
			}
		})
	}

	// The outside file must be untouched by any of the attempts above.
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(got) != "should never be reachable" {
		t.Fatalf("outside file was modified: %q", got)
	}
}

// A symlink planted INSIDE root but pointing outside it must be refused too —
// the textual "../" check alone would not catch this, since the key itself
// never mentions a parent segment.
func TestFSProviderRefusesSymlinkEscapingRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	root := t.TempDir()
	p, err := NewFSProvider(root)
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}

	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside root"), 0o600); err != nil {
		t.Fatalf("seed secret file: %v", err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ctx := context.Background()
	if _, _, err := p.Get(ctx, "escape/secret.txt"); err == nil {
		t.Fatal("Get through a symlink escaping root succeeded, want it refused")
	}
	if err := p.Put(ctx, "escape/new.txt", bytes.NewReader([]byte("x")), 1, ""); err == nil {
		t.Fatal("Put through a symlink escaping root succeeded, want it refused")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("Put wrote through the escaping symlink despite returning an error")
	}
}

// --- the security case, continued: "." and directory keys ----------------
//
// "." is not a root ESCAPE (withinRoot explicitly allows path == root) but it
// is also not a valid object identifier — it collapses to the storage root
// itself, a directory, never something Put wrote. Every operation must
// refuse it, and Delete most of all: os.Remove succeeds on an empty
// directory, so a bare "." key on a freshly-provisioned (therefore empty)
// root would otherwise delete the root itself.

func TestFSProviderGetDotKeyIsNotFound(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	if _, _, err := p.Get(context.Background(), "."); !errors.Is(err, ErrNotFound) {
		t.Fatalf(`Get(".") err = %v, want ErrNotFound`, err)
	}
}

func TestFSProviderExistsDotKeyIsFalse(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	exists, err := p.Exists(context.Background(), ".")
	if err != nil {
		t.Fatalf(`Exists("."): %v`, err)
	}
	if exists {
		t.Fatal(`Exists(".") = true, want false — nothing was ever Put at the storage root`)
	}
}

// The sharpest case: Delete(".") on an empty root must NOT delete the
// storage root itself.
func TestFSProviderDeleteDotKeyDoesNotDeleteRoot(t *testing.T) {
	root := t.TempDir()
	p, err := NewFSProvider(root)
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	if err := p.Delete(context.Background(), "."); err == nil {
		t.Fatal(`Delete(".") succeeded, want a hard error`)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("storage root was removed by Delete(\".\"): %v", err)
	}
}

func TestFSProviderPutDotKeyIsRefused(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	if err := p.Put(context.Background(), ".", bytes.NewReader([]byte("x")), 1, ""); err == nil {
		t.Fatal(`Put(".") succeeded, want a hard error`)
	}
}

// An empty key must be refused outright by every operation, not just Put
// (which the round-trip tests never exercise with one).
func TestFSProviderRefusesEmptyKey(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	ctx := context.Background()
	if err := p.Put(ctx, "", bytes.NewReader([]byte("x")), 1, ""); err == nil {
		t.Fatal(`Put("") succeeded, want a hard error`)
	}
	if _, _, err := p.Get(ctx, ""); err == nil {
		t.Fatal(`Get("") succeeded, want a hard error`)
	}
	if _, err := p.Exists(ctx, ""); err == nil {
		t.Fatal(`Exists("") succeeded, want a hard error`)
	}
	if err := p.Delete(ctx, ""); err == nil {
		t.Fatal(`Delete("") succeeded, want a hard error`)
	}
}

// A key that resolves to a directory an earlier Put created only as an
// INTERMEDIATE prefix (Put("a/b.txt") creates directory "a" as a side
// effect) must be refused exactly like any other directory key — a
// directory that happens to exist is still not an object.
func TestFSProviderRefusesKeyThatResolvesToAnIntermediateDirectory(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	ctx := context.Background()
	if err := p.Put(ctx, "a/b.txt", bytes.NewReader([]byte("x")), 1, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, _, err := p.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf(`Get("a") err = %v, want ErrNotFound (it names a directory, not the object)`, err)
	}
	exists, err := p.Exists(ctx, "a")
	if err != nil {
		t.Fatalf(`Exists("a"): %v`, err)
	}
	if exists {
		t.Fatal(`Exists("a") = true, want false — "a" is a directory, not an object`)
	}
	if err := p.Delete(ctx, "a"); err == nil {
		t.Fatal(`Delete("a") succeeded, want a hard error`)
	}
	// The refused directory operations above must not disturb the real
	// object stored underneath it.
	if _, _, err := p.Get(ctx, "a/b.txt"); err != nil {
		t.Fatalf(`Get("a/b.txt") after the refused directory ops: %v`, err)
	}
}

// --- presign: not a filesystem concept ----------------------------------

func TestFSProviderPresignReturnsNotSupported(t *testing.T) {
	p, err := NewFSProvider(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSProvider: %v", err)
	}
	ctx := context.Background()
	if _, err := p.PresignGet(ctx, "k", time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Fatalf("PresignGet err = %v, want ErrPresignNotSupported", err)
	}
	if _, err := p.PresignPut(ctx, "k", "text/plain", time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Fatalf("PresignPut err = %v, want ErrPresignNotSupported", err)
	}
}
