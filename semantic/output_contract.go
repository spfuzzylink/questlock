package semantic

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OutputContractGuardVersion identifies rejection-only checks for explicit
// presentation requests. Passing them never establishes meaning or correctness.
const OutputContractGuardVersion = 2

var (
	explicitFormat = regexp.MustCompile(`(?i)\b(?:return|respond|output|provide|give|format)\b[^.!?;\n]{0,160}?\b(?:as|in|using)\s+(?:(?:exactly|only|just|a|an|the)\s+){0,4}(?:(?:zero|one|two|three|four|five|six|seven|eight|nine|ten|[0-9]+)[-\s]+(?:(?:columns?|fields?)[-\s]+)?)?[` + "`\"" + `]?(json(?:\s+(?:object|array))?|csv|tsv|markdown\s+table)\b`)
	onlyFormat     = regexp.MustCompile(`(?i)\b(?:return|respond|output|provide|give)\s+(?:with\s+)?only\s+(?:an?\s+)?(json(?:\s+(?:object|array))?|csv|tsv)\b`)
	onlyNumeric    = regexp.MustCompile(`(?i)\b(?:return|respond|output|provide|give)\b[^.?!;\n]{0,80}?\b(?:only|just)\s+(?:(?:the|a|an)\s+)?(numeric|number|integer|decimal)\b`)
	numericOnly    = regexp.MustCompile(`(?i)\b(?:return|respond|output|provide|give)\b[^.?!;\n]{0,80}?\b(numeric|number|integer|decimal)[-\s]+only\b`)
	numericValue   = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
	integerValue   = regexp.MustCompile(`^[+-]?[0-9]+$`)
	decimalValue   = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)
	digitsOnly     = regexp.MustCompile(`^[0-9]+$`)
	noPunctuation  = regexp.MustCompile(`(?i)\bno\b[^.?!\n]{0,100}\bpunctuation\b`)
	columnCount    = regexp.MustCompile(`(?i)\b([0-9]+|zero|one|two|three|four|five|six|seven|eight|nine|ten)[-\s]+(?:(?:csv|tsv)\s+)?(?:fields?|columns?)\b`)
	prefixColumns  = regexp.MustCompile(`(?i)\b([0-9]+|zero|one|two|three|four|five|six|seven|eight|nine|ten)[-\s]+(?:fields?|columns?)[-\s]+$`)
	prefixCount    = regexp.MustCompile(`(?i)\b([0-9]+|zero|one|two|three|four|five|six|seven|eight|nine|ten)\s+$`)
	suffixColumns  = regexp.MustCompile(`(?i)^\s+(?:fields?|columns?)\b`)
	formatLink     = regexp.MustCompile(`(?i)\b(?:as|in|using)\s+`)
	tableSeparator = regexp.MustCompile(`^:?-{3,}:?$`)
	proseWithLink  = regexp.MustCompile(`(?i)\b(?:return|respond|answer|reply|write|provide|give|output|explain|summarize|summarise|describe|format)\b[^.!?;\n]{0,160}?\b(as|in|using)\s+(?:(?:a|an|the|one|single|plain|short|concise|brief|ordinary|normal|simple|only|just|regular)\s+){0,5}(prose|paragraphs?)\b`)
	proseDirect    = regexp.MustCompile(`(?i)\b(?:return|respond|answer|reply|write|provide|give|output|use)\s+(?:(?:me|your|a|an|the|only|just|one|single|plain|short|concise|brief|ordinary|normal|simple|regular)\s+){0,6}(prose|paragraphs?)\b`)
	proseSubject   = regexp.MustCompile(`(?i)\b(?:answer|response|reply|output|summary)\s+(?:must|should|needs?\s+to|has\s+to)\s+be\s+(?:(?:a|an|the|one|single|plain|short|concise|brief|ordinary|normal|simple)\s+){0,5}(prose|paragraphs?)\b`)
	noJSONClause   = regexp.MustCompile(`(?i)(?:^|[.!?;\n,])\s*(?:please\s+)?((?:no|without)\s+(?:any\s+)?json)\b`)
	noJSONDirected = regexp.MustCompile(`(?i)\b(?:return|respond|answer|reply|write|provide|give|output|explain|summarize|summarise|describe|compose|render|draft)\b[^.!?;\n]{0,160}?\b((?:without|no)\s+(?:any\s+)?json)\b`)
	noJSONNegated  = regexp.MustCompile(`(?i)\b(?:do\s+not|don't|don’t|never)\s+(?:return|use|output|write|provide|give|compose|render|draft|respond(?:\s+with|\s+in)?|answer(?:\s+with|\s+in)?)\s+(?:any\s+)?(json)\b`)
	htmlTable      = regexp.MustCompile(`(?is)^<table(?:\s[^>]*)?>.*</table>\s*$`)
	noJSONAbout    = regexp.MustCompile(`(?i)\b(?:why|whether|about|meaning\s+of|phrase)\b`)
	proseReference = regexp.MustCompile(`(?i)^\s+(?:[0-9]+\b|ids?\b|identifiers?\b|numbers?\b|counts?\b|lengths?\b|boundaries\b|segmentation\b)`)
)

// CheckReuseAnswerContract rejects a saved answer that demonstrably violates a
// recognized explicit output format. It does not infer a format from the task's
// subject, convert an answer, authorize reuse, or replace the local verifier.
// Unrecognized and ambiguous requests still require normal validity checking.
func CheckReuseAnswerContract(question, answer string) []string {
	if !validGuardText(question) || !utf8.ValidString(answer) || len(answer) > 131072 {
		return []string{"invalid_output_contract_text"}
	}
	text := strings.TrimSpace(answer)
	prose, noJSON := requestedProseContract(question)
	if noJSON && structuredJSONAnswer(text) {
		return []string{"output_no_json_contract_mismatch"}
	}
	if prose && (structuredJSONAnswer(text) || structuredTableAnswer(text)) {
		return []string{"output_prose_contract_mismatch"}
	}
	for _, pattern := range []*regexp.Regexp{onlyNumeric, numericOnly} {
		for _, match := range pattern.FindAllStringSubmatchIndex(question, -1) {
			if quotedRequest(question, match[0]) {
				continue
			}
			valid := numericValue.MatchString(text)
			switch strings.ToLower(question[match[2]:match[3]]) {
			case "integer":
				valid = integerValue.MatchString(text)
			case "decimal":
				valid = decimalValue.MatchString(text)
			}
			if noPunctuation.MatchString(question[match[0]:outputClauseEnd(question, match[1])]) {
				valid = digitsOnly.MatchString(text)
			}
			if !valid {
				return []string{"output_numeric_contract_mismatch"}
			}
		}
	}
	previous := ""
	for _, pattern := range []*regexp.Regexp{explicitFormat, onlyFormat} {
		for _, match := range pattern.FindAllStringSubmatchIndex(question, -1) {
			if quotedRequest(question, match[0]) {
				continue
			}
			if pattern == explicitFormat {
				links := formatLink.FindAllStringIndex(question[match[0]:match[2]], -1)
				if len(links) > 0 && quotedRequest(question, match[0]+links[len(links)-1][0]) {
					continue
				}
			}
			format := strings.ToLower(question[match[2]:match[3]])
			parts := strings.Fields(format)
			format = parts[0]
			if previous != "" && previous != format {
				return []string{"output_contract_conflict"}
			}
			previous = format
			columns := requestedColumns(question[match[3]:outputClauseEnd(question, match[1])])
			if prefix := strings.TrimRight(question[match[0]:match[2]], "`\""); columns < 0 {
				if position := prefixColumns.FindStringIndex(prefix); position != nil {
					columns = requestedColumns(prefix[position[0]:])
				} else if suffixColumns.MatchString(question[match[3]:]) {
					if count := prefixCount.FindStringSubmatch(prefix); count != nil {
						columns = requestedColumns(count[1] + " fields")
					}
				}
			}
			switch format {
			case "json":
				valid := json.Valid([]byte(text))
				if len(parts) == 2 && valid {
					valid = parts[1] == "object" && text[0] == '{' || parts[1] == "array" && text[0] == '['
				}
				if !valid {
					return []string{"output_json_contract_mismatch"}
				}
			case "csv", "tsv":
				delimiter := ','
				if format == "tsv" {
					delimiter = '\t'
				}
				if !delimitedAnswer(answer, delimiter, columns) {
					return []string{"output_" + format + "_contract_mismatch"}
				}
			default: // Whitespace in the recognized Markdown table phrase can vary.
				if !markdownAnswer(text, columns) {
					return []string{"output_table_contract_mismatch"}
				}
			}
		}
	}
	return nil
}

func outputClauseEnd(question string, end int) int {
	if i := strings.IndexAny(question[end:], ".?!;\n"); i >= 0 {
		return end + i
	}
	return len(question)
}

// Ignore request verbs quoted as source text; quoting a format name inside a
// real request remains supported. Apostrophes in ordinary words are not quotes.
func quotedRequest(question string, position int) bool {
	if position < 0 || position >= len(question) {
		return false
	}
	return contractQuoteMask(question)[position]
}

func requestedProseContract(question string) (prose, noJSON bool) {
	quoted := contractQuoteMask(question)
	scan := contractScanText(question, quoted)
	for _, m := range proseWithLink.FindAllStringSubmatchIndex(scan, -1) {
		if contractDirectiveAt(question, quoted, m[0]) && !quoted[m[2]] && !proseReference.MatchString(question[m[1]:]) && !languageMeta.MatchString(question[m[0]:m[2]]) && !languageIndirect.MatchString(question[m[0]:m[2]]) {
			prose = true
		}
	}
	for _, pattern := range []*regexp.Regexp{proseDirect, proseSubject} {
		for _, m := range pattern.FindAllStringSubmatchIndex(scan, -1) {
			if contractDirectiveAt(question, quoted, m[0]) && !proseReference.MatchString(question[m[1]:]) {
				prose = true
			}
		}
	}
	for _, pattern := range []*regexp.Regexp{noJSONClause, noJSONDirected, noJSONNegated} {
		for _, m := range pattern.FindAllStringSubmatchIndex(scan, -1) {
			start := m[0]
			if pattern == noJSONClause {
				start = m[2]
			}
			if pattern == noJSONDirected && strings.HasPrefix(strings.ToLower(question[m[2]:m[3]]), "no ") && noJSONAbout.MatchString(question[m[0]:m[2]]) {
				continue
			}
			if contractDirectiveAt(question, quoted, start) && !quoted[m[2]] && !languageMeta.MatchString(question[m[0]:m[2]]) && !languageIndirect.MatchString(question[m[0]:m[2]]) {
				noJSON = true
			}
		}
	}
	return prose, noJSON
}

// Recognize actual structured syntax. These checks do not decide whether prose
// is well written or complete, infer a source language, or rewrite the answer.
func structuredJSONAnswer(text string) bool {
	text = unfencedAnswer(text)
	return len(text) > 0 && (text[0] == '{' || text[0] == '[') && json.Valid([]byte(text))
}
func unfencedAnswer(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 && newline < len(text)-3 {
			return strings.TrimSpace(text[newline+1 : len(text)-3])
		}
	}
	return text
}
func structuredTableAnswer(text string) bool {
	text = unfencedAnswer(text)
	if markdownAnswer(text, -1) || htmlTable.MatchString(text) {
		return true
	}
	for _, delimiter := range []rune{',', '\t'} {
		r := csv.NewReader(strings.NewReader(text))
		r.Comma = delimiter
		rows, width := 0, 0
		for rows < 1025 {
			fields, err := r.Read()
			if err == io.EOF {
				if rows >= 2 && width >= 2 {
					return true
				}
				break
			}
			if err != nil {
				break
			}
			if rows == 0 {
				width = len(fields)
			}
			if len(fields) != width || width < 2 {
				break
			}
			rows++
		}
		if rows >= 1025 && width >= 2 {
			return true
		}
	}
	return false
}

func requestedColumns(question string) int {
	m := columnCount.FindStringSubmatch(question)
	if m == nil {
		return -1
	}
	words := map[string]int{"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10}
	if n, ok := words[strings.ToLower(m[1])]; ok {
		return n
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0 // An unrepresentable explicit width cannot fit this bounded answer.
	}
	return n
}

func delimitedAnswer(text string, delimiter rune, columns int) bool {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delimiter
	if columns >= 0 {
		// CSV's zero means infer, whereas an explicit zero-column request
		// cannot be met by a nonempty saved record.
		if columns == 0 {
			return false
		}
		r.FieldsPerRecord = columns
	}
	rows := 0
	for {
		_, err := r.Read()
		if err == io.EOF {
			return rows > 0
		}
		if err != nil || rows >= 1024 {
			return false
		}
		rows++
	}
}

func markdownAnswer(text string, columns int) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || len(lines) > 1024 {
		return false
	}
	width := -1
	for i, line := range lines {
		if !strings.Contains(line, "|") {
			return false
		}
		cells := markdownCells(line)
		if width < 0 {
			width = len(cells)
		}
		if len(cells) != width || columns >= 0 && width != columns {
			return false
		}
		if i == 1 {
			for _, cell := range cells {
				if !tableSeparator.MatchString(strings.TrimSpace(cell)) {
					return false
				}
			}
		}
	}
	return true
}

func markdownCells(line string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "|") {
		line = line[1:]
	}
	endEscaped := false
	for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
		endEscaped = !endEscaped
	}
	if strings.HasSuffix(line, "|") && !endEscaped {
		line = line[:len(line)-1]
	}
	cells, start, escaped := []string{}, 0, false
	for i := 0; i < len(line); i++ {
		if line[i] == '|' && !escaped {
			cells, start = append(cells, line[start:i]), i+1
		}
		if line[i] == '\\' {
			escaped = !escaped
		} else {
			escaped = false
		}
	}
	return append(cells, line[start:])
}
