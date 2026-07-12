package shell

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

type ReviewAnchor struct {
	Path        string `json:"path"`
	OldPosition int    `json:"old_position"`
	NewPosition int    `json:"new_position"`
}

type changedLine struct {
	path string
	line int
}

func ReviewCommand(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos review changed-line|anchor|dedupe")
	}
	switch args[0] {
	case "changed-line":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos review changed-line DIFF PATH LINE")
		}
		line, err := positiveLine(args[3])
		if err != nil {
			return err
		}
		if err := validateReviewPath(args[2]); err != nil {
			return err
		}
		changed, err := diffChangedLines(args[1])
		if err != nil {
			return err
		}
		if !changed[changedLine{path: args[2], line: line}] {
			return fmt.Errorf("%s:%d is not a changed head line", args[2], line)
		}
		fmt.Fprintln(stdout, "changed")
		return nil
	case "anchor":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos review anchor DIFF PATH LINE")
		}
		line, err := positiveLine(args[3])
		if err != nil {
			return err
		}
		if err := validateReviewPath(args[2]); err != nil {
			return err
		}
		changed, err := diffChangedLines(args[1])
		if err != nil {
			return err
		}
		if !changed[changedLine{path: args[2], line: line}] {
			return fmt.Errorf("%s:%d is not a changed head line", args[2], line)
		}
		return json.NewEncoder(stdout).Encode(ReviewAnchor{Path: args[2], NewPosition: line})
	case "dedupe":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos review dedupe")
		}
		return dedupeReview(stdin, stdout)
	default:
		return fmt.Errorf("unknown review action %q", args[0])
	}
}

func validateReviewPath(value string) error {
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("review path must be a clean repository-relative path")
	}
	return nil
}

func positiveLine(raw string) (int, error) {
	line, err := strconv.Atoi(raw)
	if err != nil || line < 1 {
		return 0, fmt.Errorf("line must be a positive integer")
	}
	return line, nil
}

func diffChangedLines(path string) (map[changedLine]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	changed := make(map[changedLine]bool)
	scanner := bufio.NewScanner(file)
	// Generated files can contain very long lines. We only inspect the first
	// byte of content lines, but Scanner still needs room to consume them.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	newPath := ""
	newLine := 0
	inHunk := false
	for scanner.Scan() {
		line := scanner.Text()
		if inHunk && newPath != "" && line != "" {
			switch line[0] {
			case '+':
				changed[changedLine{path: newPath, line: newLine}] = true
				newLine++
				continue
			case ' ':
				newLine++
				continue
			case '-':
				// A deletion has no head-side line to anchor.
				continue
			case '\\':
				// "No newline at end of file" does not consume a line.
				continue
			default:
				inHunk = false
			}
		}
		if strings.HasPrefix(line, "+++ ") {
			newPath, err = parseDiffPath(strings.TrimPrefix(line, "+++ "), "b/")
			if err != nil {
				return nil, err
			}
			inHunk = false
			continue
		}
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			newLine, _ = strconv.Atoi(match[2])
			inHunk = true
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return changed, nil
}

func parseDiffPath(raw, prefix string) (string, error) {
	if raw == "/dev/null" {
		return "", nil
	}
	if strings.HasPrefix(raw, `"`) {
		unquoted, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("decode diff path: %w", err)
		}
		raw = unquoted
	}
	if !strings.HasPrefix(raw, prefix) {
		return "", fmt.Errorf("diff path %q does not begin with %s", raw, prefix)
	}
	return strings.TrimPrefix(raw, prefix), nil
}
