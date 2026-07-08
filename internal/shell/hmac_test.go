package shell

import "testing"

func TestVerifyHexHMACSHA256(t *testing.T) {
	body := []byte(`{"ok":true}`)
	secret := "secret"
	signature := "f6b4a2841c93f8bf2fb8f2c13d8fb0b6c8e8019f09ee405d248daa8385fad638"
	if !VerifyHexHMACSHA256(body, secret, signature) {
		t.Fatal("expected signature to verify")
	}
	if VerifyHexHMACSHA256(body, secret, "deadbeef") {
		t.Fatal("unexpectedly verified bad signature")
	}
}
