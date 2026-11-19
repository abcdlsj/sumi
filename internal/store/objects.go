package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ObjectStore keeps attachment bytes outside SQLite, addressed by their SHA-256.
// Files are written to objects/<sha[:2]>/<sha>; identical content is stored once.
type ObjectStore struct {
	root string
}

func newObjectStore(root string) (*ObjectStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create object root: %w", err)
	}
	return &ObjectStore{root: root}, nil
}

func (o *ObjectStore) Root() string { return o.root }

// ErrObjectTooLarge reports that the reader exceeded the byte budget.
var ErrObjectTooLarge = errors.New("object exceeds size limit")

// Put streams r into a temp file, hashing as it goes, then atomically renames it into place.
// It never keeps partially written objects visible under their final name.
func (o *ObjectStore) Put(r io.Reader, limit int64) (sha string, size int64, err error) {
	tmp, err := os.CreateTemp(o.root, ".upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temp object: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(r, limit+1))
	if err != nil {
		return "", 0, fmt.Errorf("write object: %w", err)
	}
	if written > limit {
		return "", 0, ErrObjectTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close object: %w", err)
	}

	sha = hex.EncodeToString(hasher.Sum(nil))
	final := filepath.Join(o.root, sha[:2], sha)
	if _, statErr := os.Stat(final); statErr == nil {
		return sha, written, nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return "", 0, fmt.Errorf("create object shard: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", 0, fmt.Errorf("publish object: %w", err)
	}
	return sha, written, nil
}

// Open opens the object for reading. The caller must close it.
func (o *ObjectStore) Open(sha string) (*os.File, error) {
	if len(sha) != 64 {
		return nil, os.ErrNotExist
	}
	return os.Open(filepath.Join(o.root, sha[:2], sha))
}

// Remove deletes an object so a failed metadata insert leaves no orphan bytes.
func (o *ObjectStore) Remove(sha string) error {
	if len(sha) != 64 {
		return nil
	}
	err := os.Remove(filepath.Join(o.root, sha[:2], sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ObjectPath is the stable relative location recorded in attachments.object_path.
func ObjectPath(sha string) string {
	if len(sha) < 2 {
		return sha
	}
	return filepath.ToSlash(filepath.Join(sha[:2], sha))
}
