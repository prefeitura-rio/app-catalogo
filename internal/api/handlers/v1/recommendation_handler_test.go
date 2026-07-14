package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type recommendationProviderStub struct {
	recommendResponse          *models.RecommendationResponse
	recommendError             error
	recommendAnonymousResponse *models.RecommendationResponse
	recommendAnonymousError    error
	recommendCalls             int
	recommendAnonymousCalls    int
	recommendRequest           *models.RecommendationRequest
	recommendAnonymousRequest  *models.RecommendationRequest
}

func (provider *recommendationProviderStub) Recommend(
	_ context.Context,
	_ *models.CitizenProfile,
	recommendationRequest *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {
	provider.recommendCalls++
	provider.recommendRequest = recommendationRequest
	return provider.recommendResponse, provider.recommendError
}

func (provider *recommendationProviderStub) RecommendAnonymous(
	_ context.Context,
	recommendationRequest *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {
	provider.recommendAnonymousCalls++
	provider.recommendAnonymousRequest = recommendationRequest
	return provider.recommendAnonymousResponse, provider.recommendAnonymousError
}

type citizenProfileProviderStub struct {
	profile *models.CitizenProfile
	err     error
	calls   int
}

func (provider *citizenProfileProviderStub) GetOrSync(
	context.Context,
	string,
) (*models.CitizenProfile, error) {
	provider.calls++
	return provider.profile, provider.err
}

func TestRecommendationHandlersRejectInvalidParametersBeforeDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	invalidParameters := []struct {
		name          string
		query         string
		expectedError string
	}{
		{
			name:          "context",
			query:         "context=unsupported",
			expectedError: "contexto de recomendação inválido",
		},
		{
			name:          "item type",
			query:         "types=service,unsupported",
			expectedError: "tipo de item de recomendação inválido",
		},
		{
			name:          "limit with trailing text",
			query:         "limit=10items",
			expectedError: "limite de recomendações inválido",
		},
		{
			name:          "limit with leading text",
			query:         "limit=items10",
			expectedError: "limite de recomendações inválido",
		},
		{
			name:          "empty limit",
			query:         "limit=",
			expectedError: "limite de recomendações inválido",
		},
		{
			name:          "limit below range",
			query:         "limit=0",
			expectedError: "limite de recomendações inválido",
		},
		{
			name:          "limit above range",
			query:         "limit=51",
			expectedError: "limite de recomendações inválido",
		},
	}
	endpoints := []struct {
		name          string
		path          string
		authenticated bool
	}{
		{name: "anonymous", path: "/api/public/recommendations"},
		{name: "authenticated", path: "/api/v1/recommendations", authenticated: true},
	}

	for _, endpoint := range endpoints {
		for _, invalidParameter := range invalidParameters {
			t.Run(endpoint.name+"/"+invalidParameter.name, func(t *testing.T) {
				recommendationProvider := &recommendationProviderStub{}
				citizenProvider := &citizenProfileProviderStub{profile: &models.CitizenProfile{}}
				handler := NewRecommendationHandler(recommendationProvider, citizenProvider)
				router := gin.New()
				router.Use(middleware.RequestID())
				if endpoint.authenticated {
					router.Use(authenticatedRecommendationTestContext())
					router.GET(endpoint.path, handler.Authenticated)
				} else {
					router.GET(endpoint.path, handler.Anonymous)
				}

				request := httptest.NewRequest(
					http.MethodGet,
					endpoint.path+"?"+invalidParameter.query,
					nil,
				)
				responseRecorder := httptest.NewRecorder()
				router.ServeHTTP(responseRecorder, request)

				assertRecommendationErrorResponse(
					t,
					responseRecorder,
					http.StatusBadRequest,
					invalidParameter.expectedError,
				)
				if recommendationProvider.recommendCalls != 0 ||
					recommendationProvider.recommendAnonymousCalls != 0 || citizenProvider.calls != 0 {
					t.Fatalf(
						"invalid request reached dependencies: recommend=%d anonymous=%d citizen=%d",
						recommendationProvider.recommendCalls,
						recommendationProvider.recommendAnonymousCalls,
						citizenProvider.calls,
					)
				}
			})
		}
	}
}

func TestAnonymousRecommendationCanonicalizesTypesAndDefaultsOmittedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recommendationProvider := &recommendationProviderStub{
		recommendAnonymousResponse: &models.RecommendationResponse{
			Items:   []*models.RankedItem{},
			Context: models.ContextHomepage,
		},
	}
	handler := NewRecommendationHandler(recommendationProvider, &citizenProfileProviderStub{})
	router := gin.New()
	router.Use(middleware.RequestID())
	router.GET("/api/public/recommendations", handler.Anonymous)

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/public/recommendations?types=job&types=service,job&types=course",
		nil,
	)
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("canonical recommendation status = %d: %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	parsedRequest := recommendationProvider.recommendAnonymousRequest
	if parsedRequest == nil {
		t.Fatal("recommendation provider did not receive the canonical request")
	}
	expectedTypes := []models.ItemType{models.TypeCourse, models.TypeJob, models.TypeService}
	if !reflect.DeepEqual(parsedRequest.Types, expectedTypes) ||
		parsedRequest.Limit != models.DefaultRecommendationLimit {
		t.Fatalf("canonical recommendation request = %+v, want types=%v default limit", parsedRequest, expectedTypes)
	}
}

func TestAnonymousRecommendationPreservesValidExplicitLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recommendationProvider := &recommendationProviderStub{
		recommendAnonymousResponse: &models.RecommendationResponse{
			Items:   []*models.RankedItem{},
			Context: models.ContextHomepage,
		},
	}
	handler := NewRecommendationHandler(recommendationProvider, &citizenProfileProviderStub{})
	router := gin.New()
	router.GET("/api/public/recommendations", handler.Anonymous)

	request := httptest.NewRequest(http.MethodGet, "/api/public/recommendations?limit=7", nil)
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("explicit-limit recommendation status = %d: %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	parsedRequest := recommendationProvider.recommendAnonymousRequest
	if parsedRequest == nil || parsedRequest.Limit != 7 {
		t.Fatalf("recommendation request = %+v, want explicit limit 7", parsedRequest)
	}
}

func TestAuthenticatedRecommendationUnauthorizedResponseIncludesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recommendationProvider := &recommendationProviderStub{}
	citizenProvider := &citizenProfileProviderStub{}
	handler := NewRecommendationHandler(recommendationProvider, citizenProvider)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.GET("/api/v1/recommendations", handler.Authenticated)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)

	assertRecommendationErrorResponse(
		t,
		responseRecorder,
		http.StatusUnauthorized,
		"autenticação necessária",
	)
	if recommendationProvider.recommendCalls != 0 ||
		recommendationProvider.recommendAnonymousCalls != 0 || citizenProvider.calls != 0 {
		t.Fatal("unauthorized request reached recommendation dependencies")
	}
}

func TestRecommendationHandlersInternalErrorsIncludeRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name          string
		path          string
		authenticated bool
	}{
		{name: "anonymous", path: "/api/public/recommendations"},
		{name: "authenticated", path: "/api/v1/recommendations", authenticated: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recommendationProvider := &recommendationProviderStub{
				recommendError:          errors.New("recommendation unavailable"),
				recommendAnonymousError: errors.New("recommendation unavailable"),
			}
			citizenProvider := &citizenProfileProviderStub{profile: &models.CitizenProfile{}}
			handler := NewRecommendationHandler(recommendationProvider, citizenProvider)
			router := gin.New()
			router.Use(middleware.RequestID())
			if testCase.authenticated {
				router.Use(authenticatedRecommendationTestContext())
				router.GET(testCase.path, handler.Authenticated)
			} else {
				router.GET(testCase.path, handler.Anonymous)
			}

			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			responseRecorder := httptest.NewRecorder()
			router.ServeHTTP(responseRecorder, request)

			assertRecommendationErrorResponse(
				t,
				responseRecorder,
				http.StatusInternalServerError,
				"falha nas recomendações",
			)
			if testCase.authenticated {
				if recommendationProvider.recommendCalls != 1 ||
					recommendationProvider.recommendAnonymousCalls != 0 || citizenProvider.calls != 1 {
					t.Fatalf("authenticated dependency calls are inconsistent")
				}
			} else if recommendationProvider.recommendAnonymousCalls != 1 ||
				recommendationProvider.recommendCalls != 0 || citizenProvider.calls != 0 {
				t.Fatalf("anonymous dependency calls are inconsistent")
			}
		})
	}
}

func authenticatedRecommendationTestContext() gin.HandlerFunc {
	return func(context *gin.Context) {
		context.Set(middleware.UserCPFKey, "12345678901")
		context.Next()
	}
}

func assertRecommendationErrorResponse(
	t *testing.T,
	responseRecorder *httptest.ResponseRecorder,
	expectedStatus int,
	expectedError string,
) {
	t.Helper()
	if responseRecorder.Code != expectedStatus {
		t.Fatalf(
			"recommendation error status = %d, want %d: %s",
			responseRecorder.Code,
			expectedStatus,
			responseRecorder.Body.String(),
		)
	}
	var errorResponse models.RecommendationErrorResponse
	if decodeError := json.Unmarshal(responseRecorder.Body.Bytes(), &errorResponse); decodeError != nil {
		t.Fatalf("decode recommendation error response: %v", decodeError)
	}
	requestID := responseRecorder.Header().Get(middleware.RequestIDHeader)
	if errorResponse.Error != expectedError || requestID == "" || errorResponse.LogID != requestID {
		t.Fatalf("recommendation error response = %+v, X-Request-ID = %q", errorResponse, requestID)
	}
}
