package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// DEV-575: faixa_salarial só aceita os valores do contrato; um valor inventado
// responde 400 sem chegar ao serviço, nos dois endpoints de busca.
func TestFaixaSalarialInvalida_Responde400SemChamarServico(t *testing.T) {
	for _, route := range typesRoutes()[:2] {
		for _, q := range []string{"faixa_salarial=VALOR-INEXISTENTE-XYZ", "types=job&faixa_salarial=ate-3sm"} {
			t.Run(route.name+"/"+q, func(t *testing.T) {
				code, body, fake := serveTypes(t, route, q)
				if code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s, want 400", code, body)
				}
				var resp map[string]string
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("corpo não é JSON: %v (%s)", err, body)
				}
				if !strings.Contains(resp["error"], "faixa_salarial") || strings.Contains(resp["error"], "XYZ") {
					t.Fatalf("mensagem inesperada: %q", resp["error"])
				}
				if n := fake.count(); n != 0 {
					t.Fatalf("serviço chamado %d vez(es) com faixa_salarial inválida", n)
				}
			})
		}
	}
}

func TestParseSearchQuery_FiltrosDeCursoEVaga(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET",
		"/api/public/search?turno=noturno&gratuito=true&regime_contratacao=CLT&modelo_trabalho=hibrido&pcd=false&faixa_salarial=+2-4SM+", nil)

	req, err := parseSearchQuery(c)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	f := req.Filters
	if f.Turno != "noturno" || f.RegimeContratacao != "CLT" || f.ModeloTrabalho != "hibrido" {
		t.Errorf("filtros de texto: %+v", f)
	}
	if f.FaixaSalarial != "2-4sm" {
		t.Errorf("faixa_salarial = %q, want 2-4sm", f.FaixaSalarial)
	}
	if f.PCD == nil || *f.PCD {
		t.Errorf("pcd = %v, want false", f.PCD)
	}
	if f.Gratuito == nil || !*f.Gratuito {
		t.Errorf("gratuito = %v, want true", f.Gratuito)
	}
}
