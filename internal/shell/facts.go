package shell

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

type Facts struct {
	Occasion string
	Forge    string
	Owner    string
	Repo     string
	PR       string
	HeadSHA  string
	BaseRef  string
	BaseSHA  string
	HeadRef  string
}

func (f Facts) RepoSlug() string {
	if f.Owner == "" || f.Repo == "" {
		return ""
	}
	return f.Owner + "/" + f.Repo
}

func ParseFacts(r io.Reader) (Facts, error) {
	return parseFacts(r, false)
}

func ParseFactsAllowUnmapped(r io.Reader) (Facts, error) {
	return parseFacts(r, true)
}

func parseFacts(r io.Reader, allowUnmapped bool) (Facts, error) {
	values, err := parseKeyValues(r)
	if err != nil {
		return Facts{}, err
	}
	var facts Facts
	facts.Occasion = values["OCCASION"]
	facts.Forge = values["FORGE"]
	facts.Owner = values["OWNER"]
	facts.Repo = values["REPO"]
	facts.PR = values["PR"]
	facts.HeadSHA = values["HEAD_SHA"]
	facts.BaseRef = values["BASE_REF"]
	facts.BaseSHA = values["BASE_SHA"]
	facts.HeadRef = values["HEAD_BRANCH"]
	if facts.Occasion == "" && allowUnmapped {
		return facts, nil
	}
	if facts.Occasion == "" || facts.Owner == "" || facts.Repo == "" || facts.PR == "" {
		return Facts{}, fmt.Errorf("normalised facts missing required fields")
	}
	return facts, nil
}

func parseKeyValues(r io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid KEY=VALUE line %q", line)
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}
