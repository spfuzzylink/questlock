package semantic

import (
	"encoding/csv"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	delimitedRowRequest    = regexp.MustCompile(`(?i)(?:^|[.!?;\n])\s*(?:please\s+)?(?:(?:can|could|would|will)\s+you\s+(?:please\s+)?)?(return|give|provide|show|output|produce|emit|format|present)\b([^.!?;\n]{0,160}?)\b(with)\s+(?:a|an|one|single)\s+(csv|tsv)\s+(?:row|record)\s+per\s+[\p{L}\p{N}_-]+\b`)
	delimitedNonDirective  = regexp.MustCompile(`(?i)\b(?:not|no|never|without|avoid|avoiding|don't|don’t|if|unless|whether|hypothetically|suppose|assuming|imagine|examples?|phrases?|quotations?|quoted|metadata|labels?|headings?|templates?)\b`)
	delimitedNamedColumns  = regexp.MustCompile(`(?i)\bcolumns\b\s*:?\s*`)
	delimitedColumnName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	delimitedHeaderColumns = regexp.MustCompile(`(?i)\b(?:and|with|include|use)\s+(?:a\s+|the\s+)?header(?:\s+row)?\s+(?:with\s+)?$`)
)

type delimitedRowContract struct {
	format   string
	columns  int
	header   []string
	conflict bool
}

// Recognize positive output direction, not discussion of a possible format.
// Regex and quote-mask offsets always refer to the original UTF-8 string.
func requestedDelimitedRows(question string) []delimitedRowContract {
	quoted := contractQuoteMask(question)
	var out []delimitedRowContract
	for _, m := range delimitedRowRequest.FindAllStringSubmatchIndex(question, -1) {
		verb, link, format := m[2], m[6], m[8]
		if !contractDirectiveAt(question, quoted, verb) || quoted[link] || quoted[format] {
			continue
		}
		between := question[m[3]:format]
		if languageMeta.MatchString(between) || languageIndirect.MatchString(between) || delimitedNonDirective.MatchString(between) {
			continue
		}
		end := outputClauseEnd(question, m[1])
		columns, header, conflict := delimitedColumnContract(question, m[9], end, requestedColumns(question[m[9]:end]))
		out = append(out, delimitedRowContract{strings.ToLower(question[m[8]:m[9]]), columns, header, conflict})
	}
	return out
}

// Only a complete bounded identifier list proves a width. A named column list
// alone does not require a header row or establish the answer's meaning.
func delimitedColumnContract(question string, start, end, columns int) (int, []string, bool) {
	if end < len(question) && question[end] == '.' && end+1 < len(question) {
		next, _ := utf8.DecodeRuneInString(question[end+1:])
		if unicode.IsLetter(next) || unicode.IsNumber(next) || next == '_' {
			return columns, nil, false
		}
	}
	clause := question[start:end]
	quoted := contractQuoteMask(question)
	for _, m := range delimitedNamedColumns.FindAllStringIndex(clause, -1) {
		prefix := clause[:m[0]]
		if languageMeta.MatchString(prefix) || languageIndirect.MatchString(prefix) || delimitedNonDirective.MatchString(prefix) {
			continue
		}
		list := clause[m[1]:]
		if strings.Count(list, ",") > 31 {
			continue
		}
		visible := true
		for _, hidden := range quoted[start+m[0] : end] {
			if hidden {
				visible = false
				break
			}
		}
		if !visible {
			continue
		}
		names := strings.Split(list, ",")
		seen := map[string]bool{}
		valid := true
		for i, name := range names {
			names[i] = strings.TrimSpace(name)
			key := strings.ToLower(names[i])
			if !delimitedColumnName.MatchString(names[i]) || seen[key] {
				valid = false
				break
			}
			seen[key] = true
		}
		if !valid {
			continue
		}
		if columns >= 0 && columns != len(names) {
			return columns, nil, true
		}
		var header []string
		if delimitedHeaderColumns.MatchString(clause[:m[0]]) {
			header = names
		}
		return len(names), header, false
	}
	return columns, nil, false
}

func delimitedHeaderMatches(answer string, delimiter rune, header []string) bool {
	if len(header) == 0 {
		return true
	}
	reader := csv.NewReader(strings.NewReader(answer))
	reader.Comma = delimiter
	reader.FieldsPerRecord = len(header)
	row, err := reader.Read()
	if err != nil {
		return false
	}
	for i, name := range header {
		if row[i] != name {
			return false
		}
	}
	return true
}
