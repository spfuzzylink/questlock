package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/revision"
)

func reviseQuest(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "REVISION QUEST — six offline checked-edit scenarios\nReal HTTP broker + temporary SQLite; no model call, provider key or predicted savings."); err != nil {
		return err
	}
	for _, scenario := range []struct {
		name string
		run  func(context.Context, *modelQuestFixture) error
	}{
		{"simple 1: formatting preserves searchable hashtag", reviseQuestFormatting},
		{"simple 2: exact mission sentence emphasis", reviseQuestMission},
		{"simple 3: literal JSON configuration change", reviseQuestJSON},
		{"complex 1: chained provenance survives broker restart", reviseQuestChain},
		{"complex 2: source, count and quality failures withhold text", reviseQuestFailures},
		{"complex 3: concurrent publication, reuse and scope isolation", reviseQuestConcurrent},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		fixture, err := newModelQuestFixture(ctx)
		if err == nil {
			err = scenario.run(ctx, fixture)
			fixture.close()
		}
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", scenario.name, err)
		}
		if _, err := fmt.Fprintln(output, "PASS  "+scenario.name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "QUEST CLEARED — six real checked-edit scenarios passed. Zero model generation; host and broker costs are excluded.")
	return err
}

func reviseQuestRequest(task, text, old, replacement string, count int) revision.Request {
	return revision.Request{Schema: revision.Schema, Task: task, Revision: "approved-v1", Source: revision.Source{Text: &text, SHA256: revision.Digest(text)}, Edits: []revision.Edit{{Old: old, New: replacement, ExpectedMatches: count}}, SourceChecks: []evaluation.Check{{ID: "source", Kind: "contains", Text: old}}, Checks: []evaluation.Check{{ID: "output", Kind: "contains", Text: replacement}}}
}

func reviseQuestCompleted(result revision.Result, err error) error {
	if err != nil {
		return err
	}
	if result.Status != "completed" || !result.Durable || result.Artifact == nil || result.Artifact.Version < 1 || result.Artifact.SHA256 != revision.Digest(result.Text) || result.GenerationCalled || result.ThisRunUsage != (inference.Usage{}) || result.ProviderCostUSD != 0 {
		return errors.New("checked revision lacks durable output or zero-generation evidence")
	}
	return nil
}

func reviseQuestFormatting(ctx context.Context, fixture *modelQuestFixture) error {
	request := reviseQuestRequest("formatting", "Questlock coordinates work. #Questlock", "Questlock coordinates work.", "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 coordinates work.", 1)
	request.Checks = append(request.Checks, evaluation.Check{ID: "searchable-tag", Kind: "contains", Text: "#Questlock"})
	result, err := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, request)
	return reviseQuestCompleted(result, err)
}

func reviseQuestMission(ctx context.Context, fixture *modelQuestFixture) error {
	mission := "The mission: lower cost per accepted task, including the work a person has to redo."
	request := reviseQuestRequest("mission", mission, mission, "**"+mission+"**", 1)
	result, err := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, request)
	return reviseQuestCompleted(result, err)
}

func reviseQuestJSON(ctx context.Context, fixture *modelQuestFixture) error {
	request := reviseQuestRequest("literal-json", `{"max_output_tokens":512,"notify":["existing-command","--notify"]}`, `"max_output_tokens":512`, `"max_output_tokens":256`, 1)
	request.Checks = []evaluation.Check{{ID: "json", Kind: "json_valid"}, {ID: "output-cap", Kind: "json_equals", Pointer: "/max_output_tokens", Expected: json.RawMessage("256")}, {ID: "notify-preserved", Kind: "json_equals", Pointer: "/notify", Expected: json.RawMessage(`["existing-command","--notify"]`)}}
	result, err := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, request)
	return reviseQuestCompleted(result, err)
}

func reviseQuestChain(ctx context.Context, fixture *modelQuestFixture) error {
	firstRequest := reviseQuestRequest("chain", "Draft A.", "A", "B", 1)
	first, err := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, firstRequest)
	if err := reviseQuestCompleted(first, err); err != nil {
		return err
	}
	if err := fixture.restart(); err != nil {
		return err
	}
	secondRequest := reviseQuestRequest("chain", first.Text, "B", "C", 1)
	secondRequest.Revision = "approved-v2"
	secondRequest.Source = revision.Source{Key: first.Artifact.Key, Version: first.Artifact.Version, SHA256: first.OutputSHA256}
	second, err := (revision.Runner{Broker: fixture.clients[1]}).Run(ctx, secondRequest)
	if err := reviseQuestCompleted(second, err); err != nil {
		return err
	}
	if second.Text != "Draft C." || second.SourceSHA256 != first.OutputSHA256 || second.Artifact.Key == first.Artifact.Key {
		return errors.New("chained revision lost exact source provenance")
	}
	return nil
}

func reviseQuestFailures(ctx context.Context, fixture *modelQuestFixture) error {
	base := reviseQuestRequest("failure", "Draft A.", "A", "B", 1)
	for _, change := range []func(*revision.Request){
		func(r *revision.Request) { r.Source.SHA256 = revision.Digest("stale source") },
		func(r *revision.Request) { r.Edits = []revision.Edit{{Old: "A", New: "B", ExpectedMatches: 2}} },
		func(r *revision.Request) {
			r.Checks = []evaluation.Check{{ID: "forbidden", Kind: "contains", Text: "never present"}}
		},
	} {
		request := base
		change(&request)
		result, err := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, request)
		if err == nil || result.Text != "" || result.Durable || result.GenerationCalled || result.Artifact != nil {
			return errors.New("stale source, wrong count or failed check exposed a candidate")
		}
	}
	return nil
}

func reviseQuestConcurrent(ctx context.Context, fixture *modelQuestFixture) error {
	request := reviseQuestRequest("concurrent", "Approved draft.", "draft", "result", 1)
	type outcome struct {
		result revision.Result
		err    error
	}
	outcomes := make(chan outcome, len(fixture.clients))
	var start sync.WaitGroup
	start.Add(1)
	for _, broker := range fixture.clients {
		go func(b *client.Client) {
			start.Wait()
			result, err := (revision.Runner{Broker: b}).Run(ctx, request)
			outcomes <- outcome{result, err}
		}(broker)
	}
	start.Done()
	var first revision.Result
	for range fixture.clients {
		entry := <-outcomes
		if err := reviseQuestCompleted(entry.result, entry.err); err != nil {
			return err
		}
		if first.Artifact == nil {
			first = entry.result
		}
		if entry.result.Artifact.Key != first.Artifact.Key || entry.result.Artifact.Version != first.Artifact.Version || entry.result.OutputSHA256 != first.OutputSHA256 {
			return errors.New("concurrent requests published divergent artifacts")
		}
	}
	repeat, err := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, request)
	if err := reviseQuestCompleted(repeat, err); err != nil {
		return err
	}
	if !repeat.Reused || repeat.Artifact.Version != 1 {
		return errors.New("exact repeat did not reuse the single publication")
	}
	token, err := fixture.store.CreateToken(ctx, "other-revision-worker", "other-revision-scope")
	if err != nil {
		return err
	}
	other, err := client.New(fixture.server.URL, token)
	if err != nil {
		return err
	}
	if _, err := other.Get(ctx, first.Artifact.Key); err == nil {
		return errors.New("other scope read a revision artifact")
	}
	otherResult, err := (revision.Runner{Broker: other}).Run(ctx, request)
	if err := reviseQuestCompleted(otherResult, err); err != nil {
		return err
	}
	if otherResult.Reused {
		return errors.New("other scope reused a private artifact")
	}
	return nil
}
