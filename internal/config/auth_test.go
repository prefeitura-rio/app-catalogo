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

func TestParseAuthorizedParties(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"superapp", []string{"superapp"}},
		{" superapp , portal ,superapp,", []string{"superapp", "portal"}},
		{",", nil},
		{"  ", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := parseAuthorizedParties(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("parseAuthorizedParties(%q) = %q, esperado %q", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseAuthorizedParties(%q) = %q, esperado %q", c.raw, got, c.want)
			}
		}
	}
	if got := parseAuthorizedParties(defaultJWTAuthorizedParties); len(got) != 1 || got[0] != "superapp" {
		t.Fatalf("padrão de AUTH_JWT_AUTHORIZED_PARTIES = %q, esperado [superapp]", got)
	}
}
