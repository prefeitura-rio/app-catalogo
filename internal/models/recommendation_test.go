package models

import (
	"errors"
	"testing"
)

func TestRecommendationRequest_Normalize_Limit(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		// Ausente, zero ou negativo cai no default.
		{0, 10},
		{-5, 10},
		{1, 1},
		{10, 10},
		{50, 50},
		// Acima do máximo do contrato (openapi-v3.json: max 50) limita a 50,
		// em vez de cair no default (DEV-166).
		{51, 50},
		{200, 50},
		{9999, 50},
	}

	for _, tc := range cases {
		req := RecommendationRequest{Limit: tc.limit}
		req.Normalize()
		if req.Limit != tc.want {
			t.Errorf("Limit: input %d → got %d, want %d", tc.limit, req.Limit, tc.want)
		}
	}
}

func TestRecommendationRequest_Normalize_Context(t *testing.T) {
	req := RecommendationRequest{}
	req.Normalize()
	if req.Context != ContextHomepage {
		t.Errorf("Context vazio → got %q, want %q", req.Context, ContextHomepage)
	}

	req = RecommendationRequest{Context: ContextProfile}
	req.Normalize()
	if req.Context != ContextProfile {
		t.Errorf("Context informado → got %q, want %q", req.Context, ContextProfile)
	}
}

func TestParseRecommendationContext(t *testing.T) {
	valid := map[string]RecommendationContext{
		"":             ContextHomepage,
		"homepage":     ContextHomepage,
		"after_search": ContextAfterSearch,
		"profile":      ContextProfile,
	}
	for raw, want := range valid {
		got, err := ParseRecommendationContext(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %q, err=%v; want %q", raw, got, err, want)
		}
	}

	for _, raw := range []string{"invalido", "HOMEPAGE", "Profile", " homepage", "homepage,profile", "after-search"} {
		got, err := ParseRecommendationContext(raw)
		if !errors.Is(err, ErrInvalidRecommendationContext) {
			t.Errorf("%q: err=%v, want ErrInvalidRecommendationContext", raw, err)
		}
		if got != "" {
			t.Errorf("%q: got %q, want vazio", raw, got)
		}
	}

	if got := ErrInvalidRecommendationContext.Error(); got != "context inválido: valores aceitos são homepage, after_search, profile" {
		t.Errorf("mensagem = %q", got)
	}
}
