package forge

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// Marker is one marker's form on the pull request: a reaction by its
// content name (eyes, +1) or a label by its name. Exactly one is set.
type Marker struct {
	Reaction string `toml:"reaction" json:"reaction,omitempty"`
	Label    string `toml:"label" json:"label,omitempty"`
}

// reactionName is the shape of a forge reaction's content name.
var reactionName = regexp.MustCompile(`^[A-Za-z0-9_+-]+$`)

// Validate reports why a marker cannot be written, or nil.
func (m Marker) Validate() error {
	switch {
	case (m.Reaction == "") == (m.Label == ""):
		return errors.New("must set exactly one of reaction or label")
	case m.Reaction != "" && !reactionName.MatchString(m.Reaction):
		return errors.New("reaction must be a reaction name such as eyes or +1")
	case m.Label != "" && (strings.TrimSpace(m.Label) != m.Label || strings.IndexFunc(m.Label, unicode.IsControl) >= 0):
		return errors.New("label must be a label name without surrounding whitespace or control characters")
	}
	return nil
}
