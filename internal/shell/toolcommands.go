package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func MarkerCommand(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: pump19 marker format KEY=VALUE... | parse LINE")
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
		line, err := FormatMarker(values)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, line)
		return nil
	case "parse":
		if len(args) != 2 {
			return fmt.Errorf("usage: pump19 marker parse LINE")
		}
		values, err := ParseMarker(args[1])
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
		return fmt.Errorf("usage: pump19 handle mint [EXISTING...]")
	}
	existing := make(map[string]bool, len(args)-1)
	for _, handle := range args[1:] {
		if !ValidFindingHandle(handle) {
			return fmt.Errorf("invalid existing finding handle %q", handle)
		}
		existing[handle] = true
	}
	handle, err := MintFindingHandle(existing)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, handle)
	return nil
}
