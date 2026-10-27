package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/semantic"
)

// MaxSemanticRequestBytes bounds both semantic routes, including escaped JSON.
const MaxSemanticRequestBytes int64 = 128 << 10

func serveSemantic(w http.ResponseWriter, r *http.Request, s *store.Store, p store.Principal) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid", "semantic requests accept JSON fields only", 0)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSemanticRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if r.URL.Path == "/v1/semantic/query" {
		var query semantic.Query
		if err = decodeSemanticJSON(body, &query); err != nil {
			writeDecodeError(w, err)
			return
		}
		result, err := s.QuerySemantic(r.Context(), p, query)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	var request semantic.PutRequest
	if err = decodeSemanticJSON(body, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err = s.PutSemantic(r.Context(), p, request); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// JSON's usual last-duplicate-wins and case-folded field behavior are unsafe
// at this boundary. Reject those spellings and null values before typed decode.
func decodeSemanticJSON(data []byte, value any) error {
	if !utf8.Valid(data) || !validSemanticEscapes(data) {
		return errors.New("invalid semantic UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("semantic JSON must be an object")
	}
	if err = walkSemanticJSON(d, start, 0); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing semantic JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

func walkSemanticJSON(d *json.Decoder, token json.Token, depth int) error {
	if token == nil || depth > 8 {
		return errors.New("null or deeply nested semantic JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	names := map[string]bool{}
	items := 0
	for d.More() {
		items++
		if delim == '[' && items > 4096 || delim == '{' && items > 32 {
			return errors.New("semantic container limit")
		}
		if delim == '{' {
			name, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := name.(string)
			if !ok || key != strings.ToLower(key) || names[key] {
				return errors.New("duplicate semantic field")
			}
			names[key] = true
		}
		next, err := d.Token()
		if err != nil {
			return err
		}
		if err = walkSemanticJSON(d, next, depth+1); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return errors.New("invalid semantic JSON structure")
	}
	return nil
}

func validSemanticEscapes(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' || i+4 >= len(data) {
			continue
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
