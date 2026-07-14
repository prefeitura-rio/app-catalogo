package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type jwtClaimsVerifierStub struct {
	claims *VerifiedJWTClaims
	error  error
}

func (verifierStub jwtClaimsVerifierStub) Verify(context.Context, string) (*VerifiedJWTClaims, error) {
	return verifierStub.claims, verifierStub.error
}

func TestUserContextTrustsOnlyCryptographicallyVerifiedProxyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name              string
		headerName        string
		username          string
		verificationError error
		expectedStatus    int
		authenticated     bool
	}{
		{name: "verified proxy token with canonical CPF", headerName: userTokenHeader, username: "123.456.789-01", expectedStatus: http.StatusOK, authenticated: true},
		{name: "ordinary authorization header is ignored", headerName: "Authorization", username: "123.456.789-01", expectedStatus: http.StatusOK},
		{name: "verified token rejects path-like identity", headerName: userTokenHeader, username: "../admin", expectedStatus: http.StatusUnauthorized},
		{name: "forged proxy token is rejected", headerName: userTokenHeader, username: "123.456.789-01", verificationError: errors.New("invalid signature"), expectedStatus: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := gin.New()
			userContextMiddleware, middlewareError := NewUserContextMiddleware(jwtClaimsVerifierStub{
				claims: &jwtClaims{PreferredUsername: testCase.username},
				error:  testCase.verificationError,
			}, "superapp")
			if middlewareError != nil {
				t.Fatalf("create user context middleware: %v", middlewareError)
			}
			router.Use(RequestID())
			router.Use(userContextMiddleware)
			router.GET("/", func(context *gin.Context) {
				context.JSON(http.StatusOK, gin.H{"authenticated": IsAuthenticated(context)})
			})

			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(testCase.headerName, "signed-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != testCase.expectedStatus {
				t.Fatalf("status = %d, want %d", response.Code, testCase.expectedStatus)
			}
			if testCase.expectedStatus != http.StatusOK {
				return
			}
			wantBody := `{"authenticated":false}`
			if testCase.authenticated {
				wantBody = `{"authenticated":true}`
			}
			if response.Body.String() != wantBody {
				t.Fatalf("body = %s, want %s", response.Body.String(), wantBody)
			}
		})
	}
}

func TestRequireAdminDistinguishesMissingAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequireAdmin())
	router.GET("/", func(context *gin.Context) { context.Status(http.StatusNoContent) })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestUserContextReadsRolesOnlyFromConfiguredClient(t *testing.T) {
	t.Parallel()

	claims := &jwtClaims{PreferredUsername: "12345678901"}
	claims.ResourceAccess = map[string]struct {
		Roles []string `json:"roles"`
	}{
		"untrusted-client": {Roles: []string{"admin"}},
		"superapp":         {Roles: []string{"citizen"}},
	}
	contextMiddleware, middlewareError := NewUserContextMiddleware(
		jwtClaimsVerifierStub{claims: claims},
		"superapp",
	)
	if middlewareError != nil {
		t.Fatalf("create user context middleware: %v", middlewareError)
	}
	router := gin.New()
	router.Use(contextMiddleware)
	router.GET("/", func(requestContext *gin.Context) {
		requestContext.JSON(http.StatusOK, gin.H{"admin": IsAdmin(requestContext)})
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(userTokenHeader, "signed-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Body.String() != `{"admin":false}` {
		t.Fatalf("body = %s, want admin=false", response.Body.String())
	}
}
