//go:build darwin || linux

package retention

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
)

func coldStore(t *testing.T) ColdStore {
	t.Helper()
	// macOS exposes its temporary root through a system symlink. Use the
	// canonical parent so tests exercise the same no-symlink policy as callers.
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ColdStore{Root: filepath.Join(parent, "cold"), MaxBytes: 8 << 20}
}

func blobPath(s ColdStore, ref BlobRef) string { return filepath.Join(s.Root, ref.Digest+".gzip") }

func requireError(t *testing.T, got, expected error) {
	t.Helper()
	if !errors.Is(got, expected) {
		t.Fatalf("wanted %v, got %v", expected, got)
	}
}

func TestColdPutReadDedupAndDeterministicEncoding(t *testing.T) {
	s := coldStore(t)
	ctx := context.Background()
	for _, content := range []string{"", "hello, 世界\n", strings.Repeat("synthetic bounded content\n", 40000)} {
		ref, err := s.Put(ctx, content)
		if err != nil {
			t.Fatal(err)
		}
		if ref.Size != int64(len(content)) || ref.StoredBytes < 1 {
			t.Fatal(ref)
		}
		digest := sha256.Sum256([]byte(content))
		if ref.Digest != hex.EncodeToString(digest[:]) {
			t.Fatal(ref)
		}
		got, err := s.Read(ctx, ref)
		if err != nil || got != content {
			t.Fatalf("read mismatch: bytes %d %v", len(got), err)
		}
		before, _ := os.Stat(blobPath(s, ref))
		original, _ := os.ReadFile(blobPath(s, ref))
		again, err := s.Put(ctx, content)
		if err != nil || again != ref {
			t.Fatalf("dedup mismatch: %+v %v", again, err)
		}
		after, _ := os.Stat(blobPath(s, ref))
		if !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
			t.Fatal("dedup rewrote existing file")
		}
		other := coldStore(t)
		otherRef, err := other.Put(ctx, content)
		if err != nil || otherRef != ref {
			t.Fatalf("encoding was not deterministic: %+v %v", otherRef, err)
		}
		encoded, _ := os.ReadFile(blobPath(other, ref))
		if !bytes.Equal(original, encoded) {
			t.Fatal("gzip bytes differ between stores")
		}
		if before.Mode().Perm() != 0600 {
			t.Fatal("blob is not private")
		}
	}
	info, err := os.Stat(s.Root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("root is not private: %v", err)
	}
	inv, err := s.Inventory(ctx)
	if err != nil || inv.Files != 3 || inv.Bytes < 1 || inv.TemporaryBytes != 0 {
		t.Fatalf("unexpected inventory: %+v %v", inv, err)
	}
}

func TestColdInvalidAndMissingInputsHaveNoSideEffects(t *testing.T) {
	s := coldStore(t)
	ctx := context.Background()
	inv, err := s.Inventory(ctx)
	if err != nil || inv != (Inventory{}) {
		t.Fatalf("missing root inventory: %+v %v", inv, err)
	}
	if _, err = os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatal("inventory created a missing root")
	}
	for _, content := range []string{string([]byte{0xff}), strings.Repeat("x", protocol.MaxContentBytes+1)} {
		_, err = s.Put(ctx, content)
		requireError(t, err, ErrInvalid)
	}
	if _, err = os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatal("invalid content created a root")
	}
	for _, ref := range []BlobRef{{}, {Digest: strings.Repeat("0", 64), Size: -1, StoredBytes: 30},
		{Digest: strings.Repeat("a", 63) + "A", StoredBytes: 30},
		{Digest: strings.Repeat("a", 64), Size: protocol.MaxContentBytes + 1, StoredBytes: 30},
		{Digest: strings.Repeat("a", 64), Size: 1, StoredBytes: maxStoredBytes + 1}} {
		_, err = s.Read(ctx, ref)
		requireError(t, err, ErrInvalid)
	}
	_, err = s.Read(ctx, BlobRef{Digest: strings.Repeat("a", 64), Size: 1, StoredBytes: 30})
	requireError(t, err, ErrUnavailable)
	for _, invalid := range []ColdStore{{Root: "relative", MaxBytes: 1024}, {Root: s.Root}, {Root: s.Root, MaxBytes: -1}} {
		_, err = invalid.Put(ctx, "x")
		requireError(t, err, ErrInvalid)
		_, err = invalid.Inventory(ctx)
		requireError(t, err, ErrInvalid)
	}
}

func TestColdBudgetChargesOrphansAndTemporaryFiles(t *testing.T) {
	s := coldStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, "first synthetic payload")
	if err != nil {
		t.Fatal(err)
	}
	orphan := []byte(strings.Repeat("orphan", 20))
	temporary := []byte(strings.Repeat("temporary", 30))
	if err = os.WriteFile(filepath.Join(s.Root, "unreferenced.data"), orphan, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Root, coldTemporary+"interrupted"), temporary, 0600); err != nil {
		t.Fatal(err)
	}
	inv, err := s.Inventory(ctx)
	if err != nil || inv.Files != 3 || inv.Bytes != ref.StoredBytes+int64(len(orphan)+len(temporary)) || inv.TemporaryBytes != int64(len(temporary)) {
		t.Fatalf("unexpected inventory: %+v %v", inv, err)
	}
	s.MaxBytes = inv.Bytes
	_, err = s.Put(ctx, "another payload")
	requireError(t, err, ErrBudget)
	after, err := s.Inventory(ctx)
	if err != nil || after != inv {
		t.Fatalf("budget failure changed stored files: %+v %v", after, err)
	}
	// Reducing the limit must not obstruct recovery using an already durable
	// unreferenced blob after a previously interrupted metadata transaction.
	s.MaxBytes = 1
	got, err := s.Put(ctx, "first synthetic payload")
	if err != nil || got != ref {
		t.Fatalf("existing blob could not be reused above new limit: %+v %v", got, err)
	}
	if content, err := s.Read(ctx, ref); err != nil || content != "first synthetic payload" {
		t.Fatalf("budget change obstructed reads: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(s.Root, "unreferenced.data")); err != nil || !bytes.Equal(b, orphan) {
		t.Fatal("orphan was deleted or rewritten")
	}
}

func TestColdCorruptionAndTrailingDataFailClosed(t *testing.T) {
	for _, mode := range []string{"truncated", "crc", "trailing", "multistream", "hash", "size", "invalid_utf8", "oversized_raw"} {
		t.Run(mode, func(t *testing.T) {
			s := coldStore(t)
			ctx := context.Background()
			ref, err := s.Put(ctx, "trusted synthetic original")
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := os.ReadFile(blobPath(s, ref))
			switch mode {
			case "truncated":
				encoded = encoded[:len(encoded)-1]
			case "crc":
				encoded[len(encoded)-8] ^= 1
			case "trailing":
				encoded = append(encoded, 1)
			case "multistream":
				encoded = append(encoded, encoded...)
			case "hash":
				encoded, _ = compressContent(ctx, "changed synthetic payload")
				ref.Size = int64(len("changed synthetic payload"))
			case "size":
				ref.Size--
			case "invalid_utf8":
				encoded, _ = compressContent(ctx, string([]byte{0xff}))
				ref.Size = 1
			case "oversized_raw":
				encoded, _ = compressContent(ctx, strings.Repeat("z", protocol.MaxContentBytes+1))
				ref.Size = protocol.MaxContentBytes
			}
			if err = os.WriteFile(blobPath(s, ref), encoded, 0600); err != nil {
				t.Fatal(err)
			}
			ref.StoredBytes = int64(len(encoded))
			_, err = s.Read(ctx, ref)
			requireError(t, err, ErrIntegrity)
			before := append([]byte(nil), encoded...)
			_, err = s.Put(ctx, "trusted synthetic original")
			if mode == "size" {
				// Only the caller's reference was changed; the immutable file
				// still contains the exact original and remains reusable.
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireError(t, err, ErrIntegrity)
			}
			after, _ := os.ReadFile(blobPath(s, ref))
			if !bytes.Equal(before, after) {
				t.Fatal("Put overwrote corrupt existing file")
			}
		})
	}
}

func TestColdAcceptsValidExistingEncoderWithoutRewriting(t *testing.T) {
	s := coldStore(t)
	ctx := context.Background()
	content := strings.Repeat("alternate supported gzip encoding\n", 100)
	ref, err := s.Put(ctx, content)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	_, _ = w.Write([]byte(content))
	_ = w.Close()
	if err = os.WriteFile(blobPath(s, ref), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Put(ctx, content)
	if err != nil || got.Digest != ref.Digest || got.Size != ref.Size || got.StoredBytes != int64(b.Len()) {
		t.Fatalf("valid existing encoder not reused: %+v %v", got, err)
	}
	after, _ := os.ReadFile(blobPath(s, ref))
	if !bytes.Equal(after, b.Bytes()) {
		t.Fatal("existing valid gzip encoding was rewritten")
	}
}

func TestColdUnsafePathsEntriesAndPermissions(t *testing.T) {
	for _, mode := range []string{"root_symlink", "ancestor_symlink", "blob_symlink", "lock_symlink", "root_file", "root_permissions", "blob_permissions", "blob_directory", "inventory_symlink", "inventory_directory", "lock_content"} {
		t.Run(mode, func(t *testing.T) {
			s := coldStore(t)
			ctx := context.Background()
			ref, err := s.Put(ctx, "synthetic content")
			if err != nil {
				t.Fatal(err)
			}
			private := filepath.Join(filepath.Dir(s.Root), "untouched")
			if err = os.WriteFile(private, []byte("preserve this synthetic file"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "root_symlink":
				link := s.Root + "-link"
				if err = os.Symlink(s.Root, link); err != nil {
					t.Fatal(err)
				}
				s.Root = link
			case "ancestor_symlink":
				link := filepath.Dir(s.Root) + "-link"
				if err = os.Symlink(filepath.Dir(s.Root), link); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(link) })
				s.Root = filepath.Join(link, filepath.Base(s.Root))
			case "blob_symlink":
				_ = os.Remove(blobPath(s, ref))
				err = os.Symlink(private, blobPath(s, ref))
			case "lock_symlink":
				_ = os.Remove(filepath.Join(s.Root, coldLockName))
				err = os.Symlink(private, filepath.Join(s.Root, coldLockName))
			case "root_file":
				s.Root = private
			case "root_permissions":
				err = os.Chmod(s.Root, 0755)
			case "blob_permissions":
				err = os.Chmod(blobPath(s, ref), 0644)
			case "blob_directory":
				_ = os.Remove(blobPath(s, ref))
				err = os.Mkdir(blobPath(s, ref), 0700)
			case "inventory_symlink":
				err = os.Symlink(private, filepath.Join(s.Root, "unsafe-entry"))
			case "inventory_directory":
				err = os.Mkdir(filepath.Join(s.Root, "unexpected-directory"), 0700)
			case "lock_content":
				err = os.WriteFile(filepath.Join(s.Root, coldLockName), []byte("unexpected"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "lock_symlink" && mode != "lock_content" && mode != "inventory_symlink" && mode != "inventory_directory" {
				_, err = s.Read(ctx, ref)
				requireError(t, err, ErrUnavailable)
			}
			_, err = s.Inventory(ctx)
			if mode == "lock_content" {
				requireError(t, err, ErrIntegrity)
			} else {
				requireError(t, err, ErrUnavailable)
			}
			// New content forces quota inventory and cannot bypass unsafe entries.
			_, err = s.Put(ctx, "different payload")
			requireError(t, err, ErrUnavailable)
			got, err := os.ReadFile(private)
			if err != nil || string(got) != "preserve this synthetic file" {
				t.Fatal("unsafe path changed unrelated file")
			}
		})
	}
}

func TestColdCancellationAndBoundedLockWait(t *testing.T) {
	s := coldStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Put(ctx, "canceled")
	requireError(t, err, context.Canceled)
	_, err = s.Inventory(ctx)
	requireError(t, err, context.Canceled)
	if _, err = os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatal("canceled Put created a root")
	}
	ref, err := s.Put(context.Background(), "original")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Read(ctx, ref)
	requireError(t, err, context.Canceled)
	dir, err := openColdRoot(s.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	lock, err := lockColdDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = s.Put(ctx, "locked payload")
	requireError(t, err, context.DeadlineExceeded)
	if time.Since(started) > time.Second {
		t.Fatal("lock ignored context deadline")
	}
	_, err = s.Inventory(ctx)
	requireError(t, err, context.DeadlineExceeded)
	if got, err := s.Read(context.Background(), ref); err != nil || got != "original" {
		t.Fatal("immutable reads were blocked by a writer lock")
	}
}

func TestColdConcurrentDedupAndQuotaAcrossStores(t *testing.T) {
	s := coldStore(t)
	ctx := context.Background()
	const workers = 12
	refs := make(chan BlobRef, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			other := ColdStore{Root: s.Root, MaxBytes: s.MaxBytes}
			ref, err := other.Put(ctx, "shared content")
			refs <- ref
			errs <- err
		})
	}
	wg.Wait()
	close(refs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first BlobRef
	for ref := range refs {
		if first == (BlobRef{}) {
			first = ref
		}
		if ref != first {
			t.Fatal("dedup returned divergent references")
		}
	}
	inv, err := s.Inventory(ctx)
	if err != nil || inv.Files != 1 || inv.Bytes != first.StoredBytes || inv.TemporaryBytes != 0 {
		t.Fatalf("concurrent dedup inventory: %+v %v", inv, err)
	}
	limited := coldStore(t)
	encoded, _ := compressContent(ctx, "synthetic number 00")
	limited.MaxBytes = int64(len(encoded)) + 4
	results := make(chan error, workers)
	for i := range workers {
		wg.Go(func() {
			other := ColdStore{Root: limited.Root, MaxBytes: limited.MaxBytes}
			_, err := other.Put(ctx, fmt.Sprintf("synthetic number %02d", i))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrBudget) {
			t.Fatal(err)
		}
	}
	inv, err = limited.Inventory(ctx)
	if success != 1 || err != nil || inv.Files != 1 || inv.Bytes > limited.MaxBytes || inv.TemporaryBytes != 0 {
		t.Fatalf("quota raced: successes %d inventory %+v error %v", success, inv, err)
	}
}

func TestColdChildPut(t *testing.T) {
	root := os.Getenv("QUESTLOCK_RETENTION_TEST_ROOT")
	if root == "" {
		return
	}
	s := ColdStore{Root: root, MaxBytes: 64}
	_, err := s.Put(context.Background(), os.Getenv("QUESTLOCK_RETENTION_TEST_CONTENT"))
	if errors.Is(err, ErrBudget) {
		os.Exit(2)
	}
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestColdQuotaIsSerializedAcrossProcesses(t *testing.T) {
	s := coldStore(t)
	commands := make([]*exec.Cmd, 4)
	for i := range commands {
		cmd := exec.Command(os.Args[0], "-test.run=^TestColdChildPut$")
		cmd.Env = []string{"QUESTLOCK_RETENTION_TEST_ROOT=" + s.Root,
			fmt.Sprintf("QUESTLOCK_RETENTION_TEST_CONTENT=process synthetic payload %d", i)}
		commands[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	success := 0
	for _, cmd := range commands {
		err := cmd.Wait()
		if err == nil {
			success++
			continue
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("unexpected child result: %v", err)
		}
	}
	inv, err := s.Inventory(context.Background())
	if success != 1 || err != nil || inv.Files != 1 || inv.Bytes > 64 {
		t.Fatalf("cross-process quota raced: successes %d inventory %+v error %v", success, inv, err)
	}
}
