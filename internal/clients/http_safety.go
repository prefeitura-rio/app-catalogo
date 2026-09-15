package clients

import (
	"errors"
	"io"
	"net/http"
	"time"
)

func noRedirectHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func readBoundedHTTPBody(responseBody io.Reader, maximumBytes int64) ([]byte, error) {
	encodedBody, readError := io.ReadAll(io.LimitReader(responseBody, maximumBytes+1))
	if readError != nil {
		return nil, errors.New("read bounded HTTP response body")
	}
	if int64(len(encodedBody)) > maximumBytes {
		return nil, errors.New("HTTP response body exceeds its byte limit")
	}
	return encodedBody, nil
}
