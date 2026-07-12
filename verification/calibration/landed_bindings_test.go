package calibration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"bfj/minos/internal/product"
)

type coverageInventory struct {
	BaseCommit string                  `json:"base_commit"`
	HeadCommit string                  `json:"head_commit"`
	Files      []coverageInventoryFile `json:"files"`
}

type coverageInventoryFile struct {
	Path    string         `json:"path"`
	OldPath string         `json:"old_path,omitempty"`
	Blob    string         `json:"blob"`
	Hunks   []coverageHunk `json:"hunks"`
}

type coverageHunk struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Account  string `json:"account,omitempty"`
}

type coverageRecord struct {
	SchemaVersion int                  `json:"schema_version"`
	BaseCommit    string               `json:"base_commit"`
	HeadCommit    string               `json:"head_commit"`
	Files         []coverageRecordFile `json:"files"`
	References    []any                `json:"references"`
}

type coverageRecordFile struct {
	Path    string         `json:"path"`
	OldPath string         `json:"old_path,omitempty"`
	Blob    string         `json:"blob"`
	Account string         `json:"account"`
	Hunks   []coverageHunk `json:"hunks"`
}

type coverageResult struct {
	Status    string `json:"status"`
	Omissions []struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	} `json:"omissions"`
}

func TestCorpusOutcomesBindToClosedProductVocabulary(t *testing.T) {
	index := readJSON[corpusIndex](t, "corpus.json")
	for _, id := range index.Cases {
		t.Run(id, func(t *testing.T) {
			definition := readJSON[calibrationCase](t, filepath.Join("cases", id, "case.json"))

			result := product.ReviewConverged()
			if definition.Coverage == "partial" {
				result = product.ReviewIncomplete()
			} else if hasMaterialFinding(definition.Findings) {
				result = product.ReviewHasMaterialFindings()
			}
			verdict, err := product.VerdictFor(result)
			if err != nil {
				t.Fatalf("map calibrated result: %v", err)
			}
			if verdict.Name() != definition.ExpectedReview {
				t.Errorf("product verdict = %q, corpus expects %q", verdict.Name(), definition.ExpectedReview)
			}

			state := calibratedProductState(t, definition.ExpectedProductState)
			if !state.Valid() || state.Name() != definition.ExpectedProductState {
				t.Errorf("product state = %q valid=%t, corpus expects %q", state.Name(), state.Valid(), definition.ExpectedProductState)
			}
		})
	}
}

func TestCoverageInstrumentAccountsForMaterialisedCorpus(t *testing.T) {
	index := readJSON[corpusIndex](t, "corpus.json")
	for _, id := range index.Cases {
		t.Run(id, func(t *testing.T) {
			definition := readJSON[calibrationCase](t, filepath.Join("cases", id, "case.json"))
			repository := filepath.Join(t.TempDir(), "subject")
			_, targetSHA, headSHA := materialiseCase(t, id, repository)

			inventory := coverageInventoryFor(t, repository, targetSHA, headSHA)
			assertInventoryPathsMatchCorpus(t, id, inventory)
			record := coverageRecordFromInventory(inventory, definition.OmittedPaths)
			recordPath := filepath.Join(t.TempDir(), "inspection.json")
			writeJSON(t, recordPath, record)

			expectedCode := 0
			if definition.Coverage == "partial" {
				expectedCode = 1
			}
			result := accountCoverage(t, repository, targetSHA, headSHA, recordPath, expectedCode)
			if result.Status != definition.Coverage {
				t.Errorf("coverage status = %q, corpus expects %q", result.Status, definition.Coverage)
			}
			if definition.Coverage == "complete" && len(result.Omissions) != 0 {
				t.Errorf("complete corpus case has omissions: %#v", result.Omissions)
			}
			for _, omittedPath := range definition.OmittedPaths {
				if !hasOmission(result, "file_omitted", omittedPath) {
					t.Errorf("omitted path %q was not reported as file_omitted: %#v", omittedPath, result.Omissions)
				}
			}
		})
	}
}

func hasMaterialFinding(findings []expectedFinding) bool {
	for _, finding := range findings {
		if finding.Material {
			return true
		}
	}
	return false
}

func calibratedProductState(t *testing.T, name string) product.State {
	t.Helper()

	switch name {
	case "clean":
		return product.Clean()
	case "blocked":
		return product.Blocked()
	case "partial":
		return product.Partial()
	default:
		t.Fatalf("corpus expects product state %q without an estate binding", name)
		return product.State{}
	}
}

func coverageInventoryFor(t *testing.T, repository, base, head string) coverageInventory {
	t.Helper()

	command := exec.Command(coverageScript(t), "--repo", repository, "--base", base, "--head", head, "--inventory")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inventory coverage: %v\n%s", err, output)
	}
	var inventory coverageInventory
	if err := json.Unmarshal(output, &inventory); err != nil {
		t.Fatalf("decode coverage inventory: %v\n%s", err, output)
	}
	return inventory
}

func coverageRecordFromInventory(inventory coverageInventory, omittedPaths []string) coverageRecord {
	omitted := make(map[string]bool, len(omittedPaths))
	for _, path := range omittedPaths {
		omitted[path] = true
	}
	record := coverageRecord{
		SchemaVersion: 1,
		BaseCommit:    inventory.BaseCommit,
		HeadCommit:    inventory.HeadCommit,
		Files:         make([]coverageRecordFile, 0, len(inventory.Files)),
		References:    []any{},
	}
	for _, file := range inventory.Files {
		account := "read"
		if omitted[file.Path] {
			account = "omitted"
		}
		hunks := make([]coverageHunk, len(file.Hunks))
		for index, hunk := range file.Hunks {
			hunk.Account = account
			hunks[index] = hunk
		}
		record.Files = append(record.Files, coverageRecordFile{
			Path: file.Path, OldPath: file.OldPath, Blob: file.Blob,
			Account: account, Hunks: hunks,
		})
	}
	return record
}

func assertInventoryPathsMatchCorpus(t *testing.T, id string, inventory coverageInventory) {
	t.Helper()

	caseDir := filepath.Join("cases", id)
	target := readSourceTree(t, filepath.Join(caseDir, "target"))
	head := readSourceTree(t, filepath.Join(caseDir, "head"))
	changed := changedHeadLinesByPath(target, head)
	want := make([]string, 0, len(changed))
	for path := range changed {
		want = append(want, path)
	}
	sort.Strings(want)
	got := make([]string, 0, len(inventory.Files))
	for _, file := range inventory.Files {
		got = append(got, file.Path)
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("coverage inventory paths = %q, corpus changed paths = %q", got, want)
	}
}

func accountCoverage(t *testing.T, repository, base, head, recordPath string, expectedCode int) coverageResult {
	t.Helper()

	command := exec.Command(coverageScript(t), "--repo", repository, "--base", base, "--head", head, "--record", recordPath)
	output, err := command.CombinedOutput()
	if command.ProcessState.ExitCode() != expectedCode {
		t.Fatalf("account coverage exit = %d, want %d: %v\n%s", command.ProcessState.ExitCode(), expectedCode, err, output)
	}
	var result coverageResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode coverage result: %v\n%s", err, output)
	}
	return result
}

func hasOmission(result coverageResult, kind, path string) bool {
	for _, omission := range result.Omissions {
		if omission.Kind == kind && omission.Path == path {
			return true
		}
	}
	return false
}

func coverageScript(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "scripts", "review", "account-coverage")
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()

	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
