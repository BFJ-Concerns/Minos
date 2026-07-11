package shell

import (
	"fmt"
	"io"
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
		Lead ModelPin `toml:"lead"`
	} `toml:"roles"`
}

func ProvenanceCommand(args []string, stdout io.Writer) error {
	if len(args) != 2 || args[0] != "lead-pin" {
		return fmt.Errorf("usage: pump19 provenance lead-pin PINS_TOML")
	}
	pins, err := loadModelPins(args[1])
	if err != nil {
		return err
	}
	if err := validateLeadPin(pins.Roles.Lead); err != nil {
		return err
	}
	fmt.Fprintln(stdout, pins.Roles.Lead.Model)
	return nil
}

func loadModelPins(path string) (ModelPins, error) {
	var pins ModelPins
	metadata, err := toml.DecodeFile(path, &pins)
	if err != nil {
		return ModelPins{}, err
	}
	undecoded := metadata.Undecoded()
	if len(undecoded) == 0 {
		return pins, nil
	}
	keys := make([]string, 0, len(undecoded))
	workerRoles := false
	for _, key := range undecoded {
		name := key.String()
		keys = append(keys, name)
		if strings.HasPrefix(name, "roles.specialist") || strings.HasPrefix(name, "roles.verifiers") || strings.HasPrefix(name, "roles.bar-judges") {
			workerRoles = true
		}
	}
	sort.Strings(keys)
	if workerRoles {
		return ModelPins{}, fmt.Errorf("worker role pins are not supported; the pins file governs roles.lead only: %s", strings.Join(keys, ", "))
	}
	return ModelPins{}, fmt.Errorf("%s: unknown TOML keys: %s", path, strings.Join(keys, ", "))
}

func validateLeadPin(pin ModelPin) error {
	if pin.ID == "" || pin.Engine == "" || pin.Model == "" || pin.Family == "" {
		return fmt.Errorf("lead pin requires id, engine, model, and family")
	}
	if strings.HasPrefix(pin.Model, "REPLACE_WITH_") {
		return fmt.Errorf("lead model pin %s is not operator-approved", pin.ID)
	}
	return nil
}
