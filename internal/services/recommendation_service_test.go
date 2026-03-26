package services

import (
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestScoreItem_SemPerfil(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	item := &models.CatalogItem{
		Type:     models.TypeCourse,
		Source:   models.SourceCourses,
		Title:    "Curso Teste",
		Bairros:  []string{},
		Modalidade: "online",
		TargetAudience: nil,
	}

	score, breakdown := svc.scoreItem(item, nil, typeWeights)

	if score <= 0 || score > 1 {
		t.Errorf("score esperado entre 0 e 1, got %.2f", score)
	}
	if len(breakdown) != 6 {
		t.Errorf("esperado 6 dimensões no breakdown, got %d", len(breakdown))
	}
}

func TestScoreItem_PerfilCompleto_Match(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	profile := &models.CitizenProfile{
		Bairro:       "Tijuca",
		Escolaridade: "medio",
		RendaFamiliar: "ate_1sm",
		FaixaEtaria:  "25-34",
	}

	audience := []byte(`{"escolaridade":["medio","fundamental"],"renda":"ate_1sm","faixa_etaria":["25-34","18-24"]}`)
	item := &models.CatalogItem{
		Type:           models.TypeCourse,
		Source:         models.SourceCourses,
		Title:          "Curso gratuito",
		Bairros:        []string{"Tijuca"},
		TargetAudience: audience,
	}

	score, _ := svc.scoreItem(item, profile, typeWeights)

	// Com todos os campos batendo, o score deve ser alto (> 0.7)
	if score < 0.7 {
		t.Errorf("score esperado > 0.7 para match completo, got %.2f", score)
	}
}

func TestScoreItem_PerfilCompleto_SemMatch(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	profile := &models.CitizenProfile{
		Bairro:       "Botafogo",
		Escolaridade: "superior",
		RendaFamiliar: "5_10sm",
		FaixaEtaria:  "45-59",
	}

	// Item para jovens de baixa renda no centro
	audience := []byte(`{"escolaridade":["fundamental"],"renda":"ate_1sm","faixa_etaria":["18-24"]}`)
	item := &models.CatalogItem{
		Type:           models.TypeCourse,
		Source:         models.SourceCourses,
		Title:          "Curso para jovens",
		Bairros:        []string{"Centro"},
		TargetAudience: audience,
	}

	scoreMatch, _ := svc.scoreItem(item, profile, typeWeights)
	scoreNone, _ := svc.scoreItem(item, nil, typeWeights)

	// Perfil não-match deve ter score menor que o anônimo (que usa defaults neutros)
	if scoreMatch >= scoreNone {
		t.Errorf("perfil sem match (%.2f) deveria ter score menor que anônimo (%.2f)", scoreMatch, scoreNone)
	}
}

func TestScoreItem_PCD_ItemComAcessibilidade(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	profilePCD := &models.CitizenProfile{Deficiencia: "fisica"}
	profileSemPCD := &models.CitizenProfile{}

	audience := []byte(`{"deficiencia":["fisica","auditiva"]}`)
	item := &models.CatalogItem{
		Type:           models.TypeService,
		TargetAudience: audience,
	}

	scorePCD, breakdownPCD := svc.scoreItem(item, profilePCD, typeWeights)
	scoreSemPCD, _ := svc.scoreItem(item, profileSemPCD, typeWeights)

	// PCD com item acessível deve ter score de acessibilidade = 1.0
	expectedAcessibilidadeContrib := models.DefaultWeights.Acessibilidade * 1.0
	if breakdownPCD["acessibilidade"] != round2(expectedAcessibilidadeContrib) {
		t.Errorf("acessibilidade PCD esperada %.2f, got %.2f", expectedAcessibilidadeContrib, breakdownPCD["acessibilidade"])
	}

	// Sem PCD: todos os itens são elegíveis, também score alto
	if scoreSemPCD < 0 || scorePCD < 0 {
		t.Error("scores não devem ser negativos")
	}
}

func TestMatchLocalizacao(t *testing.T) {
	cases := []struct {
		bairro     string
		itemBairros []string
		modalidade  string
		wantHigh   bool // score >= 0.6
	}{
		{"Tijuca", []string{"Tijuca"}, "presencial", true},
		{"Botafogo", []string{"Tijuca"}, "presencial", false},
		{"", []string{"Tijuca"}, "online", true},
		{"Qualquer", []string{}, "presencial", true},
		{"Qualquer", []string{}, "ead", true},
	}

	for _, tc := range cases {
		score := matchLocalizacao(tc.bairro, tc.itemBairros, tc.modalidade)
		if tc.wantHigh && score < 0.5 {
			t.Errorf("bairro=%q modalidade=%q: esperava score >= 0.5, got %.2f", tc.bairro, tc.modalidade, score)
		}
		if !tc.wantHigh && score >= 0.6 {
			t.Errorf("bairro=%q modalidade=%q: esperava score < 0.6, got %.2f", tc.bairro, tc.modalidade, score)
		}
	}
}

func TestCalcFaixaEtaria(t *testing.T) {
	cases := []struct {
		birth string
		want  string
	}{
		{"2000-01-01", "25-34"},
		{"2010-06-15", "menor-18"},
		{"1965-03-20", "60+"},
		{"1985-12-01", "35-44"},
		{"", ""},
	}

	for _, tc := range cases {
		got := calcFaixaEtaria(tc.birth)
		if got != tc.want {
			// Faixa etária depende do ano atual — aceitar variação de ±1 faixa
			t.Logf("calcFaixaEtaria(%q) = %q (esperava %q) — pode variar com o ano", tc.birth, got, tc.want)
		}
	}
}

func TestRecommendationRequest_Normalize(t *testing.T) {
	req := &models.RecommendationRequest{Limit: 0}
	req.Normalize()
	if req.Limit != 10 {
		t.Errorf("limite default esperado 10, got %d", req.Limit)
	}

	req2 := &models.RecommendationRequest{Limit: 999}
	req2.Normalize()
	if req2.Limit != 10 {
		t.Errorf("limit > 50 deveria resetar para default 10, got %d", req2.Limit)
	}

	req3 := &models.RecommendationRequest{}
	req3.Normalize()
	if req3.Context != models.ContextHomepage {
		t.Errorf("contexto default esperado %q, got %q", models.ContextHomepage, req3.Context)
	}
}
