package shell

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func VerifyHexHMACSHA256(body []byte, secret, supplied string) bool {
	supplied = strings.TrimSpace(supplied)
	if strings.HasPrefix(supplied, "sha256=") {
		supplied = strings.TrimPrefix(supplied, "sha256=")
	}
	got, err := hex.DecodeString(supplied)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
