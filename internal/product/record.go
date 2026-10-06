package product

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RecordTargetKey names the target field of the review-binding record.
const RecordTargetKey = "target"

// recordLine is the review-binding record's grammar: the head and target a
// review covered, as an HTML comment the review body carries.
var recordLine = regexp.MustCompile(`^<!-- Minos:( [a-z][a-z0-9-]*=[A-Za-z0-9._/@-]+)+ -->$`)

// FormatRecord renders the review-binding record, refusing a token the grammar
// cannot carry.
func FormatRecord(values map[string]string) (string, error) {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if key == "" || value == "" || strings.ContainsAny(key, " =") || strings.ContainsAny(value, " =") {
			return "", fmt.Errorf("invalid product record token %q=%q", key, value)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var record strings.Builder
	record.WriteString("<!-- Minos:")
	for _, key := range keys {
		record.WriteByte(' ')
		record.WriteString(key)
		record.WriteByte('=')
		record.WriteString(values[key])
	}
	record.WriteString(" -->")
	line := record.String()
	if !recordLine.MatchString(line) {
		return "", fmt.Errorf("formatted product record did not match grammar")
	}
	return line, nil
}
