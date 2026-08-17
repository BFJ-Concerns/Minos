package product

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	RecordCauseKey            = "cause"
	RecordCauseRequiredChecks = "required-checks"
	RecordTargetKey           = "target"
)

// Record binds a review to the head and target it covered.
var recordLine = regexp.MustCompile(`^<!-- Minos:( [a-z][a-z0-9-]*=[A-Za-z0-9._/@-]+)+ -->$`)

func ParseRecord(line string) (map[string]string, error) {
	if !recordLine.MatchString(line) {
		return nil, fmt.Errorf("invalid Minos product record")
	}
	content := strings.TrimSuffix(strings.TrimPrefix(line, "<!-- Minos:"), " -->")
	fields := strings.Fields(content)
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, _ := strings.Cut(field, "=")
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate product record key %q", key)
		}
		values[key] = value
	}
	return values, nil
}

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

func TrailingRecord(body string) (map[string]string, bool) {
	lines := strings.Split(strings.TrimRight(body, "\r\n"), "\n")
	if len(lines) == 0 {
		return nil, false
	}
	values, err := ParseRecord(strings.TrimSuffix(lines[len(lines)-1], "\r"))
	return values, err == nil
}
