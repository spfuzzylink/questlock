package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultVerifierMaxInputTokens  int64 = 8192
	DefaultVerifierMaxOutputTokens int64 = 128
	DefaultVerifierTimeoutMS             = 30000
	MaxVerifierInputTokens         int64 = 32768
	MaxVerifierOutputTokens        int64 = 512
	MaxVerifierTimeoutMS                 = 120000
	MaxWaitForCandidateMS                = 30000
	MaxVerdictBytes                      = 4096
	// ProtectedTermsVersion identifies the lexical guard, independently of a
	// verifier prompt or any observed quality result.
	ProtectedTermsVersion = 2
)

// ReuseConfig opts a read-only task into bounded candidate validation. Config's
// retrieval score is not an answer-validity threshold. The embedded fields have
// the same flat JSON names as shadow configuration.
type ReuseConfig struct {
	Config
	ReadOnly                bool   `json:"read_only"`
	Intent                  string `json:"intent"`
	VerifierModel           string `json:"verifier_model"`
	VerifierMaxInputTokens  int64  `json:"verifier_max_input_tokens,omitempty"`
	VerifierMaxOutputTokens int64  `json:"verifier_max_output_tokens,omitempty"`
	VerifierTimeoutMS       int    `json:"verifier_timeout_ms,omitempty"`
	WaitForCandidateMS      int    `json:"wait_for_candidate_ms,omitempty"`
}

// NormalizeReuseConfig preserves the shadow configuration contract while
// requiring an explicit controlled, read-only policy and a bounded local model.
// It does not discover a runtime, download weights, or call a model.
func NormalizeReuseConfig(c ReuseConfig) (ReuseConfig, error) {
	if c.Mode != "controlled" || !c.ReadOnly {
		return ReuseConfig{}, errors.New("semantic reuse requires controlled mode and read_only true")
	}
	if !validLabel(c.Intent, 128) || !validOllamaModel(c.VerifierModel) {
		return ReuseConfig{}, errors.New("semantic reuse requires a bounded intent and installed local verifier model name")
	}
	base := c.Config
	base.Mode = "shadow"
	if base.TimeoutMS == 0 {
		// Controlled work includes embedding and verifier setup/execution.
		// An earlier caller deadline still bounds every phase.
		base.TimeoutMS = MaxTimeoutMS
	}
	base, err := NormalizeConfig(base)
	if err != nil {
		return ReuseConfig{}, err
	}
	base.Mode = "controlled"
	c.Config = base
	if c.VerifierMaxInputTokens == 0 {
		c.VerifierMaxInputTokens = DefaultVerifierMaxInputTokens
	}
	if c.VerifierMaxOutputTokens == 0 {
		c.VerifierMaxOutputTokens = DefaultVerifierMaxOutputTokens
	}
	if c.VerifierTimeoutMS == 0 {
		c.VerifierTimeoutMS = DefaultVerifierTimeoutMS
	}
	if c.VerifierMaxInputTokens < 1 || c.VerifierMaxInputTokens > MaxVerifierInputTokens || c.VerifierMaxOutputTokens < 16 || c.VerifierMaxOutputTokens > MaxVerifierOutputTokens || c.VerifierTimeoutMS < 1 || c.VerifierTimeoutMS > MaxVerifierTimeoutMS {
		return ReuseConfig{}, errors.New("semantic verifier limits exceed supported bounds")
	}
	if c.WaitForCandidateMS < 0 || c.WaitForCandidateMS > MaxWaitForCandidateMS {
		return ReuseConfig{}, errors.New("semantic candidate wait must be 0..30000 milliseconds")
	}
	return c, nil
}

// ValidityRequest supplies both questions and the evidence against which the
// unchanged cached answer must be assessed. Treat its strings as task data.
type ValidityRequest struct {
	OriginalQuestion string            `json:"original_question"`
	Question         string            `json:"question"`
	Instructions     string            `json:"instructions"`
	Context          string            `json:"context"`
	CachedAnswer     string            `json:"cached_answer"`
	Bindings         map[string]string `json:"bindings"`
	Intent           string            `json:"intent"`
}

// ValidityResult separates the verdict from execution evidence supplied by the
// trusted adapter. Missing usage is unknown; it is never inferred as zero.
type ValidityResult struct {
	GenerationCalled bool   `json:"generation_called"`
	Complete         bool   `json:"complete"`
	Durable          bool   `json:"durable"`
	Verdict          string `json:"verdict"`
	SameIntent       bool   `json:"same_intent"`
	SameFacts        bool   `json:"same_facts"`
	SameConstraints  bool   `json:"same_constraints"`
	AnswerSupported  bool   `json:"answer_supported"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	Reused           bool   `json:"reused"`
	// SharedWork identifies a zero-incremental-usage decision whose model work
	// is accounted once by its batch's separate work record and shared receipt.
	SharedWork   bool    `json:"shared_work,omitempty"`
	DurationMS   float64 `json:"duration_ms"`
	ReceiptKey   string  `json:"receipt_key,omitempty"`
	OutputSHA256 string  `json:"output_sha256,omitempty"`
}

type Verifier interface {
	Verify(context.Context, ValidityRequest) (ValidityResult, error)
	Identity() string
}

// Accepted is a verdict check only. Callers must separately establish complete
// execution, known bounded usage, provenance, freshness and deterministic checks.
func (v ValidityResult) Accepted() bool {
	return v.Verdict == "accept" && v.SameIntent && v.SameFacts && v.SameConstraints && v.AnswerSupported
}

// ParseVerdict accepts exactly the five model-authored decision fields. Usage,
// receipts and timing must come from the adapter, never model-authored JSON.
// Reading only primitive tokens also rejects nested/deep values without walking
// arbitrary structures. No partial result is returned on malformed input.
func ParseVerdict(data []byte) (ValidityResult, error) {
	invalid := errors.New("semantic verifier verdict must contain exactly a verdict and four explicit boolean decisions")
	if len(data) == 0 || len(data) > MaxVerdictBytes || !utf8.Valid(data) {
		return ValidityResult{}, invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return ValidityResult{}, invalid
	}
	var result ValidityResult
	seen := make(map[string]bool, 5)
	for d.More() {
		keyToken, err := d.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] || len(seen) == 5 {
			return ValidityResult{}, invalid
		}
		seen[key] = true
		value, err := d.Token()
		if err != nil {
			return ValidityResult{}, invalid
		}
		if key == "verdict" {
			verdict, ok := value.(string)
			if !ok || (verdict != "accept" && verdict != "reject" && verdict != "unknown") {
				return ValidityResult{}, invalid
			}
			result.Verdict = verdict
			continue
		}
		boolean, ok := value.(bool)
		if !ok {
			return ValidityResult{}, invalid
		}
		switch key {
		case "same_intent":
			result.SameIntent = boolean
		case "same_facts":
			result.SameFacts = boolean
		case "same_constraints":
			result.SameConstraints = boolean
		case "answer_supported":
			result.AnswerSupported = boolean
		default:
			return ValidityResult{}, invalid
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 5 || !seen["verdict"] {
		return ValidityResult{}, invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ValidityResult{}, invalid
	}
	return result, nil
}

var (
	protectedURL      = regexp.MustCompile("(?i)(?:[a-z][a-z0-9+.-]*://|www\\.)[^\\s<>\"'`]+")
	protectedNumber   = regexp.MustCompile(`[-+−]?(?:\p{N}+(?:[.,_:/−+\-]\p{N}+)*(?:[eE][-+−]?\p{N}+)?|[.,]\p{N}+)`)
	protectedWord     = regexp.MustCompile(`[\p{L}\p{M}\p{N}]+(?:['’._:/@#%+\-][\p{L}\p{M}\p{N}]+)*`)
	protectedOperator = regexp.MustCompile(`[<>=!≤≥≠≈±×÷*/^&|%‰‱]+`)
	quantityWords     = wordSet("zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty thirty forty fifty sixty seventy eighty ninety hundred thousand million billion trillion first second third fourth fifth sixth seventh eighth ninth tenth half quarter once twice thrice double triple")
	calendarWords     = wordSet("january february march april may june july august september october november december jan feb mar apr jun jul aug sep sept oct nov dec monday tuesday wednesday thursday friday saturday sunday mon tue tues wed thu thur thurs fri sat sun")
	negationWords     = wordSet("no not never none nobody nothing nowhere neither nor without cannot can't don't doesn't didn't won't wouldn't shouldn't couldn't isn't aren't wasn't weren't hasn't haven't hadn't needn't")
	unitWords         = wordSet("ns us µs μs ms s sec secs second seconds min mins minute minutes h hr hrs hour hours day days week weeks month months year years hz khz mhz ghz b byte bytes bit bits kb mb gb tb pb kib mib gib tib pib bps kbps mbps gbps pg ng mg ug µg μg mcg g kg microgram micrograms milligram milligrams gram grams kilogram kilograms ul µl μl ml cl dl l cc microliter microliters milliliter milliliters liter liters litre litres mmol mol meq iu u mm cm m km in inch inches ft feet yard yards mile miles oz ounce ounces lb lbs pound pounds pa kpa mpa bar psi mmhg cmh2o gy sv msv bq kbq mbq c f k celsius fahrenheit kelvin w kw mw watt watts kilowatt kilowatts v mv volt volts a ma amp amps mah wh kwh usd eur gbp jpy cad aud inr btc eth percent percentage")
	currencyWords     = wordSet("usd eur gbp jpy cad aud inr btc eth")
	queryWords        = wordSet("how what where when why who which can could would will please summarize summarise explain tell give provide draft write help show create generate check assess describe does do is are the a an for to i my we our it this that in on at with using based according should")
)

// CheckProtectedTerms rejects differences in explicit quantities, code-like
// identifiers, URLs, quoted text, negation, symbolic operators and quantity
// units. Ordinary sentence capitalization and natural-language constraint words
// are not symbolic identifiers/operators. Proper-name hints are compared when
// both questions supply them; an omitted name must be resolved by the mandatory
// verifier against the task's bindings and evidence. Empty is not proof of
// equivalent intent or a correct answer. Unsupported, malformed or oversized
// text fails closed before extraction. Conservative false rejections remain.
func CheckProtectedTerms(original, current string) []string {
	if !validGuardText(original) || !validGuardText(current) {
		return []string{"invalid_protected_text"}
	}
	a, aOK := protectedTerms(original)
	b, bOK := protectedTerms(current)
	if !aOK || !bOK {
		return []string{"invalid_protected_quotes"}
	}
	checks := []struct {
		code    string
		changed bool
	}{
		{"protected_numbers_changed", !slices.Equal(a.numbers, b.numbers)},
		{"protected_identifiers_changed", !slices.Equal(a.identifiers, b.identifiers) || len(a.names) > 0 && len(b.names) > 0 && !slices.Equal(a.names, b.names)},
		{"protected_urls_changed", !slices.Equal(a.urls, b.urls)},
		{"protected_quotes_changed", !slices.Equal(a.quotes, b.quotes)},
		{"protected_negations_changed", !slices.Equal(a.negations, b.negations)},
		{"protected_operators_changed", !slices.Equal(a.operators, b.operators)},
		{"protected_units_changed", !slices.Equal(a.units, b.units)},
	}
	var reasons []string
	for _, check := range checks {
		if check.changed {
			reasons = append(reasons, check.code)
		}
	}
	return reasons
}

type termSnapshot struct {
	numbers, identifiers, names, urls, quotes, negations, operators, units []string
}

func validGuardText(text string) bool {
	if len(text) == 0 || len(text) > MaxInputBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func protectedTerms(text string) (termSnapshot, bool) {
	s := termSnapshot{numbers: protectedNumber.FindAllString(text, -1), urls: protectedURL.FindAllString(text, -1), operators: protectedOperator.FindAllString(text, -1)}
	quotes, ok := quotedTerms(text)
	if !ok {
		return s, false
	}
	s.quotes = quotes
	positions := protectedWord.FindAllStringIndex(text, -1)
	for i, position := range positions {
		token := stripPossessive(text[position[0]:position[1]])
		lower := strings.ToLower(strings.ReplaceAll(token, "’", "'"))
		// A hyphenated quantity phrase keeps the same numeric words as its
		// spaced form, e.g. two-line / two line, without splitting code IDs.
		for _, part := range strings.Split(lower, "-") {
			if quantityWords[part] {
				s.numbers = append(s.numbers, part)
			}
		}
		previousQuantity := i > 0 && adjacentWords(text[positions[i-1][1]:position[0]]) && quantityToken(text[positions[i-1][0]:positions[i-1][1]])
		nextQuantity := i+1 < len(positions) && adjacentWords(text[position[1]:positions[i+1][0]]) && quantityToken(text[positions[i+1][0]:positions[i+1][1]])
		adjacentUnit := unitWords[lower] && previousQuantity || currencyWords[lower] && nextQuantity
		if calendarWords[lower] && (previousQuantity || nextQuantity || lower != "may" && lower != "march") {
			s.numbers = append(s.numbers, lower)
		}
		if negationWords[lower] {
			s.negations = append(s.negations, lower)
		}
		unit := attachedQuantityUnit(token)
		if unit != "" {
			s.units = append(s.units, unit)
		} else if adjacentUnit {
			// "a" and "in" are units only beside quantities. Interpreting
			// every English article/preposition as a unit rejects paraphrases.
			s.units = append(s.units, token)
		}
		if unit != "" || adjacentUnit || quantityWords[lower] || calendarWords[lower] || negationWords[lower] || queryWords[lower] {
			continue
		}
		if codeIdentifier(token) {
			s.identifiers = append(s.identifiers, token)
		} else if nameHint(token) && !sentenceInitial(text, position[0]) {
			s.names = append(s.names, token)
		}
	}
	for _, r := range text {
		if unicode.Is(unicode.Sc, r) || r == '°' || r == '%' || r == '‰' || r == '‱' {
			s.units = append(s.units, string(r))
		}
	}
	return s, true
}

func stripPossessive(token string) string {
	for _, suffix := range []string{"'s", "’s", "'S", "’S"} {
		if strings.HasSuffix(token, suffix) {
			return strings.TrimSuffix(token, suffix)
		}
	}
	return token
}

func adjacentWords(separator string) bool {
	for _, r := range separator {
		if !unicode.IsSpace(r) && r != '°' {
			return false
		}
	}
	return true
}

func quantityToken(token string) bool {
	if quantityWords[strings.ToLower(token)] {
		return true
	}
	match := protectedNumber.FindStringIndex(token)
	return match != nil && match[0] == 0 && match[1] == len(token)
}

func attachedQuantityUnit(token string) string {
	match := protectedNumber.FindStringIndex(token)
	if match != nil && match[0] == 0 && match[1] < len(token) {
		unit := token[match[1]:]
		if unitWords[strings.ToLower(unit)] {
			return unit
		}
	}
	parts := strings.Split(token, "-")
	if len(parts) > 1 && unitWords[strings.ToLower(parts[len(parts)-1])] {
		for _, part := range parts[:len(parts)-1] {
			if !quantityWords[strings.ToLower(part)] {
				return ""
			}
		}
		return parts[len(parts)-1]
	}
	return ""
}

func codeIdentifier(token string) bool {
	letters, capitals, number := 0, 0, false
	for _, r := range token {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				capitals++
			}
		}
		number = number || unicode.IsNumber(r)
	}
	return letters > 0 && (number || strings.ContainsAny(token, "._:/@#") || letters > 1 && capitals == letters)
}

func nameHint(token string) bool {
	for _, r := range token {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func sentenceInitial(text string, offset int) bool {
	prefix := strings.TrimSpace(text[:offset])
	if prefix == "" {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(prefix)
	return previous == '.' || previous == '?' || previous == '!'
}

func wordSet(words string) map[string]bool {
	set := make(map[string]bool)
	for _, word := range strings.Fields(words) {
		set[word] = true
	}
	return set
}

func quotedTerms(text string) ([]string, bool) {
	runes := []rune(text)
	var result []string
	for i := 0; i < len(runes); i++ {
		opening := runes[i]
		closing := opening
		switch opening {
		case '\'', '’':
			// An apostrophe inside an alphanumeric word is not a quotation
			// delimiter; this includes a code's possessive, e.g. JOB-42's.
			if i > 0 && i+1 < len(runes) && (unicode.IsLetter(runes[i-1]) || unicode.IsNumber(runes[i-1])) && (unicode.IsLetter(runes[i+1]) || unicode.IsNumber(runes[i+1])) {
				continue
			}
		case '“':
			closing = '”'
		case '‘':
			closing = '’'
		case '«':
			closing = '»'
		case '‹':
			closing = '›'
		case '„':
			closing = '“'
		case '‚':
			closing = '‘'
		case '‟':
			closing = '”'
		case '‛':
			closing = '’'
		case '「':
			closing = '」'
		case '『':
			closing = '』'
		case '《':
			closing = '》'
		case '〈':
			closing = '〉'
		case '｢':
			closing = '｣'
		case '＂', '＇':
		case '"', '`':
		case '”', '»', '›', '」', '』', '》', '〉', '｣':
			return nil, false
		default:
			if unicode.Is(unicode.Pi, opening) || unicode.Is(unicode.Pf, opening) {
				return nil, false
			}
			continue
		}
		start := i
		i++
		for i < len(runes) {
			if runes[i] == '\\' && i+1 < len(runes) {
				i += 2
				continue
			}
			if runes[i] == closing {
				break
			}
			i++
		}
		if i == len(runes) {
			return nil, false
		}
		result = append(result, string(runes[start:i+1]))
	}
	return result, true
}
