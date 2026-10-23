package retention

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/protocol"
)

const (
	coldLockName      = ".retention.lock"
	coldTemporary     = ".tmp-"
	maxStoredBytes    = protocol.MaxContentBytes + 64<<10
	maxInventoryFiles = 100000
)

// ColdStore stores immutable content-addressed gzip files in a private local
// directory. MaxBytes charges all regular data files, including unreferenced
// blobs, unknown files and interrupted temporary writes. It is a logical byte
// limit, not filesystem block accounting. There is deliberately no deletion API.
type ColdStore struct {
	Root     string
	MaxBytes int64
}

type BlobRef struct {
	Digest      string `json:"sha256"`
	Size        int64  `json:"size"`
	StoredBytes int64  `json:"stored_bytes"`
}

// Bytes and Files include all non-lock regular files; TemporaryBytes is the
// subset occupied by temporary files. Inventory never recursively follows paths.
type Inventory struct {
	Bytes          int64 `json:"bytes"`
	Files          int64 `json:"files"`
	TemporaryBytes int64 `json:"temporary_bytes"`
}

func (s ColdStore) validate() error {
	if !validRoot(s.Root) || s.MaxBytes < 1 || s.MaxBytes > 1<<50 {
		return ErrInvalid
	}
	return nil
}

func validateRef(ref BlobRef) error {
	if len(ref.Digest) != sha256.Size*2 || ref.Size < 0 || ref.Size > protocol.MaxContentBytes ||
		ref.StoredBytes < 1 || ref.StoredBytes > maxStoredBytes {
		return ErrInvalid
	}
	for _, c := range ref.Digest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrInvalid
		}
	}
	return nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

func compressContent(ctx context.Context, content string) ([]byte, error) {
	var encoded bytes.Buffer
	w, _ := gzip.NewWriterLevel(&encoded, gzip.BestSpeed)
	for start := 0; start < len(content); start += 32 << 10 {
		if err := contextError(ctx); err != nil {
			_ = w.Close()
			return nil, err
		}
		end := min(len(content), start+(32<<10))
		if _, err := io.WriteString(w, content[start:end]); err != nil {
			return nil, ErrUnavailable
		}
	}
	if err := w.Close(); err != nil {
		return nil, ErrUnavailable
	}
	return encoded.Bytes(), nil
}

// Put durably publishes one content-addressed file before returning its
// reference. A per-directory process lock serializes quota checks across stores
// and databases sharing the directory. An existing file is verified and never
// overwritten. A failed or canceled call can leave an unreferenced durable blob;
// callers must never infer that an error authorizes deleting its final name.
func (s ColdStore) Put(ctx context.Context, content string) (BlobRef, error) {
	var zero BlobRef
	if s.validate() != nil || len(content) > protocol.MaxContentBytes || !utf8.ValidString(content) {
		return zero, ErrInvalid
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	digest := sha256.Sum256([]byte(content))
	encoded, err := compressContent(ctx, content)
	if err != nil {
		return zero, err
	}
	ref := BlobRef{Digest: hex.EncodeToString(digest[:]), Size: int64(len(content)), StoredBytes: int64(len(encoded))}
	dir, err := openColdRoot(s.Root, true)
	if err != nil {
		return zero, err
	}
	defer dir.Close()
	lock, err := lockColdDirectory(ctx, dir)
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	if err = contextError(ctx); err != nil {
		return zero, err
	}
	name := ref.Digest + ".gzip"
	file, err := openColdFile(dir, name, false)
	if err == nil {
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil || info.Size() < 1 || info.Size() > maxStoredBytes {
			return zero, ErrIntegrity
		}
		// A prior supported encoder may use a different compressed byte length.
		// Verify the existing immutable representation instead of replacing it.
		ref.StoredBytes = info.Size()
		if _, err = readBlob(ctx, file, ref); err != nil {
			return zero, err
		}
		if err = file.Sync(); err == nil {
			err = dir.Sync()
		}
		if err != nil {
			return zero, ErrUnavailable
		}
		return ref, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	inv, err := inventoryDirectory(ctx, dir)
	if err != nil {
		return zero, err
	}
	if inv.Files >= maxInventoryFiles || inv.Bytes > s.MaxBytes || ref.StoredBytes > s.MaxBytes-inv.Bytes {
		return zero, ErrBudget
	}
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return zero, ErrUnavailable
	}
	temporary := coldTemporary + hex.EncodeToString(entropy[:])
	file, err = openColdFile(dir, temporary, true)
	if err != nil {
		return zero, err
	}
	// Only this call's unpublished temporary file may be cleaned up. Final
	// files can be referenced by a committed transaction after an unknown error.
	defer removeColdFile(dir, temporary)
	for start := 0; start < len(encoded); start += 32 << 10 {
		if err = contextError(ctx); err != nil {
			_ = file.Close()
			return zero, err
		}
		end := min(len(encoded), start+(32<<10))
		if _, err = file.Write(encoded[start:end]); err != nil {
			_ = file.Close()
			return zero, ErrUnavailable
		}
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return zero, ErrUnavailable
	}
	if err = file.Close(); err != nil {
		return zero, ErrUnavailable
	}
	if err = contextError(ctx); err != nil {
		return zero, err
	}
	// Link provides atomic no-overwrite publication; rename would replace an
	// existing file. The temp and final names stay on the same filesystem.
	if err = linkColdFile(dir, temporary, name); err != nil {
		return zero, err
	}
	if err = removeColdFile(dir, temporary); err != nil {
		return zero, err
	}
	if err = dir.Sync(); err != nil {
		return zero, ErrUnavailable
	}
	if err = contextError(ctx); err != nil {
		return zero, err
	}
	return ref, nil
}

// Read verifies the exact reference and bounded gzip member. Missing files,
// malformed members, changed content and trailing data all fail closed.
func (s ColdStore) Read(ctx context.Context, ref BlobRef) (string, error) {
	if s.validate() != nil || validateRef(ref) != nil {
		return "", ErrInvalid
	}
	if err := contextError(ctx); err != nil {
		return "", err
	}
	dir, err := openColdRoot(s.Root, false)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	f, err := openColdFile(dir, ref.Digest+".gzip", false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrUnavailable
		}
		return "", err
	}
	defer f.Close()
	return readBlob(ctx, f, ref)
}

func readBlob(ctx context.Context, f *os.File, ref BlobRef) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", ErrUnavailable
	}
	if info.Size() != ref.StoredBytes {
		return "", ErrIntegrity
	}
	raw := bufio.NewReader(io.LimitReader(f, maxStoredBytes+1))
	r, err := gzip.NewReader(raw)
	if err != nil {
		return "", ErrIntegrity
	}
	r.Multistream(false)
	defer r.Close()
	var decoded bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		if err = contextError(ctx); err != nil {
			return "", err
		}
		n, readErr := r.Read(buffer)
		if int64(decoded.Len()+n) > ref.Size {
			return "", ErrIntegrity
		}
		_, _ = decoded.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", ErrIntegrity
		}
	}
	if decoded.Len() != int(ref.Size) || !utf8.Valid(decoded.Bytes()) {
		return "", ErrIntegrity
	}
	if _, err = raw.ReadByte(); err != io.EOF {
		return "", ErrIntegrity
	}
	digest := sha256.Sum256(decoded.Bytes())
	if hex.EncodeToString(digest[:]) != ref.Digest {
		return "", ErrIntegrity
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() || info.Mode() != after.Mode() {
		return "", ErrIntegrity
	}
	return decoded.String(), nil
}

// Inventory uses bounded directory pages and fails closed on unsafe entries or
// excessive file counts. A missing directory is an empty inventory; no files or
// directories are created in that case.
func (s ColdStore) Inventory(ctx context.Context) (Inventory, error) {
	var zero Inventory
	if s.validate() != nil {
		return zero, ErrInvalid
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	dir, err := openColdRoot(s.Root, false)
	if errors.Is(err, os.ErrNotExist) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	defer dir.Close()
	return inventoryDirectory(ctx, dir)
}

func inventoryDirectory(ctx context.Context, dir *os.File) (Inventory, error) {
	var inv Inventory
	for {
		if err := contextError(ctx); err != nil {
			return Inventory{}, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return Inventory{}, ErrUnavailable
		}
		for _, entry := range entries {
			file, openErr := openColdFile(dir, entry.Name(), false)
			if openErr != nil {
				return Inventory{}, ErrUnavailable
			}
			info, statErr := file.Stat()
			closeErr := file.Close()
			if statErr != nil || closeErr != nil || !privateColdFile(info) {
				return Inventory{}, ErrUnavailable
			}
			if entry.Name() == coldLockName {
				if info.Size() != 0 {
					return Inventory{}, ErrIntegrity
				}
				continue
			}
			inv.Files++
			if inv.Files > maxInventoryFiles || info.Size() < 0 || info.Size() > math.MaxInt64-inv.Bytes {
				return Inventory{}, ErrBudget
			}
			inv.Bytes += info.Size()
			if len(entry.Name()) >= len(coldTemporary) && entry.Name()[:len(coldTemporary)] == coldTemporary {
				inv.TemporaryBytes += info.Size()
			}
		}
		if err == io.EOF {
			return inv, nil
		}
	}
}
