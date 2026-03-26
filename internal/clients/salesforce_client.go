package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const sfAPIVersion = "v62.0"

type SalesForceClient struct {
	instanceURL  string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	mu          sync.RWMutex
	accessToken string
	tokenExpiry time.Time
}

func NewSalesForceClient(instanceURL, clientID, clientSecret string) *SalesForceClient {
	return &SalesForceClient{
		instanceURL:  instanceURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

type sfTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (c *SalesForceClient) authenticate(ctx context.Context) error {
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.instanceURL+"/services/oauth2/token",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return fmt.Errorf("salesforce: falha ao criar request de auth: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("salesforce: falha na auth: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("salesforce: auth retornou %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp sfTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("salesforce: falha ao decodificar token: %w", err)
	}

	c.mu.Lock()
	c.accessToken = tokenResp.AccessToken
	// Renovar 60s antes da expiração; SalesForce padrão é 1h
	expiry := tokenResp.ExpiresIn
	if expiry <= 0 {
		expiry = 3600
	}
	c.tokenExpiry = time.Now().Add(time.Duration(expiry-60) * time.Second)
	c.mu.Unlock()

	log.Debug().Msg("salesforce: token renovado")
	return nil
}

func (c *SalesForceClient) getToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	token := c.accessToken
	valid := time.Now().Before(c.tokenExpiry)
	c.mu.RUnlock()

	if token != "" && valid {
		return token, nil
	}

	if err := c.authenticate(ctx); err != nil {
		return "", err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken, nil
}

type SFQueryResponse struct {
	TotalSize      int                      `json:"totalSize"`
	Done           bool                     `json:"done"`
	NextRecordsURL string                   `json:"nextRecordsUrl"`
	Records        []map[string]interface{} `json:"records"`
}

// Query executa uma SOQL query e retorna todos os registros (paginação automática).
func (c *SalesForceClient) Query(ctx context.Context, soql string) ([]map[string]interface{}, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	var allRecords []map[string]interface{}
	nextURL := fmt.Sprintf("%s/services/data/%s/query?q=%s", c.instanceURL, sfAPIVersion, url.QueryEscape(soql))

	for nextURL != "" {
		var reqURL string
		if strings.HasPrefix(nextURL, "/services") {
			reqURL = c.instanceURL + nextURL
		} else {
			reqURL = nextURL
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("salesforce: falha ao criar query request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("salesforce: falha na query: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			// Tentar renovar token uma vez
			if err := c.authenticate(ctx); err != nil {
				return nil, fmt.Errorf("salesforce: falha ao renovar token: %w", err)
			}
			token, _ = c.getToken(ctx)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("salesforce: query retornou %d: %s", resp.StatusCode, string(body))
		}

		var qr SFQueryResponse
		if err := json.Unmarshal(body, &qr); err != nil {
			return nil, fmt.Errorf("salesforce: falha ao decodificar resposta: %w", err)
		}

		allRecords = append(allRecords, qr.Records...)

		if qr.Done || qr.NextRecordsURL == "" {
			break
		}
		nextURL = qr.NextRecordsURL
	}

	return allRecords, nil
}

// QueryModifiedSince retorna registros modificados após a data informada.
func (c *SalesForceClient) QueryModifiedSince(ctx context.Context, objectType string, since time.Time) ([]map[string]interface{}, error) {
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
	return c.Query(ctx, soql)
}

// QueryAll retorna todos os registros ativos de um objeto.
func (c *SalesForceClient) QueryAll(ctx context.Context, objectType string) ([]map[string]interface{}, error) {
	soql := fmt.Sprintf(
		`SELECT Id, Name, Description__c, ShortDescription__c, Organization__c, URL__c,
		        Status__c, Theme__c, Channel__c, Neighborhood__c, Tags__c,
		        ValidFrom__c, ValidUntil__c, LastModifiedDate
		 FROM %s
		 ORDER BY LastModifiedDate ASC`,
		objectType,
	)
	return c.Query(ctx, soql)
}
