package clients

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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

func validateServiceBaseURL(rawBaseURL string, requireHTTPS bool, allowPath bool) (*url.URL, error) {
	canonicalBaseURL := strings.TrimRight(strings.TrimSpace(rawBaseURL), "/")
	parsedBaseURL, parseError := url.Parse(canonicalBaseURL)
	if parseError != nil || parsedBaseURL.Host == "" || parsedBaseURL.Hostname() == "" ||
		parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" || parsedBaseURL.Fragment != "" {
		return nil, errors.New("service base URL must be an absolute credential-free URL without query or fragment")
	}
	if parsedBaseURL.Scheme != "https" && parsedBaseURL.Scheme != "http" {
		return nil, errors.New("service base URL must use HTTP(S)")
	}
	if requireHTTPS && parsedBaseURL.Scheme != "https" && !isLoopbackServiceHost(parsedBaseURL.Hostname()) {
		return nil, errors.New("service base URL must use HTTPS outside loopback tests")
	}
	if !allowPath && parsedBaseURL.EscapedPath() != "" {
		return nil, errors.New("service base URL must not contain a path")
	}
	return parsedBaseURL, nil
}

func validateServiceCredential(fieldValue string, maximumBytes int) error {
	if fieldValue == "" || len(fieldValue) > maximumBytes || !utf8.ValidString(fieldValue) ||
		strings.IndexFunc(fieldValue, unicode.IsControl) >= 0 {
		return errors.New("service credential is missing or invalid")
	}
	return nil
}

func isLoopbackServiceHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsedAddress := net.ParseIP(host)
	return parsedAddress != nil && parsedAddress.IsLoopback()
}
