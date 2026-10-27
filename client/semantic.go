package client

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

var _ semantic.Index = (*Client)(nil)

// QuerySemantic returns advisory candidates. It never fetches or serves their
// answer text and does not retry an uncertain request.
func (c *Client) QuerySemantic(ctx context.Context, query semantic.Query) (semantic.SearchResult, error) {
	var result semantic.SearchResult
	if err := semantic.ValidateQuery(query); err != nil {
		return result, err
	}
	var raw json.RawMessage
	if err := c.doMax(ctx, http.MethodPost, "/v1/semantic/query", nil, query, &raw, 128<<10); err != nil {
		return result, err
	}
	if err := protocol.DecodeSyncJSON(raw, &result); err != nil {
		return semantic.SearchResult{}, errors.New("questlock: invalid semantic response JSON")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if !semanticResponseFields(fields, "candidates", "scanned", "truncated") || result.Candidates == nil || result.Scanned < 0 || result.Scanned > query.MaxScan || result.Truncated && result.Scanned != query.MaxScan || len(result.Candidates) > query.TopK || len(result.Candidates) > result.Scanned {
		return semantic.SearchResult{}, errors.New("questlock: invalid semantic response bounds")
	}
	seen := map[string]bool{}
	var candidates []json.RawMessage
	json.Unmarshal(fields["candidates"], &candidates)
	var previous *semantic.Candidate
	for i, candidate := range result.Candidates {
		var candidateFields, sourceFields map[string]json.RawMessage
		json.Unmarshal(candidates[i], &candidateFields)
		json.Unmarshal(candidateFields["source"], &sourceFields)
		if !semanticResponseFields(candidateFields, "source", "similarity", "created_at", "expires_at") || !semanticResponseFields(sourceFields, "key", "version", "sha256", "output_sha256") {
			return semantic.SearchResult{}, errors.New("questlock: incomplete semantic candidate")
		}
		entry := semantic.Entry{Partition: query.Partition, ModelID: query.ModelID, Vector: query.Vector, Source: candidate.Source, CreatedAt: candidate.CreatedAt, ExpiresAt: candidate.ExpiresAt}
		entry.ID = semantic.EntryID(entry)
		if semantic.ValidatePut(semantic.PutRequest{Entry: entry, MaxEntries: 1}) != nil || math.IsNaN(candidate.Similarity) || math.IsInf(candidate.Similarity, 0) || candidate.Similarity < query.MinSimilarity || candidate.Similarity > 1 || candidate.CreatedAt.After(time.Now().UTC()) || seen[entry.ID] {
			return semantic.SearchResult{}, errors.New("questlock: invalid semantic candidate")
		}
		if previous != nil && previous.Similarity < candidate.Similarity {
			return semantic.SearchResult{}, errors.New("questlock: unsorted semantic candidates")
		}
		seen[entry.ID] = true
		copy := candidate
		previous = &copy
	}
	return result, nil
}

// PutSemantic requests a derived index entry in the bearer token's scope.
func (c *Client) PutSemantic(ctx context.Context, request semantic.PutRequest) error {
	if err := semantic.ValidatePut(request); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.doMax(ctx, http.MethodPost, "/v1/semantic/entry", nil, request, &raw, 4096); err != nil {
		return err
	}
	var result struct{}
	if err := protocol.DecodeSyncJSON(raw, &result); err != nil {
		return errors.New("questlock: invalid semantic entry response")
	}
	return nil
}

func semanticResponseFields(fields map[string]json.RawMessage, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			return false
		}
	}
	return true
}
