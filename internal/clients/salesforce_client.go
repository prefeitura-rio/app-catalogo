package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	salesForceAPIVersion                = "v62.0"
	maximumSalesForceTokenResponseBytes = 64 << 10
	maximumSalesForceQueryResponseBytes = 16 << 20
	maximumSalesForceRecordsPerQuery    = 250_000
	maximumSalesForcePagesPerQuery      = 10_000
	defaultSalesForceTokenLifetime      = time.Hour
	maximumSalesForceTokenLifetime      = 24 * time.Hour
	salesForceTokenRefreshLead          = time.Minute
)

type SalesForceClient struct {
	instanceURL  *url.URL
	tokenURL     string
	clientID     string
	clientSecret string
	httpClient   *http.Client
	now          func() time.Time

	mutex        sync.RWMutex
	refreshMutex sync.Mutex
	accessToken  string
	tokenExpiry  time.Time
	tokenVersion uint64
}

func NewSalesForceClient(
	instanceURL string,
	clientID string,
	clientSecret string,
) (*SalesForceClient, error) {
	parsedInstanceURL, instanceURLError := validateServiceBaseURL(instanceURL, true, false)
	if instanceURLError != nil {
		return nil, fmt.Errorf("salesforce: invalid instance URL: %w", instanceURLError)
	}
	if credentialError := validateServiceCredential(clientID, maximumServiceCredentialBytes); credentialError != nil {
		return nil, errors.New("salesforce: client ID is missing or invalid")
	}
	if credentialError := validateServiceCredential(clientSecret, maximumServiceCredentialBytes); credentialError != nil {
		return nil, errors.New("salesforce: client secret is missing or invalid")
	}
	tokenURL, joinError := url.JoinPath(parsedInstanceURL.String(), "services", "oauth2", "token")
	if joinError != nil {
		return nil, errors.New("salesforce: build token URL")
	}
	return &SalesForceClient{
		instanceURL:  parsedInstanceURL,
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   noRedirectHTTPClient(30 * time.Second),
		now:          time.Now,
	}, nil
}

type salesForceTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (client *SalesForceClient) authenticate(ctx context.Context) error {
	formValues := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {client.clientID},
		"client_secret": {client.clientSecret},
	}
	request, requestError := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.tokenURL,
		strings.NewReader(formValues.Encode()),
	)
	if requestError != nil {
		return fmt.Errorf("salesforce: create token request: %w", requestError)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, requestError := client.httpClient.Do(request)
	if requestError != nil {
		return fmt.Errorf("salesforce: token request failed: %w", requestError)
	}
	defer response.Body.Close()
	encodedBody, readError := readBoundedHTTPBody(response.Body, maximumSalesForceTokenResponseBytes)
	if readError != nil {
		return fmt.Errorf("salesforce: invalid token response size: %w", readError)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("salesforce: token endpoint returned status %d", response.StatusCode)
	}

	var tokenResponse salesForceTokenResponse
	if decodeError := json.Unmarshal(encodedBody, &tokenResponse); decodeError != nil {
		return errors.New("salesforce: token endpoint returned invalid JSON")
	}
	if credentialError := validateServiceCredential(tokenResponse.AccessToken, maximumServiceCredentialBytes); credentialError != nil {
		return errors.New("salesforce: token endpoint returned an invalid access token")
	}
	if tokenResponse.ExpiresIn <= 0 {
		tokenResponse.ExpiresIn = int(defaultSalesForceTokenLifetime / time.Second)
	}
	if tokenResponse.ExpiresIn > int(maximumSalesForceTokenLifetime/time.Second) {
		return errors.New("salesforce: token endpoint returned an invalid lifetime")
	}
	tokenLifetime := time.Duration(tokenResponse.ExpiresIn) * time.Second
	refreshLead := min(salesForceTokenRefreshLead, tokenLifetime/2)

	client.mutex.Lock()
	client.accessToken = tokenResponse.AccessToken
	client.tokenExpiry = client.now().Add(tokenLifetime - refreshLead)
	client.tokenVersion++
	client.mutex.Unlock()
	log.Debug().Msg("salesforce: token renewed")
	return nil
}

func (client *SalesForceClient) getToken(ctx context.Context) (string, uint64, error) {
	if token, tokenVersion, valid := client.cachedToken(); valid {
		return token, tokenVersion, nil
	}
	client.refreshMutex.Lock()
	defer client.refreshMutex.Unlock()
	if token, tokenVersion, valid := client.cachedToken(); valid {
		return token, tokenVersion, nil
	}
	if authenticationError := client.authenticate(ctx); authenticationError != nil {
		return "", 0, authenticationError
	}
	token, tokenVersion, valid := client.cachedToken()
	if !valid {
		return "", 0, errors.New("salesforce: refreshed token is not usable")
	}
	return token, tokenVersion, nil
}

func (client *SalesForceClient) refreshTokenAfterUnauthorized(
	ctx context.Context,
	rejectedTokenVersion uint64,
) (string, uint64, error) {
	client.refreshMutex.Lock()
	defer client.refreshMutex.Unlock()
	if token, tokenVersion, valid := client.cachedToken(); valid && tokenVersion != rejectedTokenVersion {
		return token, tokenVersion, nil
	}
	if authenticationError := client.authenticate(ctx); authenticationError != nil {
		return "", 0, authenticationError
	}
	token, tokenVersion, valid := client.cachedToken()
	if !valid {
		return "", 0, errors.New("salesforce: refreshed token is not usable")
	}
	return token, tokenVersion, nil
}

func (client *SalesForceClient) cachedToken() (string, uint64, bool) {
	client.mutex.RLock()
	defer client.mutex.RUnlock()
	return client.accessToken,
		client.tokenVersion,
		client.accessToken != "" && client.now().Before(client.tokenExpiry)
}

type SFQueryResponse struct {
	TotalSize      int                      `json:"totalSize"`
	Done           bool                     `json:"done"`
	NextRecordsURL string                   `json:"nextRecordsUrl"`
	Records        []map[string]interface{} `json:"records"`
}

// Query executes one SOQL query with bounded, same-origin pagination.
func (client *SalesForceClient) Query(ctx context.Context, soql string) ([]map[string]interface{}, error) {
	token, tokenVersion, tokenError := client.getToken(ctx)
	if tokenError != nil {
		return nil, tokenError
	}
	queryEndpoint, joinError := url.JoinPath(
		client.instanceURL.String(),
		"services",
		"data",
		salesForceAPIVersion,
		"query",
	)
	if joinError != nil {
		return nil, errors.New("salesforce: build query URL")
	}
	parsedQueryEndpoint, parseError := url.Parse(queryEndpoint)
	if parseError != nil {
		return nil, errors.New("salesforce: build query URL")
	}
	queryParameters := parsedQueryEndpoint.Query()
	queryParameters.Set("q", soql)
	parsedQueryEndpoint.RawQuery = queryParameters.Encode()
	nextRecordsURL := parsedQueryEndpoint.String()

	allRecords := make([]map[string]interface{}, 0)
	seenPages := make(map[string]struct{})
	unauthorizedRefreshUsed := false
	pageCount := 0
	for nextRecordsURL != "" {
		requestURL, resolutionError := client.resolveQueryURL(nextRecordsURL)
		if resolutionError != nil {
			return nil, resolutionError
		}
		if _, duplicatePage := seenPages[requestURL]; duplicatePage {
			return nil, errors.New("salesforce: pagination cycle detected")
		}
		if pageCount >= maximumSalesForcePagesPerQuery {
			return nil, errors.New("salesforce: query exceeds its page limit")
		}

		request, requestError := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if requestError != nil {
			return nil, fmt.Errorf("salesforce: create query request: %w", requestError)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/json")

		response, requestError := client.httpClient.Do(request)
		if requestError != nil {
			return nil, fmt.Errorf("salesforce: query request failed: %w", requestError)
		}
		encodedBody, readError := readBoundedHTTPBody(response.Body, maximumSalesForceQueryResponseBytes)
		closeError := response.Body.Close()
		if readError != nil {
			return nil, fmt.Errorf("salesforce: invalid query response size: %w", readError)
		}
		if closeError != nil {
			return nil, errors.New("salesforce: close query response")
		}
		if response.StatusCode == http.StatusUnauthorized {
			if unauthorizedRefreshUsed {
				return nil, errors.New("salesforce: query remained unauthorized after one token renewal")
			}
			token, tokenVersion, tokenError = client.refreshTokenAfterUnauthorized(ctx, tokenVersion)
			if tokenError != nil {
				return nil, fmt.Errorf("salesforce: renew token after unauthorized response: %w", tokenError)
			}
			unauthorizedRefreshUsed = true
			continue
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("salesforce: query endpoint returned status %d", response.StatusCode)
		}

		var queryResponse SFQueryResponse
		if decodeError := json.Unmarshal(encodedBody, &queryResponse); decodeError != nil {
			return nil, errors.New("salesforce: query endpoint returned invalid JSON")
		}
		if len(allRecords) > maximumSalesForceRecordsPerQuery-len(queryResponse.Records) {
			return nil, errors.New("salesforce: query exceeds its record limit")
		}
		allRecords = append(allRecords, queryResponse.Records...)
		seenPages[requestURL] = struct{}{}
		pageCount++
		if queryResponse.Done || queryResponse.NextRecordsURL == "" {
			break
		}
		nextRecordsURL = queryResponse.NextRecordsURL
	}
	return allRecords, nil
}

func (client *SalesForceClient) resolveQueryURL(rawRequestURL string) (string, error) {
	requestReference, parseError := url.Parse(rawRequestURL)
	if parseError != nil || requestReference.User != nil || requestReference.Fragment != "" {
		return "", errors.New("salesforce: pagination URL is invalid")
	}
	resolvedURL := client.instanceURL.ResolveReference(requestReference)
	if resolvedURL.Scheme != client.instanceURL.Scheme ||
		!strings.EqualFold(resolvedURL.Host, client.instanceURL.Host) ||
		!strings.HasPrefix(resolvedURL.EscapedPath(), "/services/data/") {
		return "", errors.New("salesforce: pagination URL left the configured instance")
	}
	return resolvedURL.String(), nil
}

// QueryModifiedSince returns records modified after the supplied timestamp.
func (client *SalesForceClient) QueryModifiedSince(
	ctx context.Context,
	objectType string,
	since time.Time,
) ([]map[string]interface{}, error) {
	if objectTypeError := validateSalesForceObjectType(objectType); objectTypeError != nil {
		return nil, objectTypeError
	}
	soql := fmt.Sprintf(
		`SELECT Id, Name, Description__c, ShortDescription__c, Organization__c, URL__c,
                Status__c, Theme__c, Channel__c, Neighborhood__c, Tags__c,
                ValidFrom__c, ValidUntil__c, LastModifiedDate
         FROM %s
         WHERE LastModifiedDate > %s
         ORDER BY LastModifiedDate ASC`,
		objectType,
		since.UTC().Format("2006-01-02T15:04:05Z"),
	)
	return client.Query(ctx, soql)
}

// QueryAll returns every record for one object.
func (client *SalesForceClient) QueryAll(ctx context.Context, objectType string) ([]map[string]interface{}, error) {
	if objectTypeError := validateSalesForceObjectType(objectType); objectTypeError != nil {
		return nil, objectTypeError
	}
	soql := fmt.Sprintf(
		`SELECT Id, Name, Description__c, ShortDescription__c, Organization__c, URL__c,
                Status__c, Theme__c, Channel__c, Neighborhood__c, Tags__c,
                ValidFrom__c, ValidUntil__c, LastModifiedDate
         FROM %s
         ORDER BY LastModifiedDate ASC`,
		objectType,
	)
	return client.Query(ctx, soql)
}

func validateSalesForceObjectType(objectType string) error {
	if objectType == "" || len(objectType) > 255 || !isASCIIAlpha(objectType[0]) {
		return errors.New("salesforce: object type is invalid")
	}
	for objectTypeIndex := 1; objectTypeIndex < len(objectType); objectTypeIndex++ {
		objectTypeCharacter := objectType[objectTypeIndex]
		if !isASCIIAlpha(objectTypeCharacter) &&
			(objectTypeCharacter < '0' || objectTypeCharacter > '9') &&
			objectTypeCharacter != '_' {
			return errors.New("salesforce: object type is invalid")
		}
	}
	return nil
}

func isASCIIAlpha(character byte) bool {
	return (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z')
}
