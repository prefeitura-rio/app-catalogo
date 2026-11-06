package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
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

// cartaStubSource simula a fonte da Carta: tem Sync (delta) e FullSync.
type cartaStubSource struct {
	syncCalls atomic.Int32
	fullCalls atomic.Int32
}

func (s *cartaStubSource) Name() string                { return "salesforce" }
func (s *cartaStubSource) Source() models.ItemSource   { return models.SourceSalesForce }
func (s *cartaStubSource) SyncInterval() time.Duration { return time.Hour }
func (s *cartaStubSource) Sync(context.Context) error {
	s.syncCalls.Add(1)
	return nil
}
func (s *cartaStubSource) FullSync(context.Context) error {
	s.fullCalls.Add(1)
	return nil
}

func triggerRouter(src *cartaStubSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	m := datasource.NewManager()
	m.Register(src)
	h := NewAdminHandler(nil, m)
	r := gin.New()
	r.POST("/api/v1/admin/sync/trigger", h.TriggerSync)
	return r
}

func waitCalls(c *atomic.Int32) {
	deadline := time.Now().Add(2 * time.Second)
	for c.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
}

func TestTriggerSync_Modes(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantMode string
		wantFull int32
		wantSync int32
	}{
		{"padrão é delta", "", "delta", 0, 1},
		{"delta explícito", "?mode=delta", "delta", 0, 1},
		{"full na fonte", "?source=salesforce&mode=full", "full", 1, 0},
		{"full em todas", "?mode=full", "full", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &cartaStubSource{}
			r := triggerRouter(src)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/sync/trigger"+tc.query, nil))

			if w.Code != http.StatusAccepted {
				t.Fatalf("esperado 202, obtido %d (%s)", w.Code, w.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("corpo não é JSON: %v", err)
			}
			if body["status"] != "triggered" || body["mode"] != tc.wantMode {
				t.Fatalf("corpo inesperado: %v", body)
			}
			if tc.wantFull > 0 {
				waitCalls(&src.fullCalls)
			} else {
				waitCalls(&src.syncCalls)
			}
			if src.fullCalls.Load() != tc.wantFull || src.syncCalls.Load() != tc.wantSync {
				t.Fatalf("full=%d sync=%d; esperado full=%d sync=%d",
					src.fullCalls.Load(), src.syncCalls.Load(), tc.wantFull, tc.wantSync)
			}
		})
	}
}

func TestTriggerSync_InvalidModeIs400AndTriggersNothing(t *testing.T) {
	for _, mode := range []string{"FULL", "tudo", "1", "full%20"} {
		t.Run(mode, func(t *testing.T) {
			src := &cartaStubSource{}
			r := triggerRouter(src)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/sync/trigger?source=salesforce&mode="+mode, nil))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("esperado 400, obtido %d (%s)", w.Code, w.Body.String())
			}
			time.Sleep(50 * time.Millisecond)
			if src.fullCalls.Load() != 0 || src.syncCalls.Load() != 0 {
				t.Fatalf("mode inválido não deveria disparar sync: full=%d sync=%d", src.fullCalls.Load(), src.syncCalls.Load())
			}
		})
	}
}

func TestTriggerSync_UnregisteredSourceIs400AndTriggersNothing(t *testing.T) {
	// typesense só existe no worker; a API não a registra e não pode dispará-la.
	for _, source := range []string{"typesense", "inexistente", "SALESFORCE"} {
		t.Run(source, func(t *testing.T) {
			src := &cartaStubSource{}
			r := triggerRouter(src)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/sync/trigger?source="+source, nil))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("esperado 400, obtido %d (%s)", w.Code, w.Body.String())
			}
			var body struct {
				Error            string   `json:"error"`
				AvailableSources []string `json:"available_sources"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("corpo não é JSON: %v", err)
			}
			if !strings.Contains(body.Error, source) || !strings.Contains(body.Error, "salesforce") {
				t.Fatalf("erro deveria citar a fonte pedida e as disponíveis: %q", body.Error)
			}
			if len(body.AvailableSources) != 1 || body.AvailableSources[0] != "salesforce" {
				t.Fatalf("available_sources inesperado: %v", body.AvailableSources)
			}
			time.Sleep(50 * time.Millisecond)
			if src.fullCalls.Load() != 0 || src.syncCalls.Load() != 0 {
				t.Fatalf("fonte não registrada não deveria disparar sync: full=%d sync=%d", src.fullCalls.Load(), src.syncCalls.Load())
			}
		})
	}
}

func TestTriggerSync_NoSourcesRegisteredListsNone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAdminHandler(nil, datasource.NewManager())
	r := gin.New()
	r.POST("/api/v1/admin/sync/trigger", h.TriggerSync)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/sync/trigger?source=salesforce", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, obtido %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		AvailableSources []string `json:"available_sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("corpo não é JSON: %v", err)
	}
	if body.AvailableSources == nil || len(body.AvailableSources) != 0 {
		t.Fatalf("available_sources deveria ser lista vazia, obtido %s", w.Body.String())
	}
}
