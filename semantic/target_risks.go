package semantic

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TargetRiskGuardVersion binds the rejection-only target-risk policy. An empty
// result only passes to the existing guards and contextual verifier; it is not
// evidence of equivalence, source validity, complete coverage or safe actions.
const TargetRiskGuardVersion = 7

var (
	targetBinary             = regexp.MustCompile(`^(?:is|are|was|were|do|does|did|can|could|may|might|will|would|should|must|has|have|had)\b`)
	targetOpening            = regexp.MustCompile(`(?i)^\s*(?:\*\*|__)?(?:yes|no)(?:\*\*|__)?(?:[.!,:;\s]|$)`)
	targetWords              = regexp.MustCompile(`[\p{L}\p{N}]+`)
	targetNegative           = regexp.MustCompile(`\b(?:not|never|cannot|can't|couldn't|isn't|aren't|wasn't|weren't|doesn't|don't|didn't|won't|wouldn't|shouldn't|mustn't|forbid|forbids|forbidden|deny|denies|denied|prohibit|prohibits|prohibited|reject|rejects|rejected|disable|disables|disabled|block|blocks|blocked|unavailable|closed)\b`)
	targetScopeHead          = regexp.MustCompile(`^(?:what|which)\s+(?:(?:the|all|other|remaining|available|permitted|allowed|supported|possible|complete|full|[0-9]+)\s+){0,4}(?:operations|actions|tasks|functions|capabilities|permissions|rights|services|abilities)\b`)
	targetWhatCan            = regexp.MustCompile(`^what\s+(?:can|may|could|might)\b`)
	targetWhatPermits        = regexp.MustCompile(`^what\b[^?!.;\n]{0,160}\b(?:allows?|permits?|enables?|supports?)\b`)
	targetTimingHead         = regexp.MustCompile(`^(?:what|which)\s+(?:time|date|deadline|duration|hour|day|role|person|actor|signer)\b`)
	targetInventoryCommand   = regexp.MustCompile(`^(?:list|enumerate|describe|show|give|provide)\b[^?!.;\n]{0,120}\b(?:operations|actions|tasks|functions|capabilities|permissions|rights|services|abilities)\b`)
	targetWH                 = regexp.MustCompile(`\b(?:what|which|who|whose|when|where|how)\b`)
	targetActionStatement    = regexp.MustCompile(`\b(?:can|may|allowed to|permitted to|able to|supports?|permits?|allows?)\s+(?:(?:still|also|only)\s+){0,3}([a-z]+)\b`)
	targetInventoryLabel     = regexp.MustCompile(`\b(?:complete|remaining|available|permitted|allowed)\s+(?:operations|actions|functions|capabilities|permissions|services)\s+(?:are|include|:)`)
	targetAuthorityWord      = regexp.MustCompile(`\b(?:approv[a-z]*|authorit[a-z]*|authoriz[a-z]*|sign|signatures?|signers?|consent)\b`)
	targetExclusiveAuthority = regexp.MustCompile(`\b(?:sole|single|only|no other)\b[^.!?;\n]{0,120}\b(?:approv[a-z]*|authorit[a-z]*|authoriz[a-z]*|sign|signatures?|signers?|consent)\b`)
	targetScopedNegation     = regexp.MustCompile(`\b(?:not|never|cannot|can't|couldn't|isn't|aren't|wasn't|weren't|doesn't|don't|didn't|won't|wouldn't|shouldn't|mustn't)\b`)
	targetPermissionAction   = regexp.MustCompile(`\b(?:may|(?:is|are)\s+(?:allowed|permitted|authorized)\s+to)\s+(?:(?:still|also|only)\s+){0,3}([a-z]+)\b`)
	targetPermissionQuestion = regexp.MustCompile(`^(?:can|could|may|might)\b|^(?:is|are)\b[^.!?;\n]{0,120}\b(?:allowed|permitted|authorized)\s+to\b`)
	targetJointAuthorityText = regexp.MustCompile(`(?:^|[.!?;\n])\s*both\s+[^.!?;\n]{1,80}\band\b[^.!?;\n]{1,80}\b(?:approve|sign|authorize|consent)\b|\bboth\s+(?:parties|actors|roles|approvers|signers|signatories|authorities)\s+(?:(?:must|shall)\s+)?(?:approve|sign|authorize|consent)\b|\b(?:approval|signatures?|consent|authorization)\s+(?:from|of|by)\s+both\b|\bjoint(?:ly)?\b[^.!?;\n]{0,80}\b(?:approv[a-z]*|sign|authoriz[a-z]*|consent)\b|\band\b[^.!?;\n]{1,80}\b(?:must|shall|need to|are required to)\s+(?:both\s+)?(?:approve|sign|authorize|consent)\b`)
	targetApproverKey        = regexp.MustCompile(`^(?:required[_ -]?)?(?:approvers|signers|authorities|authorizers|signatures)$`)
	targetPhysical           = regexp.MustCompile(`\b(?:physical|physically|technical|technically)\b`)
	targetCan                = regexp.MustCompile(`^(?:can|could)\b`)
	targetInventoryDefault   = regexp.MustCompile(`\b(?:always|every)\b[^.!?;\n]{0,160}\b(?:full|complete|all)\b[^.!?;\n]{0,160}\b(?:record|fields|json|inventory|list)\b`)
	targetRole               = regexp.MustCompile(`\b(?:as|acting as|role\s*(?:is|of|:|=)|if\s+(?:i|we|they|the requester|the user)\s+(?:am|are|were|was|is))\s+(?:an?\s+|the\s+)?(owner|administrator|admin|editor|viewer|reader|writer|guest|visitor|supervisor|operator|maintainer|approver|member)\b`)
	targetDefaultRole        = regexp.MustCompile(`\b(?:requester|user|actor|role)\s*(?:is|:|=)\s*(?:an?\s+|the\s+)?(owner|administrator|admin|editor|viewer|reader|writer|guest|visitor|supervisor|operator|maintainer|approver|member)\b`)
	targetSelfRole           = regexp.MustCompile(`(?:^|[.!?;\n])\s*(?:i(?:\s+am|'m|’m)|we(?:\s+are|'re|’re))\s+(?:now\s+)?(?:an?\s+|the\s+)?((?:[a-z0-9][a-z0-9_-]*\s+){0,3}?)(owner|administrator|admin|editor|viewer|reader|writer|guest|visitor|supervisor|operator|maintainer|approver|member)s?\b`)
	targetRoleExample        = regexp.MustCompile(`\b(?:in|from|of)\s+(?:(?:an?|the|this|that)\s+)?(?:example|quotation|quote|story|fiction|label|metadata)\b`)
)

// CheckReuseTargetRisks withholds recognizable suspected target/orientation or
// permission-scope changes. It deliberately does not prove antonym equivalence
// or arbitrary completeness, rewrite the reply, infer permissions from absence,
// execute actions, or authorize reuse. Ambiguity can cost valid reuse coverage.
func CheckReuseTargetRisks(originalQuestion, instructions, currentQuestion, cachedAnswer string) []string {
	if !validGuardText(originalQuestion) || !validGuardText(currentQuestion) || instructions != "" && !validGuardText(instructions) || !targetAnswerText(cachedAnswer) {
		return []string{"target_risk_invalid_text"}
	}
	original := targetQuestion(originalQuestion)
	current := targetQuestion(currentQuestion)
	defaults := targetPlain(instructions)
	if targetPlain(originalQuestion) == targetPlain(currentQuestion) {
		return nil
	}
	reasons := make([]string, 0, 3)
	if targetOpening.MatchString(strings.TrimSpace(cachedAnswer)) && targetBinary.MatchString(original) && targetBinary.MatchString(current) && targetBinaryChange(original, current, cachedAnswer) {
		reasons = append(reasons, "target_risk_binary_orientation")
	}
	oldBroad, newBroad := targetBroad(original), targetBroad(current)
	// This is a timing-only/relative-binary risk fence, not a proof that an
	// action-bearing answer lists every permission. Full coverage remains NLI.
	scopeRisk := newBroad && (!oldBroad && !targetSharedInventory(defaults, cachedAnswer) && (targetOpening.MatchString(strings.TrimSpace(cachedAnswer)) || !targetAnswerHasActions(cachedAnswer)) || oldBroad && targetNegative.MatchString(original) != targetNegative.MatchString(current))
	// Removing an explicit physical/technical qualification leaves the meaning
	// of "can" (ability or authorization) uncertain. This is withholding, not a
	// claim that the recorded capability is false or that a permission is denied.
	if targetPhysical.MatchString(original) && !targetPhysical.MatchString(current) && targetCan.MatchString(current) {
		scopeRisk = true
	}
	if scopeRisk {
		reasons = append(reasons, "target_risk_scope_expansion")
	}
	if targetAuthorityChange(targetPlain(originalQuestion), defaults, targetPlain(currentQuestion)) || targetExclusiveAuthority.MatchString(targetPlain(currentQuestion)) && targetJointAuthority(cachedAnswer) {
		reasons = append(reasons, "target_risk_authority_change")
	}
	if targetOperationObjectChange(originalQuestion, currentQuestion) {
		reasons = append(reasons, "target_risk_operation_object_change")
	}
	if targetAdoptedUpdate(originalQuestion, instructions, currentQuestion) {
		reasons = append(reasons, "target_risk_adopted_update")
	}
	return reasons
}

func targetAnswerText(text string) bool {
	if len(text) == 0 || len(text) > 131072 || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// Hide every quoted/code span, including one-word examples. Unlike the format
// parser, a quoted word never supplies a target predicate or role here.
func targetPlain(text string) string {
	mask := contractQuoteMask(text)
	scan := []byte(text)
	for i, quoted := range mask {
		if quoted {
			scan[i] = ' '
		}
	}
	return strings.ToLower(string(scan))
}
func targetQuestion(text string) string {
	text = targetPlain(text)
	clauses := []string{}
	start := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		// File extensions, dotted identifiers and decimal points are not sentence
		// boundaries. Splitting there can manufacture a false one-token change.
		boundary := strings.ContainsRune("?!;\n\r", rune(ch)) || ch == '.' && (i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t' || text[i+1] == '\n' || text[i+1] == '\r')
		if boundary {
			clauses = append(clauses, text[start:i])
			start = i + 1
		}
	}
	clauses = append(clauses, text[start:])
	for _, clause := range clauses {
		clause = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(clause), "please "))
		clause = strings.Join(strings.Fields(clause), " ")
		if targetBinary.MatchString(clause) {
			return clause
		}
		if wh := targetWH.FindStringIndex(clause); wh != nil {
			return clause[wh[0]:]
		}
		if comma := strings.LastIndexByte(clause, ','); comma >= 0 {
			tail := strings.TrimSpace(clause[comma+1:])
			if targetBinary.MatchString(tail) {
				return tail
			}
		}
	}
	return strings.Join(strings.Fields(text), " ")
}
func targetBroad(question string) bool {
	if targetTimingHead.MatchString(question) {
		return false
	}
	return targetWhatCan.MatchString(question) || targetWhatPermits.MatchString(question) || targetScopeHead.MatchString(question) || targetInventoryCommand.MatchString(question)
}

// A permitted action or an explicit inventory prevents a timing-only
// diagnosis. This never establishes that the whole requested set is complete.
func targetAnswerHasActions(answer string) bool {
	if json.Valid([]byte(strings.TrimSpace(answer))) {
		return targetStructuredInventory(answer)
	}
	text := targetPlain(answer)
	if targetInventoryLabel.MatchString(text) {
		return true
	}
	for _, m := range targetActionStatement.FindAllStringSubmatchIndex(text, -1) {
		verb := text[m[2]:m[3]]
		switch verb {
		case "close", "closes", "end", "ends", "expire", "expires", "stop", "stops", "last", "lasts", "begin", "begins", "open", "opens":
			continue
		}
		start := m[0]
		for start > 0 && !strings.ContainsRune(".!?;\n", rune(text[start-1])) {
			start--
		}
		if !targetNegative.MatchString(text[start:m[0]]) {
			return true
		}
	}
	return false
}
func targetStructuredInventory(answer string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(answer)), &object) != nil {
		return false
	}
	for key, value := range object {
		switch strings.ToLower(key) {
		case "permitted", "permissions", "capabilities", "operations", "functions", "actions", "services", "abilities":
			var list []json.RawMessage
			if json.Unmarshal(value, &list) == nil && list != nil {
				return true
			}
		}
	}
	return false
}

func targetBinaryChange(original, current, answer string) bool {
	// Count explicit negations separately: an unrelated negative noun phrase
	// (such as closed tickets) must not hide a newly negated target predicate.
	if len(targetScopedNegation.FindAllStringIndex(original, -1)) != len(targetScopedNegation.FindAllStringIndex(current, -1)) {
		return true
	}
	if targetNegative.MatchString(original) != targetNegative.MatchString(current) {
		return true
	}
	// A word substitution alone is not a risk: ready/prepared and
	// support/accept may describe the same proposition. Only a changed modal
	// permission target backed by a different explicit saved authorization
	// action supplies this additional narrow suspicion; the verifier decides
	// all other predicate/paraphrase relationships.
	if !targetPermissionQuestion.MatchString(original) || !targetPermissionQuestion.MatchString(current) {
		return false
	}
	a, b := targetContent(original), targetContent(current)
	removed, added := "", ""
	for token, n := range a {
		if n > b[token] {
			if n-b[token] != 1 || removed != "" {
				return false
			}
			removed = token
		}
	}
	for token, n := range b {
		if n > a[token] {
			if n-a[token] != 1 || added != "" {
				return false
			}
			added = token
		}
	}
	if removed == "" || added == "" {
		return false
	}
	plain := targetPlain(answer)
	if targetContent(plain)[added] > 0 {
		return false
	}
	for _, m := range targetPermissionAction.FindAllStringSubmatch(plain, -1) {
		if targetContent(m[1])[removed] > 0 {
			return true
		}
	}
	return false
}

func targetContent(question string) map[string]int {
	out := map[string]int{}
	for _, word := range targetWords.FindAllString(question, -1) {
		switch word {
		case "a", "an", "the", "is", "are", "was", "were", "do", "does", "did", "can", "could", "may", "might", "will", "would", "should", "must", "has", "have", "had", "to", "by", "of", "in", "on", "at", "for", "its", "their", "be", "being", "been", "allowed", "allow", "allows", "permitted", "permit", "permits":
			continue
		case "supported", "supports":
			word = "support"
		case "see", "sees", "viewing", "views":
			word = "view"
		case "accepts", "accepted", "accepting", "takes", "take", "admits", "admit":
			word = "accept"
		case "enables", "enabled", "enabling":
			word = "enable"
		}
		out[word]++
	}
	return out
}

// The exemption recognizes a declared shared, full structured inventory, not
// arbitrary answer completeness. Its contents must still pass the verifier.
func targetSharedInventory(instructions, answer string) bool {
	if !targetInventoryDefault.MatchString(instructions) {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(answer)), &object) != nil {
		return false
	}
	for key, value := range object {
		switch strings.ToLower(key) {
		case "permitted", "permissions", "capabilities", "operations", "functions", "actions", "services", "abilities":
			var list []json.RawMessage
			if json.Unmarshal(value, &list) == nil && list != nil {
				return true
			}
		}
	}
	return false
}
func targetAuthorityChange(original, instructions, current string) bool {
	target := targetRole.FindStringSubmatch(current)
	asserted := targetAssertedRoles(current)
	if target == nil && len(asserted) == 0 {
		return false
	}
	source := targetRole.FindStringSubmatch(original)
	if source == nil {
		if roles := targetAssertedRoles(original); len(roles) > 0 {
			source = []string{"", roles[0]}
		}
	}
	if source == nil {
		source = targetDefaultRole.FindStringSubmatch(instructions)
	}
	if source == nil {
		return true
	}
	canonical := func(role string) string {
		if role == "administrator" {
			return "admin"
		}
		return role
	}
	if target != nil && canonical(source[1]) != canonical(target[1]) {
		return true
	}
	for _, role := range asserted {
		if canonical(source[1]) != canonical(role) {
			return true
		}
	}
	return false
}

// Recognize a bounded present-tense self-role assertion, not arbitrary mentions
// of roles. In particular, questions about a role and statements about someone
// else are left to contextual verification. Quoted/code text is already masked.
func targetAssertedRoles(text string) []string {
	var roles []string
	for _, match := range targetSelfRole.FindAllStringSubmatchIndex(text, -1) {
		qualifiers := strings.Fields(text[match[2]:match[3]])
		assertion := true
		for _, word := range qualifiers {
			switch word {
			case "a", "an", "the", "now", "currently", "still", "new", "current", "primary", "secondary", "workspace", "account", "project", "team", "group", "organization", "organisation", "repository", "repo", "site", "tenant", "system":
				// A small noun-phrase grammar, not arbitrary intervening verbs.
			default:
				// Numbered/qualified identifiers can name the relevant workspace;
				// prose such as "asking an owner" is not a role assertion.
				if !strings.ContainsAny(word, "0123456789") {
					assertion = false
				}
			}
		}
		end := len(text)
		if next := strings.IndexAny(text[match[1]:], ".!?;\n"); next >= 0 {
			end = match[1] + next
		}
		if end < len(text) && text[end] == '?' {
			assertion = false
		}
		if assertion && !targetRoleExample.MatchString(text[match[1]:end]) {
			roles = append(roles, text[match[4]:match[5]])
		}
	}
	return roles
}

// Only explicit required-authority structure or a joint approval/signing
// predicate supplies this cue. Arbitrary conjunctions and quoted examples do
// not turn a single approver into multiple required parties.
func targetJointAuthority(answer string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(answer)), &object) == nil {
		for key, value := range object {
			if !targetApproverKey.MatchString(strings.ToLower(key)) {
				continue
			}
			var parties []string
			if json.Unmarshal(value, &parties) != nil {
				continue
			}
			distinct := map[string]bool{}
			for _, party := range parties {
				party = strings.TrimSpace(party)
				if party != "" {
					distinct[party] = true
				}
			}
			if len(distinct) > 1 {
				return true
			}
		}
		return false
	}
	return targetJointAuthorityText.MatchString(targetPlain(answer))
}
