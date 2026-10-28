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

// errHTTPBodyTooLarge indica resposta maior que o limite: repetir não adianta.
var errHTTPBodyTooLarge = errors.New("HTTP response body exceeds its byte limit")

func readBoundedHTTPBody(responseBody io.Reader, maximumBytes int64) ([]byte, error) {
	encodedBody, readError := io.ReadAll(io.LimitReader(responseBody, maximumBytes+1))
	if readError != nil {
		return nil, errors.New("read bounded HTTP response body")
	}
	if int64(len(encodedBody)) > maximumBytes {
		return nil, errHTTPBodyTooLarge
	}
	return encodedBody, nil
}
