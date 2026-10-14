package outcomes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/protocol"
)

const maxCASAttempts = 32

func brokerCode(err error, code string) bool {
	var e *client.Error
	return errors.As(err, &e) && e.Code == code
}

func (s Store) load(ctx context.Context, task Task, key string) (ledger, int64, error) {
	if s.Broker == nil {
		return ledger{}, 0, invalid("broker is required")
	}
	a, err := s.Broker.Get(ctx, key)
	if err != nil {
		return ledger{}, 0, err
	}
	if a.Key != key || a.Version < 1 {
		return ledger{}, 0, invalid("broker returned a mismatched artifact")
	}
	l, err := decodeLedger(a.Content, task, a.Version)
	return l, a.Version, err
}

func receipt(task Task, key, id string, version int64, created time.Time) Receipt {
	return Receipt{Task: task, Key: key, ID: id, Version: version, CreatedAt: created}
}

// Record appends a run once. Concurrent independent runs retry only broker
// version conflicts; an uncertain transport/publication outcome is returned to
// the caller without retrying. Repeating the same run ID and payload later,
// including from another token in the same scope, returns its original receipt.
func (s Store) Record(ctx context.Context, task Task, run Run) (Receipt, error) {
	key, err := Key(task)
	if err != nil {
		return Receipt{}, err
	}
	if err := validateRun(run); err != nil {
		return Receipt{}, err
	}
	created := time.Now().UTC()
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		l, version, err := s.load(ctx, task, key)
		if err != nil {
			if !brokerCode(err, "not_found") {
				return Receipt{}, err
			}
			l = ledger{Schema: 1, Task: task, CreatedAt: created, Runs: []RecordedRun{}, Events: []RecordedFeedback{}}
		}
		for _, r := range l.Runs {
			if r.ID == run.ID {
				if !same(r.Run, run) {
					return Receipt{}, ErrIDConflict
				}
				return receipt(task, key, r.ID, r.Version, r.CreatedAt), nil
			}
		}
		if len(l.Runs) >= MaxRuns {
			return Receipt{}, ErrLimit
		}
		l.Runs = append(l.Runs, RecordedRun{Run: run, Version: version + 1, CreatedAt: created})
		if err = s.publish(ctx, key, version, l); err != nil {
			if brokerCode(err, "version_conflict") {
				continue
			}
			return Receipt{}, err
		}
		return receipt(task, key, run.ID, version+1, created), nil
	}
	return Receipt{}, fmt.Errorf("%w: concurrent run retry limit reached", ErrConflict)
}

func stateOf(l ledger) State {
	state := State{Status: "unreviewed"}
	for _, e := range l.Events {
		state = State{Status: e.Kind}
		if e.Kind == "accepted" {
			state.AcceptedRunID, state.FinalArtifactSHA256 = e.RunID, e.FinalArtifactSHA256
		}
	}
	return state
}

func (s Store) Get(ctx context.Context, task Task) (Snapshot, error) {
	key, err := Key(task)
	if err != nil {
		return Snapshot{}, err
	}
	l, version, err := s.load(ctx, task, key)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Task: task, Key: key, Version: version, CreatedAt: l.CreatedAt, Runs: l.Runs, Events: l.Events, State: stateOf(l)}, nil
}

func feedbackReplay(l ledger, task Task, key string, expected int64, f Feedback) (Receipt, bool, error) {
	for _, e := range l.Events {
		if e.ID == f.ID {
			if e.ExpectedVersion != expected || !same(e.Feedback, f) {
				return Receipt{}, true, ErrIDConflict
			}
			return receipt(task, key, e.ID, e.Version, e.CreatedAt), true, nil
		}
	}
	return Receipt{}, false, nil
}

// Feedback requires the version the operator actually reviewed. It never
// rebases a new event onto a changed ledger. Exact event retries remain valid
// after later events and preserve the original receipt and state history.
func (s Store) Feedback(ctx context.Context, task Task, expectedVersion int64, feedback Feedback) (Receipt, error) {
	key, err := Key(task)
	if err != nil {
		return Receipt{}, err
	}
	if expectedVersion < 1 {
		return Receipt{}, invalid("feedback requires a positive reviewed version")
	}
	if err := validateFeedback(feedback); err != nil {
		return Receipt{}, err
	}
	l, version, err := s.load(ctx, task, key)
	if err != nil {
		return Receipt{}, err
	}
	if r, found, err := feedbackReplay(l, task, key, expectedVersion, feedback); found {
		return r, err
	}
	if version != expectedVersion {
		return Receipt{}, ErrConflict
	}
	found := false
	for _, r := range l.Runs {
		if r.ID == feedback.RunID {
			found = true
			break
		}
	}
	if !found {
		return Receipt{}, invalid("feedback run is not recorded for this task revision")
	}
	if len(l.Events) >= MaxEvents {
		return Receipt{}, ErrLimit
	}
	created := time.Now().UTC()
	l.Events = append(l.Events, RecordedFeedback{Feedback: feedback, ExpectedVersion: expectedVersion, Version: version + 1, CreatedAt: created})
	if err := s.publish(ctx, key, version, l); err != nil {
		if !brokerCode(err, "version_conflict") {
			return Receipt{}, err
		}
		// This lookup resolves only an identical concurrent event. A different
		// event still fails stale; it is never appended onto a newer version.
		latest, _, loadErr := s.load(ctx, task, key)
		if loadErr != nil {
			return Receipt{}, loadErr
		}
		if r, found, replayErr := feedbackReplay(latest, task, key, expectedVersion, feedback); found {
			return r, replayErr
		}
		return Receipt{}, errors.Join(ErrConflict, err)
	}
	return receipt(task, key, feedback.ID, version+1, created), nil
}

func (s Store) publish(ctx context.Context, key string, version int64, l ledger) error {
	if err := validateLedger(l, l.Task, version+1); err != nil {
		return err
	}
	encoded, err := json.Marshal(l)
	if err != nil {
		return invalid("ledger cannot be encoded")
	}
	if len(encoded) > protocol.MaxContentBytes {
		return ErrLimit
	}
	// The operation ID includes the complete immutable publish request. Calls
	// that race with distinct timestamps cannot reuse an ID for different data.
	identity, _ := json.Marshal([]any{key, version, string(encoded)})
	d := sha256.Sum256(identity)
	result, err := s.Broker.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: "outcomes-" + hex.EncodeToString(d[:]), ExpectedVersion: version, Content: string(encoded)})
	if err != nil {
		return err
	}
	if result.Artifact.Key != key || result.Artifact.Version != version+1 || result.Artifact.Content != string(encoded) {
		return invalid("broker returned a mismatched publication receipt")
	}
	return nil
}
