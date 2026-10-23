package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
	"golang.org/x/sys/unix"
)

func storageCLIFixture(t *testing.T, adaptive bool) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "broker.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateToken(context.Background(), "storage-worker", "storage-scope")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{"synthetic historical one", "synthetic historical two", "synthetic current three"} {
		if _, err := s.Publish(context.Background(), principal, protocol.PublishRequest{Key: "report", OperationID: content, ExpectedVersion: int64(i), Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	config := retention.DefaultConfig(filepath.Join(dir, "cold"))
	config.Adaptive, config.HotHistoryDays, config.KeepRecentVersions, config.ArchiveCompletedCurrent = adaptive, 0, 1, false
	policyPath := filepath.Join(dir, "policy.json")
	data, _ := json.Marshal(config)
	if err := os.WriteFile(policyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := storageCommandIO([]string{"configure", "--db", dbPath, "--config", policyPath}, &output, &diagnostics, nil); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	return dbPath, policyPath
}

func runStorageCLI(t *testing.T, args []string, source admission.Source, target any) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	if err := storageCommandIO(args, &output, &diagnostics, source); err != nil {
		t.Fatal(err, diagnostics.String(), output.String())
	}
	if err := json.Unmarshal(output.Bytes(), target); err != nil {
		t.Fatal(err, output.String())
	}
}

func TestStorageConfigIsCompleteAndCreatesNoState(t *testing.T) {
	cold := filepath.Join(t.TempDir(), "not-created")
	var config retention.Config
	runStorageCLI(t, []string{"config", "--cold-dir", cold}, nil, &config)
	if config.ColdDir != cold || config.Validate() != nil {
		t.Fatal("template is not a complete valid policy", config)
	}
	if _, err := os.Lstat(cold); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("template generation created cold storage", err)
	}
	for _, args := range [][]string{{"config"}, {"config", "--cold-dir", "relative"}, {"config", "--cold-dir", cold, "extra"}, {"config", "--cold-dir", cold, "--db", "unexpected"}} {
		var output, diagnostics bytes.Buffer
		if err := storageCommandIO(args, &output, &diagnostics, nil); err == nil {
			t.Fatalf("invalid template flags accepted: %v", args)
		}
	}
}

func TestStorageCLIArchiveDryRunApplyHistoryRestoreAndVerify(t *testing.T) {
	dbPath, _ := storageCLIFixture(t, false)
	noProbe := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		t.Fatal("fixed retention policy sampled system resources")
		return admission.Snapshot{}, admission.ErrSignals
	})
	var before store.StorageInfo
	runStorageCLI(t, []string{"info", "--db", dbPath}, nil, &before)
	if before.Policy == nil || before.Versions != 3 || before.ArchivedVersions != 0 {
		t.Fatal("configured state did not round trip", before)
	}
	for _, flags := range [][]string{{"--apply=false", "--apply"}, {"--apply", "--dry-run"}, {"--dry-run=false"}} {
		var output, diagnostics bytes.Buffer
		args := append([]string{"archive", "--db", dbPath}, flags...)
		if err := storageCommandIO(args, &output, &diagnostics, noProbe); err == nil {
			t.Fatalf("ambiguous archive mutation flags accepted: %v", flags)
		}
	}
	var dry, applied, restored, verified store.StorageReport
	runStorageCLI(t, []string{"archive", "--db", dbPath}, noProbe, &dry)
	if !dry.DryRun || dry.Items != 2 {
		t.Fatal("archive did not default to a complete dry run", dry)
	}
	var unchanged store.StorageInfo
	runStorageCLI(t, []string{"info", "--db", dbPath}, nil, &unchanged)
	if unchanged.ArchivedVersions != 0 || unchanged.HotPayloadBytes != before.HotPayloadBytes {
		t.Fatal("default archive changed content storage", unchanged)
	}
	runStorageCLI(t, []string{"archive", "--db", dbPath, "--apply"}, noProbe, &applied)
	if applied.DryRun || applied.Items != 2 {
		t.Fatal("explicit archive did not move eligible content", applied)
	}
	var historical protocol.Artifact
	runStorageCLI(t, []string{"history", "--db", dbPath, "--scope", "storage-scope", "--key", "report", "--version", "1"}, nil, &historical)
	if historical.Version != 1 || historical.Content != "synthetic historical one" {
		t.Fatal("explicit history lost exact content", historical)
	}
	runStorageCLI(t, []string{"restore", "--db", dbPath, "--max-items", "1"}, nil, &restored)
	if restored.Items != 1 {
		t.Fatal("restore did not honor its batch item bound", restored)
	}
	runStorageCLI(t, []string{"verify", "--db", dbPath}, nil, &verified)
	if verified.Operation != "verify" || verified.Status != "complete" {
		t.Fatal("verified archive integrity was not reported", verified)
	}
}

func TestStorageCLIAdaptiveUnknownSignalsDeferWithoutArchiving(t *testing.T) {
	dbPath, _ := storageCLIFixture(t, true)
	var samples atomic.Int64
	source := admission.SourceFunc(func(ctx context.Context) (admission.Snapshot, error) {
		samples.Add(1)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2100*time.Millisecond {
			t.Error("storage probe has no short deadline")
		}
		return admission.Snapshot{Known: true, At: time.Now().UTC()}, errors.New("synthetic probe failure")
	})
	var report store.StorageReport
	runStorageCLI(t, []string{"archive", "--db", dbPath, "--apply"}, source, &report)
	if samples.Load() != 1 || !report.Deferred || report.Items != 0 || report.Status != "deferred" {
		t.Fatal("unknown resources did not defer bounded archive", report)
	}
	var info store.StorageInfo
	runStorageCLI(t, []string{"info", "--db", dbPath}, nil, &info)
	if info.ArchivedVersions != 0 {
		t.Fatal("deferred archive moved content", info)
	}
}

func TestStorageCLIRejectsUnsafeFlagsAndMissingDatabaseWithoutCreation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.db")
	for _, args := range [][]string{
		{"info", "--db", missing}, {"archive", "--db", missing}, {"restore", "--db", missing},
		{"verify", "--db", missing}, {"compact", "--db", missing},
		{"history", "--db", missing, "--scope", "scope", "--key", "key", "--version", "1"},
		{"archive", "--db", missing, "--dry-run=false"}, {"archive", "--db", missing, "--apply", "--dry-run"},
		{"archive", "--db", missing, "--apply", "--apply=false"}, {"info", "--db", missing, "-db", missing},
		{"restore", "--db", missing, "--max-items", "0"}, {"restore", "--db", missing, "--max-items", "129"},
		{"history", "--db", missing, "--scope", "scope", "--key", "key"},
		{"info", "--db", missing, "--timeout", "0s"}, {"info", "--db", missing, "--timeout", "6m"},
		{"info", "--db", missing, "unexpected"}, {"info", "--db", missing, "--unknown"},
	} {
		var output, diagnostics bytes.Buffer
		if err := storageCommandIO(args, &output, &diagnostics, nil); err == nil {
			t.Fatalf("unsafe or missing database arguments accepted: %v", args)
		}
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("storage inspection created a database", err)
	}
}

func TestRetentionPolicyReaderRefusesBlockingAndMalformedInputs(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.json")
	data, _ := json.Marshal(retention.DefaultConfig(filepath.Join(dir, "cold")))
	if err := os.WriteFile(valid, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRetentionConfig(valid); err != nil {
		t.Fatal("generated policy rejected", err)
	}
	fifo, link := filepath.Join(dir, "no-writer.fifo"), filepath.Join(dir, "linked.json")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"oversized.json": bytes.Repeat([]byte{' '}, (64<<10)+1),
		"partial.json":   []byte(`{"schema":1}`),
		"duplicate.json": append([]byte(`{"schema":1,`), data[1:]...),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{dir, fifo, link, filepath.Join(dir, "oversized.json"), filepath.Join(dir, "partial.json"), filepath.Join(dir, "duplicate.json")} {
		done := make(chan error, 1)
		go func() { _, err := loadRetentionConfig(path); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("unsafe or malformed policy accepted")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("policy reader blocked before the command timeout")
		}
	}
}

func TestStorageCLITelemetryFailureDoesNotChangeArchiveResult(t *testing.T) {
	dbPath, _ := storageCLIFixture(t, false)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "private synthetic telemetry response")
	}))
	defer server.Close()
	t.Setenv("QUESTLOCK_LANGFUSE_ENDPOINT", server.URL+"/api/public/otel/v1/traces")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "fixture-public")
	t.Setenv("LANGFUSE_SECRET_KEY", "fixture-secret")
	var report store.StorageReport
	runStorageCLI(t, []string{"archive", "--db", dbPath, "--apply", "--langfuse"}, nil, &report)
	if report.Items != 2 || report.DryRun || requests.Load() != 1 {
		t.Fatal("telemetry failure changed the archive result or retried", report, requests.Load())
	}
	var info store.StorageInfo
	runStorageCLI(t, []string{"info", "--db", dbPath}, nil, &info)
	if info.ArchivedVersions != 2 {
		t.Fatal("telemetry failure changed durable archive state", info)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "private synthetic") || strings.Contains(string(encoded), "fixture-secret") {
		t.Fatal("telemetry response or credential entered command output")
	}
}
