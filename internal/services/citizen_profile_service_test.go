package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestHashCPFUsesKeyedHMAC(t *testing.T) {
	const secret = "test-cpf-hmac-key-with-at-least-32-bytes"
	const cpf = "12345678901"
	service := NewCitizenProfileService(nil, nil, secret, 0)

	wantMessageAuthenticationCode := hmac.New(sha256.New, []byte(secret))
	_, _ = wantMessageAuthenticationCode.Write([]byte(cpf))
	want := fmt.Sprintf("%x", wantMessageAuthenticationCode.Sum(nil))
	if got := service.HashCPF(cpf); got != want {
		t.Fatalf("HashCPF() = %q, want HMAC %q", got, want)
	}

	unkeyedDigest := sha256.Sum256([]byte(cpf + secret))
	if got := service.HashCPF(cpf); got == fmt.Sprintf("%x", unkeyedDigest) {
		t.Fatal("HashCPF() still uses an unkeyed concatenated digest")
	}
}
