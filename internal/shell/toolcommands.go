package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"bfj/minos/internal/product"
)

func MarkerCommand(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: minos marker format KEY=VALUE... | parse LINE")
	}
	switch args[0] {
	case "format":
		values := make(map[string]string, len(args)-1)
		for _, token := range args[1:] {
			key, value, ok := strings.Cut(token, "=")
			if !ok || key == "" || value == "" {
				return fmt.Errorf("invalid marker token %q", token)
			}
			if _, duplicate := values[key]; duplicate {
				return fmt.Errorf("duplicate marker key %q", key)
			}
			values[key] = value
		}
		line, err := product.FormatRecord(values)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, line)
		return nil
	case "parse":
		if len(args) != 2 {
			return fmt.Errorf("usage: minos marker parse LINE")
		}
		values, err := product.ParseRecord(args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(values)
	default:
		return fmt.Errorf("unknown marker action %q", args[0])
	}
}

func HandleCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "mint" {
		return fmt.Errorf("usage: minos handle mint [EXISTING...]")
	}
	existing := make(map[product.FindingID]struct{}, len(args)-1)
	for _, handle := range args[1:] {
		id, err := product.ParseFindingID(handle)
		if err != nil {
			return fmt.Errorf("invalid existing finding handle %q", handle)
		}
		existing[id] = struct{}{}
	}
	handle, err := product.MintFindingID(existing)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, handle.String())
	return nil
}
