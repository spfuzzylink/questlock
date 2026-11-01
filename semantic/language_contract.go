package semantic

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LanguageContractGuardVersion identifies rejection-only checks for explicit
// language directives. It is not a detector of an answer's language or meaning.
const LanguageContractGuardVersion = 1

const languageToken = `([\p{L}][\p{L}\p{N}_-]{0,39})`

var (
	languageDirected     = regexp.MustCompile(`(?i)\b(answer|respond|reply|write|return|output|provide|give|compose|produce|summarize|summarise|explain|draft|render|translate|responde|responder|réponds|répondez)\b([^.!?;\n]{0,160}?)\b(in|into|to|using|en)\s+(?:(?:the|el|la|plain|simple|clear|natural|fluent)\s+){0,2}(?:language\s+)?["'` + "`" + `“‘]?` + languageToken)
	languageLabel        = regexp.MustCompile(`(?i)\b(?:(?:output|response|reply|answer)\s+)?language\s*[:=]\s*["'` + "`" + `“‘]?` + languageToken)
	languageSubject      = regexp.MustCompile(`(?i)\b(?:answer|response|reply|output|summary|text)\s+(?:must|should|needs?\s+to|has\s+to|is\s+to)\s+be\s+(?:written\s+)?(?:(in|en)\s+)?(?:(?:plain|simple|clear|natural|fluent)\s+)?["'` + "`" + `“‘]?` + languageToken)
	languageUse          = regexp.MustCompile(`(?i)\buse\s+(?:(?:only|the)\s+)?(?:language\s+)?["'` + "`" + `“‘]?` + languageToken + `["'` + "`" + `”’]?(?:\s+language)?(?:\s+for\s+(?:the|your|this)\s+(?:answer|reply|response|output|summary)|\s*(?:[.!?;\n]|$))`)
	languagePrefix       = regexp.MustCompile(`(?i)(?:^|[.!?;\n])\s*(?:please\s+)?(?:in|en)\s+["'` + "`" + `“‘]?` + languageToken + `["'` + "`" + `”’]?\s*[:,]`)
	languageAdjective    = regexp.MustCompile(`(?i)\b(?:give|write|return|provide|output|compose|draft|produce)\s+(?:(?:me|only|a|an|the|your)\s+){0,3}["'` + "`" + `“‘]?` + languageToken + `["'` + "`" + `”’]?\s+(?:answer|response|reply|summary|paragraph|prose|text)\b`)
	languageListTail     = regexp.MustCompile(`(?i)^\s*(?:,\s*(?:and\s+|or\s+)?|and\s+|or\s+|/\s*)` + languageToken)
	languageTag          = regexp.MustCompile(`(?i)^[a-z]{2}(?:-[a-z0-9]{2,8})+$`)
	languageMeta         = regexp.MustCompile(`(?i)\b(?:the\s+(?:phrase|words|instruction|example)|for\s+example|(?:source|article|text|prompt|user|message|manual|handbook|documentation|snippet|template)\s+(?:says|said|contains)|meaning\s+of)\b`)
	languageDefaultIs    = regexp.MustCompile(`(?i)\b` + languageToken + `\s+is\s+(?:the\s+)?default\s+(?:output\s+)?language\b`)
	languageDefaultLabel = regexp.MustCompile(`(?i)\bdefault\s+(?:(?:output|response|answer)\s+)?language\s*[:=]\s*["'` + "`" + `“‘]?` + languageToken)
	languageUseDefault   = regexp.MustCompile(`(?i)\buse\s+(?:the\s+)?["'` + "`" + `“‘]?` + languageToken + `["'` + "`" + `”’]?(?:\s+language)?\s+by\s+default\b`)
	languageLabelMeta    = regexp.MustCompile(`(?i)\b(?:what|which|why|whether|about|describe|explain|compare|default)\b`)
	languageIndirect     = regexp.MustCompile(`(?i)\b(?:how|whether|why)\s+(?:to\s+)?(?:answer|respond|reply|write|return|output|translate|use)\b`)
	languageNegated      = regexp.MustCompile(`(?i)\b(?:do\s+not|don't|don’t|never|not|avoid)\s*$`)
	languageNonNames     = wordSet("json csv tsv markdown html xml yaml text prose paragraph detail depth brief full short ordinary plain code python java go golang rust javascript typescript ruby swift kotlin sql bash sh zsh cpp c csharp utf8 ascii unicode")
	languageOtherNames   = wordSet("german deutsch italian portuguese japanese chinese mandarin cantonese korean hindi arabic russian dutch polish swedish danish norwegian finnish greek turkish hebrew indonesian vietnamese thai ukrainian klingon esperanto latin")
)

type languageDirective struct {
	positive  map[string]bool
	forbidden map[string]bool
	unknown   bool
	any       bool
}

// CheckReuseLanguageContract compares explicit output-language contracts only.
// A source question overrides the shared instruction default; the current
// question independently overrides that same default. Recognized unsupported
// or multiple languages withhold reuse. No directive and no known change is only a pass to
// the mandatory contextual verifier, never evidence that an answer is correct.
// English, Spanish and French names/basic ISO tags are recognized; regional tags
// and other explicit languages conservatively remain unknown.
func CheckReuseLanguageContract(original, instructions, current string) []string {
	if !validGuardText(original) || !validGuardText(current) || instructions != "" && !validGuardText(instructions) {
		return []string{"invalid_language_contract_text"}
	}
	defaults := parseLanguageDefaults(instructions)
	source := effectiveLanguage(parseLanguageDirectives(original), defaults)
	target := effectiveLanguage(parseLanguageDirectives(current), defaults)
	if source.unknown || target.unknown {
		return []string{"output_language_unknown"}
	}
	if len(source.positive) > 1 || len(target.positive) > 1 {
		return []string{"output_language_ambiguous"}
	}
	sourceLanguage := onlyLanguage(source.positive)
	targetLanguage := onlyLanguage(target.positive)
	if sourceLanguage != "" && target.forbidden[sourceLanguage] {
		return []string{"output_language_changed"}
	}
	if targetLanguage != "" {
		if sourceLanguage == "" {
			return []string{"output_language_source_unknown"}
		}
		if sourceLanguage != targetLanguage {
			return []string{"output_language_changed"}
		}
	}
	return nil
}

func effectiveLanguage(question, defaults languageDirective) languageDirective {
	// A positive directive in the actual question has precedence over a
	// default language. Explicit prohibitions still constrain that question.
	if !question.any {
		return defaults
	}
	if len(question.positive) == 0 && !question.unknown {
		question.positive = defaults.positive
		question.unknown = defaults.unknown
	}
	for language := range defaults.forbidden {
		question.forbidden[language] = true
	}
	for language := range question.positive {
		if question.forbidden[language] {
			question.unknown = true
		}
	}
	return question
}

func onlyLanguage(languages map[string]bool) string {
	for language := range languages {
		return language
	}
	return ""
}

func parseLanguageDirectives(text string) languageDirective {
	out := languageDirective{positive: map[string]bool{}, forbidden: map[string]bool{}}
	quoted := contractQuoteMask(text)
	scan := contractScanText(text, quoted)
	add := func(start, end, wordStart, wordEnd int, explicitLabel bool) {
		if !contractDirectiveAt(text, quoted, start) {
			return
		}
		word := text[wordStart:wordEnd]
		language, recognized := contractLanguageName(word, explicitLabel)
		if !recognized {
			return
		}
		out.any = true
		negative := languageNegated.MatchString(text[contractClauseStart(text, start):start])
		if language == "" {
			out.unknown = true
			return
		}
		if negative {
			out.forbidden[language] = true
		} else {
			out.positive[language] = true
		}
		if tail := languageListTail.FindStringSubmatchIndex(text[end:]); tail != nil {
			other, known := contractLanguageName(text[end+tail[2]:end+tail[3]], false)
			if known {
				if other == "" {
					out.unknown = true
				} else if negative {
					out.forbidden[other] = true
				} else {
					out.positive[other] = true
				}
			}
		}
	}
	for _, m := range languageDirected.FindAllStringSubmatchIndex(scan, -1) {
		verb, between, link := strings.ToLower(text[m[2]:m[3]]), text[m[4]:m[5]], strings.ToLower(text[m[6]:m[7]])
		if quoted[m[6]] || languageIndirect.MatchString(between) || languageMeta.MatchString(between) || link == "to" && verb != "translate" && verb != "render" {
			continue
		}
		explicitLanguage := strings.Contains(strings.ToLower(text[m[7]:m[8]]), "language") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(text[m[1]:])), "language")
		add(m[0], m[1], m[8], m[9], explicitLanguage)
	}
	for _, m := range languageLabel.FindAllStringSubmatchIndex(scan, -1) {
		if languageLabelMeta.MatchString(text[contractClauseStart(text, m[0]):m[0]]) {
			continue
		}
		add(m[0], m[1], m[2], m[3], true)
	}
	for _, m := range languageSubject.FindAllStringSubmatchIndex(scan, -1) {
		if m[2] >= 0 && quoted[m[2]] {
			continue
		}
		add(m[0], m[1], m[4], m[5], false)
	}
	for _, pattern := range []*regexp.Regexp{languageUse, languageAdjective} {
		for _, m := range pattern.FindAllStringSubmatchIndex(scan, -1) {
			add(m[0], m[1], m[2], m[3], false)
		}
	}
	for _, m := range languagePrefix.FindAllStringSubmatchIndex(scan, -1) {
		word := text[m[2]:m[3]]
		lang, _ := contractLanguageName(word, false)
		if lang == "" && !languageOtherNames[strings.ToLower(word)] && !languageTag.MatchString(word) {
			continue
		}
		start := m[0]
		for start < m[2] && strings.ContainsRune(".!?;\n \t", rune(text[start])) {
			start++
		}
		add(start, m[1], m[2], m[3], false)
	}
	return out
}

// Defaults are interpreted only in the approved instructions. Questions
// asking about a default do not themselves become output-language directives.
func parseLanguageDefaults(text string) languageDirective {
	out := parseLanguageDirectives(text)
	quoted := contractQuoteMask(text)
	scan := contractScanText(text, quoted)
	for _, pattern := range []*regexp.Regexp{languageDefaultIs, languageDefaultLabel, languageUseDefault} {
		for _, m := range pattern.FindAllStringSubmatchIndex(scan, -1) {
			if !contractDirectiveAt(text, quoted, m[0]) {
				continue
			}
			language, _ := contractLanguageName(text[m[2]:m[3]], true)
			out.any = true
			if language == "" {
				out.unknown = true
				continue
			}
			if languageNegated.MatchString(text[contractClauseStart(text, m[0]):m[0]]) {
				out.forbidden[language] = true
			} else {
				out.positive[language] = true
			}
		}
	}
	return out
}

func contractLanguageName(word string, explicitLabel bool) (string, bool) {
	lower := strings.ToLower(word)
	switch lower {
	case "english", "en", "anglais", "inglés", "ingles":
		return "english", true
	case "spanish", "es", "español", "espanol", "espagnol":
		return "spanish", true
	case "french", "fr", "français", "francais", "francés", "frances":
		return "french", true
	}
	if languageNonNames[lower] && !explicitLabel {
		return "", false
	}
	if explicitLabel || languageOtherNames[lower] || languageTag.MatchString(word) {
		return "", true
	}
	// Capitalization alone cannot identify a language: column labels, units
	// and programming-language descriptions are common output data.
	return "", false
}

func contractClauseStart(text string, position int) int {
	if i := strings.LastIndexAny(text[:position], ".!?;\n"); i >= 0 {
		return i + 1
	}
	return 0
}

func contractDirectiveAt(text string, quoted []bool, position int) bool {
	if position < 0 || position >= len(text) || quoted[position] {
		return false
	}
	start := contractClauseStart(text, position)
	prefix := text[start:position]
	if strings.HasPrefix(strings.TrimSpace(prefix), ">") || languageMeta.MatchString(prefix) || languageIndirect.MatchString(prefix) {
		return false
	}
	return true
}

// Mark quoted data without treating contractions/possessives as opening quotes.
// Byte offsets stay aligned with regexp matches. A quoted language name is
// still usable when its requesting verb/preposition is outside quoted data.
func contractQuoteMask(text string) []bool {
	mask := make([]bool, len(text))
	var closeQuote rune
	escaped := false
	for offset, r := range text {
		size := utf8.RuneLen(r)
		if escaped {
			if closeQuote != 0 {
				for i := offset; i < offset+size; i++ {
					mask[i] = true
				}
			}
			escaped = false
			continue
		}
		if r == '\\' {
			if closeQuote != 0 {
				mask[offset] = true
			}
			escaped = true
			continue
		}
		if closeQuote != 0 {
			for i := offset; i < offset+size; i++ {
				mask[i] = true
			}
			if r == closeQuote {
				closeQuote = 0
			}
			continue
		}
		close := r
		switch r {
		case '“':
			close = '”'
		case '‘':
			close = '’'
		case '«':
			close = '»'
		case '「':
			close = '」'
		case '"', '\'', '`':
		default:
			continue
		}
		if r == '\'' && offset > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:offset])
			if unicode.IsLetter(prev) || unicode.IsNumber(prev) {
				continue
			}
		}
		closeQuote = close
		for i := offset; i < offset+size; i++ {
			mask[i] = true
		}
	}
	return mask
}

// Preserve byte positions while hiding quoted examples. A quoted single label
// (for example "French" or "CSV") can still name an active output contract;
// quoted requesting verbs remain excluded by contractDirectiveAt.
func contractScanText(text string, quoted []bool) string {
	scan := []byte(text)
	for start := 0; start < len(scan); {
		if !quoted[start] {
			start++
			continue
		}
		end := start + 1
		for end < len(scan) && quoted[end] {
			end++
		}
		label := strings.TrimSpace(strings.Trim(string(scan[start:end]), "\"'`“”‘’«»「」"))
		single := label != ""
		for _, r := range label {
			if !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-' {
				single = false
				break
			}
		}
		if !single && strings.ToLower(label) != "markdown table" && strings.ToLower(label) != "plain prose" {
			for i := start; i < end; i++ {
				scan[i] = ' '
			}
		}
		start = end
	}
	return string(scan)
}
