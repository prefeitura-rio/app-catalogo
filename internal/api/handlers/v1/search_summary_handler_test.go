package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type searchSummaryProviderStub struct {
	response *models.SearchSummaryResponse
	err      error
}

func (stub *searchSummaryProviderStub) Generate(
	context.Context,
	*models.SearchSummaryRequest,
) (*models.SearchSummaryResponse, error) {
	return stub.response, stub.err
}

func TestSearchSummaryHandlerReturnsGroundedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	providerStub := &searchSummaryProviderStub{response: &models.SearchSummaryResponse{
		Query: "IPTU", Generated: true,
		Segments: []models.SearchSummarySegment{{Text: "Consulte o IPTU.", Slug: "iptu", URL: "/servicos/categoria/fazenda/iptu"}},
	}}
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/public/search-summary",
		strings.NewReader(`{"query":"IPTU","catalog_revision":"catalog-v2:1:eligible","candidate_ids":["018f2f2f-4f68-7a68-8000-000000000001"]}`),
	)
	ginContext.Request.Header.Set("Content-Type", "application/json")

	NewSearchSummaryHandler(providerStub).Generate(ginContext)

	if responseRecorder.Code != http.StatusOK || !strings.Contains(responseRecorder.Body.String(), `"generated":true`) {
		t.Fatalf("summary response status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
}

func TestSearchSummaryHandlerRejectsMalformedCandidateID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/public/search-summary",
		strings.NewReader(`{"query":"IPTU","catalog_revision":"catalog-v2:1:eligible","candidate_ids":["not-a-uuid"]}`),
	)
	ginContext.Request.Header.Set("Content-Type", "application/json")

	NewSearchSummaryHandler(&searchSummaryProviderStub{}).Generate(ginContext)

	if responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("summary malformed candidate status = %d", responseRecorder.Code)
	}
}

func TestSearchSummaryHandlerRequiresJSONMediaType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	responseRecorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(responseRecorder)
	ginContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/public/search-summary",
		strings.NewReader(`{"query":"IPTU"}`),
	)

	NewSearchSummaryHandler(&searchSummaryProviderStub{}).Generate(ginContext)

	if responseRecorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("summary media type status = %d", responseRecorder.Code)
	}
}
