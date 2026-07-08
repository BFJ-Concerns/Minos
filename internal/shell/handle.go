package shell

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

var handlePattern = regexp.MustCompile(`^F-[0-9A-HJKMNP-TV-Z]{4}$`)

const crockfordHandleAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func ValidFindingHandle(handle string) bool {
	return handlePattern.MatchString(handle)
}

func MintFindingHandle(existing map[string]bool) (string, error) {
	var buf [4]byte
	for attempt := 0; attempt < 128; attempt++ {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		handle := []byte("F-0000")
		for i, value := range buf {
			handle[i+2] = crockfordHandleAlphabet[int(value)%len(crockfordHandleAlphabet)]
		}
		candidate := string(handle)
		if !existing[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("finding handle collision budget exhausted")
}
