package calibration

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type corpusIndex struct {
	Version int      `json:"version"`
	Cases   []string `json:"cases"`
}

type calibrationCase struct {
	ID                   string              `json:"id"`
	Summary              string              `json:"summary"`
	Tags                 []string            `json:"tags"`
	ExpectedReview       string              `json:"expected_review"`
	ExpectedProductState string              `json:"expected_product_state"`
	Coverage             string              `json:"coverage"`
	OmittedPaths         []string            `json:"omitted_paths"`
	Findings             []expectedFinding   `json:"findings"`
	SuppressedConcerns   []suppressedConcern `json:"suppressed_concerns"`
	Repair               *repairExpectation  `json:"repair"`
}

type expectedFinding struct {
	ID       string `json:"id"`
	Priority string `json:"priority"`
	Material bool   `json:"material"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Anchor   string `json:"anchor"`
	Trigger  string `json:"trigger"`
}

type suppressedConcern struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Anchor string `json:"anchor"`
	Reason string `json:"reason"`
}

type repairExpectation struct {
	Tree                 string   `json:"tree"`
	Resolves             []string `json:"resolves"`
	ExpectedReview       string   `json:"expected_review"`
	ExpectedProductState string   `json:"expected_product_state"`
}

type sourceTree map[string][]string

var caseIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func TestCalibrationCorpusIsInternallyConsistent(t *testing.T) {
	index := readJSON[corpusIndex](t, "corpus.json")
	if index.Version != 1 {
		t.Fatalf("corpus version = %d, want 1", index.Version)
	}
	if len(index.Cases) < 6 {
		t.Fatalf("corpus has %d cases, want at least 6 distinct review shapes", len(index.Cases))
	}

	seenCases := make(map[string]bool, len(index.Cases))
	caseDirectories, err := os.ReadDir("cases")
	if err != nil {
		t.Fatalf("read case directories: %v", err)
	}
	coveredShapes := make(map[string]bool)
	for _, id := range index.Cases {
		if !caseIDPattern.MatchString(id) {
			t.Errorf("case id %q does not use lowercase kebab-case", id)
			continue
		}
		if seenCases[id] {
			t.Errorf("case id %q is repeated", id)
			continue
		}
		seenCases[id] = true

		t.Run(id, func(t *testing.T) {
			caseDir := filepath.Join("cases", id)
			definition := readJSON[calibrationCase](t, filepath.Join(caseDir, "case.json"))
			if definition.ID != id {
				t.Errorf("case id = %q, want directory/index id %q", definition.ID, id)
			}
			if strings.TrimSpace(definition.Summary) == "" {
				t.Error("case summary is empty")
			}

			target := readSourceTree(t, filepath.Join(caseDir, "target"))
			head := readSourceTree(t, filepath.Join(caseDir, "head"))
			changed := changedHeadLinesByPath(target, head)
			if len(changed) == 0 {
				t.Fatal("target and head trees are identical")
			}

			validateExpectedOutcome(t, definition)
			validateFindings(t, definition.Findings, head, changed)
			validateSuppressedConcerns(t, definition.SuppressedConcerns, head, changed)
			validateOmissions(t, definition, head, changed)
			validateRepair(t, caseDir, definition, head)

			for _, tag := range definition.Tags {
				coveredShapes["tag:"+tag] = true
			}
			if definition.Coverage == "partial" {
				coveredShapes["partial"] = true
			}
			for _, finding := range definition.Findings {
				if finding.Material {
					coveredShapes["material"] = true
				} else {
					coveredShapes["non-material"] = true
				}
			}
			if len(definition.SuppressedConcerns) > 0 {
				coveredShapes["suppressed"] = true
			}
		})
	}
	for _, entry := range caseDirectories {
		if entry.IsDir() && !seenCases[entry.Name()] {
			t.Errorf("case directory %q is not declared in corpus.json", entry.Name())
		}
	}

	for _, shape := range []string{
		"material",
		"non-material",
		"suppressed",
		"partial",
		"tag:clean",
		"tag:correctness",
		"tag:security",
		"tag:documentation",
		"tag:historical-regression",
	} {
		if !coveredShapes[shape] {
			t.Errorf("corpus does not cover required shape %q", shape)
		}
	}
}

func TestCalibrationGoTreesCompile(t *testing.T) {
	index := readJSON[corpusIndex](t, "corpus.json")
	goCache := t.TempDir()
	for _, id := range index.Cases {
		definition := readJSON[calibrationCase](t, filepath.Join("cases", id, "case.json"))
		trees := []string{"target", "head"}
		if definition.Repair != nil {
			trees = append(trees, definition.Repair.Tree)
		}
		for _, tree := range trees {
			t.Run(id+"/"+tree, func(t *testing.T) {
				root := filepath.Join("cases", id, tree)
				if _, err := os.Stat(filepath.Join(root, "go.mod")); errors.Is(err, os.ErrNotExist) {
					return
				} else if err != nil {
					t.Fatalf("inspect go.mod: %v", err)
				}

				command := exec.Command("go", "test", "./...")
				command.Dir = root
				command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=readonly", "GOCACHE="+goCache)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("compile fixture: %v\n%s", err, output)
				}
			})
		}
	}
}

func validateExpectedOutcome(t *testing.T, definition calibrationCase) {
	t.Helper()

	validReviews := map[string]bool{"approve": true, "request-changes": true, "comment": true}
	if !validReviews[definition.ExpectedReview] {
		t.Errorf("expected_review = %q, want approve, request-changes, or comment", definition.ExpectedReview)
	}
	validStates := map[string]bool{"clean": true, "blocked": true, "partial": true}
	if !validStates[definition.ExpectedProductState] {
		t.Errorf("expected_product_state = %q, want clean, blocked, or partial", definition.ExpectedProductState)
	}
	if definition.Coverage != "complete" && definition.Coverage != "partial" {
		t.Errorf("coverage = %q, want complete or partial", definition.Coverage)
	}

	hasMaterial := false
	for _, finding := range definition.Findings {
		hasMaterial = hasMaterial || finding.Material
	}
	if definition.Coverage == "partial" {
		if definition.ExpectedReview != "comment" || definition.ExpectedProductState != "partial" {
			t.Error("partial coverage must produce a comment review and partial product state")
		}
		return
	}
	if hasMaterial && (definition.ExpectedReview != "request-changes" || definition.ExpectedProductState != "blocked") {
		t.Error("complete review with material findings must request changes and block")
	}
	if !hasMaterial && (definition.ExpectedReview != "approve" || definition.ExpectedProductState != "clean") {
		t.Error("complete review without material findings must approve and be clean")
	}
}

func validateFindings(t *testing.T, findings []expectedFinding, head sourceTree, changed map[string]map[int]bool) {
	t.Helper()

	seen := make(map[string]bool, len(findings))
	for _, finding := range findings {
		if !caseIDPattern.MatchString(finding.ID) {
			t.Errorf("finding id %q does not use lowercase kebab-case", finding.ID)
		}
		if seen[finding.ID] {
			t.Errorf("finding id %q is repeated", finding.ID)
		}
		seen[finding.ID] = true
		if finding.Priority != "P0" && finding.Priority != "P1" && finding.Priority != "P2" && finding.Priority != "P3" {
			t.Errorf("finding %q priority = %q, want P0-P3", finding.ID, finding.Priority)
		}
		validateChangedAnchor(t, "finding "+finding.ID, finding.Path, finding.Line, finding.Anchor, head, changed)
		if strings.TrimSpace(finding.Trigger) == "" {
			t.Errorf("finding %q has no concrete trigger", finding.ID)
		}
	}
}

func validateSuppressedConcerns(t *testing.T, concerns []suppressedConcern, head sourceTree, changed map[string]map[int]bool) {
	t.Helper()

	for _, concern := range concerns {
		validateChangedAnchor(t, "suppressed concern "+concern.ID, concern.Path, concern.Line, concern.Anchor, head, changed)
		if strings.TrimSpace(concern.Reason) == "" {
			t.Errorf("suppressed concern %q has no suppression reason", concern.ID)
		}
	}
}

func validateChangedAnchor(t *testing.T, subject, path string, line int, anchor string, head sourceTree, changed map[string]map[int]bool) {
	t.Helper()

	lines, ok := head[path]
	if !ok {
		t.Errorf("%s path %q does not exist in head tree", subject, path)
		return
	}
	if line < 1 || line > len(lines) {
		t.Errorf("%s line %d is outside %s (1-%d)", subject, line, path, len(lines))
		return
	}
	if !changed[path][line] {
		t.Errorf("%s anchor %s:%d is not on a changed head line", subject, path, line)
	}
	if strings.TrimSpace(anchor) == "" || !strings.Contains(lines[line-1], anchor) {
		t.Errorf("%s anchor %q not found at %s:%d: %q", subject, anchor, path, line, lines[line-1])
	}
}

func validateOmissions(t *testing.T, definition calibrationCase, head sourceTree, changed map[string]map[int]bool) {
	t.Helper()

	if definition.Coverage == "complete" && len(definition.OmittedPaths) != 0 {
		t.Error("complete coverage case declares omitted paths")
	}
	if definition.Coverage == "partial" && len(definition.OmittedPaths) == 0 {
		t.Error("partial coverage case must name at least one omitted path")
	}
	for _, path := range definition.OmittedPaths {
		if _, ok := head[path]; !ok {
			t.Errorf("omitted path %q does not exist in head tree", path)
		}
		if len(changed[path]) == 0 {
			t.Errorf("omitted path %q is not changed", path)
		}
	}
}

func validateRepair(t *testing.T, caseDir string, definition calibrationCase, head sourceTree) {
	t.Helper()

	if definition.Repair == nil {
		return
	}
	if definition.Repair.ExpectedReview != "approve" || definition.Repair.ExpectedProductState != "clean" {
		t.Error("calibrated repair must converge to approve and clean")
	}
	if len(definition.Repair.Resolves) == 0 {
		t.Error("repair does not name any resolved finding")
	}
	findingIDs := make(map[string]bool, len(definition.Findings))
	for _, finding := range definition.Findings {
		findingIDs[finding.ID] = true
	}
	for _, id := range definition.Repair.Resolves {
		if !findingIDs[id] {
			t.Errorf("repair resolves unknown finding %q", id)
		}
	}
	repaired := readSourceTree(t, filepath.Join(caseDir, definition.Repair.Tree))
	if len(changedHeadLinesByPath(head, repaired)) == 0 {
		t.Error("repair tree is identical to defective head tree")
	}
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s: unexpected trailing JSON", path)
	}
	return value
}

func readSourceTree(t *testing.T, root string) sourceTree {
	t.Helper()

	tree := make(sourceTree)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(relative)] = splitLines(string(contents))
		return nil
	})
	if err != nil {
		t.Fatalf("read source tree %s: %v", root, err)
	}
	if len(tree) == 0 {
		t.Fatalf("source tree %s is empty", root)
	}
	return tree
}

func splitLines(contents string) []string {
	contents = strings.TrimSuffix(contents, "\n")
	if contents == "" {
		return nil
	}
	return strings.Split(contents, "\n")
}

func changedHeadLinesByPath(target, head sourceTree) map[string]map[int]bool {
	paths := make([]string, 0, len(target)+len(head))
	seen := make(map[string]bool, len(target)+len(head))
	for path := range target {
		seen[path] = true
		paths = append(paths, path)
	}
	for path := range head {
		if !seen[path] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	changed := make(map[string]map[int]bool)
	for _, path := range paths {
		lines := changedHeadLines(target[path], head[path])
		if len(lines) > 0 || (head[path] == nil && target[path] != nil) {
			changed[path] = lines
		}
	}
	return changed
}

// changedHeadLines marks inserted or replaced lines in the head tree. The
// corpus files are deliberately small, so a direct longest-common-subsequence
// table keeps the validator self-contained and its definition of "changed"
// explicit.
func changedHeadLines(target, head []string) map[int]bool {
	lcs := make([][]int, len(target)+1)
	for index := range lcs {
		lcs[index] = make([]int, len(head)+1)
	}
	for targetIndex := len(target) - 1; targetIndex >= 0; targetIndex-- {
		for headIndex := len(head) - 1; headIndex >= 0; headIndex-- {
			if target[targetIndex] == head[headIndex] {
				lcs[targetIndex][headIndex] = 1 + lcs[targetIndex+1][headIndex+1]
			} else if lcs[targetIndex+1][headIndex] >= lcs[targetIndex][headIndex+1] {
				lcs[targetIndex][headIndex] = lcs[targetIndex+1][headIndex]
			} else {
				lcs[targetIndex][headIndex] = lcs[targetIndex][headIndex+1]
			}
		}
	}

	changed := make(map[int]bool)
	targetIndex, headIndex := 0, 0
	for targetIndex < len(target) && headIndex < len(head) {
		if target[targetIndex] == head[headIndex] {
			targetIndex++
			headIndex++
			continue
		}
		if lcs[targetIndex+1][headIndex] >= lcs[targetIndex][headIndex+1] {
			targetIndex++
		} else {
			changed[headIndex+1] = true
			headIndex++
		}
	}
	for ; headIndex < len(head); headIndex++ {
		changed[headIndex+1] = true
	}
	return changed
}
