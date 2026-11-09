package semantic

import (
	"regexp"
	"strings"
)

const updateSubject = `snapshot|facts|evidence|context|policy|rules|state|conditions|requirements|record`

var (
	updateReference           = regexp.MustCompile(`\b(use|using|under|apply|applying|given|with|against|based on|according to)\s+(?:(?:the|this|that|an?|our)\s+)?(?:new|updated|revised|changed|replacement)\s+(` + updateSubject + `)\b`)
	updateLabel               = regexp.MustCompile(`\b(?:new|updated|revised|changed|replacement)\s+(` + updateSubject + `)\s*[:=]\s*`)
	updatePostLabel           = regexp.MustCompile(`\b(` + updateSubject + `)\s+update\s*[:=]\s*`)
	updateAssume              = regexp.MustCompile(`^(?:please\s+)?assume\b[^.!?;\n]{1,256}\b(?:now|instead|changed|updated|revised)\b`)
	updateDiscussion          = regexp.MustCompile(`\b(?:if|unless|whether|hypothetically|suppose|perhaps|possibly|examples?|quotations?|quoted|metadata|labels?|headings?|templates?|phrases?|fiction|story|says?|said|reports?|reported|asks?|asked|discussion|meaning|compare)\b`)
	updateNegated             = regexp.MustCompile(`\b(?:not|never|no|without|avoid|don't|don’t|doesn't|doesn’t|isn't|isn’t|aren't|aren’t)\b`)
	updateConditionalTail     = regexp.MustCompile(`^\s+(?:(?:only|even)\s+)?(?:if|unless|when|provided|assuming)\b`)
	updateQuery               = regexp.MustCompile(`^(?:what|which|who|whose|when|where|why|how|is|are|was|were|do|does|did|can|could|may|might|will|would|should|must|has|have|had)\b`)
	updateReferenceDiscussion = regexp.MustCompile(`\b(?:means?|meaning|phrases?|expressions?|examples?|quotations?|quoted|metadata|labels?|headings?|templates?|reported|says?|said)\b`)
)

// An explicit instruction to use a new snapshot cannot borrow the old answer's
// certificate. Recognizing no such instruction says nothing about validity;
// implicit changes and arbitrary natural language still need the verifier.
func targetAdoptedUpdate(original, instructions, current string) bool {
	old := targetUpdateMarkers(original)
	for marker := range targetUpdateMarkers(instructions) {
		old[marker] = true
	}
	for marker := range targetUpdateMarkers(current) {
		if !old[marker] {
			return true
		}
	}
	return false
}

func targetUpdateMarkers(text string) map[string]bool {
	out := map[string]bool{}
	// Normalize first so Unicode case conversion cannot invalidate byte offsets.
	plain := targetPlain(strings.ToLower(text))
	for _, clause := range targetUpdateClauses(plain) {
		clause = strings.TrimSpace(clause)
		if clause == "" || strings.HasPrefix(clause, ">") {
			continue
		}
		for _, match := range updateReference.FindAllStringSubmatchIndex(clause, -1) {
			prefix := clause[:match[0]]
			link, tail := clause[match[2]:match[3]], clause[match[1]:]
			// A scoped question can adopt evidence ("under the updated policy,
			// who ...?"). Asking whether to use that policy does not adopt it.
			askingToUse := updateQuery.MatchString(clause) && (link == "use" || link == "apply")
			bareQuestion := strings.HasSuffix(clause, "?") && strings.TrimSpace(strings.TrimSuffix(tail, "?")) == ""
			if askingToUse || bareQuestion || updateDiscussion.MatchString(prefix) || updateReferenceDiscussion.MatchString(clause) || updateNegated.MatchString(prefix) || updateConditionalTail.MatchString(tail) {
				continue
			}
			out["reference:"+clause[match[4]:match[5]]] = true
		}
		if updateQuery.MatchString(clause) || strings.HasSuffix(clause, "?") {
			continue
		}
		for _, pattern := range []*regexp.Regexp{updateLabel, updatePostLabel} {
			for _, match := range pattern.FindAllStringSubmatchIndex(clause, -1) {
				prefix := clause[:match[0]]
				body := strings.Join(strings.Fields(clause[match[1]:]), " ")
				if body == "" || updateDiscussion.MatchString(prefix) || updateNegated.MatchString(prefix) {
					continue
				}
				out["declaration:"+clause[match[2]:match[3]]+":"+body] = true
			}
		}
		if match := updateAssume.FindStringIndex(clause); match != nil && !updateDiscussion.MatchString(clause) && !updateNegated.MatchString(clause[:match[1]]) {
			out["assumption:"+strings.Join(strings.Fields(clause), " ")] = true
		}
	}
	return out
}

// Sentence punctuation followed by whitespace is a boundary; decimal points
// and dotted names must not erase a preceding quotation/reporting qualifier.
func targetUpdateClauses(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		boundary := strings.ContainsRune("?!;\n\r", rune(ch)) || ch == '.' && (i+1 == len(text) || strings.ContainsRune(" \t\r\n", rune(text[i+1])))
		if boundary {
			out = append(out, text[start:i+1])
			start = i + 1
		}
	}
	out = append(out, text[start:])
	return out
}
