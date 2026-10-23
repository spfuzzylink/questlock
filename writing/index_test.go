package writing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

func publishIndex(t *testing.T, broker inference.Broker, index ReviewIndex) protocol.Artifact {
	t.Helper()
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	result, err := broker.Publish(testContext, protocol.PublishRequest{Key: indexPrefix + index.ReferenceID, OperationID: "index-fixture-" + Hash(data), Content: string(data)})
	if err != nil {
		t.Fatal(err)
	}
	return result.Artifact
}

func TestIndexAppendIsIdempotentAndPreservesDifferentHistoricalAnswer(t *testing.T) {
	broker := realBroker(t)
	entry := fixtureEntry(1)
	ref := publishEntry(t, broker, entry)
	if err := indexEntry(testContext, broker, entry); err != nil {
		t.Fatal(err)
	}
	first, err := broker.Get(testContext, indexPrefix+entry.ReferenceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := indexEntry(testContext, broker, entry); err != nil {
		t.Fatal(err)
	}
	second, err := broker.Get(testContext, first.Key)
	if err != nil || first != second {
		t.Fatalf("idempotent append mutated index: %+v %+v %v", first, second, err)
	}
	// The same approved request may have a later durable version after TTL or
	// runtime changes. Both metadata entries remain historical references.
	later := entry
	later.AnswerVersion++
	later.AnswerSHA256 = Hash([]byte("later answer"))
	laterRef := publishEntry(t, broker, later)
	if ref.Key == laterRef.Key {
		t.Fatal("new historical version collided with existing immutable metadata")
	}
	if err := indexEntry(testContext, broker, later); err != nil {
		t.Fatal(err)
	}
	index, err := ReadIndex(testContext, broker, entry.ReferenceID)
	if err != nil || !reflect.DeepEqual(index.Entries, []EntryRef{ref, laterRef}) {
		t.Fatalf("historical entries were overwritten: %+v %v", index, err)
	}
}

func TestIndexRetriesOnlyConfirmedVersionConflictsWithNewOperationIdentity(t *testing.T) {
	broker := realBroker(t)
	entry, other := fixtureEntry(1), fixtureEntry(2)
	ref, otherRef := publishEntry(t, broker, entry), publishEntry(t, broker, other)
	var requests []protocol.PublishRequest
	wrapped := brokerHooks{Broker: broker, publish: func(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			publishIndex(t, broker, ReviewIndex{Schema: 1, ReferenceID: entry.ReferenceID, Entries: []EntryRef{otherRef}})
			return protocol.PublishResult{}, fmt.Errorf("concurrent append: %w", &client.Error{Code: "version_conflict"})
		}
		return broker.Publish(ctx, request)
	}}
	if err := indexEntry(testContext, wrapped, entry); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ExpectedVersion != 0 || requests[1].ExpectedVersion != 1 || requests[0].OperationID == requests[1].OperationID {
		t.Fatalf("conflict changed no operation identity: %+v", requests)
	}
	for _, request := range requests {
		want := "writing-index-" + Hash([]byte(request.Content)) + "-" + strconv.FormatInt(request.ExpectedVersion, 10)
		if request.OperationID != want {
			t.Fatalf("operation does not bind content and expected version: %+v", request)
		}
	}
	index, err := ReadIndex(testContext, broker, entry.ReferenceID)
	if err != nil || !reflect.DeepEqual(index.Entries, []EntryRef{otherRef, ref}) {
		t.Fatalf("concurrent entry lost: %+v %v", index, err)
	}
}

func TestIndexStopsAtEightConflictsAndNeverReplaysUncertainWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"confirmed conflict", &client.Error{Code: "version_conflict"}, 8},
		{"uncertain transport", errors.New("connection lost after commit"), 1},
		{"untyped conflict", errors.New("version_conflict"), 1},
		{"permission", &client.Error{Code: "forbidden"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker, entry := realBroker(t), fixtureEntry(1)
			publishEntry(t, broker, entry)
			publishes, indexGets := 0, 0
			wrapped := brokerHooks{Broker: broker,
				get: func(ctx context.Context, key string) (protocol.Artifact, error) {
					if strings.HasPrefix(key, indexPrefix) {
						indexGets++
					}
					return broker.Get(ctx, key)
				}, publish: func(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
					publishes++
					return protocol.PublishResult{}, tc.err
				}}
			if err := indexEntry(testContext, wrapped, entry); !errors.Is(err, tc.err) || publishes != tc.want || indexGets != tc.want {
				t.Fatalf("unbounded or uncertain replay: %v publishes=%d reads=%d", err, publishes, indexGets)
			}
		})
	}
}

func TestIndexLostCommitResponseReturnsUncertaintyWithoutAnotherReadOrWrite(t *testing.T) {
	broker, entry := realBroker(t), fixtureEntry(1)
	ref := publishEntry(t, broker, entry)
	uncertain := errors.New("commit response lost")
	publishes, indexGets := 0, 0
	wrapped := brokerHooks{Broker: broker,
		get: func(ctx context.Context, key string) (protocol.Artifact, error) {
			if strings.HasPrefix(key, indexPrefix) {
				indexGets++
			}
			return broker.Get(ctx, key)
		}, publish: func(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
			publishes++
			if _, err := broker.Publish(ctx, request); err != nil {
				t.Fatal(err)
			}
			return protocol.PublishResult{}, uncertain
		}}
	if err := indexEntry(testContext, wrapped, entry); !errors.Is(err, uncertain) || publishes != 1 || indexGets != 1 {
		t.Fatalf("uncertainty caused reconciliation/replay: %v writes=%d reads=%d", err, publishes, indexGets)
	}
	// A separate, explicit read can reconcile this metadata-only commit.
	index, err := ReadIndex(testContext, broker, entry.ReferenceID)
	if err != nil || !reflect.DeepEqual(index.Entries, []EntryRef{ref}) {
		t.Fatalf("real committed index unavailable: %+v %v", index, err)
	}
}

func TestFullIndexNeverEvictsAndExistingEntryStillSucceeds(t *testing.T) {
	broker, entry := realBroker(t), fixtureEntry(1)
	firstRef := publishEntry(t, broker, entry)
	index := ReviewIndex{Schema: 1, ReferenceID: entry.ReferenceID, Entries: []EntryRef{firstRef}}
	for i := 2; i <= MaxIndexEntries; i++ {
		next := fixtureEntry(i)
		data, _ := json.Marshal(next)
		index.Entries = append(index.Entries, EntryRef{Key: entryKey(next), Version: 1, SHA256: Hash(data)})
	}
	before := publishIndex(t, broker, index)
	if err := indexEntry(testContext, broker, entry); err != nil {
		t.Fatalf("full index rejected existing idempotent entry: %v", err)
	}
	next := fixtureEntry(MaxIndexEntries + 1)
	publishEntry(t, broker, next)
	if err := indexEntry(testContext, broker, next); !errors.Is(err, ErrIndexFull) {
		t.Fatalf("full index appended or evicted: %v", err)
	}
	after, err := broker.Get(testContext, before.Key)
	if err != nil || before != after {
		t.Fatalf("full index mutated: %+v %+v %v", before, after, err)
	}
}

func TestReadIndexRequiresExplicitNotFoundAndBoundedStrictData(t *testing.T) {
	entry := fixtureEntry(1)
	data, _ := json.Marshal(entry)
	ref := EntryRef{Key: entryKey(entry), Version: 1, SHA256: Hash(data)}
	valid := ReviewIndex{Schema: 1, ReferenceID: entry.ReferenceID, Entries: []EntryRef{ref}}
	encoded, _ := json.Marshal(valid)
	for _, tc := range []struct {
		name string
		err  error
		pass bool
	}{
		{"recognized", &client.Error{Code: "not_found"}, true},
		{"wrapped", fmt.Errorf("lookup: %w", &client.Error{Code: "not_found"}), true},
		{"untyped", errors.New("not_found"), false},
		{"denied", &client.Error{Code: "unauthorized"}, false},
	} {
		index, err := ReadIndex(testContext, brokerHooks{get: func(context.Context, string) (protocol.Artifact, error) { return protocol.Artifact{}, tc.err }}, entry.ReferenceID)
		if tc.pass && (err != nil || index.Schema != 1 || index.Entries == nil || len(index.Entries) != 0) || !tc.pass && !errors.Is(err, tc.err) {
			t.Fatalf("%s read changed error semantics: %+v %v", tc.name, index, err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*protocol.Artifact)
	}{
		{"wrong key", func(a *protocol.Artifact) { a.Key += "-wrong" }},
		{"zero version", func(a *protocol.Artifact) { a.Version = 0 }},
		{"unknown field", func(a *protocol.Artifact) {
			a.Content = strings.Replace(a.Content, `"schema":1`, `"schema":1,"accepted":true`, 1)
		}},
		{"duplicate field", func(a *protocol.Artifact) {
			a.Content = strings.Replace(a.Content, `"schema":1`, `"schema":1,"schema":1`, 1)
		}},
		{"case alias", func(a *protocol.Artifact) { a.Content = strings.Replace(a.Content, `"schema":1`, `"Schema":1`, 1) }},
		{"wrong reference", func(a *protocol.Artifact) {
			a.Content = strings.Replace(a.Content, `"reference_id":"sample-profile"`, `"reference_id":"other-profile"`, 1)
		}},
		{"wrong schema", func(a *protocol.Artifact) { a.Content = strings.Replace(a.Content, `"schema":1`, `"schema":2`, 1) }},
		{"oversized", func(a *protocol.Artifact) { a.Content = strings.Repeat(" ", MaxIndexBytes) + a.Content }},
		{"duplicate entry", func(a *protocol.Artifact) {
			index := valid
			index.Entries = []EntryRef{ref, ref}
			body, _ := json.Marshal(index)
			a.Content = string(body)
		}},
		{"mismatched pointer", func(a *protocol.Artifact) { a.Content = strings.Replace(a.Content, `"version":1`, `"version":2`, 1) }},
		{"null entries", func(a *protocol.Artifact) { a.Content = `{"schema":1,"reference_id":"sample-profile","entries":null}` }},
		{"over entry cap", func(a *protocol.Artifact) {
			index := valid
			index.Entries = make([]EntryRef, MaxIndexEntries+1)
			body, _ := json.Marshal(index)
			a.Content = string(body)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact := protocol.Artifact{Key: indexPrefix + entry.ReferenceID, Version: 1, Content: string(encoded)}
			tc.edit(&artifact)
			wrapped := brokerHooks{get: func(context.Context, string) (protocol.Artifact, error) { return artifact, nil }}
			if _, err := ReadIndex(testContext, wrapped, entry.ReferenceID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid index accepted: %v", err)
			}
		})
	}
}

func TestIndexRejectsMismatchedImmutableMetadataAndReceipts(t *testing.T) {
	broker, entry := realBroker(t), fixtureEntry(1)
	ref := publishEntry(t, broker, entry)
	for _, tc := range []struct {
		name string
		edit func(*protocol.Artifact)
	}{
		{"version", func(a *protocol.Artifact) { a.Version++ }},
		{"key", func(a *protocol.Artifact) { a.Key += "-wrong" }},
		{"content", func(a *protocol.Artifact) { a.Content += " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publishes := 0
			wrapped := brokerHooks{Broker: broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
				a, err := broker.Get(ctx, key)
				if key == ref.Key && err == nil {
					tc.edit(&a)
				}
				return a, err
			}, publish: func(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
				publishes++
				return protocol.PublishResult{}, nil
			}}
			if err := indexEntry(testContext, wrapped, entry); !errors.Is(err, ErrUnconfirmed) || publishes != 0 {
				t.Fatalf("unconfirmed metadata indexed: %v %d", err, publishes)
			}
		})
	}
	wrapped := brokerHooks{Broker: broker, publish: func(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
		return protocol.PublishResult{}, nil
	}}
	if err := indexEntry(testContext, wrapped, entry); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("unconfirmed index receipt accepted: %v", err)
	}
	publishIndex(t, broker, ReviewIndex{Schema: 1, ReferenceID: entry.ReferenceID, Entries: []EntryRef{ref}})
	wrapped = brokerHooks{Broker: broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
		a, err := broker.Get(ctx, key)
		if key == ref.Key && err == nil {
			a.Content = strings.Replace(a.Content, `"human_acceptance":"not_asserted"`, `"human_acceptance":"accepted"`, 1)
		}
		return a, err
	}}
	if _, err := ReadEntries(testContext, wrapped, entry.ReferenceID); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("tampered acceptance metadata resolved: %v", err)
	}
}
