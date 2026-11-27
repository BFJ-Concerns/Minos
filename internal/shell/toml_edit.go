package shell

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// editTOML returns text with the value at key replaced by value, or with the
// key removed when value is nil, and every other byte the deployer wrote left
// where it was: their comments, blank lines, key order and spelling of each
// value the edit does not change.
//
// The edit is made in the text, statement by statement, and then proved: the
// result is decoded, and it must equal the original decoded with that one
// key changed. A file laid out in a way the edit cannot place is therefore
// refused with an error, never written wrong. A value is changed where its
// statement stands, and a field of an inline table within the braces, its
// other fields as they were written; a new key joins its table's section, or
// opens one at the end of the file. A removed key leaves the comment written
// beside it, and a table that only the removed key's dotted name defined goes
// with it. An array of tables is replaced as a unit, and comments between its
// elements stay where they were. A file that ends without a newline still
// does after the edit.
func editTOML(original string, key []string, value any) (string, error) {
	if len(key) == 0 {
		return "", errors.New("a setting needs a key")
	}
	text := original
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	before, err := decodeTOMLGeneric(text)
	if err != nil {
		return "", err
	}
	want, defined, err := genericTOMLValue(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", dottedTOMLKey(key), err)
	}
	expected := copyTOMLValue(before).(map[string]any)
	if err := setTOMLPath(expected, key, want, defined); err != nil {
		return "", fmt.Errorf("%s: %w", dottedTOMLKey(key), err)
	}

	// A key written inside an inline table is changed by rewriting the
	// statement that holds it, so the edit moves out to that statement.
	editKey, editWant, editDefined := key, want, defined
	statements, err := scanTOML(text)
	if err != nil {
		return "", err
	}
	for _, statement := range statements {
		if !statement.header && len(statement.key) < len(key) && hasTOMLPrefix(key, statement.key) {
			editKey = statement.key
			editWant, editDefined = lookupTOMLPath(expected, editKey)
			break
		}
	}

	var edited string
	if editDefined {
		have, haveDefined := lookupTOMLPath(before, editKey)
		edited, err = setTOML(text, editKey, editWant, have, haveDefined)
	} else {
		edited, err = removeTOML(text, editKey)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", dottedTOMLKey(key), err)
	}
	if edited == text {
		return original, nil
	}
	after, err := decodeTOMLGeneric(edited)
	if err != nil {
		return "", fmt.Errorf("%s: the edited file would not parse: %w", dottedTOMLKey(key), err)
	}
	if !defined {
		if err := pruneImpliedTables(expected, key, edited); err != nil {
			return "", err
		}
	}
	if !reflect.DeepEqual(after, expected) {
		return "", fmt.Errorf("%s: this file's layout is one the edit cannot place the key in without changing something else", dottedTOMLKey(key))
	}
	if !strings.HasSuffix(original, "\n") {
		edited = strings.TrimSuffix(edited, "\n")
	}
	return edited, nil
}

// pruneImpliedTables drops from expected each table above a removed key that
// is left empty and that no statement of edited names any more: a table the
// removed key's dotted name alone defined, which goes with the key.
func pruneImpliedTables(expected map[string]any, key []string, edited string) error {
	statements, err := scanTOML(edited)
	if err != nil {
		return err
	}
	for depth := len(key) - 1; depth >= 1; depth-- {
		table, _ := lookupTOMLPath(expected, key[:depth])
		if fields, isTable := table.(map[string]any); !isTable || len(fields) > 0 {
			return nil
		}
		for _, statement := range statements {
			if hasTOMLPrefix(statement.key, key[:depth]) {
				return nil
			}
		}
		parent, _ := lookupTOMLPath(expected, key[:depth-1])
		delete(parent.(map[string]any), key[depth-1])
	}
	return nil
}

// tomlStatement is one statement of a TOML file: a table header, or a key
// with its value. Offsets are bytes into the text scanned.
type tomlStatement struct {
	key    []string // the whole key path from the file's root
	header bool     // a [table] or [[array of tables]] line
	array  bool     // the header of an array of tables
	// start is the first byte of the statement's first line and end the byte
	// after its last line's newline; the value lies in [valueStart, valueEnd).
	start, end           int
	valueStart, valueEnd int
	// comment is the comment written after the statement on its last line,
	// from its "#", or empty.
	comment string
}

// scanTOML finds each statement's extent. It reads only as much TOML as that
// needs, and editTOML's proof is what makes a misreading harmless.
func scanTOML(text string) ([]tomlStatement, error) {
	var statements []tomlStatement
	var table []string
	for pos := 0; pos < len(text); {
		start := pos
		pos = skipTOMLSpace(text, pos)
		if pos >= len(text) {
			break
		}
		switch text[pos] {
		case '\n', '\r':
			pos++
		case '#':
			pos = endOfTOMLLine(text, pos)
		case '[':
			array := strings.HasPrefix(text[pos:], "[[")
			closing := "]"
			if array {
				closing = "]]"
			}
			key, next, err := scanTOMLKey(text, pos+len(closing))
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(text[next:], closing) {
				return nil, fmt.Errorf("unreadable table header at byte %d", start)
			}
			after := next + len(closing)
			pos = endOfTOMLLine(text, after)
			statements = append(statements, tomlStatement{key: key, header: true, array: array, start: start, end: pos, comment: trailingTOMLComment(text, after)})
			table = key
		default:
			key, next, err := scanTOMLKey(text, pos)
			if err != nil {
				return nil, err
			}
			if next >= len(text) || text[next] != '=' {
				return nil, fmt.Errorf("unreadable key at byte %d", start)
			}
			valueStart := skipTOMLSpace(text, next+1)
			valueEnd, err := scanTOMLValue(text, valueStart)
			if err != nil {
				return nil, err
			}
			pos = endOfTOMLLine(text, valueEnd)
			statements = append(statements, tomlStatement{
				key:   append(append([]string{}, table...), key...),
				start: start, end: pos, valueStart: valueStart, valueEnd: valueEnd,
				comment: trailingTOMLComment(text, valueEnd),
			})
		}
	}
	return statements, nil
}

func skipTOMLSpace(text string, pos int) int {
	for pos < len(text) && (text[pos] == ' ' || text[pos] == '\t') {
		pos++
	}
	return pos
}

// trailingTOMLComment is the comment that follows pos on its line, from its
// "#" to the end of the line, or empty where none does.
func trailingTOMLComment(text string, pos int) string {
	pos = skipTOMLSpace(text, pos)
	if pos >= len(text) || text[pos] != '#' {
		return ""
	}
	return strings.TrimRight(text[pos:endOfTOMLLine(text, pos)], "\r\n")
}

// withoutTOMLStatement is text with statement deleted and the comment beside
// it, if any, kept on a line of its own where the statement stood.
func withoutTOMLStatement(text string, statement tomlStatement) string {
	kept := ""
	if statement.comment != "" {
		kept = statement.comment + "\n"
	}
	return text[:statement.start] + kept + text[statement.end:]
}

// endOfTOMLLine is the byte after the newline that ends pos's line.
func endOfTOMLLine(text string, pos int) int {
	if newline := strings.IndexByte(text[pos:], '\n'); newline >= 0 {
		return pos + newline + 1
	}
	return len(text)
}

// scanTOMLKey reads a bare, quoted or dotted key and returns the byte after
// it and any space that follows.
func scanTOMLKey(text string, pos int) ([]string, int, error) {
	var key []string
	for {
		pos = skipTOMLSpace(text, pos)
		if pos >= len(text) {
			return nil, 0, errors.New("the file ends inside a key")
		}
		var segment string
		switch quote := text[pos]; quote {
		case '"', '\'':
			closing, err := endOfTOMLString(text, pos)
			if err != nil {
				return nil, 0, err
			}
			segment = text[pos+1 : closing-1]
			if quote == '"' {
				unquoted, err := strconv.Unquote(text[pos:closing])
				if err != nil {
					return nil, 0, fmt.Errorf("unreadable quoted key at byte %d", pos)
				}
				segment = unquoted
			}
			pos = closing
		default:
			end := pos
			for end < len(text) && isBareTOMLKeyByte(text[end]) {
				end++
			}
			if end == pos {
				return nil, 0, fmt.Errorf("unreadable key at byte %d", pos)
			}
			segment, pos = text[pos:end], end
		}
		key = append(key, segment)
		pos = skipTOMLSpace(text, pos)
		if pos >= len(text) || text[pos] != '.' {
			return key, pos, nil
		}
		pos++
	}
}

func isBareTOMLKeyByte(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// scanTOMLValue returns the byte after the value that starts at pos: the
// last byte before the line's comment or newline once every bracket and
// string the value opened has closed.
func scanTOMLValue(text string, pos int) (int, error) {
	depth, last := 0, pos
	for i := pos; i < len(text); {
		switch c := text[i]; c {
		case '"', '\'':
			closing, err := endOfTOMLString(text, i)
			if err != nil {
				return 0, err
			}
			i, last = closing, closing
		case '[', '{':
			depth++
			i++
			last = i
		case ']', '}':
			depth--
			i++
			last = i
		case '#':
			if depth <= 0 {
				return endOfTOMLValue(pos, last)
			}
			i = endOfTOMLLine(text, i)
		case '\n':
			if depth <= 0 {
				return endOfTOMLValue(pos, last)
			}
			i++
		case ' ', '\t', '\r':
			i++
		default:
			i++
			last = i
		}
	}
	if depth > 0 {
		return 0, errors.New("the file ends inside a value")
	}
	return endOfTOMLValue(pos, last)
}

func endOfTOMLValue(start, end int) (int, error) {
	if end == start {
		return 0, fmt.Errorf("a key at byte %d has no value", start)
	}
	return end, nil
}

// endOfTOMLString returns the byte after the string opening at pos, in any
// of the four forms: basic, literal, and the multi-line form of each.
func endOfTOMLString(text string, pos int) (int, error) {
	quote := text[pos]
	if delimiter := strings.Repeat(string(quote), 3); strings.HasPrefix(text[pos:], delimiter) {
		for i := pos + 3; i < len(text); i++ {
			switch {
			case quote == '"' && text[i] == '\\':
				i++
			case strings.HasPrefix(text[i:], delimiter):
				// A multi-line string may end in up to two quotes of its own.
				i += 3
				for extra := 0; extra < 2 && i < len(text) && text[i] == quote; extra++ {
					i++
				}
				return i, nil
			}
		}
		return 0, fmt.Errorf("the string opened at byte %d never closes", pos)
	}
	for i := pos + 1; i < len(text) && text[i] != '\n'; i++ {
		switch {
		case quote == '"' && text[i] == '\\':
			i++
		case text[i] == quote:
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("the string opened at byte %d never closes", pos)
}

// setTOML writes want at path. have is what the file holds there now: a
// value equal to it is left exactly as the deployer spelled it.
func setTOML(text string, path []string, want, have any, haveDefined bool) (string, error) {
	if haveDefined && reflect.DeepEqual(have, want) {
		return text, nil
	}
	statements, err := scanTOML(text)
	if err != nil {
		return "", err
	}
	for _, statement := range statements {
		if !statement.header && slices.Equal(statement.key, path) {
			inline, err := respellTOML(text[statement.valueStart:statement.valueEnd], want, have)
			if err != nil {
				return "", err
			}
			return text[:statement.valueStart] + inline + text[statement.valueEnd:], nil
		}
	}
	switch want := want.(type) {
	case map[string]any:
		return setTOMLTable(text, path, want, have, haveDefined)
	case []any:
		if len(want) > 0 && allTOMLTables(want) {
			return setTOMLTableArray(text, path, want)
		}
	}
	// A plain value where the file holds a table or nothing at all.
	if haveDefined {
		if text, err = removeTOML(text, path); err != nil {
			return "", err
		}
	}
	return insertTOMLValue(text, path, want)
}

// setTOMLTable writes a table key by key, so a key whose value stays is not
// touched, and leaves the table defined even when it is empty: a repository
// that defines a table empty overrides the service-wide one with nothing.
func setTOMLTable(text string, path []string, want map[string]any, have any, haveDefined bool) (string, error) {
	haveTable, isTable := have.(map[string]any)
	var err error
	if haveDefined && !isTable {
		if text, err = removeTOML(text, path); err != nil {
			return "", err
		}
	}
	for _, name := range sortedTOMLKeys(haveTable) {
		if _, kept := want[name]; !kept {
			if text, err = removeTOML(text, childTOMLPath(path, name)); err != nil {
				return "", err
			}
		}
	}
	for _, name := range sortedTOMLKeys(want) {
		child, childDefined := haveTable[name]
		if text, err = setTOML(text, childTOMLPath(path, name), want[name], child, childDefined); err != nil {
			return "", err
		}
	}
	statements, err := scanTOML(text)
	if err != nil {
		return "", err
	}
	for _, statement := range statements {
		if hasTOMLPrefix(statement.key, path) {
			return text, nil
		}
	}
	return appendTOMLSection(text, "["+dottedTOMLKey(path)+"]\n"), nil
}

// setTOMLTableArray replaces a whole array of tables with [[path]] blocks,
// where the first of the old blocks stood or at the end of the file.
func setTOMLTableArray(text string, path []string, want []any) (string, error) {
	var blocks strings.Builder
	for _, element := range want {
		table := element.(map[string]any)
		blocks.WriteString("[[" + dottedTOMLKey(path) + "]]\n")
		for _, name := range sortedTOMLKeys(table) {
			inline, err := inlineTOML(table[name])
			if err != nil {
				return "", err
			}
			blocks.WriteString(tomlKeySegment(name) + " = " + inline + "\n")
		}
	}
	statements, err := scanTOML(text)
	if err != nil {
		return "", err
	}
	at := -1
	for index := len(statements) - 1; index >= 0; index-- {
		statement := statements[index]
		if !hasTOMLPrefix(statement.key, path) {
			continue
		}
		text = withoutTOMLStatement(text, statement)
		// Blocks may stand only where a header stood: put in place of a key
		// in another table's body they would take the keys that follow it.
		at = -1
		if statement.header {
			at = statement.start
		}
	}
	if at < 0 {
		return appendTOMLSection(text, blocks.String()), nil
	}
	return text[:at] + blocks.String() + text[at:], nil
}

// removeTOML deletes every statement that sets path or anything beneath it,
// headers included, and nothing else: a comment beside a removed key stays,
// on the line where the key stood.
func removeTOML(text string, path []string) (string, error) {
	statements, err := scanTOML(text)
	if err != nil {
		return "", err
	}
	for index := len(statements) - 1; index >= 0; index-- {
		if statement := statements[index]; hasTOMLPrefix(statement.key, path) {
			text = withoutTOMLStatement(text, statement)
		}
	}
	return text, nil
}

// insertTOMLValue adds a key the file does not have: at the end of its
// table's section where the table has one, in a new section at the end of
// the file where the table is new, and otherwise as a dotted key in the
// nearest enclosing table that has a body of its own.
func insertTOMLValue(text string, path []string, want any) (string, error) {
	inline, err := inlineTOML(want)
	if err != nil {
		return "", err
	}
	statements, err := scanTOML(text)
	if err != nil {
		return "", err
	}
	parent := path[:len(path)-1]
	defined := len(parent) == 0
	for _, statement := range statements {
		defined = defined || hasTOMLPrefix(statement.key, parent)
	}
	if !defined {
		return appendTOMLSection(text, "["+dottedTOMLKey(parent)+"]\n"+tomlKeySegment(path[len(path)-1])+" = "+inline+"\n"), nil
	}
	for depth := len(parent); depth >= 0; depth-- {
		at, found := endOfTOMLTableBody(statements, path[:depth])
		if !found {
			continue
		}
		return text[:at] + dottedTOMLKey(path[depth:]) + " = " + inline + "\n" + text[at:], nil
	}
	return "", errors.New("no table in the file can take the key")
}

// endOfTOMLTableBody is where a new key of table goes: after the last key of
// its [table] section, or of the keys before the first header for the root.
func endOfTOMLTableBody(statements []tomlStatement, table []string) (int, bool) {
	at, inside := 0, len(table) == 0
	found := inside
	for _, statement := range statements {
		switch {
		case statement.header:
			inside = !statement.array && slices.Equal(statement.key, table)
			if inside {
				at, found = statement.end, true
			}
		case inside:
			at = statement.end
		}
	}
	return at, found
}

func appendTOMLSection(text, section string) string {
	if text != "" && !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}
	return text + section
}

// respellTOML spells want in place of written, the value text the file holds
// now, which decodes to have. Where both are tables and written is an inline
// table, a field whose value stays keeps its spelling and place, a changed
// field is respelt where it stands, a dropped field goes, and a new field
// follows the others; any other value is spelt afresh.
func respellTOML(written string, want, have any) (string, error) {
	wantTable, wantIsTable := want.(map[string]any)
	haveTable, haveIsTable := have.(map[string]any)
	if !wantIsTable || !haveIsTable || !strings.HasPrefix(written, "{") {
		return inlineTOML(want)
	}
	fields, err := scanInlineTOMLTable(written)
	if err != nil {
		return "", err
	}
	separator := ", "
	if len(fields) > 1 {
		separator = written[fields[0].valueEnd:fields[1].start]
	}
	opening, closing := "", ""
	if len(fields) > 0 {
		opening = written[1:fields[0].start]
		closing = written[fields[len(fields)-1].valueEnd : len(written)-1]
	}
	var spelled []string
	seen := map[string]bool{}
	for _, field := range fields {
		if len(field.key) != 1 {
			// A dotted field sets a table of its own; the whole value is
			// spelt afresh rather than taken apart.
			return inlineTOML(want)
		}
		name := field.key[0]
		seen[name] = true
		value, kept := wantTable[name]
		if !kept {
			continue
		}
		valueText := written[field.valueStart:field.valueEnd]
		if !reflect.DeepEqual(value, haveTable[name]) {
			if valueText, err = respellTOML(valueText, value, haveTable[name]); err != nil {
				return "", err
			}
		}
		spelled = append(spelled, written[field.start:field.valueStart]+valueText)
	}
	for _, name := range sortedTOMLKeys(wantTable) {
		if seen[name] {
			continue
		}
		inline, err := inlineTOML(wantTable[name])
		if err != nil {
			return "", err
		}
		spelled = append(spelled, tomlKeySegment(name)+" = "+inline)
	}
	if len(spelled) == 0 {
		return "{}", nil
	}
	return "{" + opening + strings.Join(spelled, separator) + closing + "}", nil
}

// inlineTOMLField is one key and value of an inline table: its key starts at
// start and its value lies in [valueStart, valueEnd), offsets into the table.
type inlineTOMLField struct {
	key                         []string
	start, valueStart, valueEnd int
}

// scanInlineTOMLTable reads the fields of the inline table that is the whole
// of written, braces included.
func scanInlineTOMLTable(written string) ([]inlineTOMLField, error) {
	var fields []inlineTOMLField
	pos := 1
	for {
		pos = skipTOMLSpace(written, pos)
		if pos >= len(written) {
			return nil, errors.New("an inline table never closes")
		}
		if written[pos] == '}' && len(fields) == 0 {
			return fields, nil
		}
		start := pos
		key, next, err := scanTOMLKey(written, pos)
		if err != nil {
			return nil, err
		}
		if next >= len(written) || written[next] != '=' {
			return nil, fmt.Errorf("unreadable inline table field at byte %d", start)
		}
		valueStart := skipTOMLSpace(written, next+1)
		valueEnd, err := scanInlineTOMLValue(written, valueStart)
		if err != nil {
			return nil, err
		}
		fields = append(fields, inlineTOMLField{key: key, start: start, valueStart: valueStart, valueEnd: valueEnd})
		pos = skipTOMLSpace(written, valueEnd)
		if pos >= len(written) {
			return nil, errors.New("an inline table never closes")
		}
		switch written[pos] {
		case ',':
			pos++
		case '}':
			return fields, nil
		default:
			return nil, fmt.Errorf("unreadable inline table at byte %d", pos)
		}
	}
}

// scanInlineTOMLValue returns the byte after the value of an inline table's
// field that starts at pos: the last byte before the comma or brace that
// ends it.
func scanInlineTOMLValue(text string, pos int) (int, error) {
	depth, last := 0, pos
	for i := pos; i < len(text); {
		switch c := text[i]; c {
		case '"', '\'':
			closing, err := endOfTOMLString(text, i)
			if err != nil {
				return 0, err
			}
			i, last = closing, closing
		case '[', '{':
			depth++
			i++
			last = i
		case ']', '}':
			if depth == 0 {
				return endOfTOMLValue(pos, last)
			}
			depth--
			i++
			last = i
		case ',':
			if depth == 0 {
				return endOfTOMLValue(pos, last)
			}
			i++
		case ' ', '\t', '\r', '\n':
			i++
		default:
			i++
			last = i
		}
	}
	return 0, errors.New("an inline table never closes")
}

// inlineTOML spells a decoded value as it is written after an equals sign.
func inlineTOML(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return tomlString(value), nil
	case bool:
		return strconv.FormatBool(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float64:
		spelled := strconv.FormatFloat(value, 'g', -1, 64)
		if !strings.ContainsAny(spelled, ".en") {
			spelled += ".0"
		}
		return spelled, nil
	case time.Time:
		return value.Format(time.RFC3339Nano), nil
	case []any:
		elements := make([]string, 0, len(value))
		for _, element := range value {
			inline, err := inlineTOML(element)
			if err != nil {
				return "", err
			}
			elements = append(elements, inline)
		}
		return "[" + strings.Join(elements, ", ") + "]", nil
	case map[string]any:
		pairs := make([]string, 0, len(value))
		for _, name := range sortedTOMLKeys(value) {
			inline, err := inlineTOML(value[name])
			if err != nil {
				return "", err
			}
			pairs = append(pairs, tomlKeySegment(name)+" = "+inline)
		}
		return "{" + strings.Join(pairs, ", ") + "}", nil
	default:
		return "", fmt.Errorf("a %T cannot be written as a TOML value", value)
	}
}

func tomlString(value string) string {
	var spelled strings.Builder
	spelled.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			spelled.WriteString(`\"`)
		case '\\':
			spelled.WriteString(`\\`)
		case '\n':
			spelled.WriteString(`\n`)
		case '\r':
			spelled.WriteString(`\r`)
		case '\t':
			spelled.WriteString(`\t`)
		default:
			if character < 0x20 || character == 0x7f {
				fmt.Fprintf(&spelled, `\u%04X`, character)
			} else {
				spelled.WriteRune(character)
			}
		}
	}
	spelled.WriteByte('"')
	return spelled.String()
}

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKeySegment(segment string) string {
	if bareTOMLKey.MatchString(segment) {
		return segment
	}
	return tomlString(segment)
}

func dottedTOMLKey(key []string) string {
	segments := make([]string, 0, len(key))
	for _, segment := range key {
		segments = append(segments, tomlKeySegment(segment))
	}
	return strings.Join(segments, ".")
}

func childTOMLPath(path []string, name string) []string {
	return append(append([]string{}, path...), name)
}

func hasTOMLPrefix(key, prefix []string) bool {
	return len(key) >= len(prefix) && slices.Equal(key[:len(prefix)], prefix)
}

func sortedTOMLKeys(table map[string]any) []string {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func allTOMLTables(values []any) bool {
	for _, value := range values {
		if _, isTable := value.(map[string]any); !isTable {
			return false
		}
	}
	return true
}

// decodeTOMLGeneric decodes text into maps, []any and scalars alone, so two
// decodings compare equal exactly when the files mean the same.
func decodeTOMLGeneric(text string) (map[string]any, error) {
	decoded := map[string]any{}
	if _, err := toml.Decode(text, &decoded); err != nil {
		return nil, err
	}
	return copyTOMLValue(decoded).(map[string]any), nil
}

// genericTOMLValue puts a value of any Go type the encoder takes, a knob's
// own struct included, into the decoded form. Nil is no value: the key is to
// be removed.
func genericTOMLValue(value any) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(map[string]any{"value": value}); err != nil {
		return nil, false, err
	}
	decoded, err := decodeTOMLGeneric(encoded.String())
	if err != nil {
		return nil, false, err
	}
	generic, present := decoded["value"]
	if !present {
		return nil, false, errors.New("the value is empty, which TOML cannot write: give an empty list or table to set one, or nil to remove the key")
	}
	return generic, true, nil
}

func copyTOMLValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(value))
		for name, child := range value {
			copied[name] = copyTOMLValue(child)
		}
		return copied
	case []map[string]any:
		copied := make([]any, 0, len(value))
		for _, child := range value {
			copied = append(copied, copyTOMLValue(child))
		}
		return copied
	case []any:
		copied := make([]any, 0, len(value))
		for _, child := range value {
			copied = append(copied, copyTOMLValue(child))
		}
		return copied
	default:
		return value
	}
}

func lookupTOMLPath(root map[string]any, path []string) (any, bool) {
	var value any = root
	for _, name := range path {
		table, isTable := value.(map[string]any)
		if !isTable {
			return nil, false
		}
		child, present := table[name]
		if !present {
			return nil, false
		}
		value = child
	}
	return value, true
}

// setTOMLPath changes the decoded form as the edit is meant to change the
// file: the value set, or the key deleted, and nothing else.
func setTOMLPath(root map[string]any, path []string, value any, defined bool) error {
	table := root
	for _, name := range path[:len(path)-1] {
		child, present := table[name]
		if !present {
			if !defined {
				return nil
			}
			child = map[string]any{}
			table[name] = child
		}
		next, isTable := child.(map[string]any)
		if !isTable {
			return fmt.Errorf("%s is not a table in this file", name)
		}
		table = next
	}
	if defined {
		table[path[len(path)-1]] = value
	} else {
		delete(table, path[len(path)-1])
	}
	return nil
}
