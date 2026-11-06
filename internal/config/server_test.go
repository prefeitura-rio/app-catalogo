package config

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultTrustedProxies(t *testing.T) {
	got := parseCSV(defaultTrustedProxies)
	// Sidecar do Istio (127.0.0.6), gateway/pods e proxies do load balancer do Google.
	for _, want := range []string{"127.0.0.0/8", "10.0.0.0/8", "35.191.0.0/16", "130.211.0.0/22"} {
		if !slices.Contains(got, want) {
			t.Errorf("padrão de TRUSTED_PROXIES sem %s: %q", want, got)
		}
	}
	// Confiar em qualquer origem devolve ao cliente o controle do IP.
	for _, forbidden := range []string{"0.0.0.0/0", "::/0"} {
		if slices.Contains(got, forbidden) {
			t.Errorf("padrão de TRUSTED_PROXIES não pode conter %s", forbidden)
		}
	}
	s := ServerSettings{Host: "0.0.0.0", Port: 8080, TrustedProxies: got}
	if err := s.Validate(); err != nil {
		t.Fatalf("padrão de TRUSTED_PROXIES inválido: %v", err)
	}
}

func TestServerSettingsValidateTrustedProxies(t *testing.T) {
	valid := [][]string{
		nil,
		{"127.0.0.1"},
		{"8.233.204.175/32", "35.191.0.0/16", "::1", "fc00::/7"},
	}
	for _, proxies := range valid {
		s := ServerSettings{Host: "0.0.0.0", Port: 8080, TrustedProxies: proxies}
		if err := s.Validate(); err != nil {
			t.Errorf("TRUSTED_PROXIES=%q: esperado válido, obtido %v", proxies, err)
		}
	}
	invalid := [][]string{
		{"nao-e-ip"},
		{"10.0.0.0/33"},
		{"127.0.0.1", "300.1.1.1"},
		{"35.191.0.0/"},
	}
	for _, proxies := range invalid {
		s := ServerSettings{Host: "0.0.0.0", Port: 8080, TrustedProxies: proxies}
		if err := s.Validate(); err == nil {
			t.Errorf("TRUSTED_PROXIES=%q: esperado erro", proxies)
		}
	}
}

func TestParseCSVTrustedProxies(t *testing.T) {
	got := parseCSV(" 127.0.0.0/8 , 8.233.204.175/32,,127.0.0.0/8 ")
	want := []string{"127.0.0.0/8", "8.233.204.175/32"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseCSV = %q, esperado %q", got, want)
	}
	if got := parseCSV(","); got != nil {
		t.Fatalf("parseCSV(\",\") = %q, esperado vazio", got)
	}
}

func TestParseRateLimitEnabled(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", true},
		{"  ", true},
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{" False ", false},
		{"0", false},
	}
	for _, tc := range cases {
		got, err := parseRateLimitEnabled(tc.raw)
		if err != nil {
			t.Errorf("RATE_LIMIT_ENABLED=%q: erro inesperado %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("RATE_LIMIT_ENABLED=%q: esperado %v, obtido %v", tc.raw, tc.want, got)
		}
	}
	// Um erro de digitação não pode desligar o limite em silêncio.
	for _, raw := range []string{"nao", "off", "disabled", "flase"} {
		if _, err := parseRateLimitEnabled(raw); err == nil {
			t.Errorf("RATE_LIMIT_ENABLED=%q: esperado erro", raw)
		}
	}
}

func TestServerSettingsValidateMetricsPort(t *testing.T) {
	cases := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{name: "porta própria", port: 9092},
		{name: "0 desativa", port: 0},
		{name: "igual à porta pública", port: 8080, wantErr: true},
		{name: "negativa", port: -1, wantErr: true},
		{name: "acima de 65535", port: 70000, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := ServerSettings{Host: "0.0.0.0", Port: 8080, MetricsPort: tc.port}
			err := s.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("esperado erro de validação")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestLoadMetricsPort(t *testing.T) {
	t.Setenv("CPF_HASH_SALT", strings.Repeat("s", 64))

	t.Setenv("METRICS_PORT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.MetricsPort != 9092 {
		t.Fatalf("METRICS_PORT padrão: esperado 9092, obtido %d", cfg.Server.MetricsPort)
	}

	t.Setenv("METRICS_PORT", "0")
	if cfg, err = Load(); err != nil {
		t.Fatalf("METRICS_PORT=0: Load: %v", err)
	}
	if cfg.Server.MetricsPort != 0 {
		t.Fatalf("METRICS_PORT=0: esperado 0, obtido %d", cfg.Server.MetricsPort)
	}

	t.Setenv("METRICS_PORT", "8080")
	if _, err := Load(); err == nil {
		t.Fatal("METRICS_PORT igual a SERVER_PORT deve falhar o startup")
	}
}

func TestLoadRateLimitEnabled(t *testing.T) {
	t.Setenv("CPF_HASH_SALT", strings.Repeat("s", 64))

	cases := []struct {
		name         string
		value        string
		wantDisabled bool
		wantErr      bool
	}{
		{name: "ausente mantém ligado", value: "", wantDisabled: false},
		{name: "true mantém ligado", value: "true", wantDisabled: false},
		{name: "false desliga", value: "false", wantDisabled: true},
		{name: "inválido falha o startup", value: "desligado", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RATE_LIMIT_ENABLED", tc.value)
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("esperado erro de configuração")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.RateLimitDisabled != tc.wantDisabled {
				t.Fatalf("RateLimitDisabled esperado %v, obtido %v", tc.wantDisabled, cfg.Server.RateLimitDisabled)
			}
		})
	}
}
