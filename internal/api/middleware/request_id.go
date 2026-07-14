package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	RequestIDHeader      = "X-Request-ID"
	SearchIDKey          = "search_id"
	UpstreamRequestIDKey = "upstream_request_id"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := uuid.New()
		rawRequestID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if suppliedRequestID, parseError := uuid.Parse(rawRequestID); parseError == nil {
			requestID = suppliedRequestID
		} else if canonicalDistributedLogID(rawRequestID) {
			c.Set(UpstreamRequestIDKey, rawRequestID)
		}
		canonicalRequestID := requestID.String()
		c.Set("request_id", canonicalRequestID)
		c.Header(RequestIDHeader, canonicalRequestID)
		c.Next()
	}
}

const maximumDistributedLogID = "340282366920938463463374607431768211455"

func canonicalDistributedLogID(identifier string) bool {
	if identifier == "" || len(identifier) > len(maximumDistributedLogID) || identifier[0] == '0' {
		return false
	}
	for characterIndex := range len(identifier) {
		if identifier[characterIndex] < '0' || identifier[characterIndex] > '9' {
			return false
		}
	}
	return len(identifier) < len(maximumDistributedLogID) || identifier <= maximumDistributedLogID
}

// SearchID preserves a canonical caller-supplied search correlation UUID or
// creates one for direct clients. It remains distinct from the support/log ID.
func SearchID() gin.HandlerFunc {
	return func(context *gin.Context) {
		searchID := uuid.New()
		if suppliedSearchID, parseError := uuid.Parse(context.GetHeader(SearchIDHeader)); parseError == nil &&
			suppliedSearchID.String() == context.GetHeader(SearchIDHeader) {
			searchID = suppliedSearchID
		}
		canonicalSearchID := searchID.String()
		context.Set(SearchIDKey, canonicalSearchID)
		context.Header(SearchIDHeader, canonicalSearchID)
		context.Next()
	}
}
