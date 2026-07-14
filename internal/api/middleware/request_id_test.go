package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestRequestIDPreservesCanonicalUUID(t *testing.T) {
	t.Parallel()

	expectedRequestID := uuid.NewString()
	responseRecorder := executeRequestIDMiddleware(expectedRequestID)
	if actualRequestID := responseRecorder.Header().Get(RequestIDHeader); actualRequestID != expectedRequestID {
		t.Fatalf("request ID = %q, want %q", actualRequestID, expectedRequestID)
	}
}

func TestRequestIDReplacesInvalidCallerValue(t *testing.T) {
	t.Parallel()

	responseRecorder := executeRequestIDMiddleware("untrusted-correlation-value")
	actualRequestID := responseRecorder.Header().Get(RequestIDHeader)
	if _, parseError := uuid.Parse(actualRequestID); parseError != nil {
		t.Fatalf("generated request ID = %q, want UUID", actualRequestID)
	}
	if actualRequestID == "untrusted-correlation-value" {
		t.Fatalf("invalid caller request ID was preserved")
	}
}

func TestRequestIDRetainsCanonicalLegacyDistributedIDAsUpstreamCorrelation(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(context *gin.Context) {
		context.String(http.StatusOK, context.GetString(UpstreamRequestIDKey))
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "1800000000000000001")
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)
	if responseRecorder.Body.String() != "1800000000000000001" {
		t.Fatalf("upstream request ID = %q", responseRecorder.Body.String())
	}
	if _, parseError := uuid.Parse(responseRecorder.Header().Get(RequestIDHeader)); parseError != nil {
		t.Fatalf("internal request ID is not a UUID: %v", parseError)
	}
}

func TestRequestIDRetainsCanonicalDistributedIdentifierAsUpstreamCorrelation(t *testing.T) {
	t.Parallel()

	const expectedIdentifier = "340282366920938463463374607431768211455"
	responseRecorder := executeRequestIDMiddleware(expectedIdentifier)
	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d", responseRecorder.Code)
	}

	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(context *gin.Context) {
		context.String(http.StatusOK, context.GetString(UpstreamRequestIDKey))
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, expectedIdentifier)
	upstreamRecorder := httptest.NewRecorder()
	router.ServeHTTP(upstreamRecorder, request)
	if upstreamRecorder.Body.String() != expectedIdentifier {
		t.Fatalf("upstream request ID = %q", upstreamRecorder.Body.String())
	}
}

func TestCanonicalDistributedLogIDRejectsInvalidOrOutOfRangeValues(t *testing.T) {
	t.Parallel()

	for _, identifier := range []string{
		"",
		"0",
		"01",
		"-1",
		"1.0",
		"340282366920938463463374607431768211456",
		"9999999999999999999999999999999999999999",
	} {
		if canonicalDistributedLogID(identifier) {
			t.Fatalf("accepted invalid distributed log ID %q", identifier)
		}
	}
}

func TestSearchIDPreservesOnlyCanonicalUUID(t *testing.T) {
	t.Parallel()

	expectedSearchID := uuid.NewString()
	for _, testCase := range []struct {
		name             string
		suppliedSearchID string
		preserved        bool
	}{
		{name: "canonical", suppliedSearchID: expectedSearchID, preserved: true},
		{name: "uppercase", suppliedSearchID: strings.ToUpper(expectedSearchID)},
		{name: "invalid", suppliedSearchID: "search-one"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := gin.New()
			router.Use(SearchID())
			router.GET("/", func(context *gin.Context) {
				context.String(http.StatusOK, context.GetString(SearchIDKey))
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(SearchIDHeader, testCase.suppliedSearchID)
			responseRecorder := httptest.NewRecorder()
			router.ServeHTTP(responseRecorder, request)
			actualSearchID := responseRecorder.Header().Get(SearchIDHeader)
			if _, parseError := uuid.Parse(actualSearchID); parseError != nil {
				t.Fatalf("search ID = %q, want UUID", actualSearchID)
			}
			if (actualSearchID == testCase.suppliedSearchID) != testCase.preserved {
				t.Fatalf("search ID preservation = %t, want %t", actualSearchID == testCase.suppliedSearchID, testCase.preserved)
			}
		})
	}
}

func executeRequestIDMiddleware(requestID string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(context *gin.Context) {
		context.JSON(http.StatusOK, gin.H{"request_id": context.GetString("request_id")})
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, requestID)
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)
	return responseRecorder
}
