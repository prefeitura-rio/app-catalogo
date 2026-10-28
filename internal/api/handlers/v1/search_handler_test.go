package v1

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestParseSearchQuery_PerPageNormalizado cobre o caminho da query string até a
// normalização que o SearchService aplica, para os dois endpoints de busca
// (/api/v1/search e /api/public/search usam o mesmo parseSearchQuery).
func TestParseSearchQuery_PerPageNormalizado(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		query       string
		wantPage    int
		wantPerPage int
	}{
		{"q=curso", 1, 10},
		{"q=curso&per_page=50", 1, 50},
		{"q=curso&per_page=100", 1, 100},
		{"q=curso&per_page=101", 1, 100},
		{"q=curso&per_page=9999", 1, 100},
		{"q=curso&per_page=0", 1, 10},
		{"q=curso&per_page=-3", 1, 10},
		{"q=curso&per_page=abc", 1, 10},
		{"q=curso&page=2&per_page=9999", 2, 100},
	}

	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/api/public/search?"+tc.query, nil)

		req := parseSearchQuery(c)
		req.Normalize()

		if req.Page != tc.wantPage {
			t.Errorf("%s: page = %d, want %d", tc.query, req.Page, tc.wantPage)
		}
		if req.PerPage != tc.wantPerPage {
			t.Errorf("%s: per_page = %d, want %d", tc.query, req.PerPage, tc.wantPerPage)
		}
	}
}
