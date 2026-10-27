package config

import "testing"

func TestKeycloakIssuer(t *testing.T) {
	cases := []struct {
		url, realm, want string
	}{
		{"https://auth.example.test", "teste", "https://auth.example.test/realms/teste"},
		{"https://auth.example.test/ ", " teste", "https://auth.example.test/realms/teste"},
		{"", "teste", ""},
		{"https://auth.example.test", "", ""},
	}
	for _, c := range cases {
		if got := keycloakIssuer(c.url, c.realm); got != c.want {
			t.Errorf("keycloakIssuer(%q, %q) = %q, esperado %q", c.url, c.realm, got, c.want)
		}
	}
}

func TestDefaultJWKSURL(t *testing.T) {
	if got := defaultJWKSURL("https://auth.example.test/realms/teste"); got != "https://auth.example.test/realms/teste/protocol/openid-connect/certs" {
		t.Errorf("defaultJWKSURL = %q", got)
	}
	if got := defaultJWKSURL(""); got != "" {
		t.Errorf("defaultJWKSURL vazio = %q", got)
	}
}
