package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestGetGeminiAPIKeyReadsCanonicalShellVariable(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "canonical-test-key")

	if apiKey := getGeminiAPIKey(viper.New()); apiKey != "canonical-test-key" {
		t.Fatalf("Gemini API key source did not read GEMINI_API_KEY")
	}
}

func TestLoadDatabaseSettingsDoesNotRequireApplicationSecrets(t *testing.T) {
	configuration := viper.New()
	configuration.Set("DB_HOST", "database.internal")
	configuration.Set("DB_PORT", "5433")

	settings, settingsError := loadDatabaseSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load database settings: %v", settingsError)
	}
	if settings.Host != "database.internal" || settings.Port != 5433 {
		t.Fatalf("database settings = %#v", settings)
	}
}

func TestLoadCacheSettingsParsesStrictPositiveDurations(t *testing.T) {
	t.Parallel()

	configuration := viper.New()
	configuration.Set("CACHE_SEARCH_TTL", "45s")
	configuration.Set("CACHE_RECOMMENDATION_AUTHENTICATED_TTL", "4m")
	configuration.Set("CACHE_RECOMMENDATION_CLUSTER_TTL", "12m")

	settings, settingsError := loadCacheSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load cache settings: %v", settingsError)
	}
	if settings.SearchTTL != 45*time.Second ||
		settings.RecommendationAuthenticatedTTL != 4*time.Minute ||
		settings.RecommendationAnonymousTTL != 12*time.Minute {
		t.Fatalf("cache settings = %#v", settings)
	}
}

func TestLoadCacheSettingsRejectsMalformedOrNonPositiveDurations(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "malformed search TTL", key: "CACHE_SEARCH_TTL", value: "later"},
		{name: "zero search TTL", key: "CACHE_SEARCH_TTL", value: "0s"},
		{name: "negative authenticated TTL", key: "CACHE_RECOMMENDATION_AUTHENTICATED_TTL", value: "-1s"},
		{name: "zero anonymous TTL", key: "CACHE_RECOMMENDATION_CLUSTER_TTL", value: "0s"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			invalidConfiguration := viper.New()
			invalidConfiguration.Set(testCase.key, testCase.value)
			if _, settingsError := loadCacheSettings(invalidConfiguration); settingsError == nil ||
				!strings.Contains(settingsError.Error(), testCase.key) {
				t.Fatalf("error = %v, want strict %s validation", settingsError, testCase.key)
			}
		})
	}
}

func TestLoadTypesenseSettingsValidatesFullSnapshotSchedule(t *testing.T) {
	configuration := viper.New()
	configuration.Set("TYPESENSE_SYNC_INTERVAL", "15m")
	configuration.Set("TYPESENSE_FULL_SYNC_INTERVAL", "12h")

	settings, settingsError := loadTypesenseSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load Typesense settings: %v", settingsError)
	}
	if settings.SyncInterval != 15*time.Minute || settings.FullSyncInterval != 12*time.Hour {
		t.Fatalf("Typesense intervals = %#v", settings)
	}

	for _, testCase := range []struct {
		name             string
		syncInterval     string
		fullSyncInterval string
	}{
		{name: "malformed sync interval", syncInterval: "often", fullSyncInterval: "12h"},
		{name: "zero sync interval", syncInterval: "0s", fullSyncInterval: "12h"},
		{name: "malformed full interval", syncInterval: "15m", fullSyncInterval: "daily"},
		{name: "full interval shorter than delta", syncInterval: "1h", fullSyncInterval: "30m"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			invalidConfiguration := viper.New()
			invalidConfiguration.Set("TYPESENSE_SYNC_INTERVAL", testCase.syncInterval)
			invalidConfiguration.Set("TYPESENSE_FULL_SYNC_INTERVAL", testCase.fullSyncInterval)
			if _, invalidSettingsError := loadTypesenseSettings(invalidConfiguration); invalidSettingsError == nil {
				t.Fatalf(
					"loadTypesenseSettings accepted sync=%q full=%q",
					testCase.syncInterval,
					testCase.fullSyncInterval,
				)
			}
		})
	}
}

func TestLoadAppGoAPISettingsRejectsUnsafeSyncIntervals(t *testing.T) {
	configuration := viper.New()
	configuration.Set("APP_GO_API_BASE_URL", " https://app-go.example.test ")
	configuration.Set("APP_GO_API_SYNC_INTERVAL", "20m")

	settings, settingsError := loadAppGoAPISettings(configuration)
	if settingsError != nil {
		t.Fatalf("load app-go-api settings: %v", settingsError)
	}
	if settings.BaseURL != "https://app-go.example.test" || settings.SyncInterval != 20*time.Minute {
		t.Fatalf("app-go-api settings = %#v", settings)
	}

	for _, invalidInterval := range []string{"often", "0s", "-1m"} {
		invalidConfiguration := viper.New()
		invalidConfiguration.Set("APP_GO_API_SYNC_INTERVAL", invalidInterval)
		if _, invalidSettingsError := loadAppGoAPISettings(invalidConfiguration); invalidSettingsError == nil {
			t.Fatalf("loadAppGoAPISettings accepted interval %q", invalidInterval)
		}
	}
}

func TestValidateCPFHashSecretRejectsMissingOrWeakSecrets(t *testing.T) {
	for _, secret := range []string{"", "short"} {
		if validationError := validateCPFHashSecret(secret); validationError == nil {
			t.Fatalf("validateCPFHashSecret accepted %q", secret)
		}
	}
	if validationError := validateCPFHashSecret("test-cpf-hmac-key-with-at-least-32-bytes"); validationError != nil {
		t.Fatalf("validate valid CPF hash secret: %v", validationError)
	}
}

func TestLoadServerSettingsParsesBoundsAndTrustedProxies(t *testing.T) {
	configuration := viper.New()
	configuration.Set("SERVER_HOST", " 127.0.0.1 ")
	configuration.Set("SERVER_PORT", "9090")
	configuration.Set("SERVER_REQUEST_TIMEOUT", "30")
	configuration.Set("SERVER_READ_HEADER_TIMEOUT", "2s")
	configuration.Set("SERVER_READ_TIMEOUT", "4s")
	configuration.Set("SERVER_WRITE_TIMEOUT", "31s")
	configuration.Set("SERVER_SHUTDOWN_TIMEOUT", "32s")
	configuration.Set("SERVER_IDLE_TIMEOUT", "45s")
	configuration.Set("SERVER_MAX_HEADER_BYTES", "65536")
	configuration.Set("SERVER_TRUSTED_PROXIES", "10.0.0.8/24, 2001:db8::1, 10.0.0.0/24")

	settings, settingsError := loadServerSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load valid server settings: %v", settingsError)
	}
	if settings.Host != "127.0.0.1" || settings.Port != 9090 ||
		settings.RequestTimeout != 30*time.Second || settings.ReadHeaderTimeout != 2*time.Second ||
		settings.ReadTimeout != 4*time.Second || settings.WriteTimeout != 31*time.Second ||
		settings.ShutdownTimeout != 32*time.Second ||
		settings.IdleTimeout != 45*time.Second || settings.MaxHeaderBytes != 65536 {
		t.Fatalf("parsed server settings = %#v", settings)
	}
	expectedTrustedProxies := []string{"10.0.0.0/24", "2001:db8::1"}
	if !reflect.DeepEqual(settings.TrustedProxies, expectedTrustedProxies) {
		t.Fatalf("trusted proxies = %v, want %v", settings.TrustedProxies, expectedTrustedProxies)
	}
}

func TestLoadServerSettingsRejectsUnsafeOrMalformedValues(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "malformed port", key: "SERVER_PORT", value: "http"},
		{name: "port out of range", key: "SERVER_PORT", value: "65536"},
		{name: "malformed request timeout", key: "SERVER_REQUEST_TIMEOUT", value: "eventually"},
		{name: "read timeout below header timeout", key: "SERVER_READ_TIMEOUT", value: "1s"},
		{name: "write timeout cannot emit request timeout", key: "SERVER_WRITE_TIMEOUT", value: "30s"},
		{name: "malformed shutdown timeout", key: "SERVER_SHUTDOWN_TIMEOUT", value: "eventually"},
		{name: "shutdown timeout cannot drain write timeout", key: "SERVER_SHUTDOWN_TIMEOUT", value: "35s"},
		{name: "unbounded header size", key: "SERVER_MAX_HEADER_BYTES", value: "1048577"},
		{name: "trust every IPv4 proxy", key: "SERVER_TRUSTED_PROXIES", value: "0.0.0.0/0"},
		{name: "trust every IPv6 proxy", key: "SERVER_TRUSTED_PROXIES", value: "::/0"},
		{name: "malformed trusted proxy", key: "SERVER_TRUSTED_PROXIES", value: "proxy.internal"},
		{name: "empty trusted proxy entry", key: "SERVER_TRUSTED_PROXIES", value: "10.0.0.1,,10.0.0.2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := viper.New()
			configuration.Set(testCase.key, testCase.value)
			if _, settingsError := loadServerSettings(configuration); settingsError == nil {
				t.Fatalf("loadServerSettings accepted %s=%q", testCase.key, testCase.value)
			}
		})
	}
}

func TestLoadRateLimitSettingsValidatesResourceBounds(t *testing.T) {
	configuration := viper.New()
	configuration.Set("RATE_LIMIT_REQUESTS", "42")
	configuration.Set("RATE_LIMIT_WINDOW", "30s")
	configuration.Set("RATE_LIMIT_REDIS_TIMEOUT", "75ms")
	configuration.Set("RATE_LIMIT_KEY_SECRET", "rate-limit-test-key-with-enough-entropy")

	settings, settingsError := loadRateLimitSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load valid rate-limit settings: %v", settingsError)
	}
	if settings.RequestsPerWindow != 42 || settings.Window != 30*time.Second ||
		settings.RedisTimeout != 75*time.Millisecond || settings.KeySecret == "" {
		t.Fatalf("parsed rate-limit settings = %#v", settings)
	}

	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "malformed requests", key: "RATE_LIMIT_REQUESTS", value: "many"},
		{name: "zero requests", key: "RATE_LIMIT_REQUESTS", value: "0"},
		{name: "sub-millisecond window", key: "RATE_LIMIT_WINDOW", value: "1ns"},
		{name: "zero backend timeout", key: "RATE_LIMIT_REDIS_TIMEOUT", value: "0s"},
		{name: "backend timeout beyond window", key: "RATE_LIMIT_REDIS_TIMEOUT", value: "2m"},
		{name: "missing key secret", key: "RATE_LIMIT_KEY_SECRET", value: ""},
		{name: "weak key secret", key: "RATE_LIMIT_KEY_SECRET", value: "short"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			invalidConfiguration := viper.New()
			invalidConfiguration.Set("RATE_LIMIT_KEY_SECRET", "rate-limit-test-key-with-enough-entropy")
			invalidConfiguration.Set(testCase.key, testCase.value)
			if _, invalidSettingsError := loadRateLimitSettings(invalidConfiguration); invalidSettingsError == nil {
				t.Fatalf("loadRateLimitSettings accepted %s=%q", testCase.key, testCase.value)
			}
		})
	}
}

func TestLoadInternalAPISettingsRequiresVisibleASCIISecretAndBoundedSkew(t *testing.T) {
	configuration := viper.New()
	configuration.Set("APP_CATALOGO_INTERNAL_API_KEY", "catalog-internal-api-key-with-enough-entropy")
	configuration.Set("APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW", "90s")

	settings, settingsError := loadInternalAPISettings(configuration)
	if settingsError != nil {
		t.Fatalf("load valid internal API settings: %v", settingsError)
	}
	if settings.Key == "" || settings.CatalogSearchSignatureSkew != 90*time.Second {
		t.Fatalf("internal API settings = %#v", settings)
	}

	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing secret", key: "APP_CATALOGO_INTERNAL_API_KEY", value: ""},
		{name: "short secret", key: "APP_CATALOGO_INTERNAL_API_KEY", value: "short"},
		{name: "unicode secret", key: "APP_CATALOGO_INTERNAL_API_KEY", value: strings.Repeat("é", 32)},
		{name: "space in secret", key: "APP_CATALOGO_INTERNAL_API_KEY", value: "catalog internal api key with enough entropy"},
		{name: "malformed skew", key: "APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW", value: "recently"},
		{name: "sub-second skew", key: "APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW", value: "500ms"},
		{name: "unbounded skew", key: "APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW", value: "11m"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			invalidConfiguration := viper.New()
			invalidConfiguration.Set("APP_CATALOGO_INTERNAL_API_KEY", "catalog-internal-api-key-with-enough-entropy")
			invalidConfiguration.Set(testCase.key, testCase.value)
			if _, invalidSettingsError := loadInternalAPISettings(invalidConfiguration); invalidSettingsError == nil {
				t.Fatalf("loadInternalAPISettings accepted %s=%q", testCase.key, testCase.value)
			}
		})
	}
}

func TestLoadJWTSettingsRequiresTrustedEndpointsAndClaims(t *testing.T) {
	configuration := viper.New()
	configuration.Set("AUTH_JWT_ISSUER", "https://identity.example/realms/citizen")
	configuration.Set("AUTH_JWT_JWKS_URL", "https://identity.example/realms/citizen/protocol/openid-connect/certs")
	configuration.Set("AUTH_JWT_AUDIENCE", "app-catalogo")
	configuration.Set("AUTH_JWT_AUTHORIZED_PARTY", "superapp")
	configuration.Set("AUTH_JWT_ROLE_CLIENT_ID", "superapp")
	configuration.Set("AUTH_JWT_CLOCK_SKEW", "45s")
	configuration.Set("AUTH_JWT_JWKS_CACHE_TTL", "10m")
	configuration.Set("AUTH_JWT_UNKNOWN_KEY_REFRESH_INTERVAL", "20s")
	configuration.Set("AUTH_JWT_HTTP_TIMEOUT", "2s")

	settings, settingsError := loadJWTSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load JWT settings: %v", settingsError)
	}
	if settings.Audience != "app-catalogo" || settings.RoleClientID != "superapp" ||
		settings.ClockSkew != 45*time.Second || settings.JWKSCacheTTL != 10*time.Minute {
		t.Fatalf("JWT settings = %#v", settings)
	}
}

func TestLoadJWTSettingsRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "non HTTPS issuer", key: "AUTH_JWT_ISSUER", value: "http://identity.example/realms/citizen"},
		{name: "JWKS query", key: "AUTH_JWT_JWKS_URL", value: "https://identity.example/certs?key=1"},
		{name: "missing audience", key: "AUTH_JWT_AUDIENCE", value: ""},
		{name: "missing role client", key: "AUTH_JWT_ROLE_CLIENT_ID", value: ""},
		{name: "invalid duration", key: "AUTH_JWT_HTTP_TIMEOUT", value: "soon"},
		{name: "refresh exceeds TTL", key: "AUTH_JWT_UNKNOWN_KEY_REFRESH_INTERVAL", value: "20m"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := viper.New()
			configuration.Set("AUTH_JWT_ISSUER", "https://identity.example/realms/citizen")
			configuration.Set("AUTH_JWT_JWKS_URL", "https://identity.example/realms/citizen/protocol/openid-connect/certs")
			configuration.Set("AUTH_JWT_AUDIENCE", "app-catalogo")
			configuration.Set("AUTH_JWT_ROLE_CLIENT_ID", "superapp")
			configuration.Set(testCase.key, testCase.value)
			if _, settingsError := loadJWTSettings(configuration); settingsError == nil {
				t.Fatalf("loadJWTSettings accepted %s=%q", testCase.key, testCase.value)
			}
		})
	}
}

func TestLoadSalesForceSettingsParsesAndValidatesSyncIntervals(t *testing.T) {
	configuration := viper.New()
	configuration.Set("SALESFORCE_SYNC_INTERVAL", "10m")
	configuration.Set("SALESFORCE_FULL_SYNC_INTERVAL", "12h")
	configuration.Set("SALESFORCE_INSTANCE_URL", "https://example.my.salesforce.com")
	configuration.Set("SALESFORCE_WEBHOOK_SECRET", "salesforce-test-secret-with-enough-entropy")

	settings, settingsError := loadSalesForceSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load valid Salesforce settings: %v", settingsError)
	}
	if settings.SyncInterval != 10*time.Minute || settings.FullSyncInterval != 12*time.Hour {
		t.Fatalf("parsed Salesforce settings = %#v", settings)
	}

	missingWebhookSecretConfiguration := viper.New()
	missingWebhookSecretConfiguration.Set("SALESFORCE_INSTANCE_URL", "https://example.my.salesforce.com")
	if _, missingSecretError := loadSalesForceSettings(missingWebhookSecretConfiguration); missingSecretError == nil {
		t.Fatal("loadSalesForceSettings accepted enabled Salesforce without a webhook secret")
	}

	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "malformed delta interval", key: "SALESFORCE_SYNC_INTERVAL", value: "often"},
		{name: "zero delta interval", key: "SALESFORCE_SYNC_INTERVAL", value: "0s"},
		{name: "malformed full interval", key: "SALESFORCE_FULL_SYNC_INTERVAL", value: "daily"},
		{name: "zero full interval", key: "SALESFORCE_FULL_SYNC_INTERVAL", value: "0s"},
		{name: "negative full interval", key: "SALESFORCE_FULL_SYNC_INTERVAL", value: "-1h"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			invalidConfiguration := viper.New()
			invalidConfiguration.Set(testCase.key, testCase.value)
			if _, invalidSettingsError := loadSalesForceSettings(invalidConfiguration); invalidSettingsError == nil {
				t.Fatalf("loadSalesForceSettings accepted %s=%q", testCase.key, testCase.value)
			}
		})
	}
}

func TestLoadSearchSettingsRejectsMalformedValues(t *testing.T) {
	testCases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "candidate pool", key: "SEARCH_CANDIDATE_POOL_SIZE", value: "40items"},
		{name: "semantic overfetch factor", key: "SEARCH_SEMANTIC_OVERFETCH_FACTOR", value: "many"},
		{name: "semantic timeout", key: "SEARCH_SEMANTIC_TIMEOUT", value: "quickly"},
		{name: "hyde flag", key: "SEARCH_HYDE_ENABLED", value: "yes"},
		{name: "retrieval weight", key: "SEARCH_SEMANTIC_WEIGHT", value: "1.0garbage"},
		{name: "Facilita weight", key: "SEARCH_FACILITA_WEIGHT", value: "heavy"},
		{name: "semantic distance", key: "SEARCH_MAX_SEMANTIC_DISTANCE", value: "close"},
		{name: "ranker version", key: "SEARCH_RANKER_VERSION", value: "invalid version"},
		{name: "reranker version", key: "SEARCH_RERANKER_VERSION", value: "invalid version"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := viper.New()
			configuration.Set(testCase.key, testCase.value)

			_, settingsError := loadSearchSettings(configuration)
			if settingsError == nil || !strings.Contains(settingsError.Error(), testCase.key) {
				t.Fatalf("error = %v, want strict %s validation", settingsError, testCase.key)
			}
		})
	}
}

func TestLoadSearchSettingsParsesExplicitValidValues(t *testing.T) {
	configuration := viper.New()
	configuration.Set("SEARCH_RANKER_VERSION", " test-ranker ")
	configuration.Set("SEARCH_CANDIDATE_POOL_SIZE", "80")
	configuration.Set("SEARCH_SEMANTIC_OVERFETCH_FACTOR", "6")
	configuration.Set("SEARCH_MAX_SEMANTIC_DISTANCE", "0.42")
	configuration.Set("SEARCH_RERANKER_VERSION", "cross-encoder-v1")
	configuration.Set("SEARCH_SEMANTIC_TIMEOUT", "1500ms")
	configuration.Set("SEARCH_HYDE_ENABLED", "true")
	configuration.Set("SEARCH_EXACT_WEIGHT", "4.5")
	configuration.Set("SEARCH_FULL_TEXT_WEIGHT", "2")
	configuration.Set("SEARCH_TRIGRAM_WEIGHT", "1.5")
	configuration.Set("SEARCH_SEMANTIC_WEIGHT", "3")
	configuration.Set("SEARCH_HYDE_WEIGHT", "0.25")
	configuration.Set("SEARCH_FACILITA_WEIGHT", "2.5")

	settings, settingsError := loadSearchSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load valid search settings: %v", settingsError)
	}
	if settings.RankerVersion != "test-ranker" || settings.RerankerVersion != "cross-encoder-v1" ||
		settings.CandidatePoolSize != 80 || settings.SemanticOverfetchFactor != 6 || settings.MaximumSemanticDistance != 0.42 ||
		settings.SemanticTimeout.String() != "1.5s" || !settings.HyDEEnabled ||
		settings.ExactWeight != 4.5 || settings.FullTextWeight != 2 ||
		settings.TrigramWeight != 1.5 || settings.SemanticWeight != 3 || settings.HyDEWeight != 0.25 ||
		settings.FacilitaWeight != 2.5 {
		t.Fatalf("parsed search settings = %#v", settings)
	}
}

func TestLoadFacilitaSearchSettingsRequiresCompleteConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		baseURL     string
		internalKey string
	}{
		{name: "URL only", baseURL: "https://facilita.example"},
		{name: "key only", internalKey: "test-facilita-internal-api-key-000000000000"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := viper.New()
			configuration.Set("FACILITA_SEARCH_BASE_URL", testCase.baseURL)
			configuration.Set("FACILITA_INTERNAL_API_KEY", testCase.internalKey)
			if _, settingsError := loadFacilitaSearchSettings(configuration); settingsError == nil {
				t.Fatal("partial Facilita search configuration was accepted")
			}
		})
	}
}

func TestLoadFacilitaSearchSettingsParsesExplicitValues(t *testing.T) {
	configuration := viper.New()
	configuration.Set("FACILITA_SEARCH_BASE_URL", " https://facilita.example ")
	configuration.Set("FACILITA_INTERNAL_API_KEY", "test-facilita-internal-api-key-000000000000")
	configuration.Set("FACILITA_SEARCH_TIMEOUT", "750ms")

	settings, settingsError := loadFacilitaSearchSettings(configuration)
	if settingsError != nil {
		t.Fatalf("load Facilita search settings: %v", settingsError)
	}
	if !settings.Enabled() || settings.BaseURL != "https://facilita.example" || settings.Timeout != 750*time.Millisecond {
		t.Fatalf("Facilita settings = %#v", settings)
	}
}

func TestLoadFacilitaSearchSettingsRejectsInvalidTimeout(t *testing.T) {
	configuration := viper.New()
	configuration.Set("FACILITA_SEARCH_TIMEOUT", "0s")
	if _, settingsError := loadFacilitaSearchSettings(configuration); settingsError == nil {
		t.Fatal("nonpositive Facilita timeout was accepted")
	}
}

func TestSearchSourceAvailabilityRejectsUnavailableOnlyWeightedSource(t *testing.T) {
	t.Parallel()

	searchSettings := SearchSettings{FacilitaWeight: 2}
	if availabilityError := validateSearchSourceAvailability(
		searchSettings,
		FacilitaSearchSettings{},
	); availabilityError == nil {
		t.Fatal("unconfigured sole Facilita retrieval source was accepted")
	}
	if availabilityError := validateSearchSourceAvailability(
		searchSettings,
		FacilitaSearchSettings{
			BaseURL:        "https://facilita.example",
			InternalAPIKey: "test-facilita-internal-api-key-000000000000",
		},
	); availabilityError != nil {
		t.Fatalf("configured Facilita retrieval source: %v", availabilityError)
	}
}

func TestLoadEmbeddingSettingsRejectsMalformedOrNonPositiveDurations(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "malformed request timeout", key: "EMBEDDING_REQUEST_TIMEOUT", value: "eventually"},
		{name: "zero request timeout", key: "EMBEDDING_REQUEST_TIMEOUT", value: "0s"},
		{name: "negative interval", key: "EMBEDDING_BACKFILL_INTERVAL", value: "-1s"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := viper.New()
			configuration.Set(testCase.key, testCase.value)
			if _, settingsError := loadEmbeddingSettings(configuration); settingsError == nil {
				t.Fatalf("loadEmbeddingSettings accepted %s=%q", testCase.key, testCase.value)
			}
		})
	}
}
