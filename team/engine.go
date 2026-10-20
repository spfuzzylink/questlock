package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/protocol"
)

func (e Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func key(fingerprint, role string) string { return "team/v1/" + fingerprint + "/roles/" + role }
func notFound(err error) bool {
	var api *client.Error
	return errors.As(err, &api) && api.Code == "not_found"
}

// Run executes only the selected role ("all" selects the ordered local pipeline).
// A remote reviewer may call it before the master is ready: it returns waiting
// without claiming work or making a model call. Workers share HTTP, never SQLite.
func (e Engine) Run(ctx context.Context, r Request, selected string) (Report, error) {
	fp, err := Fingerprint(r)
	out := Report{Schema: 1, RequestSHA256: fp, Status: "waiting", HumanAcceptance: "not_reported", LocalUsage: Usage{Known: true}, HostedUsageKnown: true}
	if err != nil {
		return out, err
	}
	if e.Broker == nil || !identifier.MatchString(e.WorkerID) {
		return out, errors.New("identified worker and scoped broker required")
	}
	roles := append([]Role{r.Master}, r.Reviewers...)
	valid := selected == "all" || selected == "status"
	for _, role := range roles {
		valid = valid || role.ID == selected
	}
	if !valid {
		return out, errors.New("select all, status, master or a declared reviewer ID")
	}
	var candidate string
	incomplete := false
	for _, role := range roles {
		candidateSHA := ""
		if role.ID != "master" {
			candidateSHA = hash(candidate)
		}
		rec, found, readErr := e.read(ctx, fp, role.ID, candidateSHA, r.ReuseForSeconds)
		if readErr != nil {
			return out, readErr
		}
		fresh := false
		if !found && selected != "status" && (selected == "all" || selected == role.ID) {
			if e.Factory == nil {
				return out, errors.New("local runner factory required for unfinished work")
			}
			perform := func(callCtx context.Context) error {
				var runErr error
				rec, runErr = e.execute(callCtx, r, role, fp, candidate, candidateSHA)
				fresh = rec.Outcome != nil
				return runErr
			}
			admitted := false
			admittedPerform := func(callCtx context.Context) error { admitted = true; return perform(callCtx) }
			if e.Admit != nil {
				err = e.Admit(ctx, admittedPerform)
			} else {
				err = admittedPerform(ctx)
			}
			if err != nil {
				out.Status = "pending"
				if !admitted {
					out.Status = "blocked"
				}
				if rec.Outcome != nil {
					s := stepSummary(rec, false)
					out.Steps = append(out.Steps, s)
					out.AccountingComplete = accountingComplete(out.Steps)
					out.LocalUsage.Known = out.LocalUsage.Known && s.Usage.Known
					out.LocalUsage.InputTokens += s.Usage.InputTokens
					out.LocalUsage.OutputTokens += s.Usage.OutputTokens
					out.LocalUsage.GenerationCalls += s.Usage.GenerationCalls
				}
				return out, err
			}
			found = true
		}
		if !found {
			if role.ID == "master" {
				return out, nil
			}
			out.Steps = append(out.Steps, Step{Role: role.ID, State: "waiting", OutcomeTrackingStatus: "not_recorded", Usage: Usage{Known: true}})
			out.AccountingComplete = false
			incomplete = true
			continue
		}
		step := stepSummary(rec, !fresh)
		out.Steps = append(out.Steps, step)
		out.AccountingComplete = accountingComplete(out.Steps)
		out.LocalUsage.Known = out.LocalUsage.Known && step.Usage.Known
		out.LocalUsage.InputTokens += step.Usage.InputTokens
		out.LocalUsage.OutputTokens += step.Usage.OutputTokens
		out.LocalUsage.GenerationCalls += step.Usage.GenerationCalls
		if !e.now().Before(rec.ExpiresAt) {
			out.Status = "expired"
			return out, ErrExpired
		}
		if rec.State == "pending" {
			out.Status = "pending"
			if role.ID == "master" || selected == "all" || selected == "status" || selected == role.ID {
				return out, ErrPending
			}
			incomplete = true
			continue
		}
		if rec.State != "completed" {
			out.Status = rec.State
			return out, ErrQuality
		}
		if rec.Outcome == nil || rec.Outcome.Response == nil {
			return out, ErrRecord
		}
		if role.ID == "master" {
			candidate = rec.Outcome.Response.Text
			var pass bool
			out.Checks, pass = evaluation.CheckOutput(candidate, r.Checks)
			if !pass {
				out.Status = "quality_failed"
				return out, ErrQuality
			}
		} else if !reviewApproved(rec.Outcome.Response.Text) {
			out.Status = "quality_failed"
			return out, ErrQuality
		}
	}
	if incomplete {
		out.Status = "waiting"
		return out, nil
	}
	out.Status = "completed"
	out.Text = candidate
	out.OutputSHA256 = hash(candidate)
	return out, nil
}

func (e Engine) read(ctx context.Context, fp, role, candidate string, ttl int64) (record, bool, error) {
	a, err := e.Broker.Get(ctx, key(fp, role))
	if notFound(err) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	var rec record
	if protocol.DecodeDataJSON([]byte(a.Content), &rec) != nil || a.Key != key(fp, role) || rec.Schema != 1 || rec.RequestSHA256 != fp || rec.Role != role || rec.CandidateSHA256 != candidate || !identifier.MatchString(rec.WorkerID) || rec.CreatedAt.IsZero() || !rec.ExpiresAt.Equal(rec.CreatedAt.Add(time.Duration(ttl)*time.Second)) || rec.CreatedAt.After(e.now().Add(30*time.Second)) {
		return rec, false, ErrRecord
	}
	if !e.now().Before(rec.ExpiresAt) {
		return rec, false, ErrExpired
	}
	if rec.State == "pending" {
		if a.Version != 1 || rec.Outcome != nil {
			return rec, false, ErrRecord
		}
		return rec, true, nil
	}
	if a.Version != 2 || rec.Outcome == nil || (rec.State != "completed" && rec.State != "quality_failed" && rec.State != "stopped") || rec.State != rec.Outcome.Status {
		return rec, false, ErrRecord
	}
	if rec.State == "completed" && (rec.Outcome.Response == nil || !rec.Outcome.Response.Complete || rec.Outcome.Response.Text == "") {
		return rec, false, ErrRecord
	}
	return rec, true, nil
}

func (e Engine) execute(ctx context.Context, r Request, role Role, fp, candidate, candidateSHA string) (record, error) {
	started := e.now()
	rec := record{Schema: 1, RequestSHA256: fp, Role: role.ID, WorkerID: e.WorkerID, CandidateSHA256: candidateSHA, State: "pending", CreatedAt: started, ExpiresAt: started.Add(time.Duration(r.ReuseForSeconds) * time.Second)}
	claimID := "team-claim-" + hash(fp + "/" + role.ID + "/" + e.WorkerID)[:40]
	b, _ := json.Marshal(rec)
	claim, err := e.Broker.Publish(ctx, protocol.PublishRequest{Key: key(fp, role.ID), OperationID: claimID, ExpectedVersion: 0, Content: string(b)})
	if err != nil {
		return record{}, errors.Join(ErrPending, err)
	}
	// A repeated operation receipt does not grant another execution right.
	if claim.Replayed || claim.Artifact.Version != 1 || claim.Artifact.Content != string(b) {
		return record{}, ErrPending
	}
	request := workerRequest(r, role, candidate)
	router := delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) { return e.Factory(ctx, role) }}
	result, _ := router.Run(ctx, request)
	if result.Status == "completed" && role.ID != "master" && (result.Response == nil || !reviewApproved(result.Response.Text)) {
		result.Status = "quality_failed"
		result.Response = nil
	}
	delegation.RecordOutcome(ctx, e.Broker, request, &result, "team-"+hash(fp + "/" + role.ID)[:48])
	rec.State = result.Status
	rec.Outcome = &result
	b, _ = json.Marshal(rec)
	// An uncertain completion publication is left for explicit inspection. No
	// timeout, disconnect or rejected answer ever starts a new model attempt.
	op := "team-done-" + hash(fp + "/" + role.ID + "/" + e.WorkerID)[:40]
	publication, err := e.Broker.Publish(ctx, protocol.PublishRequest{Key: key(fp, role.ID), OperationID: op, ExpectedVersion: 1, Content: string(b)})
	if err != nil || publication.Artifact.Version != 2 || publication.Artifact.Content != string(b) {
		return rec, ErrPending
	}
	return rec, nil
}

func reviewApproved(text string) bool {
	var v struct {
		Approved *bool     `json:"approved"`
		Issues   *[]string `json:"issues"`
	}
	if len(text) > 16384 || protocol.DecodeDataJSON([]byte(text), &v) != nil || v.Approved == nil || v.Issues == nil || len(*v.Issues) > 8 {
		return false
	}
	return *v.Approved && len(*v.Issues) == 0
}

func stepSummary(rec record, reused bool) Step {
	s := Step{Role: rec.Role, State: rec.State, WorkerID: rec.WorkerID, Reused: reused, Usage: Usage{Known: true}, OutcomeTrackingStatus: "not_recorded"}
	if rec.Outcome == nil {
		s.Usage.Known = false
		return s
	}
	if rec.Outcome.Response != nil {
		s.OutputSHA256 = hash(rec.Outcome.Response.Text)
	}
	if rec.Outcome.OutcomeTracking != nil {
		s.OutcomeTrackingStatus = rec.Outcome.OutcomeTracking.Status
	}
	if reused {
		return s
	}
	for _, a := range rec.Outcome.Attempts {
		m := a.Result.Measurement
		if m == nil {
			s.Usage.Known = false
			continue
		}
		if m.GenerationCalled {
			s.Usage.GenerationCalls++
			s.Usage.Known = s.Usage.Known && m.KnownGenerationUsage
		}
		s.Usage.InputTokens += a.Result.ThisRunUsage.InputTokens
		s.Usage.OutputTokens += a.Result.ThisRunUsage.OutputTokens
	}
	return s
}

func accountingComplete(steps []Step) bool {
	if len(steps) == 0 {
		return false
	}
	for _, s := range steps {
		if s.OutcomeTrackingStatus != "recorded" {
			return false
		}
	}
	return true
}

// Describe is offline and reveals the stable shared-state keys, never credentials.
func Describe(r Request) (map[string]any, error) {
	fp, err := Fingerprint(r)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, role := range append([]Role{r.Master}, r.Reviewers...) {
		keys[role.ID] = key(fp, role.ID)
	}
	return map[string]any{"schema": 1, "request_sha256": fp, "role_keys": keys, "paid_fallback_enabled": false, "role_order": fmt.Sprintf("master then %d independent reviewer(s)", len(r.Reviewers)), "claim_recovery": "explicit inspection; never automatic takeover"}, nil
}
