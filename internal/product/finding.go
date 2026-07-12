package product

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

var findingIDPattern = regexp.MustCompile(`^F-[0-9A-HJKMNP-TV-Z]{4}$`)

const crockfordFindingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// FindingID is hidden lineage used only to associate a finding across reviews.
// It deliberately contains no verdict, clearance, or merge-authority field.
type FindingID struct{ value string }

func ParseFindingID(value string) (FindingID, error) {
	if !findingIDPattern.MatchString(value) {
		return FindingID{}, fmt.Errorf("invalid finding identity %q", value)
	}
	return FindingID{value: value}, nil
}

// MustParseFindingID is intended for static, package-controlled fixtures.
func MustParseFindingID(value string) FindingID {
	id, err := ParseFindingID(value)
	if err != nil {
		panic(err)
	}
	return id
}

func (id FindingID) String() string { return id.value }
func (id FindingID) Valid() bool    { return findingIDPattern.MatchString(id.value) }

// MintFindingID creates a valid identity outside the supplied existing set.
func MintFindingID(existing map[FindingID]struct{}) (FindingID, error) {
	var random [4]byte
	for range 128 {
		if _, err := rand.Read(random[:]); err != nil {
			return FindingID{}, fmt.Errorf("read finding identity randomness: %w", err)
		}
		value := []byte("F-0000")
		for i, octet := range random {
			value[i+2] = crockfordFindingAlphabet[int(octet)%len(crockfordFindingAlphabet)]
		}
		candidate := FindingID{value: string(value)}
		if _, duplicate := existing[candidate]; !duplicate {
			return candidate, nil
		}
	}
	return FindingID{}, fmt.Errorf("finding identity collision budget exhausted")
}

// FindingDedupe separates retained lineage from findings which need a new ID.
type FindingDedupe struct {
	Recurring []FindingID
	New       int
}

// DeduplicateFindings classifies candidate lineage against identities already
// published for the PR. An empty candidate is new; a named candidate must refer
// to exactly one existing finding and may occur only once.
func DeduplicateFindings(existing, candidates []FindingID) (FindingDedupe, error) {
	known := make(map[FindingID]struct{}, len(existing))
	for _, id := range existing {
		if !id.Valid() {
			return FindingDedupe{}, fmt.Errorf("existing finding has invalid identity %q", id.String())
		}
		if _, duplicate := known[id]; duplicate {
			return FindingDedupe{}, fmt.Errorf("finding identity %s appears more than once", id)
		}
		known[id] = struct{}{}
	}

	result := FindingDedupe{Recurring: []FindingID{}}
	seen := make(map[FindingID]struct{}, len(candidates))
	for _, id := range candidates {
		if id == (FindingID{}) {
			result.New++
			continue
		}
		if !id.Valid() {
			return FindingDedupe{}, fmt.Errorf("candidate has invalid finding identity %q", id.String())
		}
		if _, duplicate := seen[id]; duplicate {
			return FindingDedupe{}, fmt.Errorf("candidate finding identity %s is repeated", id)
		}
		seen[id] = struct{}{}
		if _, exists := known[id]; !exists {
			return FindingDedupe{}, fmt.Errorf("candidate finding identity %s has no existing finding", id)
		}
		result.Recurring = append(result.Recurring, id)
	}
	return result, nil
}
