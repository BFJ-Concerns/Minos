package shell

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var markerLine = regexp.MustCompile(`^Minos:( [a-z][a-z0-9-]*=[A-Za-z0-9._/@-]+)+$`)

func ParseMarker(line string) (map[string]string, error) {
	if !markerLine.MatchString(line) {
		return nil, fmt.Errorf("invalid marker line")
	}
	fields := strings.Fields(strings.TrimPrefix(line, "Minos:"))
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, _ := strings.Cut(field, "=")
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate marker key %q", key)
		}
		values[key] = value
	}
	return values, nil
}

func FormatMarker(values map[string]string) (string, error) {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if key == "" || value == "" || strings.ContainsAny(key, " =") || strings.ContainsAny(value, " =") {
			return "", fmt.Errorf("invalid marker token %q=%q", key, value)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("Minos:")
	for _, key := range keys {
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(values[key])
	}
	line := b.String()
	if !markerLine.MatchString(line) {
		return "", fmt.Errorf("formatted marker did not match grammar")
	}
	return line, nil
}
