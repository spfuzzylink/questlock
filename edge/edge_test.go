//go:build darwin || linux

package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	serverstore "github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func temp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	return p
}
func upstream(t *testing.T) (*serverstore.Store, *httptest.Server, *client.Client) {
	t.Helper()
	s, err := serverstore.Open(filepath.Join(temp(t), "authority.db"))
	must(t, err)
	t.Cleanup(func() { s.Close() })
	token, err := s.CreateToken(ctx, "edge-one", "scope")
	must(t, err)
	server := httptest.NewServer(httpapi.New(s))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL, token)
	must(t, err)
	return s, server, c
}
func otherClient(t *testing.T, s *serverstore.Store, server *httptest.Server, agent, scope string) *client.Client {
	t.Helper()
	token, err := s.CreateToken(ctx, agent, scope)
	must(t, err)
	c, err := client.New(server.URL, token)
	must(t, err)
	return c
}
func openEdge(t *testing.T, c *client.Client) (*Store, string) {
	t.Helper()
	dir := filepath.Join(temp(t), "edge")
	s, err := Open(dir)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	_, err = s.Bind(ctx, c)
	must(t, err)
	return s, dir
}
func pullAll(t *testing.T, s *Store, c *client.Client, limit int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		p, err := s.Pull(ctx, c, PullOptions{Limit: limit})
		must(t, err)
		if !p.HasMore {
			return
		}
	}
	t.Fatal("pull did not finish its bounded batch")
}

func TestLostResponseRestartRetriesSameIntentAndReceipt(t *testing.T) {
	authority, _, base := upstream(t)
	identity, err := base.Identity(ctx)
	must(t, err)
	// Keep the exact credential, but put a one-shot lost-response fixture in front
	// of the same authority. The body commits before the connection disappears.
	token, err := authority.CreateToken(ctx, "lost-reader", "scope")
	must(t, err)
	var lost atomic.Bool
	var calls atomic.Int64
	var bodiesMu sync.Mutex
	var bodies [][]byte
	handler := httpapi.New(authority)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/artifact/pinned" {
			calls.Add(1)
			var b bytes.Buffer
			_, _ = b.ReadFrom(r.Body)
			r.Body.Close()
			raw := append([]byte(nil), b.Bytes()...)
			bodiesMu.Lock()
			bodies = append(bodies, raw)
			bodiesMu.Unlock()
			r.Body = ioBody(raw)
			if !lost.Swap(true) {
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, r)
				if rr.Code != 200 {
					t.Error("fixture did not commit before dropping response")
				}
				conn, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					conn.Close()
				}
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	c, err := client.New(proxy.URL, token)
	must(t, err)
	s, dir := openEdge(t, c)
	r := protocol.PublishRequest{Key: "artifact", OperationID: "persist-before-send", Content: "exact original"}
	_, err = s.Enqueue(ctx, r)
	must(t, err)
	uncertain, err := s.Push(ctx, c, r.OperationID)
	if !errors.Is(err, ErrUncertain) || uncertain.State != "uncertain" || calls.Load() != 1 {
		t.Fatalf("lost response not retained: %+v %v calls=%d", uncertain, err, calls.Load())
	}
	if err := s.Resolve(ctx, r.OperationID, "abandon"); !errors.Is(err, ErrUncertain) {
		t.Fatal("uncertain accepted work was abandonable")
	}
	must(t, s.Close())
	s, err = Open(dir)
	must(t, err)
	defer s.Close()
	persisted, err := s.Intent(ctx, r.OperationID)
	must(t, err)
	if persisted.State != "uncertain" || persisted.Request != r {
		t.Fatal("restart changed uncertain intent")
	}
	accepted, err := s.Push(ctx, c, r.OperationID)
	must(t, err)
	if accepted.State != "accepted" || accepted.Receipt == nil || !accepted.Receipt.Replayed || accepted.Receipt.Artifact.Version != 1 {
		t.Fatal(accepted)
	}
	bodiesMu.Lock()
	same := len(bodies) == 2 && bytes.Equal(bodies[0], bodies[1])
	bodiesMu.Unlock()
	if !same {
		t.Fatal("retry changed exact HTTP request body")
	}
	// An old replay receipt is not proof of the current upstream head.
	_, err = c.Publish(ctx, protocol.PublishRequest{Key: r.Key, OperationID: "newer", ExpectedVersion: 1, Content: "new head"})
	must(t, err)
	if _, err := s.Get(ctx, r.Key); !errors.Is(err, ErrNotReady) {
		t.Fatal("publish receipt was promoted into read cache")
	}
	before := calls.Load()
	_, err = s.Push(ctx, c, r.OperationID)
	must(t, err)
	if calls.Load() != before {
		t.Fatal("confirmed receipt was re-sent")
	}
	pullAll(t, s, c, 1)
	current, err := s.Get(ctx, r.Key)
	must(t, err)
	if current.Content != "new head" || current.Version != 2 {
		t.Fatal("historical replay replaced current cache")
	}
	status, err := s.Status(ctx)
	must(t, err)
	if status.Identity.Authority != identity.Authority {
		t.Fatal("source identity changed")
	}
}

func ioBody(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }

func TestTwoEdgesConcurrentStaleWritersBlockWithoutRebase(t *testing.T) {
	authority, server, c1 := upstream(t)
	c2 := otherClient(t, authority, server, "edge-two", "scope")
	e1, _ := openEdge(t, c1)
	e2, _ := openEdge(t, c2)
	for i, e := range []*Store{e1, e2} {
		_, err := e.Enqueue(ctx, protocol.PublishRequest{Key: "shared", OperationID: "same-local-id", Content: []string{"one", "two"}[i]})
		must(t, err)
	}
	start := make(chan struct{})
	type answer struct {
		edge   *Store
		client *client.Client
		intent Intent
		err    error
	}
	done := make(chan answer, 2)
	for _, pair := range []struct {
		s *Store
		c *client.Client
	}{{e1, c1}, {e2, c2}} {
		go func(s *Store, c *client.Client) {
			<-start
			v, err := s.Push(ctx, c, "same-local-id")
			done <- answer{s, c, v, err}
		}(pair.s, pair.c)
	}
	close(start)
	var winner, loser answer
	for i := 0; i < 2; i++ {
		a := <-done
		if a.err == nil {
			winner = a
		} else if errors.Is(a.err, ErrBlocked) {
			loser = a
		} else {
			t.Fatal(a.err)
		}
	}
	if winner.edge == nil || loser.edge == nil || loser.intent.Code != "version_conflict" || loser.intent.Request.ExpectedVersion != 0 {
		t.Fatal("stale writer was not blocked by authoritative CAS")
	}
	_, err := loser.edge.Push(ctx, loser.client, "same-local-id")
	if !errors.Is(err, ErrBlocked) {
		t.Fatal("blocked intent retried implicitly")
	}
	must(t, loser.edge.Resolve(ctx, "same-local-id", "retry"))
	again, err := loser.edge.Push(ctx, loser.client, "same-local-id")
	if !errors.Is(err, ErrBlocked) || again.Request != loser.intent.Request {
		t.Fatal("explicit retry changed stale request")
	}
	must(t, loser.edge.Resolve(ctx, "same-local-id", "abandon"))
	pullAll(t, loser.edge, loser.client, 1)
	a, err := loser.edge.Get(ctx, "shared")
	must(t, err)
	if a.Content != winner.intent.Request.Content || a.Version != 1 {
		t.Fatal("loser overwrote authoritative result")
	}
}

func TestPinnedScopeAgentAndAuthorityDenyForwarding(t *testing.T) {
	authority, server, c := upstream(t)
	for _, tc := range []struct {
		name string
		c    *client.Client
	}{
		{"another agent", otherClient(t, authority, server, "another", "scope")},
		{"another scope", otherClient(t, authority, server, "private-agent", "private")},
		{"another authority", func() *client.Client { _, _, c := upstream(t); return c }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := openEdge(t, c)
			r := protocol.PublishRequest{Key: "secret", OperationID: "pin", Content: "approved only for original binding"}
			_, err := e.Enqueue(ctx, r)
			must(t, err)
			v, err := e.Push(ctx, tc.c, r.OperationID)
			if !errors.Is(err, ErrUncertain) || v.State != "uncertain" {
				t.Fatalf("wrong identity accepted: %+v %v", v, err)
			}
			if _, err := tc.c.Get(ctx, r.Key); err == nil {
				t.Fatal("wrong authority/scope received artifact")
			}
			if _, err := e.Pull(ctx, tc.c, PullOptions{}); !errors.Is(err, ErrIdentity) {
				t.Fatal("pull accepted different receipt identity")
			}
			if _, err := e.Bind(ctx, tc.c); !errors.Is(err, ErrIdentity) {
				t.Fatal("edge silently rebound")
			}
		})
	}
}

func TestSnapshotStagingRestartAndDeltaCatchupHaveNoMissingEntries(t *testing.T) {
	_, _, c := upstream(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		_, err := c.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: key, Content: "original"})
		must(t, err)
	}
	s, dir := openEdge(t, c)
	first, err := s.Pull(ctx, c, PullOptions{Limit: 1})
	must(t, err)
	if !first.HasMore {
		t.Fatal("snapshot was not paginated")
	}
	if _, err := s.Get(ctx, "a"); !errors.Is(err, ErrNotReady) {
		t.Fatal("partial snapshot was exposed")
	}
	_, err = c.Publish(ctx, protocol.PublishRequest{Key: "a", OperationID: "a2", ExpectedVersion: 1, Content: "updated"})
	must(t, err)
	_, err = c.Publish(ctx, protocol.PublishRequest{Key: "0-new", OperationID: "new", Content: "new earlier key"})
	must(t, err)
	for {
		must(t, s.Close())
		s, err = Open(dir)
		must(t, err)
		page, err := s.Pull(ctx, c, PullOptions{Limit: 1})
		must(t, err)
		if !page.HasMore {
			break
		}
	}
	defer s.Close()
	for _, key := range []string{"a", "b", "c", "d"} {
		a, err := s.Get(ctx, key)
		must(t, err)
		if a.Version != 1 || a.Content != "original" {
			t.Fatal("snapshot horizon leaked a new version")
		}
	}
	if _, err := s.Get(ctx, "0-new"); err == nil {
		t.Fatal("new key entered frozen snapshot")
	}
	pullAll(t, s, c, 1)
	a, err := s.Get(ctx, "a")
	must(t, err)
	if a.Version != 2 || a.Content != "updated" {
		t.Fatal("missed version after snapshot restart")
	}
	_, err = s.Get(ctx, "0-new")
	must(t, err)
	must(t, s.ResetPull(ctx))
	_, err = s.Pull(ctx, c, PullOptions{Limit: 1})
	must(t, err)
	a, err = s.Get(ctx, "a")
	must(t, err)
	if a.Version != 2 {
		t.Fatal("new staging snapshot hid complete prior cache")
	}
}

func TestPullPageAndCheckpointRollbackTogether(t *testing.T) {
	_, _, c := upstream(t)
	for _, key := range []string{"a", "b"} {
		_, err := c.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: key, Content: "old"})
		must(t, err)
	}
	s, _ := openEdge(t, c)
	_, err := s.db.Exec(`CREATE TRIGGER reject_fixture BEFORE INSERT ON cache WHEN NEW.key='b' BEGIN SELECT RAISE(ABORT,'fixture storage failure'); END`)
	must(t, err)
	if _, err := s.Pull(ctx, c, PullOptions{}); err == nil {
		t.Fatal("fixture did not interrupt page application")
	}
	var count int
	var cursor string
	must(t, s.db.QueryRow("SELECT COUNT(*) FROM cache").Scan(&count))
	must(t, s.db.QueryRow("SELECT cursor FROM pull_state WHERE id=1").Scan(&cursor))
	if count != 0 || cursor != "" {
		t.Fatal("failed page partly applied or advanced its checkpoint")
	}
	_, err = s.db.Exec("DROP TRIGGER reject_fixture")
	must(t, err)
	pullAll(t, s, c, 1)
	before, err := s.Status(ctx)
	must(t, err)
	for _, key := range []string{"a", "b"} {
		_, err := c.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: key + "2", ExpectedVersion: 1, Content: "new"})
		must(t, err)
	}
	_, err = s.db.Exec(`CREATE TRIGGER reject_fixture BEFORE UPDATE ON cache WHEN NEW.key='b' BEGIN SELECT RAISE(ABORT,'fixture storage failure'); END`)
	must(t, err)
	if _, err := s.Pull(ctx, c, PullOptions{}); err == nil {
		t.Fatal("delta fixture did not fail")
	}
	after, err := s.Status(ctx)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed delta advanced checkpoint/status")
	}
	for _, key := range []string{"a", "b"} {
		a, err := s.Get(ctx, key)
		must(t, err)
		if a.Content != "old" {
			t.Fatal("delta page partly applied")
		}
	}
	_, err = s.db.Exec("DROP TRIGGER reject_fixture")
	must(t, err)
	pullAll(t, s, c, 1)
	for _, key := range []string{"a", "b"} {
		a, err := s.Get(ctx, key)
		must(t, err)
		if a.Content != "new" {
			t.Fatal("delta retry lost entry")
		}
	}
}

func TestConcurrentPushAndPrivateFiles(t *testing.T) {
	authority, _, _ := upstream(t)
	token, err := authority.CreateToken(ctx, "blocking", "scope")
	must(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var publishes atomic.Int64
	handler := httpapi.New(authority)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/artifact/pinned" {
			publishes.Add(1)
			close(entered)
			<-release
		}
		handler.ServeHTTP(w, r)
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); server.Close() }()
	c, err := client.New(server.URL, token)
	must(t, err)
	s, dir := openEdge(t, c)
	_, err = s.Enqueue(ctx, protocol.PublishRequest{Key: "a", OperationID: "one", Content: "one"})
	must(t, err)
	if other, err := Open(dir); !errors.Is(err, ErrBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatal("same private edge had two owners")
	}
	done := make(chan error, 1)
	go func() { _, err := s.Push(ctx, c, "one"); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("push did not enter fixture: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("push did not enter fixture")
	}
	if _, err := s.Push(ctx, c, "one"); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent flush was admitted")
	}
	releaseOnce.Do(func() { close(release) })
	must(t, <-done)
	if publishes.Load() != 1 {
		t.Fatal("duplicate publish attempts")
	}
	for _, name := range []string{"edge.lock", "edge.db", "edge.db-wal", "edge.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		must(t, err)
		if info.Mode().Perm() != 0600 {
			t.Fatal("nonprivate edge file", name)
		}
	}
	info, err := os.Stat(dir)
	must(t, err)
	if info.Mode().Perm() != 0700 {
		t.Fatal("nonprivate edge directory")
	}
}

func TestUnsafeLocalFilesAndBoundedImmutableOutbox(t *testing.T) {
	_, _, c := upstream(t)
	for _, name := range []string{"edge.lock", "edge.db", "edge.db-wal", "edge.db-shm", "edge.db-journal"} {
		t.Run(name, func(t *testing.T) {
			root := temp(t)
			dir := filepath.Join(root, "edge")
			must(t, os.Mkdir(dir, 0700))
			outside := filepath.Join(root, "outside")
			must(t, os.WriteFile(outside, []byte("preserve"), 0600))
			must(t, os.Symlink(outside, filepath.Join(dir, name)))
			if s, err := Open(dir); err == nil {
				s.Close()
				t.Fatal("symlinked edge file accepted")
			}
			b, err := os.ReadFile(outside)
			must(t, err)
			if string(b) != "preserve" {
				t.Fatal("symlink destination changed")
			}
		})
	}
	dir := filepath.Join(temp(t), "public")
	must(t, os.Mkdir(dir, 0755))
	must(t, os.Chmod(dir, 0755)) // The rejection fixture must survive a private umask.
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("public edge directory accepted")
	}
	s, _ := openEdge(t, c)
	for i := 0; i < MaxIntents; i++ {
		_, err := s.Enqueue(ctx, protocol.PublishRequest{Key: "a", OperationID: fmt.Sprint(i), Content: "one"})
		must(t, err)
	}
	if _, err := s.Enqueue(ctx, protocol.PublishRequest{Key: "a", OperationID: "over", Content: "one"}); !errors.Is(err, ErrLimit) {
		t.Fatal("outbox count limit failed")
	}
	if _, err := s.Enqueue(ctx, protocol.PublishRequest{Key: "a", OperationID: "0", Content: "changed"}); err == nil {
		t.Fatal("immutable intent body replaced")
	}
	if _, err := s.Enqueue(ctx, protocol.PublishRequest{Key: "a", OperationID: "too-large", Content: strings.Repeat("x", protocol.MaxContentBytes+1)}); err == nil {
		t.Fatal("oversized intent accepted")
	}
}

func TestQueuedIntentSurvivesRestartWithoutNetworkOrFreshOperationID(t *testing.T) {
	_, server, c := upstream(t)
	s, dir := openEdge(t, c)
	server.Close()
	r := protocol.PublishRequest{Key: "offline", OperationID: "reviewed-once", Content: "durable before network"}
	_, err := s.Enqueue(ctx, r)
	must(t, err)
	must(t, s.Close())
	s, err = Open(dir)
	must(t, err)
	defer s.Close()
	v, err := s.Intent(ctx, r.OperationID)
	must(t, err)
	if v.State != "queued" || v.Request != r {
		t.Fatal("offline restart changed request")
	}
	again, err := s.Enqueue(ctx, r)
	must(t, err)
	if again.Request != r {
		t.Fatal("re-enqueue changed immutable body")
	}
	status, err := s.Status(ctx)
	must(t, err)
	if status.Intents["queued"] != 1 || status.PendingBytes < 1 || status.OutboxReservedBytes <= status.PendingBytes {
		t.Fatal("pending byte accounting is missing")
	}
}

func TestUncertainRetryCannotBeResolvedByAnotherAuthorityOrNegativeEnvelope(t *testing.T) {
	a, _, _ := upstream(t)
	token, err := a.CreateToken(ctx, "uncertain", "scope")
	must(t, err)
	handler := httpapi.New(a)
	var drop atomic.Bool
	drop.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && drop.Swap(false) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, r)
			if rr.Code != http.StatusOK {
				t.Error("lost response fixture did not accept request")
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	c, err := client.New(server.URL, token)
	must(t, err)
	e, dir := openEdge(t, c)
	r := protocol.PublishRequest{Key: "uncertain", OperationID: "retain-original", Content: "already accepted at A"}
	_, err = e.Enqueue(ctx, r)
	must(t, err)
	if _, err = e.Push(ctx, c, r.OperationID); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	must(t, e.Close())
	e, err = Open(dir)
	must(t, err)
	defer e.Close()
	_, _, b := upstream(t)
	clients := []*client.Client{b}
	for _, code := range []string{"version_conflict", "operation_conflict", "identity_conflict"} {
		negative := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"code":%q,"message":"cannot disprove prior acceptance","current_version":0}`, code)
		}))
		defer negative.Close()
		bad, err := client.New(negative.URL, token)
		must(t, err)
		clients = append(clients, bad)
	}
	for _, wrong := range clients {
		v, err := e.Push(ctx, wrong, r.OperationID)
		if !errors.Is(err, ErrUncertain) || v.State != "uncertain" || v.Request != r {
			t.Fatalf("prior uncertainty erased: %+v %v", v, err)
		}
		if err := e.Resolve(ctx, r.OperationID, "abandon"); !errors.Is(err, ErrUncertain) {
			t.Fatal("possibly accepted intent became abandonable")
		}
	}
	v, err := e.Push(ctx, c, r.OperationID)
	must(t, err)
	if v.Receipt == nil || !v.Receipt.Replayed || v.Receipt.Artifact.Version != 1 || v.Request != r {
		t.Fatal("original authority did not recover original receipt")
	}
}

func TestEscapedIntentReservationsEnforceByteCapWithoutDroppingHistory(t *testing.T) {
	_, _, c := upstream(t)
	s, dir := openEdge(t, c)
	content := strings.Repeat("\x00", protocol.MaxContentBytes)
	count := 0
	for i := 0; i < MaxIntents; i++ {
		_, err := s.Enqueue(ctx, protocol.PublishRequest{Key: "escaped", OperationID: fmt.Sprint(i), Content: content})
		if errors.Is(err, ErrLimit) {
			break
		}
		must(t, err)
		count++
	}
	if count == 0 || count >= MaxIntents {
		t.Fatal("encoded byte cap was not enforced before row cap")
	}
	before, err := s.Status(ctx)
	must(t, err)
	if before.OutboxReservedBytes > MaxOutboxBytes || before.Intents["queued"] != count {
		t.Fatal(before)
	}
	// Abandonment retains immutable history/reservations; pressure never deletes it.
	must(t, s.Resolve(ctx, "0", "abandon"))
	if _, err := s.Enqueue(ctx, protocol.PublishRequest{Key: "escaped", OperationID: "after-abandon", Content: content}); !errors.Is(err, ErrLimit) {
		t.Fatal("abandonment silently freed retained history")
	}
	must(t, s.Close())
	s, err = Open(dir)
	must(t, err)
	defer s.Close()
	after, err := s.Status(ctx)
	must(t, err)
	if after.OutboxReservedBytes != before.OutboxReservedBytes || after.Intents["abandoned"] != 1 || after.Intents["queued"] != count-1 {
		t.Fatal("restart changed retained quota accounting")
	}
}

func TestPrivateFileModesLinksAndCanonicalPathsAreRefused(t *testing.T) {
	for _, kind := range []string{"public file", "hard link", "oversized file"} {
		t.Run(kind, func(t *testing.T) {
			root := temp(t)
			dir := filepath.Join(root, "edge")
			must(t, os.Mkdir(dir, 0700))
			path := filepath.Join(dir, "edge.db")
			switch kind {
			case "public file":
				must(t, os.WriteFile(path, []byte("preserve"), 0644))
				must(t, os.Chmod(path, 0644))
			case "hard link":
				outside := filepath.Join(root, "outside")
				must(t, os.WriteFile(outside, []byte("preserve"), 0600))
				must(t, os.Link(outside, path))
			case "oversized file":
				file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
				must(t, err)
				must(t, file.Truncate(MaxFileBytes+1))
				must(t, file.Close())
			}
			if s, err := Open(dir); err == nil {
				s.Close()
				t.Fatal("unsafe existing edge file accepted")
			}
		})
	}
	root := temp(t)
	if s, err := Open(root + "/unused/../edge"); err == nil {
		s.Close()
		t.Fatal("noncanonical path accepted")
	}
	alias := filepath.Join(root, "alias")
	must(t, os.Symlink(root, alias))
	if s, err := Open(filepath.Join(alias, "edge")); err == nil {
		s.Close()
		t.Fatal("symlinked parent accepted")
	}
}

func TestPinnedPushToHonestOldBrokerCannotPublishOrFallback(t *testing.T) {
	_, _, a := upstream(t)
	e, _ := openEdge(t, a)
	b, err := serverstore.Open(filepath.Join(temp(t), "old-authority.db"))
	must(t, err)
	defer b.Close()
	token, err := b.CreateToken(ctx, "edge-one", "scope")
	must(t, err)
	handler := httpapi.New(b)
	var oldWrites, newRequests atomic.Int64
	// Compatibility fixture reproduces v0.7's route dispatch: it only knows the
	// original artifact route and ignores headers that did not exist in v0.7.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/artifact" {
			newRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			oldWrites.Add(1)
		}
		for _, name := range []string{protocol.ExpectedAuthorityHeader, protocol.ExpectedScopeHeader, protocol.ExpectedAgentHeader} {
			r.Header.Del(name)
		}
		handler.ServeHTTP(w, r)
	}))
	defer old.Close()
	oldClient, err := client.New(old.URL, token)
	must(t, err)
	r := protocol.PublishRequest{Key: "must-not-cross-authority", OperationID: "original", Content: "bound only to A"}
	_, err = e.Enqueue(ctx, r)
	must(t, err)
	intent, err := e.Push(ctx, oldClient, r.OperationID)
	if !errors.Is(err, ErrUncertain) || intent.State != "uncertain" || oldWrites.Load() != 0 || newRequests.Load() != 1 {
		t.Fatalf("old broker received an unsafe write or fallback: %+v %v old=%d new=%d", intent, err, oldWrites.Load(), newRequests.Load())
	}
	if _, err := oldClient.Get(ctx, r.Key); err == nil {
		t.Fatal("wrong old authority was mutated")
	}
	if _, err := a.Get(ctx, r.Key); err == nil {
		t.Fatal("original authority unexpectedly mutated")
	}
	recovered, err := e.Push(ctx, a, r.OperationID)
	must(t, err)
	if recovered.Receipt == nil || recovered.Receipt.Artifact.Version != 1 || recovered.Receipt.Replayed {
		t.Fatal("exact intent could not recover at original new broker")
	}
}

func TestMalformedPullCannotChangeCacheStagingOrCheckpoint(t *testing.T) {
	for _, phase := range []string{"snapshot", "changes"} {
		for _, field := range []string{"content", "has_more"} {
			for _, mode := range []string{"null", "missing"} {
				t.Run(phase+"/"+field+"/"+mode, func(t *testing.T) {
					authority, _, _ := upstream(t)
					token, err := authority.CreateToken(ctx, "malformed-reader", "scope")
					must(t, err)
					handler := httpapi.New(authority)
					var corrupt atomic.Bool
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/v1/sync" || !corrupt.Load() {
							handler.ServeHTTP(w, r)
							return
						}
						rr := httptest.NewRecorder()
						handler.ServeHTTP(rr, r)
						if rr.Code != 200 {
							t.Error("malformed page fixture did not obtain a valid source page")
						}
						var object map[string]any
						if err := json.Unmarshal(rr.Body.Bytes(), &object); err != nil {
							t.Error(err)
							http.Error(w, "fixture error", 500)
							return
						}
						target := object
						if field == "content" {
							target = object["entries"].([]any)[0].(map[string]any)["artifact"].(map[string]any)
						}
						if mode == "missing" {
							delete(target, field)
						} else {
							target[field] = nil
						}
						json.NewEncoder(w).Encode(object)
					}))
					defer server.Close()
					c, err := client.New(server.URL, token)
					must(t, err)
					for _, key := range []string{"a", "b"} {
						_, err := c.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: key, Content: "old"})
						must(t, err)
					}
					e, _ := openEdge(t, c)
					pullAll(t, e, c, 1)
					for _, key := range []string{"a", "b"} {
						_, err := c.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: key + "2", ExpectedVersion: 1, Content: "new"})
						must(t, err)
					}
					if phase == "snapshot" {
						must(t, e.ResetPull(ctx))
					}
					snapshot := func() []string {
						var state string
						must(t, e.db.QueryRow("SELECT json_array(cursor,phase,through,last_seq,last_key,complete,visible_through) FROM pull_state WHERE id=1").Scan(&state))
						values := []string{state}
						rows, err := e.db.Query("SELECT json_array(kind,key,version,sequence,artifact) FROM cache ORDER BY kind,key")
						must(t, err)
						defer rows.Close()
						for rows.Next() {
							var row string
							must(t, rows.Scan(&row))
							values = append(values, row)
						}
						must(t, rows.Err())
						return values
					}
					before := snapshot()
					corrupt.Store(true)
					if _, err := e.Pull(ctx, c, PullOptions{Limit: 1}); err == nil {
						t.Fatal("ambiguous page was accepted")
					}
					if after := snapshot(); !reflect.DeepEqual(before, after) {
						t.Fatal("malformed page changed visible/staged bytes or durable checkpoint")
					}
					for _, key := range []string{"a", "b"} {
						current, err := e.Get(ctx, key)
						must(t, err)
						if current.Content != "old" || current.Version != 1 {
							t.Fatal("malformed page changed complete cache")
						}
					}
					corrupt.Store(false)
					pullAll(t, e, c, 1)
					for _, key := range []string{"a", "b"} {
						current, err := e.Get(ctx, key)
						must(t, err)
						if current.Content != "new" || current.Version != 2 {
							t.Fatal("valid retry could not apply original page")
						}
					}
				})
			}
		}
	}
}
