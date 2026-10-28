package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const adminTestItemID = "00000000-0000-4000-8000-000000000001"

type stubCatalogItemReader struct {
	item *models.CatalogItem
	err  error
}

func (s stubCatalogItemReader) GetByID(_ context.Context, _ uuid.UUID) (*models.CatalogItem, error) {
	return s.item, s.err
}

func (s stubCatalogItemReader) GetPublicByID(_ context.Context, _ uuid.UUID) (*models.CatalogItem, error) {
	return s.item, s.err
}

func catalogItemRouter(reader catalogItemReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &AdminHandler{items: reader}
	r := gin.New()
	r.GET("/api/v1/catalog/:id", h.GetCatalogItem)
	r.GET("/api/public/catalog/:id", h.GetPublicCatalogItem)
	return r
}

func TestCatalogItemHandlers_ErrorMapping(t *testing.T) {
	// Erro interno com detalhe que nunca pode chegar ao cliente.
	internalErr := errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")

	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{"item ausente", pgx.ErrNoRows, http.StatusNotFound, "item não encontrado"},
		{"item ausente embrulhado", fmt.Errorf("scan: %w", pgx.ErrNoRows), http.StatusNotFound, "item não encontrado"},
		{"banco fora", internalErr, http.StatusInternalServerError, "falha ao buscar item"},
		{"contexto expirado", context.DeadlineExceeded, http.StatusInternalServerError, "falha ao buscar item"},
	}

	for _, path := range []string{"/api/v1/catalog/", "/api/public/catalog/"} {
		for _, tc := range cases {
			t.Run(path+tc.name, func(t *testing.T) {
				r := catalogItemRouter(stubCatalogItemReader{err: tc.err})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+adminTestItemID, nil))

				if w.Code != tc.wantCode {
					t.Fatalf("esperado %d, obtido %d (%s)", tc.wantCode, w.Code, w.Body.String())
				}
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("corpo não é JSON: %v", err)
				}
				if body["error"] != tc.wantMsg {
					t.Fatalf("mensagem: esperado %q, obtido %q", tc.wantMsg, body["error"])
				}
				if strings.Contains(w.Body.String(), "10.0.0.5") || strings.Contains(w.Body.String(), "connection refused") {
					t.Fatalf("resposta vaza o erro interno: %s", w.Body.String())
				}
			})
		}
	}
}

func TestCatalogItemHandlers_Found(t *testing.T) {
	id := uuid.MustParse(adminTestItemID)
	item := &models.CatalogItem{ID: id, Title: "Curso de teste", Status: models.ItemStatus("active")}
	r := catalogItemRouter(stubCatalogItemReader{item: item})

	for _, path := range []string{"/api/v1/catalog/", "/api/public/catalog/"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+adminTestItemID, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: esperado 200, obtido %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "Curso de teste") {
			t.Fatalf("%s: corpo sem o item: %s", path, w.Body.String())
		}
	}
}

func TestCatalogItemHandlers_InvalidID(t *testing.T) {
	r := catalogItemRouter(stubCatalogItemReader{err: errors.New("não deveria consultar")})
	for _, path := range []string{"/api/v1/catalog/nao-uuid", "/api/public/catalog/nao-uuid"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: esperado 400, obtido %d", path, w.Code)
		}
	}
}
