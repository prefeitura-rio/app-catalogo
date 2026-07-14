package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type catalogHandlerRepositoryStub struct {
	serviceResolution   *repository.PublicServiceResolution
	serviceError        error
	serviceRelations    *repository.PublicServiceRelationsSnapshot
	categorySnapshot    *repository.PublicServiceCategorySnapshot
	subcategorySnapshot *repository.PublicServiceSubcategorySnapshot
	serviceListSnapshot *repository.PublicServiceListSnapshot
	suggestions         []models.PublicServiceSuggestion
}

func (stub *catalogHandlerRepositoryStub) GetPublicServiceBySlug(context.Context, string) (*repository.PublicServiceResolution, error) {
	return stub.serviceResolution, stub.serviceError
}

func (stub *catalogHandlerRepositoryStub) GetPublicServiceRelations(context.Context, string, int) (*repository.PublicServiceRelationsSnapshot, error) {
	return stub.serviceRelations, stub.serviceError
}

func (stub *catalogHandlerRepositoryStub) ListPublicServiceCategories(context.Context) (*repository.PublicServiceCategorySnapshot, error) {
	return stub.categorySnapshot, nil
}

func (stub *catalogHandlerRepositoryStub) ListPublicServiceSubcategories(context.Context, string) (*repository.PublicServiceSubcategorySnapshot, error) {
	return stub.subcategorySnapshot, nil
}

func (stub *catalogHandlerRepositoryStub) ListPublicServices(context.Context, string, string, int, int) (*repository.PublicServiceListSnapshot, error) {
	return stub.serviceListSnapshot, nil
}

func (stub *catalogHandlerRepositoryStub) SuggestPublicServices(context.Context, string, int) ([]models.PublicServiceSuggestion, error) {
	return stub.suggestions, nil
}

func TestCatalogHandlerReturnsPublicSuggestions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repositoryStub := &catalogHandlerRepositoryStub{suggestions: []models.PublicServiceSuggestion{{
		Title: "Emissão de segunda via do IPTU", Slug: "iptu", URL: "/servicos/categoria/fazenda/iptu",
	}}}
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/public/suggest", strings.NewReader(`{"query":"IPTU"}`),
	)
	ginContext.Request.Header.Set("Content-Type", "application/json")

	NewCatalogHandler(repositoryStub).SuggestPublicServices(ginContext)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("suggestion status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var suggestionResponse models.PublicSuggestionResponse
	if decodeError := json.Unmarshal(responseRecorder.Body.Bytes(), &suggestionResponse); decodeError != nil {
		t.Fatalf("decode suggestion response: %v", decodeError)
	}
	if len(suggestionResponse.Suggestions) != 1 || suggestionResponse.Suggestions[0].Slug != "iptu" {
		t.Fatalf("suggestion response = %#v", suggestionResponse)
	}
}

func TestCatalogHandlerRejectsUnknownSuggestionFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/public/suggest", strings.NewReader(`{"query":"IPTU","citizen_identifier":"forbidden"}`),
	)
	ginContext.Request.Header.Set("Content-Type", "application/json")

	NewCatalogHandler(&catalogHandlerRepositoryStub{}).SuggestPublicServices(ginContext)

	if responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown suggestion field status = %d", responseRecorder.Code)
	}
}

func TestCatalogHandlerReturnsUnifiedServiceRelations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	relation := models.PublicServiceRelation{
		ID:   uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000010"),
		Slug: "certidao-negativa", Title: "Certidão negativa", Reason: "próximo passo",
	}
	repositoryStub := &catalogHandlerRepositoryStub{serviceRelations: &repository.PublicServiceRelationsSnapshot{
		CatalogRevision: "catalog-v2:10:eligible", CanonicalSlug: "iptu", Theme: "jornada tributária",
		Recommendations: []models.PublicServiceRelation{relation}, Journey: []models.PublicServiceRelation{relation},
		Cluster: []models.PublicServiceRelation{},
	}}
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/api/public/services/iptu/relations", nil)
	ginContext.Params = gin.Params{{Key: "slug", Value: "iptu"}}

	NewCatalogHandler(repositoryStub).GetPublicServiceRelations(ginContext)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("relations status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var relationsResponse models.PublicServiceRelationsResponse
	if decodeError := json.Unmarshal(responseRecorder.Body.Bytes(), &relationsResponse); decodeError != nil {
		t.Fatalf("decode relations response: %v", decodeError)
	}
	if relationsResponse.Journey.ServiceSlug != "iptu" || len(relationsResponse.Recommendations) != 1 ||
		relationsResponse.Cluster.Services == nil {
		t.Fatalf("relations response = %#v", relationsResponse)
	}
}

func TestCatalogHandlerRedirectsHistoricalServiceSlug(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repositoryStub := &catalogHandlerRepositoryStub{serviceResolution: &repository.PublicServiceResolution{
		Item: &models.CatalogItem{}, CanonicalSlug: "canonical-service",
	}}
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/api/public/services/old-service", nil)
	ginContext.Params = gin.Params{{Key: "slug", Value: "old-service"}}

	NewCatalogHandler(repositoryStub).GetPublicServiceBySlug(ginContext)

	if responseRecorder.Code != http.StatusPermanentRedirect {
		t.Fatalf("historical service status = %d, want %d", responseRecorder.Code, http.StatusPermanentRedirect)
	}
	if location := responseRecorder.Header().Get("Location"); location != "/api/public/services/canonical-service" {
		t.Fatalf("historical service location = %q", location)
	}
}

func TestCatalogHandlerReturnsAllowlistedServiceDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repositoryStub := &catalogHandlerRepositoryStub{serviceResolution: &repository.PublicServiceResolution{
		CanonicalSlug: "canonical-service",
		Item: &models.CatalogItem{
			ID:         uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000001"),
			ExternalID: "source-service",
			Type:       models.TypeService,
			Title:      "Canonical service",
			SourceData: json.RawMessage(`{"slug":"canonical-service","private_upstream_field":"secret"}`),
		},
	}}
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/api/public/services/canonical-service", nil)
	ginContext.Params = gin.Params{{Key: "slug", Value: "canonical-service"}}

	NewCatalogHandler(repositoryStub).GetPublicServiceBySlug(ginContext)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("service detail status = %d body=%s", responseRecorder.Code, responseRecorder.Body.String())
	}
	if responseBody := responseRecorder.Body.String(); !containsAll(responseBody, "canonical-service", "Canonical service") || containsAll(responseBody, "private_upstream_field") {
		t.Fatalf("service detail body = %s", responseBody)
	}
}

func TestCatalogHandlerRejectsInvalidBrowsePagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/api/public/services?page=0", nil)

	NewCatalogHandler(&catalogHandlerRepositoryStub{}).ListPublicServices(ginContext)

	if responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid service pagination status = %d, want %d", responseRecorder.Code, http.StatusBadRequest)
	}
}

func containsAll(candidate string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(candidate, fragment) {
			return false
		}
	}
	return true
}
