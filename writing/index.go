package writing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

const MaxIndexEntries = 128
const MaxIndexBytes = 128 << 10
const indexPrefix = "writing/v1/index/"
const entryPrefix = "writing/v1/answers/"

var ErrIndexFull = errors.New("writing_review_index_full")

// EntryRef pins an immutable metadata entry, not the answer itself. Metadata
// entries contain source versions and review status, never profile or prose.
type EntryRef struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
	SHA256  string `json:"sha256"`
}

// ReviewIndex persists bounded historical metadata references. It grants no
// cache eligibility, extends no inference TTL, and asserts no human acceptance.
type ReviewIndex struct {
	Schema      int        `json:"schema"`
	ReferenceID string     `json:"reference_id"`
	Entries     []EntryRef `json:"entries"`
}

// ReadIndex returns an empty schema-1 index only when the broker explicitly
// reports not_found. Every other read or validation failure is preserved.
func ReadIndex(ctx context.Context, broker inference.Broker, referenceID string) (ReviewIndex, error) {
	index, _, err := readIndex(ctx, broker, referenceID)
	return index, err
}

// ReadEntries resolves only the immutable metadata referenced by ReadIndex. It
// verifies each pointer's version, bytes and reference binding. Answer content
// and profile/evidence documents are never fetched by this function.
func ReadEntries(ctx context.Context, broker inference.Broker, referenceID string) ([]Entry, error) {
	index, err := ReadIndex(ctx, broker, referenceID)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(index.Entries))
	for _, ref := range index.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		artifact, err := broker.Get(ctx, ref.Key)
		if err != nil {
			return nil, err
		}
		var entry Entry
		if artifact.Key != ref.Key || artifact.Version != ref.Version || len(artifact.Content) > MaxIndexBytes || Hash([]byte(artifact.Content)) != ref.SHA256 || protocol.DecodeDataJSON([]byte(artifact.Content), &entry) != nil || !validEntry(entry) || entry.ReferenceID != referenceID || ref.Key != entryKey(entry) {
			return nil, ErrUnconfirmed
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func readIndex(ctx context.Context, broker inference.Broker, referenceID string) (ReviewIndex, int64, error) {
	if broker == nil || !id.MatchString(referenceID) {
		return ReviewIndex{}, 0, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ReviewIndex{}, 0, err
	}
	key := indexPrefix + referenceID
	artifact, err := broker.Get(ctx, key)
	if err != nil {
		if brokerCode(err, "not_found") {
			return ReviewIndex{Schema: 1, ReferenceID: referenceID, Entries: []EntryRef{}}, 0, nil
		}
		return ReviewIndex{}, 0, err
	}
	var index ReviewIndex
	if artifact.Key != key || artifact.Version < 1 || len(artifact.Content) > MaxIndexBytes || protocol.DecodeDataJSON([]byte(artifact.Content), &index) != nil || index.Schema != 1 || index.ReferenceID != referenceID || index.Entries == nil || len(index.Entries) > MaxIndexEntries {
		return ReviewIndex{}, 0, ErrInvalid
	}
	seen := make(map[string]bool, len(index.Entries))
	for _, entry := range index.Entries {
		if !validEntryRef(entry) || seen[entry.Key] {
			return ReviewIndex{}, 0, ErrInvalid
		}
		seen[entry.Key] = true
	}
	return index, artifact.Version, nil
}

func indexEntry(ctx context.Context, broker inference.Broker, entry Entry) error {
	if broker == nil || !validEntry(entry) {
		return ErrInvalid
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return ErrInvalid
	}
	ref := EntryRef{Key: entryKey(entry), Version: 1, SHA256: Hash(encoded)}
	// Confirm the immutable metadata before publishing a reference to it. An
	// uncertain prior write does not authorize a second publication here.
	artifact, err := broker.Get(ctx, ref.Key)
	if err != nil {
		return err
	}
	if artifact.Key != ref.Key || artifact.Version != ref.Version || artifact.Content != string(encoded) {
		return ErrUnconfirmed
	}
	var conflict error
	for attempt := 0; attempt < 8; attempt++ {
		index, version, err := readIndex(ctx, broker, entry.ReferenceID)
		if err != nil {
			return err
		}
		for _, existing := range index.Entries {
			if existing.Key == ref.Key {
				if existing != ref {
					return ErrUnconfirmed
				}
				return nil
			}
		}
		if len(index.Entries) >= MaxIndexEntries {
			return ErrIndexFull
		}
		if version == math.MaxInt64 {
			return ErrInvalid
		}
		index.Entries = append(index.Entries, ref)
		body, err := json.Marshal(index)
		if err != nil || len(body) > MaxIndexBytes {
			return ErrIndexFull
		}
		request := protocol.PublishRequest{Key: indexPrefix + entry.ReferenceID, ExpectedVersion: version,
			OperationID: "writing-index-" + Hash(body) + "-" + strconv.FormatInt(version, 10), Content: string(body)}
		receipt, err := broker.Publish(ctx, request)
		if err == nil {
			if receipt.Artifact.Key != request.Key || receipt.Artifact.Version != version+1 || receipt.Artifact.Content != request.Content {
				return ErrUnconfirmed
			}
			return nil
		}
		if !brokerCode(err, "version_conflict") {
			return err
		}
		conflict = err
	}
	return conflict
}

func validEntryRef(ref EntryRef) bool {
	if ref.Version != 1 || !validHash(ref.SHA256) || !strings.HasPrefix(ref.Key, entryPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(ref.Key, entryPrefix), "/")
	return len(parts) == 3 && validHash(parts[0]) && (parts[1] == "unreviewed" || parts[1] == "assistant_reviewed") && parts[2] == ref.SHA256
}

func entryKey(entry Entry) string {
	encoded, _ := json.Marshal(entry)
	return entryPrefix + entry.RequestSHA256 + "/" + entry.ReviewStatus + "/" + Hash(encoded)
}

func validEntry(entry Entry) bool {
	return entry.Schema == 1 && validHash(entry.RequestSHA256) && id.MatchString(entry.ReferenceID) && id.MatchString(entry.ReferenceRevision) && validHash(entry.ReferenceSHA256) && id.MatchString(entry.EvidenceID) && id.MatchString(entry.EvidenceRevision) && validHash(entry.EvidenceSHA256) && entry.AnswerVersion >= 1 && strings.HasPrefix(entry.AnswerKey, "inference/v1/jobs/") && validHash(strings.TrimPrefix(entry.AnswerKey, "inference/v1/jobs/")) && validHash(entry.AnswerSHA256) && (entry.ReviewStatus == "unreviewed" || entry.ReviewStatus == "assistant_reviewed") && entry.HumanAcceptance == "not_asserted"
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func brokerCode(err error, code string) bool {
	var service *client.Error
	return errors.As(err, &service) && service.Code == code
}
