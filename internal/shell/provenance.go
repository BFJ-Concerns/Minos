package shell

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type ModelPin struct {
	ID            string `toml:"id"`
	Engine        string `toml:"engine"`
	Model         string `toml:"model"`
	Family        string `toml:"family"`
	Effort        string `toml:"effort"`
	FallbackModel string `toml:"fallback-model"`
}

type ModelPins struct {
	Roles struct {
		Lead       ModelPin   `toml:"lead"`
		Specialist ModelPin   `toml:"specialist"`
		Verifiers  []ModelPin `toml:"verifiers"`
		BarJudges  []ModelPin `toml:"bar-judges"`
	} `toml:"roles"`
}

type rolePin struct {
	role string
	pin  ModelPin
}

type resolvedModel struct {
	ID            string `json:"id"`
	ResolvedModel string `json:"resolved_model"`
}

type ProvenanceRecord struct {
	Role            string `json:"role"`
	ID              string `json:"id"`
	RequestedModel  string `json:"requested_model"`
	RequestedEngine string `json:"requested_engine"`
	ResolvedModel   string `json:"resolved_model"`
	Family          string `json:"family"`
	FamilySource    string `json:"family_source"`
	Split           string `json:"split,omitempty"`
	Degraded        bool   `json:"degraded,omitempty"`
}

func ProvenanceCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pump19 provenance lead-pin|combine|assemble")
	}
	switch args[0] {
	case "lead-pin":
		if len(args) != 2 {
			return fmt.Errorf("usage: pump19 provenance lead-pin PINS_TOML")
		}
		pins, err := loadModelPins(args[1])
		if err != nil {
			return err
		}
		if _, err := validatedRolePins(pins); err != nil {
			return err
		}
		fmt.Fprintln(stdout, pins.Roles.Lead.Model)
		return nil
	case "assemble":
		fs := flag.NewFlagSet("provenance assemble", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		pinsPath := fs.String("pins", "", "model pins TOML")
		resolvedPath := fs.String("resolved", "", "resolved-model JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *pinsPath == "" || *resolvedPath == "" || fs.NArg() != 0 {
			return fmt.Errorf("usage: pump19 provenance assemble --pins FILE --resolved FILE")
		}
		records, err := assembleProvenance(*pinsPath, *resolvedPath)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(records)
	case "combine":
		fs := flag.NewFlagSet("provenance combine", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		leadPath := fs.String("lead", "", "resolved lead-model JSON")
		workersPath := fs.String("workers", "", "resolved worker-model JSON")
		outputPath := fs.String("output", "", "combined resolved-model JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *leadPath == "" || *workersPath == "" || *outputPath == "" || fs.NArg() != 0 {
			return fmt.Errorf("usage: pump19 provenance combine --lead FILE --workers FILE --output FILE")
		}
		return combineResolvedModels(*leadPath, *workersPath, *outputPath)
	default:
		return fmt.Errorf("unknown provenance action %q", args[0])
	}
}

func combineResolvedModels(leadPath, workersPath, outputPath string) error {
	read := func(path string) ([]resolvedModel, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var records []resolvedModel
		if err := json.Unmarshal(data, &records); err != nil {
			return nil, fmt.Errorf("decode resolved models from %s: %w", path, err)
		}
		for _, record := range records {
			if record.ID == "" || record.ResolvedModel == "" {
				return nil, fmt.Errorf("resolved model record in %s requires id and resolved_model", path)
			}
		}
		return records, nil
	}
	lead, err := read(leadPath)
	if err != nil {
		return err
	}
	if len(lead) != 1 {
		return fmt.Errorf("resolved lead input has %d records, want exactly one", len(lead))
	}
	workers, err := read(workersPath)
	if err != nil {
		return err
	}
	combined := append(append([]resolvedModel{}, lead...), workers...)
	data, err := json.Marshal(combined)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".resolved-models-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, outputPath)
}

func assembleProvenance(pinsPath, resolvedPath string) ([]ProvenanceRecord, error) {
	pins, err := loadModelPins(pinsPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	var resolved []resolvedModel
	if err := json.Unmarshal(data, &resolved); err != nil {
		return nil, fmt.Errorf("decode resolved models: %w", err)
	}
	byID := make(map[string]string, len(resolved))
	for _, item := range resolved {
		if item.ID == "" || item.ResolvedModel == "" {
			return nil, fmt.Errorf("resolved model record requires id and resolved_model")
		}
		if _, duplicate := byID[item.ID]; duplicate {
			return nil, fmt.Errorf("duplicate resolved model record for %s", item.ID)
		}
		byID[item.ID] = item.ResolvedModel
	}
	roles, err := validatedRolePins(pins)
	if err != nil {
		return nil, err
	}
	records := make([]ProvenanceRecord, 0, len(roles))
	seenPins := make(map[string]bool, len(roles))
	for _, item := range roles {
		pin := item.pin
		seenPins[pin.ID] = true
		served, ok := byID[pin.ID]
		if !ok {
			return nil, fmt.Errorf("no resolved model record for %s", pin.ID)
		}
		degraded := served == "model-unknown"
		if !degraded && served != pin.Model {
			return nil, fmt.Errorf("model pin mismatch for %s: requested %s, resolved %s", pin.ID, pin.Model, served)
		}
		record := ProvenanceRecord{
			Role:            item.role,
			ID:              pin.ID,
			RequestedModel:  pin.Model,
			RequestedEngine: pin.Engine,
			ResolvedModel:   served,
			Family:          pin.Family,
			FamilySource:    "operator-asserted",
			Degraded:        degraded,
		}
		if item.role != "lead" {
			record.Split = "same_family"
			if pin.Family != pins.Roles.Lead.Family {
				record.Split = "cross_family"
			}
		}
		records = append(records, record)
	}
	for id := range byID {
		if !seenPins[id] {
			return nil, fmt.Errorf("resolved model record %s has no pin", id)
		}
	}
	// Arrays preserve role order; sorting equal-role records by id keeps output
	// stable when a deployment has several verifiers or bar judges.
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Role != records[j].Role {
			return roleOrder(records[i].Role) < roleOrder(records[j].Role)
		}
		return records[i].ID < records[j].ID
	})
	return records, nil
}

func loadModelPins(path string) (ModelPins, error) {
	var pins ModelPins
	if _, err := toml.DecodeFile(path, &pins); err != nil {
		return ModelPins{}, err
	}
	return pins, nil
}

func validatedRolePins(pins ModelPins) ([]rolePin, error) {
	if len(pins.Roles.Verifiers) == 0 || len(pins.Roles.BarJudges) == 0 {
		return nil, fmt.Errorf("pins require at least one verifier and bar judge")
	}
	roles := []rolePin{{role: "lead", pin: pins.Roles.Lead}, {role: "specialist", pin: pins.Roles.Specialist}}
	for _, pin := range pins.Roles.Verifiers {
		roles = append(roles, rolePin{role: "verifier", pin: pin})
	}
	for _, pin := range pins.Roles.BarJudges {
		roles = append(roles, rolePin{role: "bar_judge", pin: pin})
	}
	seen := make(map[string]bool, len(roles))
	for _, item := range roles {
		pin := item.pin
		if pin.ID == "" || pin.Engine == "" || pin.Model == "" || pin.Family == "" {
			return nil, fmt.Errorf("%s pin requires id, engine, model, and family", item.role)
		}
		if strings.HasPrefix(pin.Model, "REPLACE_WITH_") {
			return nil, fmt.Errorf("%s model pin %s is not operator-approved", item.role, pin.ID)
		}
		if seen[pin.ID] {
			return nil, fmt.Errorf("duplicate pin id %s", pin.ID)
		}
		seen[pin.ID] = true
	}
	return roles, nil
}

func roleOrder(role string) int {
	switch role {
	case "lead":
		return 0
	case "specialist":
		return 1
	case "verifier":
		return 2
	case "bar_judge":
		return 3
	default:
		return 4
	}
}
