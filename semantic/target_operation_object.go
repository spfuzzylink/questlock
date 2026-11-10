package semantic

import (
	"regexp"
	"strings"
)

var (
	targetExclusiveOperation = regexp.MustCompile(`\b([a-z]{2,24}ing)\s+only\s+`)
	targetRelocatedOperation = regexp.MustCompile(`^while\s+([a-z]{2,24}ing)\s+`)
	targetOperationWhile     = regexp.MustCompile(`\bwhile\b`)
	targetObjectWord         = regexp.MustCompile(`^[\p{L}\p{N}]+(?:-[\p{L}\p{N}]+)*$`)
	targetObjectCoordination = regexp.MustCompile(`\b(?:and|or|with|without|using|under|if|unless)\b`)
	targetOperationReported  = regexp.MustCompile(`\b(?:whether|if|unless|example|metadata|label|quotation|quote|story|fiction|said|says|reported|reporting|earlier|previously|formerly|yesterday)\b`)
)

type targetExclusiveObject struct {
	operation, object string
	end               int
}

// Withhold a recognizable directed role swap, not a mere noun substitution.
// Literal gerunds and bounded object spans deliberately leave other syntax to
// the contextual verifier. No match authorizes reuse or proves completeness.
func targetOperationObjectChange(original, current string) bool {
	old := targetExclusiveObjects(original)
	if len(old) == 0 {
		return false
	}
	// Lowercasing can change UTF-8 widths. Every offset and mask below must
	// refer to the same normalized byte string, not the caller's original.
	current = strings.ToLower(current)
	plain, quoted := targetPlain(current), contractQuoteMask(current)
	for _, selected := range targetExclusiveObjects(current) {
		tail := plain[selected.end:]
		match := targetRelocatedOperation.FindStringSubmatchIndex(tail)
		if match == nil {
			continue
		}
		otherOperation := tail[match[2]:match[3]]
		if otherOperation == selected.operation {
			continue
		}
		start := selected.end + match[1]
		kept, _, ok := targetOperationObject(plain, quoted, start)
		if !ok {
			continue
		}
		for _, source := range old {
			if source.operation == selected.operation && source.object == kept && source.object != selected.object && !targetObjectContained(source.object, selected.object) {
				return true
			}
		}
	}
	return false
}

func targetExclusiveObjects(text string) []targetExclusiveObject {
	text = strings.ToLower(text) // Keep directive, regexp and quote-mask offsets aligned.
	plain, quoted := targetPlain(text), contractQuoteMask(text)
	var out []targetExclusiveObject
	for _, match := range targetExclusiveOperation.FindAllStringSubmatchIndex(plain, -1) {
		if !contractDirectiveAt(text, quoted, match[0]) {
			continue
		}
		start := contractClauseStart(plain, match[0])
		prefix := plain[start:match[0]]
		if targetBinary.MatchString(targetQuestion(plain[start:])) || targetScopedNegation.MatchString(prefix) || strings.Contains(prefix, "without ") || targetOperationReported.MatchString(prefix) {
			continue
		}
		object, end, ok := targetOperationObject(plain, quoted, match[1])
		if !ok {
			continue
		}
		out = append(out, targetExclusiveObject{plain[match[2]:match[3]], object, end})
	}
	return out
}

func targetOperationObject(plain string, quoted []bool, start int) (string, int, bool) {
	end := len(plain)
	if next := strings.IndexAny(plain[start:], ",.!?;\n\r"); next >= 0 {
		end = start + next
	}
	if next := targetOperationWhile.FindStringIndex(plain[start:end]); next != nil {
		end = start + next[0]
	}
	for _, hidden := range quoted[start:end] {
		if hidden {
			return "", end, false
		}
	}
	span := strings.TrimSpace(plain[start:end])
	if len(span) > 80 || targetScopedNegation.MatchString(span) || targetObjectCoordination.MatchString(span) {
		return "", end, false
	}
	words := strings.Fields(span)
	if len(words) > 0 && (words[0] == "a" || words[0] == "an" || words[0] == "the") {
		words = words[1:]
	}
	if len(words) < 1 || len(words) > 6 {
		return "", end, false
	}
	for _, word := range words {
		if !targetObjectWord.MatchString(word) {
			return "", end, false
		}
	}
	return strings.Join(words, " "), end, true
}

func targetObjectContained(a, b string) bool {
	return strings.Contains(" "+a+" ", " "+b+" ") || strings.Contains(" "+b+" ", " "+a+" ")
}
