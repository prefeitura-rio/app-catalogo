package cache

import (
	"strings"
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestMarshalCacheValueRejectsOversizedSearchEntries(t *testing.T) {
	t.Parallel()

	oversizedValue := strings.Repeat("x", models.MaximumPublicSearchResponseBytes)
	if _, marshalError := marshalCacheValue(SearchKeyPrefix+"oversized", oversizedValue); marshalError == nil {
		t.Fatal("marshalCacheValue accepted an oversized search response")
	}
	if _, marshalError := marshalCacheValue("catalogo:other:v1:key", oversizedValue); marshalError != nil {
		t.Fatalf("non-search cache entry unexpectedly used the search response bound: %v", marshalError)
	}
}

func TestMarshalCacheValueAcceptsBoundedSearchEntries(t *testing.T) {
	t.Parallel()

	boundedValue := strings.Repeat("x", models.MaximumPublicSearchResponseBytes-32)
	encodedValue, marshalError := marshalCacheValue(SearchKeyPrefix+"bounded", boundedValue)
	if marshalError != nil {
		t.Fatalf("marshalCacheValue rejected a bounded search response: %v", marshalError)
	}
	if len(encodedValue) > models.MaximumPublicSearchResponseBytes {
		t.Fatalf("encoded search cache entry = %d bytes", len(encodedValue))
	}
}
