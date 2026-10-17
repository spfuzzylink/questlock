// Package edgequest exercises real loopback HTTP and private SQLite without a
// model or an external service. It is fixture evidence, not a two-host claim.
package edgequest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/edge"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

type Case struct {
	ID         string `json:"id"`
	Complexity string `json:"complexity"`
	Passed     bool   `json:"passed"`
}
type Report struct {
	Evidence             string `json:"evidence"`
	ModelCalls           int    `json:"model_calls"`
	RemoteHostsValidated bool   `json:"remote_hosts_validated"`
	Cases                []Case `json:"cases"`
}

func Run(ctx context.Context) (Report, error) {
	report := Report{Evidence: "loopback_http_and_private_sqlite_fixture"}
	pass := func(id, complexity string) { report.Cases = append(report.Cases, Case{id, complexity, true}) }
	root, err := os.MkdirTemp("", "questlock-edge-quest-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return report, err
	}
	authority, err := store.Open(filepath.Join(root, "authority.db"))
	if err != nil {
		return report, err
	}
	defer authority.Close()
	handler := httpapi.New(authority)
	var lose atomic.Bool
	var attempts atomic.Int64
	var bodyMu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/artifact/pinned" {
			attempts.Add(1)
			b, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			r.Body.Close()
			if e != nil {
				http.Error(w, "fixture request failed", 500)
				return
			}
			bodyMu.Lock()
			bodies = append(bodies, b)
			bodyMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(b))
			if lose.Swap(false) {
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, r)
				if rr.Code != http.StatusOK {
					http.Error(w, "fixture did not accept", 500)
					return
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
	defer server.Close()
	var edges [2]*edge.Store
	var clients [2]*client.Client
	var directories [2]string
	defer func() {
		for _, e := range edges {
			if e != nil {
				e.Close()
			}
		}
	}()
	for i, name := range []string{"one", "two"} {
		token, e := authority.CreateToken(ctx, name, "fixture")
		if e != nil {
			return report, e
		}
		clients[i], e = client.New(server.URL, token)
		if e != nil {
			return report, e
		}
		directories[i] = filepath.Join(root, name)
		edges[i], e = edge.Open(directories[i])
		if e != nil {
			return report, e
		}
		if _, e = edges[i].Bind(ctx, clients[i]); e != nil {
			return report, e
		}
	}
	reopen := func(i int) error {
		if err := edges[i].Close(); err != nil {
			return err
		}
		edges[i] = nil
		var err error
		edges[i], err = edge.Open(directories[i])
		return err
	}
	pullAll := func(i int) error {
		for n := 0; n < 32; n++ {
			page, err := edges[i].Pull(ctx, clients[i], edge.PullOptions{Limit: 1})
			if err != nil {
				return err
			}
			if !page.HasMore {
				return nil
			}
		}
		return errors.New("fixture exceeded bounded page count")
	}
	request := protocol.PublishRequest{Key: "a", OperationID: "exact-intent", Content: "original"}
	if _, err = edges[0].Enqueue(ctx, request); err != nil {
		return report, err
	}
	if err = reopen(0); err != nil {
		return report, err
	}
	saved, err := edges[0].Intent(ctx, request.OperationID)
	if err != nil {
		return report, err
	}
	if saved.Request != request || saved.State != "queued" || attempts.Load() != 0 {
		return report, errors.New("queued intent did not survive without network")
	}
	pass("durable-before-network", "simple")
	lose.Store(true)
	uncertain, err := edges[0].Push(ctx, clients[0], request.OperationID)
	if !errors.Is(err, edge.ErrUncertain) || uncertain.State != "uncertain" {
		return report, errors.New("lost response did not preserve uncertainty")
	}
	if err = edges[0].Resolve(ctx, request.OperationID, "abandon"); !errors.Is(err, edge.ErrUncertain) {
		return report, errors.New("uncertain intent was abandonable")
	}
	if err = reopen(0); err != nil {
		return report, err
	}
	accepted, err := edges[0].Push(ctx, clients[0], request.OperationID)
	if err != nil {
		return report, err
	}
	bodyMu.Lock()
	same := len(bodies) == 2 && bytes.Equal(bodies[0], bodies[1])
	bodyMu.Unlock()
	if !same || accepted.Receipt == nil || !accepted.Receipt.Replayed || accepted.Receipt.Artifact.Version != 1 {
		return report, errors.New("restart did not recover same intent receipt")
	}
	pass("lost-response-restart-exact-retry", "complex")
	identity, err := clients[0].Identity(ctx)
	if err != nil {
		return report, err
	}
	_, err = clients[1].PublishPinned(ctx, identity, protocol.PublishRequest{Key: "denied", OperationID: "wrong-agent", Content: "must not publish"})
	var api *client.Error
	if !errors.As(err, &api) || api.Code != "identity_conflict" {
		return report, errors.New("receipt agent pin was not enforced")
	}
	if _, err = clients[0].Get(ctx, "denied"); err == nil {
		return report, errors.New("denied intent created an artifact")
	}
	pass("authenticated-receipt-agent-pin", "simple")
	if _, err = clients[0].Publish(ctx, protocol.PublishRequest{Key: "a", OperationID: "a2", ExpectedVersion: 1, Content: "new head"}); err != nil {
		return report, err
	}
	before := attempts.Load()
	if _, err = edges[0].Push(ctx, clients[0], request.OperationID); err != nil {
		return report, err
	}
	if attempts.Load() != before {
		return report, errors.New("confirmed receipt resent")
	}
	if err = pullAll(0); err != nil {
		return report, err
	}
	current, err := edges[0].Get(ctx, "a")
	if err != nil {
		return report, err
	}
	if current.Version != 2 || current.Content != "new head" {
		return report, errors.New("historical receipt replaced current pull state")
	}
	pass("pull-current-not-historical-receipt", "simple")
	for i, e := range edges {
		if _, err = e.Enqueue(ctx, protocol.PublishRequest{Key: "b", OperationID: "stale-writer", Content: fmt.Sprintf("edge-%d", i)}); err != nil {
			return report, err
		}
	}
	type outcome struct {
		index  int
		intent edge.Intent
		err    error
	}
	done := make(chan outcome, 2)
	start := make(chan struct{})
	for i := range edges {
		go func(i int) {
			<-start
			v, err := edges[i].Push(ctx, clients[i], "stale-writer")
			done <- outcome{i, v, err}
		}(i)
	}
	close(start)
	winner, loser := -1, -1
	for range 2 {
		v := <-done
		if v.err == nil && v.intent.State == "accepted" {
			winner = v.index
		} else if errors.Is(v.err, edge.ErrBlocked) && v.intent.Code == "version_conflict" && v.intent.Request.ExpectedVersion == 0 {
			loser = v.index
		} else {
			return report, errors.New("concurrent fixture did not produce one CAS conflict")
		}
	}
	if winner < 0 || loser < 0 {
		return report, errors.New("concurrent writers did not serialize")
	}
	if _, err = edges[loser].Push(ctx, clients[loser], "stale-writer"); !errors.Is(err, edge.ErrBlocked) {
		return report, errors.New("conflict retried implicitly")
	}
	if err = edges[loser].Resolve(ctx, "stale-writer", "abandon"); err != nil {
		return report, err
	}
	pass("two-edge-concurrent-cas-conflict", "complex")
	if _, err = clients[0].Publish(ctx, protocol.PublishRequest{Key: "c", OperationID: "c1", Content: "original"}); err != nil {
		return report, err
	}
	first, err := edges[1].Pull(ctx, clients[1], edge.PullOptions{Limit: 1})
	if err != nil {
		return report, err
	}
	if !first.HasMore {
		return report, errors.New("fixture did not paginate snapshot")
	}
	if _, err = edges[1].Get(ctx, "a"); !errors.Is(err, edge.ErrNotReady) {
		return report, errors.New("partial snapshot exposed")
	}
	if _, err = clients[0].Publish(ctx, protocol.PublishRequest{Key: "a", OperationID: "a3", ExpectedVersion: 2, Content: "during snapshot"}); err != nil {
		return report, err
	}
	if _, err = clients[0].Publish(ctx, protocol.PublishRequest{Key: "0-new", OperationID: "new-earlier-key", Content: "catch up"}); err != nil {
		return report, err
	}
	for n := 0; n < 32; n++ {
		if err = reopen(1); err != nil {
			return report, err
		}
		page, err := edges[1].Pull(ctx, clients[1], edge.PullOptions{Limit: 1})
		if err != nil {
			return report, err
		}
		if !page.HasMore {
			break
		}
		if n == 31 {
			return report, errors.New("snapshot did not complete")
		}
	}
	frozen, err := edges[1].Get(ctx, "a")
	if err != nil {
		return report, err
	}
	if frozen.Version != 2 {
		return report, errors.New("concurrent write entered frozen snapshot")
	}
	if err = pullAll(1); err != nil {
		return report, err
	}
	for _, key := range []string{"a", "b", "c", "0-new"} {
		local, err := edges[1].Get(ctx, key)
		if err != nil {
			return report, err
		}
		remote, err := clients[1].Get(ctx, key)
		if err != nil {
			return report, err
		}
		if local != remote {
			return report, errors.New("snapshot restart and catchup lost an accepted artifact")
		}
	}
	pass("paginated-snapshot-restart-and-catchup", "complex")
	return report, nil
}
