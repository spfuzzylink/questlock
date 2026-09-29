package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spfuzzylink/agent-fence/protocol"
)

var testContext = context.Background()

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func newAgent(t *testing.T, s *Store, id, scope string) (string, Principal) {
	t.Helper()
	token, err := s.CreateToken(testContext, id, scope)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(testContext, token)
	if err != nil {
		t.Fatal(err)
	}
	return token, p
}

func requireCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want error code %q, got %v", code, err)
	}
	return e
}

func publish(t *testing.T, s *Store, p Principal, r protocol.PublishRequest) protocol.PublishResult {
	t.Helper()
	v, err := s.Publish(testContext, p, r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenPragmasAndPermissions(t *testing.T) {
	s, path := newStore(t)
	for pragma, expected := range map[string]string{"journal_mode": "wal", "synchronous": "2", "busy_timeout": "5000", "foreign_keys": "1"} {
		var got string
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != expected {
			t.Fatalf("PRAGMA %s: got %q, %v; want %q", pragma, got, err, expected)
		}
	}
	for name, want := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions for %s: %v, %v; want %v", name, info, err, want)
		}
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0750); err != nil {
		t.Fatal(err)
	}
	other, err := Open(filepath.Join(parent, "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	info, err := os.Stat(parent)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("Open changed existing parent permissions: %v, %v", info, err)
	}
	link := filepath.Join(t.TempDir(), "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	_, err = Open(link)
	requireCode(t, err, "invalid")
}

func TestAgentCredentialsAndRevocation(t *testing.T) {
	s, _ := newStore(t)
	token, p := newAgent(t, s, "worker-1", "team-a")
	otherToken, _ := newAgent(t, s, "worker-2", "team-a")
	if token == otherToken || len(token) != 46 {
		t.Fatal("tokens should be independent 256-bit secrets")
	}
	var persisted string
	if err := s.db.QueryRow("SELECT token_hash FROM agents WHERE agent_id = 'worker-1'").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != tokenHash(token) || strings.Contains(persisted, token) {
		t.Fatal("the database must contain a digest, not the plaintext token")
	}
	_, err := s.Authenticate(testContext, "wrong")
	requireCode(t, err, "unauthorized")
	_, err = s.Authenticate(testContext, "af_"+strings.Repeat("x", 43))
	requireCode(t, err, "unauthorized")
	_, err = s.CreateToken(testContext, "worker-1", "team-b")
	requireCode(t, err, "agent_exists")
	for _, value := range []string{"", "../evil", ".hidden", "with space", "ä", strings.Repeat("x", 65)} {
		_, err = s.CreateToken(testContext, value, "scope")
		requireCode(t, err, "invalid")
		_, err = s.CreateToken(testContext, "unused", value)
		requireCode(t, err, "invalid")
	}
	publish(t, s, p, protocol.PublishRequest{Key: "x", OperationID: "one", Content: "safe"})
	if err = s.RevokeAgent(testContext, p.AgentID); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAgent(testContext, p.AgentID); err != nil {
		t.Fatalf("revocation should be idempotent: %v", err)
	}
	_, err = s.Authenticate(testContext, token)
	requireCode(t, err, "unauthorized")
	_, err = s.Publish(testContext, p, protocol.PublishRequest{Key: "x", OperationID: "two", ExpectedVersion: 1, Content: "unsafe"})
	requireCode(t, err, "unauthorized")
	_, err = s.Publish(testContext, p, protocol.PublishRequest{Key: "x", OperationID: "one", Content: "safe"})
	requireCode(t, err, "unauthorized")
	_, err = s.Get(testContext, p, "x")
	requireCode(t, err, "unauthorized")
	_, err = s.List(testContext, p)
	requireCode(t, err, "unauthorized")
	_, err = s.Audit(testContext, p, 10)
	requireCode(t, err, "unauthorized")
	_, err = s.CreateToken(testContext, p.AgentID, p.Scope)
	requireCode(t, err, "agent_exists")
	requireCode(t, s.RevokeAgent(testContext, "missing"), "not_found")
}

func TestPublishReplayAndReopen(t *testing.T) {
	s, path := newStore(t)
	token, p := newAgent(t, s, "worker", "scope")
	originalRequest := protocol.PublishRequest{Key: "reports/today.json", OperationID: "attempt-1", Content: `{"answer":42}`}
	first := publish(t, s, p, originalRequest)
	if first.Replayed || first.Artifact.Version != 1 || first.Artifact.UpdatedAt == "" {
		t.Fatalf("unexpected first response: %+v", first)
	}
	second := publish(t, s, p, protocol.PublishRequest{Key: originalRequest.Key, OperationID: "attempt-2", ExpectedVersion: 1, Content: "newer"})
	if second.Artifact.Version != 2 {
		t.Fatalf("want version 2, got %+v", second)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	p, err = reopened.Authenticate(testContext, token)
	if err != nil {
		t.Fatal(err)
	}
	replay := publish(t, reopened, p, originalRequest)
	if !replay.Replayed || !reflect.DeepEqual(replay.Artifact, first.Artifact) {
		t.Fatalf("retry must return exact original artifact: first=%+v replay=%+v", first, replay)
	}
	for name, changed := range map[string]protocol.PublishRequest{
		"content": {Key: originalRequest.Key, OperationID: originalRequest.OperationID, Content: "different"},
		"key":     {Key: "another", OperationID: originalRequest.OperationID, Content: originalRequest.Content},
		"version": {Key: originalRequest.Key, OperationID: originalRequest.OperationID, ExpectedVersion: 2, Content: originalRequest.Content},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := reopened.Publish(testContext, p, changed)
			requireCode(t, err, "operation_conflict")
		})
	}
	_, err = reopened.Publish(testContext, p, protocol.PublishRequest{Key: originalRequest.Key, OperationID: "stale", ExpectedVersion: 1, Content: "bad"})
	if e := requireCode(t, err, "version_conflict"); e.CurrentVersion != 2 {
		t.Fatalf("conflict must report current version: %+v", e)
	}
	current, err := reopened.Get(testContext, p, originalRequest.Key)
	if err != nil || !reflect.DeepEqual(current, second.Artifact) {
		t.Fatalf("stale write or replay changed current artifact: %+v, %v", current, err)
	}
	events, err := reopened.Audit(testContext, p, 100)
	if err != nil || len(events) != 7 {
		t.Fatalf("want all 7 committed attempts in audit: %+v, %v", events, err)
	}
	counts := map[string]int{}
	for i, event := range events {
		counts[event.Outcome]++
		if i > 0 && event.ID >= events[i-1].ID {
			t.Fatal("audit must be newest-first")
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"accepted": 2, "replayed": 1, "operation_conflict": 3, "version_conflict": 1}) {
		t.Fatalf("unexpected audit: %+v", counts)
	}
	var versions, receipts int
	if err = reopened.db.QueryRow("SELECT COUNT(*) FROM artifact_versions").Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err = reopened.db.QueryRow("SELECT COUNT(*) FROM operations").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if versions != 2 || receipts != 2 {
		t.Fatalf("rejected writes/replays must not create content or receipts: versions=%d receipts=%d", versions, receipts)
	}
}

func TestReplayAuditReportsCurrentHead(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	r := protocol.PublishRequest{Key: "artifact", OperationID: "first", Content: "version one"}
	first := publish(t, s, p, r)
	publish(t, s, p, protocol.PublishRequest{Key: r.Key, OperationID: "second", ExpectedVersion: 1, Content: "version two"})
	third := publish(t, s, p, protocol.PublishRequest{Key: r.Key, OperationID: "third", ExpectedVersion: 2, Content: "version three"})
	replay := publish(t, s, p, r)
	if !replay.Replayed || !reflect.DeepEqual(replay.Artifact, first.Artifact) || replay.Artifact.Version != 1 {
		t.Fatalf("replay must preserve original v1 response: %+v", replay)
	}
	events, err := s.Audit(testContext, p, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("read replay audit: %+v, %v", events, err)
	}
	if events[0].Outcome != "replayed" || events[0].CurrentVersion != 3 || events[0].ExpectedVersion != 0 {
		t.Fatalf("replay audit must describe actual v3 head and original expectation: %+v", events[0])
	}
	current, err := s.Get(testContext, p, r.Key)
	if err != nil || !reflect.DeepEqual(current, third.Artifact) {
		t.Fatalf("replay must leave current v3 unchanged: %+v, %v", current, err)
	}
}

func TestScopedAccessAndAgentOperationNamespaces(t *testing.T) {
	s, _ := newStore(t)
	_, alice := newAgent(t, s, "alice", "scope-a")
	_, bob := newAgent(t, s, "bob", "scope-b")
	_, peer := newAgent(t, s, "alice-peer", "scope-a")
	publish(t, s, alice, protocol.PublishRequest{Key: "only-a", OperationID: "same-operation", Content: "private-a"})
	publish(t, s, bob, protocol.PublishRequest{Key: "only-b", OperationID: "same-operation", Content: "private-b"})
	publish(t, s, peer, protocol.PublishRequest{Key: "only-a", OperationID: "same-operation", ExpectedVersion: 1, Content: "peer-a"})
	_, err := s.Get(testContext, bob, "only-a")
	requireCode(t, err, "not_found")
	items, err := s.List(testContext, alice)
	if err != nil || len(items) != 1 || items[0].Key != "only-a" || items[0].Version != 2 || items[0].Content != "" {
		t.Fatalf("list must include only scoped metadata: %+v, %v", items, err)
	}
	events, err := s.Audit(testContext, alice, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("scope audit should include both agents in scope A: %+v, %v", events, err)
	}
	for _, event := range events {
		if event.Scope != "scope-a" {
			t.Fatal("audit leaked another scope")
		}
	}
	forged := alice
	forged.Scope = bob.Scope
	_, err = s.Get(testContext, forged, "only-b")
	requireCode(t, err, "unauthorized")
	_, err = s.Publish(testContext, forged, protocol.PublishRequest{Key: "only-b", OperationID: "forged", ExpectedVersion: 1, Content: "bad"})
	requireCode(t, err, "unauthorized")
}

func TestValidationAndContentBoundaries(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	for _, key := range []string{"", "/absolute", "a/", "a//b", ".", "..", "a/../b", "a/./b", `a\b`, "a\nb", "\x00", "a/ /b", string([]byte{0xff}), strings.Repeat("a", 513)} {
		_, err := s.Publish(testContext, p, protocol.PublishRequest{Key: key, OperationID: "op", Content: "hello"})
		requireCode(t, err, "invalid")
		_, err = s.Get(testContext, p, key)
		requireCode(t, err, "invalid")
	}
	for _, op := range []string{"", " ", "a\nb", "\x7f", string([]byte{0xff}), strings.Repeat("a", 129)} {
		_, err := s.Publish(testContext, p, protocol.PublishRequest{Key: "key", OperationID: op})
		requireCode(t, err, "invalid")
	}
	for _, version := range []int64{-1, math.MaxInt64} {
		_, err := s.Publish(testContext, p, protocol.PublishRequest{Key: "key", OperationID: "op", ExpectedVersion: version})
		requireCode(t, err, "invalid")
	}
	for _, content := range []string{strings.Repeat("x", protocol.MaxContentBytes+1), string([]byte{0xff})} {
		_, err := s.Publish(testContext, p, protocol.PublishRequest{Key: "key", OperationID: "op", Content: content})
		requireCode(t, err, "invalid")
	}
	maxContent := strings.Repeat("x", protocol.MaxContentBytes)
	publish(t, s, p, protocol.PublishRequest{Key: "nested/key", OperationID: "max", Content: maxContent})
	a, err := s.Get(testContext, p, "nested/key")
	if err != nil || a.Content != maxContent {
		t.Fatalf("exactly 1 MiB must round-trip: bytes=%d, %v", len(a.Content), err)
	}
	publish(t, s, p, protocol.PublishRequest{Key: "unicode/λ", OperationID: "empty-content"})
	events, err := s.Audit(testContext, p, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("invalid requests must not modify state: events=%d, %v", len(events), err)
	}
}

func TestConcurrentCASAcrossConnections(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	const writers = 24
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			store := s
			if i%2 == 1 {
				store = other
			}
			_, err := store.Publish(testContext, p, protocol.PublishRequest{Key: "contested", OperationID: fmt.Sprintf("op-%d", i), Content: fmt.Sprintf("writer-%d", i)})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if e := requireCode(t, err, "version_conflict"); e.CurrentVersion != 1 {
			t.Fatalf("loser saw wrong current version: %+v", e)
		}
	}
	if winners != 1 {
		t.Fatalf("CAS must admit exactly one winner, got %d", winners)
	}
	events, err := s.Audit(testContext, p, 100)
	if err != nil || len(events) != writers {
		t.Fatalf("audit lost concurrent attempts: %d, %v", len(events), err)
	}
}

func TestConcurrentSameOperationReplays(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	const count = 12
	type response struct {
		result protocol.PublishResult
		err    error
	}
	results := make(chan response, count)
	start := make(chan struct{})
	for i := range count {
		go func(i int) {
			<-start
			target := s
			if i%2 == 1 {
				target = other
			}
			r, err := target.Publish(testContext, p, protocol.PublishRequest{Key: "same", OperationID: "same", Content: "same"})
			results <- response{r, err}
		}(i)
	}
	close(start)
	replays := 0
	var original protocol.Artifact
	for i := 0; i < count; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.result.Replayed {
			replays++
		}
		if i == 0 {
			original = r.result.Artifact
		}
		if !reflect.DeepEqual(original, r.result.Artifact) {
			t.Fatal("simultaneous retries returned different artifacts")
		}
	}
	if replays != count-1 || original.Version != 1 {
		t.Fatalf("want 1 write and %d replays; got %+v and %d replays", count-1, original, replays)
	}
}

func TestAtomicRollbackWhenJournalFails(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	// Fail the final write in the publication transaction to verify that the
	// preceding content, current pointer, and retry receipt also roll back.
	_, err := s.db.Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	r := protocol.PublishRequest{Key: "atomic", OperationID: "once", Content: "content"}
	if _, err = s.Publish(testContext, p, r); err == nil {
		t.Fatal("expected injected failure")
	}
	for _, table := range []string{"artifact_versions", "artifacts", "operations", "audit"} {
		var count int
		if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial transaction in %s: count=%d, %v", table, count, err)
		}
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_audit"); err != nil {
		t.Fatal(err)
	}
	if result := publish(t, s, p, r); result.Replayed || result.Artifact.Version != 1 {
		t.Fatalf("failed publication must not consume receipt/version: %+v", result)
	}
}

func TestListAndAuditLimits(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	for i := range 1002 {
		publish(t, s, p, protocol.PublishRequest{Key: fmt.Sprintf("key-%04d", i), OperationID: fmt.Sprint(i), Content: "not in list"})
	}
	items, err := s.List(testContext, p)
	if err != nil || len(items) != 1000 || items[0].Key != "key-0000" || items[999].Key != "key-0999" || items[0].Content != "" {
		t.Fatalf("unexpected capped metadata list: len=%d, err=%v", len(items), err)
	}
	for limit, count := range map[int]int{0: 100, -1: 100, 1: 1, 1001: 1000} {
		events, err := s.Audit(testContext, p, limit)
		if err != nil || len(events) != count || events[0].Key != "key-1001" {
			t.Fatalf("limit %d: got %d events, %v", limit, len(events), err)
		}
	}
}

func TestCrossProcessCompareAndSwap(t *testing.T) {
	s, path := newStore(t)
	token, p := newAgent(t, s, "worker", "scope")
	const count = 4
	type child struct {
		cmd   *exec.Cmd
		input io.WriteCloser
		out   *bufio.Reader
		err   *bytes.Buffer
	}
	children := make([]child, 0, count)
	for i := range count {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStoreProcessHelper$")
		cmd.Env = append(os.Environ(), "FENCE_STORE_TEST_HELPER=1", "FENCE_STORE_TEST_PATH="+path, "FENCE_STORE_TEST_TOKEN="+token, fmt.Sprintf("FENCE_STORE_TEST_OPERATION=process-%d", i))
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr := new(bytes.Buffer)
		cmd.Stderr = stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		children = append(children, child{cmd, input, bufio.NewReader(output), stderr})
	}
	for _, c := range children {
		line, err := c.out.ReadString('\n')
		if err != nil || line != "READY\n" {
			t.Fatalf("process setup: %q, %v, %s", line, err, c.err)
		}
	}
	for _, c := range children {
		if _, err := io.WriteString(c.input, "start\n"); err != nil {
			t.Fatal(err)
		}
		_ = c.input.Close()
	}
	winners := 0
	for _, c := range children {
		line, err := c.out.ReadString('\n')
		if err != nil {
			t.Fatalf("process output: %v, %s", err, c.err)
		}
		switch line {
		case "ACCEPTED\n":
			winners++
		case "CONFLICT\n":
		default:
			t.Fatalf("unexpected process output: %q, %s", line, c.err)
		}
		if err = c.cmd.Wait(); err != nil {
			t.Fatalf("process failed: %v, %s", err, c.err)
		}
	}
	if winners != 1 {
		t.Fatalf("cross-process CAS admitted %d winners", winners)
	}
	events, err := s.Audit(testContext, p, 10)
	if err != nil || len(events) != count {
		t.Fatalf("cross-process attempts missing from journal: %d, %v", len(events), err)
	}
}

func TestStoreProcessHelper(t *testing.T) {
	if os.Getenv("FENCE_STORE_TEST_HELPER") != "1" {
		return
	}
	s, err := Open(os.Getenv("FENCE_STORE_TEST_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Authenticate(testContext, os.Getenv("FENCE_STORE_TEST_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	if _, err = bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(testContext, p, protocol.PublishRequest{Key: "cross-process", OperationID: os.Getenv("FENCE_STORE_TEST_OPERATION"), Content: "winner"})
	if err == nil {
		fmt.Println("ACCEPTED")
		return
	}
	if e := requireCode(t, err, "version_conflict"); e.CurrentVersion != 1 {
		t.Fatalf("unexpected current version: %+v", e)
	}
	fmt.Println("CONFLICT")
}
