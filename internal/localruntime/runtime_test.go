//go:build darwin || linux

package localruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
	"golang.org/x/sys/unix"
)

const testVersion = "test-local-runtime"

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "__local-broker" && os.Args[2] == "--state-dir" {
		state := os.Args[3]
		_ = os.WriteFile(filepath.Join(state, "test-helper.pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		if f, err := os.OpenFile(filepath.Join(state, "test-launches"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			_, _ = fmt.Fprintln(f, os.Getpid())
			_ = f.Close()
		}
		// Bound even a failed test's detached subprocess lifetime independently
		// of metadata-based cleanup in the parent.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := Serve(ctx, Options{StateDir: state, Version: testVersion}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "__test-ensure" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, err := Ensure(ctx, Options{StateDir: os.Args[2], Executable: os.Args[0], Version: testVersion})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		report, err := Inspect(ctx, os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

func testOptions(t *testing.T) Options {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	must(t, os.Chmod(root, 0700))
	o := Options{StateDir: filepath.Join(root, "state"), Executable: os.Args[0], Version: testVersion}
	// This helper PID belongs solely to this private fixture. Cleanup remains
	// possible if a failing test deliberately corrupts public runtime metadata.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = Stop(ctx, o.StateDir)
		pidBytes, pidErr := os.ReadFile(filepath.Join(o.StateDir, "test-helper.pid"))
		if os.IsNotExist(pidErr) {
			return // This fixture never launched the test executable.
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if lock, err := acquire(context.Background(), filepath.Join(o.StateDir, "owner.lock"), false); err == nil {
				_ = lock.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if pidErr == nil {
			if pid, err := strconv.Atoi(string(pidBytes)); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
	})
	return o
}

func ensure(t *testing.T, o Options) Handle {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	h, err := Ensure(ctx, o)
	must(t, err)
	return h
}

func stopAndWait(t *testing.T, state string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, Stop(ctx, state))
	lock, err := acquire(ctx, filepath.Join(state, "owner.lock"), true)
	must(t, err)
	must(t, lock.Close())
}

func request(t *testing.T, method, endpoint, header, value string) int {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, nil)
	must(t, err)
	if header != "" {
		req.Header.Set(header, value)
	}
	h := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer h.CloseIdleConnections()
	resp, err := h.Do(req)
	must(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestConcurrentEnsureSharesOneAuthenticatedDetachedBroker(t *testing.T) {
	o := testOptions(t)
	const callers = 6
	var wg sync.WaitGroup
	results := make(chan Handle, callers)
	errors := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			h, err := Ensure(ctx, o)
			results <- h
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		must(t, err)
	}
	var endpoint string
	for h := range results {
		if endpoint == "" {
			endpoint = h.URL
		}
		if h.URL != endpoint || h.Version != testVersion {
			t.Fatal("concurrent setup returned different broker owners")
		}
	}
	if launches := strings.Fields(string(read(t, filepath.Join(o.StateDir, "test-launches")))); len(launches) != 1 {
		t.Fatalf("started %d broker processes", len(launches))
	}
	u, err := url.Parse(endpoint)
	must(t, err)
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" {
		t.Fatal("broker is not literal loopback")
	}
	c, err := readConnection(o.StateDir)
	must(t, err)
	if status := request(t, http.MethodGet, endpoint+"/_questlock/local/ready", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated readiness: %d", status)
	}
	if status := request(t, http.MethodPost, endpoint+"/_questlock/local/stop", "X-Questlock-Local-Admin", "incorrect"); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stop: %d", status)
	}
	if status := request(t, http.MethodGet, endpoint+"/_questlock/local/ready", "X-Questlock-Local-Admin", c.Admin); status != http.StatusOK {
		t.Fatalf("authenticated readiness: %d", status)
	}
	if status := request(t, http.MethodGet, endpoint+"/v1/artifacts", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated data access: %d", status)
	}
	if lock, err := acquire(context.Background(), filepath.Join(o.StateDir, "owner.lock"), false); err == nil {
		lock.Close()
		t.Fatal("detached broker has no lifetime owner lock")
	}
	report, err := Inspect(context.Background(), o.StateDir)
	must(t, err)
	if report.Status != "ready" || !report.Loopback || !report.CredentialsPrivate {
		t.Fatalf("readiness report: %+v", report)
	}
	info, err := os.Stat(o.StateDir)
	must(t, err)
	if info.Mode().Perm() != 0700 {
		t.Fatal("state directory is not private")
	}
	for _, name := range []string{"broker.json", "worker.token", "state.db", "broker.log", "owner.lock", "start.lock"} {
		info, err := os.Stat(filepath.Join(o.StateDir, name))
		must(t, err)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s is not private", name)
		}
	}
}

func TestEnsureSubprocessStdoutAndLogsNeverContainCredentials(t *testing.T) {
	o := testOptions(t)
	cmd := exec.Command(os.Args[0], "__test-ensure", o.StateDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ensure helper: %v: %s", err, stderr.String())
	}
	c, err := readConnection(o.StateDir)
	must(t, err)
	token := strings.TrimSpace(string(read(t, filepath.Join(o.StateDir, "worker.token"))))
	for _, output := range [][]byte{stdout.Bytes(), stderr.Bytes(), read(t, filepath.Join(o.StateDir, "broker.log"))} {
		for _, secret := range []string{token, c.Admin, c.Instance} {
			if bytes.Contains(output, []byte(secret)) {
				t.Fatal("private credential or broker identity appeared in output")
			}
		}
	}
	var report Report
	must(t, json.Unmarshal(stdout.Bytes(), &report))
	if report.Status != "ready" {
		t.Fatal(report)
	}
}

func TestStopRestartPreservesTokenStoreAndPublicationReceipts(t *testing.T) {
	o := testOptions(t)
	h := ensure(t, o)
	request := protocol.PublishRequest{Key: "onboarding/test-artifact", OperationID: "persistent-op", Content: "complete deterministic task", ExpectedVersion: 0}
	first, err := h.Client.Publish(context.Background(), request)
	must(t, err)
	before, err := readConnection(o.StateDir)
	must(t, err)
	token := read(t, filepath.Join(o.StateDir, "worker.token"))
	stopAndWait(t, o.StateDir)
	if _, err := os.Stat(filepath.Join(o.StateDir, "broker.json")); !os.IsNotExist(err) {
		t.Fatal("stopped broker retained live identity")
	}
	// Model an unclean prior exit retaining valid metadata. The lifetime owner
	// has released its lock, so startup may replace this stale process identity.
	stale, err := json.Marshal(before)
	must(t, err)
	must(t, writePrivate(filepath.Join(o.StateDir, "broker.json"), stale))
	h = ensure(t, o)
	after, err := readConnection(o.StateDir)
	must(t, err)
	if before.Instance == after.Instance || before.Admin == after.Admin {
		t.Fatal("restart reused process identity")
	}
	if !bytes.Equal(read(t, filepath.Join(o.StateDir, "worker.token")), token) {
		t.Fatal("restart rotated stored worker token")
	}
	artifact, err := h.Client.Get(context.Background(), request.Key)
	must(t, err)
	if artifact.Content != request.Content || artifact.Version != first.Artifact.Version {
		t.Fatal("restart lost persistent artifact")
	}
	replayed, err := h.Client.Publish(context.Background(), request)
	must(t, err)
	if !replayed.Replayed || replayed.Artifact.Version != first.Artifact.Version {
		t.Fatal("restart lost operation receipt")
	}
}

func TestLiveOwnerWithStaleMetadataIsNeverReplaced(t *testing.T) {
	o := testOptions(t)
	_ = ensure(t, o)
	metadataPath := filepath.Join(o.StateDir, "broker.json")
	original := read(t, metadataPath)
	t.Cleanup(func() { _ = writePrivate(metadataPath, original) })
	var c connection
	must(t, json.Unmarshal(original, &c))
	c.Instance = strings.Repeat("0", 64)
	changed, err := json.Marshal(c)
	must(t, err)
	must(t, writePrivate(metadataPath, changed))
	token := read(t, filepath.Join(o.StateDir, "worker.token"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Ensure(ctx, o); err == nil || !strings.Contains(err.Error(), "no second owner") {
		t.Fatalf("live owner was not protected: %v", err)
	}
	if len(strings.Fields(string(read(t, filepath.Join(o.StateDir, "test-launches"))))) != 1 {
		t.Fatal("stale metadata started a second broker")
	}
	if !bytes.Equal(read(t, metadataPath), changed) || !bytes.Equal(read(t, filepath.Join(o.StateDir, "worker.token")), token) {
		t.Fatal("unresponsive owner metadata or token was replaced")
	}
	report, err := Inspect(ctx, o.StateDir)
	if err == nil || report.Status != "unavailable" {
		t.Fatal("stale identity reported ready")
	}
	must(t, writePrivate(metadataPath, original))
}

func TestHeldOwnerWithoutMetadataCannotBeTakenOver(t *testing.T) {
	o := testOptions(t)
	must(t, privateDir(o.StateDir))
	lock, err := acquire(context.Background(), filepath.Join(o.StateDir, "owner.lock"), false)
	must(t, err)
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Ensure(ctx, o); err == nil {
		t.Fatal("unresponsive live owner was replaced")
	}
	if _, err := os.Lstat(filepath.Join(o.StateDir, "test-launches")); !os.IsNotExist(err) {
		t.Fatal("second broker launched behind held owner lock")
	}
}

func TestMalformedMetadataNeverAuthorizesReplacement(t *testing.T) {
	valid := connection{Schema: 1, URL: "http://127.0.0.1:12345", Instance: strings.Repeat("1", 64), Admin: strings.Repeat("2", 64), Version: testVersion}
	base, err := json.Marshal(valid)
	must(t, err)
	cases := map[string][]byte{"bad JSON": []byte("{"), "unknown field": append(append([]byte(nil), base[:len(base)-1]...), []byte(",\"extra\":true}")...), "trailing JSON": append(append([]byte(nil), base...), []byte(" {}")...)}
	for name, endpoint := range map[string]string{"hostname": "http://localhost:12345", "remote": "http://192.0.2.1:12345", "user info": "http://name:secret@127.0.0.1:12345", "path": "http://127.0.0.1:12345/path", "query": "http://127.0.0.1:12345/?secret=hidden"} {
		modified := valid
		modified.URL = endpoint
		cases[name], err = json.Marshal(modified)
		must(t, err)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			o := testOptions(t)
			must(t, privateDir(o.StateDir))
			path := filepath.Join(o.StateDir, "broker.json")
			must(t, os.WriteFile(path, data, 0600))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := Ensure(ctx, o); err == nil {
				t.Fatal("malformed broker metadata accepted")
			}
			if !bytes.Equal(read(t, path), data) {
				t.Fatal("invalid metadata overwritten")
			}
			if _, err := os.Lstat(filepath.Join(o.StateDir, "test-launches")); !os.IsNotExist(err) {
				t.Fatal("invalid metadata launched a broker")
			}
		})
	}
}

func TestPrivateFilesRefuseSymlinks(t *testing.T) {
	for _, name := range []string{"start.lock", "owner.lock", "broker.json", "worker.token", "state.db", "state.db-wal", "state.db-shm", "broker.log"} {
		t.Run(name, func(t *testing.T) {
			o := testOptions(t)
			must(t, privateDir(o.StateDir))
			outside := filepath.Join(filepath.Dir(o.StateDir), "outside")
			original := []byte("outside file must remain unchanged")
			must(t, os.WriteFile(outside, original, 0600))
			link := filepath.Join(o.StateDir, name)
			must(t, os.Symlink(outside, link))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := Ensure(ctx, o); err == nil {
				t.Fatal("symlinked runtime file accepted")
			}
			info, err := os.Lstat(link)
			must(t, err)
			if info.Mode()&os.ModeSymlink == 0 || !bytes.Equal(read(t, outside), original) {
				t.Fatal("symlink or its destination changed")
			}
		})
	}
}

func TestCanonicalPathsAndSpecialLockFilesFailClosed(t *testing.T) {
	o := testOptions(t)
	for _, path := range []string{"relative/state", "/", filepath.Dir(o.StateDir) + "/unused/../state"} {
		if err := CheckPath(path); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	alias := filepath.Join(filepath.Dir(o.StateDir), "alias")
	must(t, os.Symlink(filepath.Dir(o.StateDir), alias))
	if err := CheckPath(filepath.Join(alias, "state")); err == nil {
		t.Fatal("symlink directory accepted")
	}
	must(t, privateDir(o.StateDir))
	for _, name := range []string{"start.lock", "owner.lock"} {
		path := filepath.Join(o.StateDir, name)
		must(t, unix.Mkfifo(path, 0600))
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		done := make(chan error, 1)
		go func() {
			lock, err := acquire(ctx, path, false)
			if lock != nil {
				lock.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("FIFO accepted as a lock")
			}
		case <-time.After(time.Second):
			t.Fatal("FIFO blocked before context could be checked")
		}
		cancel()
		must(t, os.Remove(path))
	}
}

func TestInspectDoesNotProvisionOrStart(t *testing.T) {
	o := testOptions(t)
	report, err := Inspect(context.Background(), o.StateDir)
	must(t, err)
	if report.Status != "not_started" {
		t.Fatal(report)
	}
	if _, err := os.Lstat(o.StateDir); !os.IsNotExist(err) {
		t.Fatal("inspection created local state")
	}
}

func TestAdminIdentityAndCredentialAreBothRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Questlock-Local-Admin") != strings.Repeat("a", 64) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"instance": strings.Repeat("b", 64), "version": testVersion})
	}))
	defer server.Close()
	c := connection{URL: server.URL, Admin: strings.Repeat("a", 64), Instance: strings.Repeat("c", 64), Version: testVersion}
	if err := adminRequest(context.Background(), c, http.MethodGet, "/_questlock/local/ready"); err == nil {
		t.Fatal("stale instance identity accepted")
	}
	c.Instance = strings.Repeat("b", 64)
	c.Admin = strings.Repeat("d", 64)
	if err := adminRequest(context.Background(), c, http.MethodGet, "/_questlock/local/ready"); err == nil {
		t.Fatal("bad admin credential accepted")
	}
}

func TestInvalidWorkerTokenIsNotAcceptedByReadiness(t *testing.T) {
	o := testOptions(t)
	h := ensure(t, o)
	tokenPath := filepath.Join(o.StateDir, "worker.token")
	original := read(t, tokenPath)
	t.Cleanup(func() { _ = writePrivate(tokenPath, original) })
	must(t, writePrivate(tokenPath, []byte("af_invalid-token\n")))
	if _, err := handle(context.Background(), o.StateDir); err == nil {
		t.Fatal("readiness trusted invalid saved worker token")
	}
	bad, err := client.New(h.URL, "af_invalid-token")
	must(t, err)
	_, err = bad.Get(context.Background(), "onboarding/v1/ready")
	var apiError *client.Error
	if !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized {
		t.Fatalf("invalid token accepted: %v", err)
	}
	must(t, writePrivate(tokenPath, original))
}

func TestServeRefusesSavedTokenWithoutDatabase(t *testing.T) {
	o := testOptions(t)
	must(t, privateDir(o.StateDir))
	tokenPath := filepath.Join(o.StateDir, "worker.token")
	original := []byte("af_preexisting-credential\n")
	must(t, writePrivate(tokenPath, original))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Serve(ctx, o); err == nil {
		t.Fatal("saved credential without database started an unusable broker")
	}
	if _, err := os.Lstat(filepath.Join(o.StateDir, "state.db")); !os.IsNotExist(err) {
		t.Fatal("saved-token refusal created a new database")
	}
	if !bytes.Equal(read(t, tokenPath), original) {
		t.Fatal("saved credential was rotated")
	}
	if _, err := os.Lstat(filepath.Join(o.StateDir, "broker.json")); !os.IsNotExist(err) {
		t.Fatal("saved-token refusal published a live broker identity")
	}
}

func TestServeAuthenticatesSavedTokenBeforePublishingIdentity(t *testing.T) {
	o := testOptions(t)
	must(t, privateDir(o.StateDir))
	dbPath := filepath.Join(o.StateDir, "state.db")
	s, err := store.Open(dbPath)
	must(t, err)
	valid, err := s.CreateToken(context.Background(), "local-onboarding", "local-onboarding")
	must(t, err)
	must(t, s.Close())
	tokenPath := filepath.Join(o.StateDir, "worker.token")
	invalid := []byte("af_incorrect-credential\n")
	must(t, writePrivate(tokenPath, invalid))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Serve(ctx, o); err == nil {
		t.Fatal("unauthenticated saved credential started a broker")
	}
	if !bytes.Equal(read(t, tokenPath), invalid) {
		t.Fatal("invalid saved credential was silently replaced")
	}
	if _, err := os.Lstat(filepath.Join(o.StateDir, "broker.json")); !os.IsNotExist(err) {
		t.Fatal("invalid saved credential published broker metadata")
	}
	s, err = store.Open(dbPath)
	must(t, err)
	defer s.Close()
	if _, err := s.Authenticate(context.Background(), valid); err != nil {
		t.Fatal("saved-token refusal modified the original store authorization")
	}
}
