package config

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spf13/viper"
)

type AppConfig struct {
	App         AppSettings
	Database    DatabaseSettings
	Server      ServerSettings
	Redis       RedisSettings
	Tracing     TracingSettings
	Migrations  MigrationSettings
	Keycloak    KeycloakSettings
	JWT         JWTSettings
	RMI         RMISettings
	AppGoAPI    AppGoAPISettings
	SalesForce  SalesForceSettings
	CitizenSync CitizenSyncSettings
	Cache       CacheSettings
	CPFHashSalt string
	Swagger     SwaggerSettings
	Heimdall    HeimdallSettings
	Typesense   TypesenseSettings
	Gemini      GeminiSettings
	Reranker    RerankerSettings
	Embedding   EmbeddingSettings
	Search      SearchSettings
	RateLimit   RateLimitSettings
	InternalAPI InternalAPISettings
}

type AppSettings struct {
	Environment string
	Debug       bool
	LogLevel    string
}

func (a *AppSettings) IsDevelopment() bool {
	return strings.ToLower(a.Environment) == "development"
}

type DatabaseSettings struct {
	Host         string
	Port         int
	User         string
	Password     string
	Name         string
	SSLMode      string
	Timezone     string
	MaxOpenConns int
	MinConns     int
}

const minimumCPFHashSecretBytes = 32

func (db *DatabaseSettings) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		db.Host, db.Port, db.User, db.Password, db.Name, db.SSLMode, db.Timezone,
	)
}

func (db *DatabaseSettings) URL() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		db.User, db.Password, db.Host, db.Port, db.Name, db.SSLMode,
	)
}

func (db *DatabaseSettings) Validate() error {
	if db.Host == "" {
		return errors.New("DB_HOST não pode estar vazio")
	}
	if db.Port <= 0 {
		return errors.New("DB_PORT deve ser maior que zero")
	}
	return nil
}

type ServerSettings struct {
	Host              string
	Port              int
	RequestTimeout    time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ShutdownTimeout   time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	TrustedProxies    []string
}

const maximumHTTPHeaderBytes = 1 << 20

func (settings ServerSettings) Validate() error {
	if strings.TrimSpace(settings.Host) == "" {
		return errors.New("SERVER_HOST não pode estar vazio")
	}
	if settings.Port <= 0 || settings.Port > 65535 {
		return errors.New("SERVER_PORT deve ser uma porta TCP válida")
	}
	if settings.RequestTimeout <= 0 {
		return errors.New("SERVER_REQUEST_TIMEOUT deve ser maior que zero")
	}
	if settings.ReadHeaderTimeout <= 0 {
		return errors.New("SERVER_READ_HEADER_TIMEOUT deve ser maior que zero")
	}
	if settings.ReadTimeout < settings.ReadHeaderTimeout {
		return errors.New("SERVER_READ_TIMEOUT deve ser maior ou igual a SERVER_READ_HEADER_TIMEOUT")
	}
	if settings.WriteTimeout <= settings.RequestTimeout {
		return errors.New("SERVER_WRITE_TIMEOUT deve ser maior que SERVER_REQUEST_TIMEOUT")
	}
	if settings.ShutdownTimeout <= settings.WriteTimeout {
		return errors.New("SERVER_SHUTDOWN_TIMEOUT deve ser maior que SERVER_WRITE_TIMEOUT")
	}
	if settings.IdleTimeout <= 0 {
		return errors.New("SERVER_IDLE_TIMEOUT deve ser maior que zero")
	}
	if settings.MaxHeaderBytes <= 0 || settings.MaxHeaderBytes > maximumHTTPHeaderBytes {
		return fmt.Errorf("SERVER_MAX_HEADER_BYTES deve estar entre 1 e %d", maximumHTTPHeaderBytes)
	}
	for _, trustedProxy := range settings.TrustedProxies {
		if _, normalizationError := normalizeTrustedProxy(trustedProxy); normalizationError != nil {
			return fmt.Errorf("SERVER_TRUSTED_PROXIES contém %q inválido: %w", trustedProxy, normalizationError)
		}
	}
	return nil
}

type RateLimitSettings struct {
	RequestsPerWindow int
	Window            time.Duration
	RedisTimeout      time.Duration
	KeySecret         string
}

type InternalAPISettings struct {
	Key                        string
	CatalogSearchSignatureSkew time.Duration
}

const (
	minimumInternalAPIKeyBytes        = 32
	minimumCatalogSearchSignatureSkew = time.Second
	maximumCatalogSearchSignatureSkew = 10 * time.Minute
)

func (settings InternalAPISettings) Validate() error {
	if len(settings.Key) < minimumInternalAPIKeyBytes {
		return fmt.Errorf(
			"APP_CATALOGO_INTERNAL_API_KEY must contain at least %d bytes",
			minimumInternalAPIKeyBytes,
		)
	}
	for byteIndex := range len(settings.Key) {
		if settings.Key[byteIndex] < 0x21 || settings.Key[byteIndex] > 0x7e {
			return errors.New("APP_CATALOGO_INTERNAL_API_KEY must contain visible ASCII characters only")
		}
	}
	if settings.CatalogSearchSignatureSkew < minimumCatalogSearchSignatureSkew ||
		settings.CatalogSearchSignatureSkew > maximumCatalogSearchSignatureSkew {
		return fmt.Errorf(
			"APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW must be between %s and %s",
			minimumCatalogSearchSignatureSkew,
			maximumCatalogSearchSignatureSkew,
		)
	}
	return nil
}

const minimumRateLimitKeySecretBytes = 32

func (settings RateLimitSettings) Validate() error {
	if settings.RequestsPerWindow <= 0 {
		return errors.New("RATE_LIMIT_REQUESTS deve ser maior que zero")
	}
	if settings.Window < time.Millisecond {
		return errors.New("RATE_LIMIT_WINDOW deve ser de pelo menos um milissegundo")
	}
	if settings.RedisTimeout <= 0 || settings.RedisTimeout > settings.Window {
		return errors.New("RATE_LIMIT_REDIS_TIMEOUT deve ser maior que zero e não superar RATE_LIMIT_WINDOW")
	}
	if len(settings.KeySecret) < minimumRateLimitKeySecretBytes {
		return fmt.Errorf("RATE_LIMIT_KEY_SECRET deve conter pelo menos %d bytes", minimumRateLimitKeySecretBytes)
	}
	return nil
}

type RedisSettings struct {
	Host         string
	Port         int
	Password     string
	DB           int
	PoolSize     int
	MinIdleConns int
}

type TracingSettings struct {
	Enabled        bool
	Endpoint       string
	ServiceName    string
	ServiceVersion string
}

type MigrationSettings struct {
	Run bool
}

type KeycloakSettings struct {
	URL          string
	Realm        string
	ClientID     string
	ClientSecret string
}

type JWTSettings struct {
	Issuer                    string
	JWKSURL                   string
	Audience                  string
	AuthorizedParty           string
	RoleClientID              string
	ClockSkew                 time.Duration
	JWKSCacheTTL              time.Duration
	UnknownKeyRefreshInterval time.Duration
	HTTPTimeout               time.Duration
}

const maximumJWTConfiguredClaimBytes = 256

func (settings JWTSettings) Validate() error {
	for settingName, settingURL := range map[string]string{
		"AUTH_JWT_ISSUER":   settings.Issuer,
		"AUTH_JWT_JWKS_URL": settings.JWKSURL,
	} {
		parsedURL, parseError := url.Parse(settingURL)
		if parseError != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" ||
			parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" ||
			parsedURL.String() != settingURL {
			return fmt.Errorf("%s must be a canonical absolute HTTPS URL", settingName)
		}
	}
	for settingName, claimValue := range map[string]string{
		"AUTH_JWT_AUDIENCE":       settings.Audience,
		"AUTH_JWT_ROLE_CLIENT_ID": settings.RoleClientID,
	} {
		if !validJWTConfiguredClaim(claimValue) {
			return fmt.Errorf("%s must be a bounded non-empty value", settingName)
		}
	}
	if settings.AuthorizedParty != "" && !validJWTConfiguredClaim(settings.AuthorizedParty) {
		return errors.New("AUTH_JWT_AUTHORIZED_PARTY must be a bounded value")
	}
	for settingName, settingDuration := range map[string]time.Duration{
		"AUTH_JWT_CLOCK_SKEW":                   settings.ClockSkew,
		"AUTH_JWT_JWKS_CACHE_TTL":               settings.JWKSCacheTTL,
		"AUTH_JWT_UNKNOWN_KEY_REFRESH_INTERVAL": settings.UnknownKeyRefreshInterval,
		"AUTH_JWT_HTTP_TIMEOUT":                 settings.HTTPTimeout,
	} {
		if settingDuration <= 0 {
			return fmt.Errorf("%s must be positive", settingName)
		}
	}
	if settings.UnknownKeyRefreshInterval > settings.JWKSCacheTTL {
		return errors.New("AUTH_JWT_UNKNOWN_KEY_REFRESH_INTERVAL must not exceed AUTH_JWT_JWKS_CACHE_TTL")
	}
	return nil
}

func validJWTConfiguredClaim(claimValue string) bool {
	return claimValue != "" && len(claimValue) <= maximumJWTConfiguredClaimBytes &&
		strings.IndexFunc(claimValue, unicode.IsControl) == -1
}

type RMISettings struct {
	BaseURL string
}

type AppGoAPISettings struct {
	BaseURL      string
	SyncInterval time.Duration
	SyncEnabled  bool
}

type SalesForceSettings struct {
	InstanceURL      string
	ClientID         string
	ClientSecret     string
	WebhookSecret    string
	SyncInterval     time.Duration
	FullSyncInterval time.Duration
	ObjectType       string
}

const minimumSalesForceWebhookSecretBytes = 32

func (settings SalesForceSettings) Enabled() bool {
	return strings.TrimSpace(settings.InstanceURL) != ""
}

func (settings SalesForceSettings) Validate() error {
	if !settings.Enabled() {
		return nil
	}
	if len(settings.WebhookSecret) < minimumSalesForceWebhookSecretBytes {
		return fmt.Errorf(
			"SALESFORCE_WEBHOOK_SECRET deve conter pelo menos %d bytes quando Salesforce está habilitado",
			minimumSalesForceWebhookSecretBytes,
		)
	}
	return nil
}

type CitizenSyncSettings struct {
	StaleThreshold time.Duration
}

type CacheSettings struct {
	SearchTTL                      time.Duration
	RecommendationAuthenticatedTTL time.Duration
	RecommendationAnonymousTTL     time.Duration
}

// The historical environment-variable name is externally managed through the
// catalogo-secrets object. Keep that deployment contract until its owner can
// coordinate an atomic secret and application rollout.
const recommendationAnonymousTTLEnvironmentVariable = "CACHE_RECOMMENDATION_CLUSTER_TTL"

type SwaggerSettings struct {
	Host string
}

type HeimdallSettings struct {
	BaseURL string
}

type TypesenseSettings struct {
	URL              string
	APIKey           string
	Collection       string
	BaseServiceURL   string
	SyncInterval     time.Duration
	FullSyncInterval time.Duration
	SyncEnabled      bool
}

type GeminiSettings struct {
	APIKey string
}

type RerankerSettings struct {
	URL     string
	Timeout time.Duration
}

type EmbeddingSettings struct {
	BackfillInterval time.Duration
	RequestTimeout   time.Duration
}

type SearchSettings struct {
	RankerVersion           string
	RerankerVersion         string
	CandidatePoolSize       int
	SemanticOverfetchFactor int
	MaximumSemanticDistance float64
	SemanticTimeout         time.Duration
	HyDEEnabled             bool
	ExactWeight             float64
	FullTextWeight          float64
	TrigramWeight           float64
	SemanticWeight          float64
	HyDEWeight              float64
}

const (
	maximumSearchCandidatePoolSize = 200
	maximumSemanticOverfetchFactor = 10
	maximumSemanticDistance        = 2.0
)

var searchRankerVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func (settings SearchSettings) Validate() error {
	if !searchRankerVersionPattern.MatchString(strings.TrimSpace(settings.RankerVersion)) {
		return errors.New("SEARCH_RANKER_VERSION deve ser um identificador estável")
	}
	if settings.RerankerVersion != "" && !searchRankerVersionPattern.MatchString(strings.TrimSpace(settings.RerankerVersion)) {
		return errors.New("SEARCH_RERANKER_VERSION deve ser um identificador estável")
	}
	if settings.CandidatePoolSize < 1 || settings.CandidatePoolSize > maximumSearchCandidatePoolSize {
		return fmt.Errorf("SEARCH_CANDIDATE_POOL_SIZE deve estar entre 1 e %d", maximumSearchCandidatePoolSize)
	}
	if settings.SemanticOverfetchFactor < 1 || settings.SemanticOverfetchFactor > maximumSemanticOverfetchFactor {
		return fmt.Errorf(
			"SEARCH_SEMANTIC_OVERFETCH_FACTOR deve estar entre 1 e %d",
			maximumSemanticOverfetchFactor,
		)
	}
	if settings.SemanticTimeout <= 0 {
		return errors.New("SEARCH_SEMANTIC_TIMEOUT deve ser maior que zero")
	}
	if math.IsNaN(settings.MaximumSemanticDistance) || math.IsInf(settings.MaximumSemanticDistance, 0) ||
		settings.MaximumSemanticDistance <= 0 || settings.MaximumSemanticDistance > maximumSemanticDistance {
		return fmt.Errorf("SEARCH_MAX_SEMANTIC_DISTANCE deve ser maior que zero e no máximo %.1f", maximumSemanticDistance)
	}

	weights := []float64{
		settings.ExactWeight,
		settings.FullTextWeight,
		settings.TrigramWeight,
		settings.SemanticWeight,
		settings.HyDEWeight,
	}
	positiveWeight := false
	for _, weight := range weights {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
			return errors.New("pesos SEARCH_*_WEIGHT devem ser números finitos não negativos")
		}
		positiveWeight = positiveWeight || weight > 0
	}
	if !positiveWeight {
		return errors.New("ao menos um peso SEARCH_*_WEIGHT deve ser maior que zero")
	}
	return nil
}

var (
	instance *AppConfig
	once     sync.Once
	mu       sync.RWMutex
	v        *viper.Viper
)

func Initialize() error {
	v = viper.New()
	v.AutomaticEnv()
	v.SetConfigType("env")
	v.SetConfigName(".env")
	v.AddConfigPath(".")
	v.WatchConfig()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Printf("Aviso: erro ao ler .env: %v", err)
		}
	}

	return nil
}

func Load() (*AppConfig, error) {
	if v == nil {
		if err := Initialize(); err != nil {
			return nil, err
		}
	}
	searchSettings, searchSettingsError := loadSearchSettings(v)
	if searchSettingsError != nil {
		return nil, fmt.Errorf("configuração de busca inválida: %w", searchSettingsError)
	}
	embeddingSettings, embeddingSettingsError := loadEmbeddingSettings(v)
	if embeddingSettingsError != nil {
		return nil, fmt.Errorf("configuração de embeddings inválida: %w", embeddingSettingsError)
	}
	serverSettings, serverSettingsError := loadServerSettings(v)
	if serverSettingsError != nil {
		return nil, fmt.Errorf("configuração de servidor inválida: %w", serverSettingsError)
	}
	rateLimitSettings, rateLimitSettingsError := loadRateLimitSettings(v)
	if rateLimitSettingsError != nil {
		return nil, fmt.Errorf("configuração de rate limit inválida: %w", rateLimitSettingsError)
	}
	internalAPISettings, internalAPISettingsError := loadInternalAPISettings(v)
	if internalAPISettingsError != nil {
		return nil, fmt.Errorf("invalid internal API configuration: %w", internalAPISettingsError)
	}
	jwtSettings, jwtSettingsError := loadJWTSettings(v)
	if jwtSettingsError != nil {
		return nil, fmt.Errorf("invalid JWT configuration: %w", jwtSettingsError)
	}
	salesForceSettings, salesForceSettingsError := loadSalesForceSettings(v)
	if salesForceSettingsError != nil {
		return nil, fmt.Errorf("configuração do Salesforce inválida: %w", salesForceSettingsError)
	}
	appGoAPISettings, appGoAPISettingsError := loadAppGoAPISettings(v)
	if appGoAPISettingsError != nil {
		return nil, fmt.Errorf("invalid app-go-api configuration: %w", appGoAPISettingsError)
	}
	typesenseSettings, typesenseSettingsError := loadTypesenseSettings(v)
	if typesenseSettingsError != nil {
		return nil, fmt.Errorf("invalid Typesense configuration: %w", typesenseSettingsError)
	}
	databaseSettings, databaseSettingsError := loadDatabaseSettings(v)
	if databaseSettingsError != nil {
		return nil, fmt.Errorf("configuração de banco inválida: %w", databaseSettingsError)
	}
	cacheSettings, cacheSettingsError := loadCacheSettings(v)
	if cacheSettingsError != nil {
		return nil, fmt.Errorf("configuração de cache inválida: %w", cacheSettingsError)
	}
	cpfHashSecret := getEnv(v, "CPF_HASH_SALT", "")
	if cpfHashSecretError := validateCPFHashSecret(cpfHashSecret); cpfHashSecretError != nil {
		return nil, cpfHashSecretError
	}

	cfg := &AppConfig{
		App: AppSettings{
			Environment: getEnv(v, "APP_ENV", "development"),
			Debug:       getBool(v, "APP_DEBUG", true),
			LogLevel:    getEnv(v, "LOG_LEVEL", "info"),
		},
		Database: databaseSettings,
		Server:   serverSettings,
		Redis: RedisSettings{
			Host:         getEnv(v, "REDIS_HOST", "localhost"),
			Port:         getInt(v, "REDIS_PORT", 6379),
			Password:     getEnv(v, "REDIS_PASSWORD", ""),
			DB:           getInt(v, "REDIS_DB", 0),
			PoolSize:     getInt(v, "REDIS_POOL_SIZE", 10),
			MinIdleConns: getInt(v, "REDIS_MIN_IDLE_CONNS", 2),
		},
		Tracing: TracingSettings{
			Enabled:        getBool(v, "TRACING_ENABLED", false),
			Endpoint:       getEnv(v, "TRACING_ENDPOINT", "localhost:4317"),
			ServiceName:    getEnv(v, "TRACING_SERVICE_NAME", "app-catalogo"),
			ServiceVersion: getEnv(v, "TRACING_SERVICE_VERSION", "v1.0.0"),
		},
		Migrations: MigrationSettings{
			Run: getBool(v, "RUN_MIGRATIONS", false),
		},
		Keycloak: KeycloakSettings{
			URL:          getEnv(v, "KEYCLOAK_URL", ""),
			Realm:        getEnv(v, "KEYCLOAK_REALM", ""),
			ClientID:     getEnv(v, "KEYCLOAK_CLIENT_ID", ""),
			ClientSecret: getEnv(v, "KEYCLOAK_CLIENT_SECRET", ""),
		},
		JWT: jwtSettings,
		RMI: RMISettings{
			BaseURL: getEnv(v, "RMI_BASE_URL", ""),
		},
		AppGoAPI:   appGoAPISettings,
		SalesForce: salesForceSettings,
		CitizenSync: CitizenSyncSettings{
			StaleThreshold: getDuration(v, "CITIZEN_PROFILE_STALE_THRESHOLD", 1*time.Hour),
		},
		Cache:       cacheSettings,
		CPFHashSalt: cpfHashSecret,
		Swagger: SwaggerSettings{
			Host: getEnv(v, "SWAGGER_HOST", "localhost:8080"),
		},
		Heimdall: HeimdallSettings{
			BaseURL: getEnv(v, "HEIMDALL_BASE_URL", ""),
		},
		Typesense: typesenseSettings,
		Gemini: GeminiSettings{
			APIKey: getGeminiAPIKey(v),
		},
		Reranker: RerankerSettings{
			URL:     getEnv(v, "RERANKER_URL", ""),
			Timeout: getDuration(v, "RERANKER_TIMEOUT", 2*time.Second),
		},
		Embedding:   embeddingSettings,
		Search:      searchSettings,
		RateLimit:   rateLimitSettings,
		InternalAPI: internalAPISettings,
	}
	if strings.TrimSpace(cfg.Reranker.URL) != "" && cfg.Search.RerankerVersion == "" {
		return nil, errors.New("SEARCH_RERANKER_VERSION is required when RERANKER_URL is configured")
	}
	return cfg, nil
}

func loadCacheSettings(configuration *viper.Viper) (CacheSettings, error) {
	searchTTL, searchTTLError := strictDurationSetting(configuration, "CACHE_SEARCH_TTL", 60*time.Second)
	if searchTTLError != nil {
		return CacheSettings{}, searchTTLError
	}
	authenticatedTTL, authenticatedTTLError := strictDurationSetting(
		configuration,
		"CACHE_RECOMMENDATION_AUTHENTICATED_TTL",
		5*time.Minute,
	)
	if authenticatedTTLError != nil {
		return CacheSettings{}, authenticatedTTLError
	}
	anonymousTTL, anonymousTTLError := strictDurationSetting(
		configuration,
		recommendationAnonymousTTLEnvironmentVariable,
		15*time.Minute,
	)
	if anonymousTTLError != nil {
		return CacheSettings{}, anonymousTTLError
	}

	settings := CacheSettings{
		SearchTTL:                      searchTTL,
		RecommendationAuthenticatedTTL: authenticatedTTL,
		RecommendationAnonymousTTL:     anonymousTTL,
	}
	for _, setting := range []struct {
		name  string
		value time.Duration
	}{
		{name: "CACHE_SEARCH_TTL", value: settings.SearchTTL},
		{name: "CACHE_RECOMMENDATION_AUTHENTICATED_TTL", value: settings.RecommendationAuthenticatedTTL},
		{name: recommendationAnonymousTTLEnvironmentVariable, value: settings.RecommendationAnonymousTTL},
	} {
		if setting.value <= 0 {
			return CacheSettings{}, fmt.Errorf("%s deve ser maior que zero", setting.name)
		}
	}
	return settings, nil
}

func loadDatabaseSettings(configuration *viper.Viper) (DatabaseSettings, error) {
	settings := DatabaseSettings{
		Host:         getEnv(configuration, "DB_HOST", "localhost"),
		Port:         getInt(configuration, "DB_PORT", 5432),
		User:         getEnv(configuration, "DB_USER", "catalogo"),
		Password:     getEnv(configuration, "DB_PASSWORD", "catalogo"),
		Name:         getEnv(configuration, "DB_NAME", "catalogo"),
		SSLMode:      getEnv(configuration, "DB_SSL_MODE", "disable"),
		Timezone:     getEnv(configuration, "DB_TIMEZONE", "America/Sao_Paulo"),
		MaxOpenConns: getInt(configuration, "DB_MAX_OPEN_CONNS", 25),
		MinConns:     getInt(configuration, "DB_MIN_CONNS", 5),
	}
	if validationError := settings.Validate(); validationError != nil {
		return DatabaseSettings{}, validationError
	}
	return settings, nil
}

// LoadDatabaseSettings loads only the settings required by migration tooling.
// It deliberately avoids validating unrelated API, worker, or rate-limit
// configuration so migrations remain independently operable.
func LoadDatabaseSettings() (DatabaseSettings, error) {
	if v == nil {
		if initializationError := Initialize(); initializationError != nil {
			return DatabaseSettings{}, initializationError
		}
	}
	return loadDatabaseSettings(v)
}

func validateCPFHashSecret(secret string) error {
	if len(secret) < minimumCPFHashSecretBytes {
		return fmt.Errorf("CPF_HASH_SALT deve conter pelo menos %d bytes", minimumCPFHashSecretBytes)
	}
	return nil
}

func loadServerSettings(configuration *viper.Viper) (ServerSettings, error) {
	port, portError := strictIntegerSetting(configuration, "SERVER_PORT", 8080)
	if portError != nil {
		return ServerSettings{}, portError
	}
	requestTimeout, requestTimeoutError := strictDurationOrSecondsSetting(
		configuration,
		"SERVER_REQUEST_TIMEOUT",
		30*time.Second,
	)
	if requestTimeoutError != nil {
		return ServerSettings{}, requestTimeoutError
	}
	readHeaderTimeout, readHeaderTimeoutError := strictDurationSetting(
		configuration,
		"SERVER_READ_HEADER_TIMEOUT",
		5*time.Second,
	)
	if readHeaderTimeoutError != nil {
		return ServerSettings{}, readHeaderTimeoutError
	}
	readTimeout, readTimeoutError := strictDurationSetting(configuration, "SERVER_READ_TIMEOUT", 15*time.Second)
	if readTimeoutError != nil {
		return ServerSettings{}, readTimeoutError
	}
	writeTimeout, writeTimeoutError := strictDurationSetting(configuration, "SERVER_WRITE_TIMEOUT", 35*time.Second)
	if writeTimeoutError != nil {
		return ServerSettings{}, writeTimeoutError
	}
	shutdownTimeout, shutdownTimeoutError := strictDurationSetting(
		configuration,
		"SERVER_SHUTDOWN_TIMEOUT",
		40*time.Second,
	)
	if shutdownTimeoutError != nil {
		return ServerSettings{}, shutdownTimeoutError
	}
	idleTimeout, idleTimeoutError := strictDurationSetting(configuration, "SERVER_IDLE_TIMEOUT", 60*time.Second)
	if idleTimeoutError != nil {
		return ServerSettings{}, idleTimeoutError
	}
	maximumHeaderBytes, maximumHeaderBytesError := strictIntegerSetting(
		configuration,
		"SERVER_MAX_HEADER_BYTES",
		maximumHTTPHeaderBytes,
	)
	if maximumHeaderBytesError != nil {
		return ServerSettings{}, maximumHeaderBytesError
	}
	trustedProxies, trustedProxiesError := parseTrustedProxies(getEnv(configuration, "SERVER_TRUSTED_PROXIES", ""))
	if trustedProxiesError != nil {
		return ServerSettings{}, trustedProxiesError
	}

	settings := ServerSettings{
		Host:              strings.TrimSpace(getEnv(configuration, "SERVER_HOST", "0.0.0.0")),
		Port:              port,
		RequestTimeout:    requestTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		ShutdownTimeout:   shutdownTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maximumHeaderBytes,
		TrustedProxies:    trustedProxies,
	}
	if validationError := settings.Validate(); validationError != nil {
		return ServerSettings{}, validationError
	}
	return settings, nil
}

func loadRateLimitSettings(configuration *viper.Viper) (RateLimitSettings, error) {
	requestsPerWindow, requestsPerWindowError := strictIntegerSetting(configuration, "RATE_LIMIT_REQUESTS", 300)
	if requestsPerWindowError != nil {
		return RateLimitSettings{}, requestsPerWindowError
	}
	window, windowError := strictDurationSetting(configuration, "RATE_LIMIT_WINDOW", time.Minute)
	if windowError != nil {
		return RateLimitSettings{}, windowError
	}
	redisTimeout, redisTimeoutError := strictDurationSetting(configuration, "RATE_LIMIT_REDIS_TIMEOUT", 100*time.Millisecond)
	if redisTimeoutError != nil {
		return RateLimitSettings{}, redisTimeoutError
	}

	settings := RateLimitSettings{
		RequestsPerWindow: requestsPerWindow,
		Window:            window,
		RedisTimeout:      redisTimeout,
		KeySecret:         getEnv(configuration, "RATE_LIMIT_KEY_SECRET", ""),
	}
	if validationError := settings.Validate(); validationError != nil {
		return RateLimitSettings{}, validationError
	}
	return settings, nil
}

func loadInternalAPISettings(configuration *viper.Viper) (InternalAPISettings, error) {
	signatureSkew, signatureSkewError := strictDurationSetting(
		configuration,
		"APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW",
		2*time.Minute,
	)
	if signatureSkewError != nil {
		return InternalAPISettings{}, signatureSkewError
	}
	settings := InternalAPISettings{
		Key:                        getEnv(configuration, "APP_CATALOGO_INTERNAL_API_KEY", ""),
		CatalogSearchSignatureSkew: signatureSkew,
	}
	if validationError := settings.Validate(); validationError != nil {
		return InternalAPISettings{}, validationError
	}
	return settings, nil
}

func loadJWTSettings(configuration *viper.Viper) (JWTSettings, error) {
	clockSkew, clockSkewError := strictDurationSetting(configuration, "AUTH_JWT_CLOCK_SKEW", 30*time.Second)
	if clockSkewError != nil {
		return JWTSettings{}, clockSkewError
	}
	cacheTTL, cacheTTLError := strictDurationSetting(configuration, "AUTH_JWT_JWKS_CACHE_TTL", 15*time.Minute)
	if cacheTTLError != nil {
		return JWTSettings{}, cacheTTLError
	}
	unknownRefreshInterval, unknownRefreshError := strictDurationSetting(
		configuration,
		"AUTH_JWT_UNKNOWN_KEY_REFRESH_INTERVAL",
		30*time.Second,
	)
	if unknownRefreshError != nil {
		return JWTSettings{}, unknownRefreshError
	}
	httpTimeout, httpTimeoutError := strictDurationSetting(configuration, "AUTH_JWT_HTTP_TIMEOUT", 3*time.Second)
	if httpTimeoutError != nil {
		return JWTSettings{}, httpTimeoutError
	}
	settings := JWTSettings{
		Issuer:                    strings.TrimSpace(getEnv(configuration, "AUTH_JWT_ISSUER", "")),
		JWKSURL:                   strings.TrimSpace(getEnv(configuration, "AUTH_JWT_JWKS_URL", "")),
		Audience:                  strings.TrimSpace(getEnv(configuration, "AUTH_JWT_AUDIENCE", "")),
		AuthorizedParty:           strings.TrimSpace(getEnv(configuration, "AUTH_JWT_AUTHORIZED_PARTY", "")),
		RoleClientID:              strings.TrimSpace(getEnv(configuration, "AUTH_JWT_ROLE_CLIENT_ID", "")),
		ClockSkew:                 clockSkew,
		JWKSCacheTTL:              cacheTTL,
		UnknownKeyRefreshInterval: unknownRefreshInterval,
		HTTPTimeout:               httpTimeout,
	}
	if validationError := settings.Validate(); validationError != nil {
		return JWTSettings{}, validationError
	}
	return settings, nil
}

func loadSalesForceSettings(configuration *viper.Viper) (SalesForceSettings, error) {
	syncInterval, syncIntervalError := strictDurationSetting(
		configuration,
		"SALESFORCE_SYNC_INTERVAL",
		15*time.Minute,
	)
	if syncIntervalError != nil {
		return SalesForceSettings{}, syncIntervalError
	}
	fullSyncInterval, fullSyncIntervalError := strictDurationSetting(
		configuration,
		"SALESFORCE_FULL_SYNC_INTERVAL",
		24*time.Hour,
	)
	if fullSyncIntervalError != nil {
		return SalesForceSettings{}, fullSyncIntervalError
	}
	if syncInterval <= 0 {
		return SalesForceSettings{}, errors.New("SALESFORCE_SYNC_INTERVAL deve ser maior que zero")
	}
	if fullSyncInterval <= 0 {
		return SalesForceSettings{}, errors.New("SALESFORCE_FULL_SYNC_INTERVAL deve ser maior que zero")
	}

	settings := SalesForceSettings{
		InstanceURL:      strings.TrimSpace(getEnv(configuration, "SALESFORCE_INSTANCE_URL", "")),
		ClientID:         getEnv(configuration, "SALESFORCE_CLIENT_ID", ""),
		ClientSecret:     getEnv(configuration, "SALESFORCE_CLIENT_SECRET", ""),
		WebhookSecret:    getEnv(configuration, "SALESFORCE_WEBHOOK_SECRET", ""),
		SyncInterval:     syncInterval,
		FullSyncInterval: fullSyncInterval,
		ObjectType:       getEnv(configuration, "SALESFORCE_OBJECT_TYPE", "Service__c"),
	}
	if validationError := settings.Validate(); validationError != nil {
		return SalesForceSettings{}, validationError
	}
	return settings, nil
}

func loadTypesenseSettings(configuration *viper.Viper) (TypesenseSettings, error) {
	syncInterval, syncIntervalError := strictDurationSetting(
		configuration,
		"TYPESENSE_SYNC_INTERVAL",
		30*time.Minute,
	)
	if syncIntervalError != nil {
		return TypesenseSettings{}, syncIntervalError
	}
	fullSyncInterval, fullSyncIntervalError := strictDurationSetting(
		configuration,
		"TYPESENSE_FULL_SYNC_INTERVAL",
		24*time.Hour,
	)
	if fullSyncIntervalError != nil {
		return TypesenseSettings{}, fullSyncIntervalError
	}
	if syncInterval <= 0 {
		return TypesenseSettings{}, errors.New("TYPESENSE_SYNC_INTERVAL must be positive")
	}
	if fullSyncInterval < syncInterval {
		return TypesenseSettings{}, errors.New("TYPESENSE_FULL_SYNC_INTERVAL must not be shorter than TYPESENSE_SYNC_INTERVAL")
	}
	return TypesenseSettings{
		URL:              strings.TrimSpace(getEnv(configuration, "TYPESENSE_URL", "")),
		APIKey:           getEnv(configuration, "TYPESENSE_API_KEY", ""),
		Collection:       getEnv(configuration, "TYPESENSE_COLLECTION", "prefrio_services_base"),
		BaseServiceURL:   getEnv(configuration, "TYPESENSE_BASE_SERVICE_URL", "https://prefeitura.rio"),
		SyncInterval:     syncInterval,
		FullSyncInterval: fullSyncInterval,
		SyncEnabled:      getBool(configuration, "TYPESENSE_SYNC_ENABLED", true),
	}, nil
}

func loadAppGoAPISettings(configuration *viper.Viper) (AppGoAPISettings, error) {
	syncInterval, syncIntervalError := strictDurationSetting(
		configuration,
		"APP_GO_API_SYNC_INTERVAL",
		30*time.Minute,
	)
	if syncIntervalError != nil {
		return AppGoAPISettings{}, syncIntervalError
	}
	if syncInterval <= 0 {
		return AppGoAPISettings{}, errors.New("APP_GO_API_SYNC_INTERVAL must be positive")
	}
	return AppGoAPISettings{
		BaseURL:      strings.TrimSpace(getEnv(configuration, "APP_GO_API_BASE_URL", "")),
		SyncInterval: syncInterval,
		SyncEnabled:  getBool(configuration, "APP_GO_API_SYNC_ENABLED", true),
	}, nil
}

func parseTrustedProxies(rawTrustedProxies string) ([]string, error) {
	if strings.TrimSpace(rawTrustedProxies) == "" {
		return nil, nil
	}

	trustedProxies := make([]string, 0)
	seenTrustedProxies := make(map[string]struct{})
	for _, rawTrustedProxy := range strings.Split(rawTrustedProxies, ",") {
		trustedProxy := strings.TrimSpace(rawTrustedProxy)
		if trustedProxy == "" {
			return nil, errors.New("SERVER_TRUSTED_PROXIES contém uma entrada vazia")
		}

		normalizedTrustedProxy, normalizationError := normalizeTrustedProxy(trustedProxy)
		if normalizationError != nil {
			return nil, fmt.Errorf("SERVER_TRUSTED_PROXIES contém %q inválido: %w", trustedProxy, normalizationError)
		}
		if _, alreadySeen := seenTrustedProxies[normalizedTrustedProxy]; alreadySeen {
			continue
		}
		seenTrustedProxies[normalizedTrustedProxy] = struct{}{}
		trustedProxies = append(trustedProxies, normalizedTrustedProxy)
	}
	return trustedProxies, nil
}

func normalizeTrustedProxy(trustedProxy string) (string, error) {
	if strings.Contains(trustedProxy, "/") {
		prefix, prefixError := netip.ParsePrefix(trustedProxy)
		if prefixError != nil {
			return "", errors.New("deve ser um IP ou CIDR válido")
		}
		if prefix.Bits() == 0 {
			return "", errors.New("não pode confiar em toda a Internet")
		}
		return prefix.Masked().String(), nil
	}

	address, addressError := netip.ParseAddr(trustedProxy)
	if addressError != nil || address.Zone() != "" {
		return "", errors.New("deve ser um IP ou CIDR válido")
	}
	return address.String(), nil
}

func loadSearchSettings(v *viper.Viper) (SearchSettings, error) {
	candidatePoolSize, candidatePoolError := strictIntegerSetting(v, "SEARCH_CANDIDATE_POOL_SIZE", 40)
	if candidatePoolError != nil {
		return SearchSettings{}, candidatePoolError
	}
	semanticOverfetchFactor, overfetchFactorError := strictIntegerSetting(
		v,
		"SEARCH_SEMANTIC_OVERFETCH_FACTOR",
		4,
	)
	if overfetchFactorError != nil {
		return SearchSettings{}, overfetchFactorError
	}
	semanticTimeout, semanticTimeoutError := strictDurationSetting(v, "SEARCH_SEMANTIC_TIMEOUT", 3*time.Second)
	if semanticTimeoutError != nil {
		return SearchSettings{}, semanticTimeoutError
	}
	hydeEnabled, hydeEnabledError := strictBooleanSetting(v, "SEARCH_HYDE_ENABLED", false)
	if hydeEnabledError != nil {
		return SearchSettings{}, hydeEnabledError
	}
	maximumDistance, maximumDistanceError := strictFloatSetting(v, "SEARCH_MAX_SEMANTIC_DISTANCE", 1.0)
	if maximumDistanceError != nil {
		return SearchSettings{}, maximumDistanceError
	}

	weightSettings := []struct {
		key          string
		defaultValue float64
	}{
		{key: "SEARCH_EXACT_WEIGHT", defaultValue: 3.0},
		{key: "SEARCH_FULL_TEXT_WEIGHT", defaultValue: 1.0},
		{key: "SEARCH_TRIGRAM_WEIGHT", defaultValue: 1.0},
		{key: "SEARCH_SEMANTIC_WEIGHT", defaultValue: 1.0},
		{key: "SEARCH_HYDE_WEIGHT", defaultValue: 0.5},
	}
	weights := make([]float64, len(weightSettings))
	for weightIndex, weightSetting := range weightSettings {
		weight, weightError := strictFloatSetting(v, weightSetting.key, weightSetting.defaultValue)
		if weightError != nil {
			return SearchSettings{}, weightError
		}
		weights[weightIndex] = weight
	}

	settings := SearchSettings{
		RankerVersion:           strings.TrimSpace(getEnv(v, "SEARCH_RANKER_VERSION", "hybrid-rrf-v3")),
		RerankerVersion:         strings.TrimSpace(getEnv(v, "SEARCH_RERANKER_VERSION", "")),
		CandidatePoolSize:       candidatePoolSize,
		SemanticOverfetchFactor: semanticOverfetchFactor,
		MaximumSemanticDistance: maximumDistance,
		SemanticTimeout:         semanticTimeout,
		HyDEEnabled:             hydeEnabled,
		ExactWeight:             weights[0],
		FullTextWeight:          weights[1],
		TrigramWeight:           weights[2],
		SemanticWeight:          weights[3],
		HyDEWeight:              weights[4],
	}
	if validationError := settings.Validate(); validationError != nil {
		return SearchSettings{}, validationError
	}
	return settings, nil
}

func loadEmbeddingSettings(v *viper.Viper) (EmbeddingSettings, error) {
	backfillInterval, backfillIntervalError := strictDurationSetting(v, "EMBEDDING_BACKFILL_INTERVAL", 5*time.Minute)
	if backfillIntervalError != nil {
		return EmbeddingSettings{}, backfillIntervalError
	}
	requestTimeout, requestTimeoutError := strictDurationSetting(v, "EMBEDDING_REQUEST_TIMEOUT", 30*time.Second)
	if requestTimeoutError != nil {
		return EmbeddingSettings{}, requestTimeoutError
	}
	if backfillInterval <= 0 {
		return EmbeddingSettings{}, errors.New("EMBEDDING_BACKFILL_INTERVAL deve ser maior que zero")
	}
	if requestTimeout <= 0 {
		return EmbeddingSettings{}, errors.New("EMBEDDING_REQUEST_TIMEOUT deve ser maior que zero")
	}
	return EmbeddingSettings{
		BackfillInterval: backfillInterval,
		RequestTimeout:   requestTimeout,
	}, nil
}

func Get() (*AppConfig, error) {
	once.Do(func() {
		cfg, err := Load()
		if err != nil {
			log.Printf("Erro ao carregar configurações: %v", err)
			return
		}
		instance = cfg
	})

	if instance == nil {
		return nil, errors.New("falha ao inicializar configurações")
	}

	mu.RLock()
	defer mu.RUnlock()
	return instance, nil
}

func getEnv(v *viper.Viper, key, defaultValue string) string {
	if v.IsSet(key) {
		return v.GetString(key)
	}
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getGeminiAPIKey(v *viper.Viper) string {
	return getEnv(v, "GEMINI_API_KEY", "")
}

func getInt(v *viper.Viper, key string, defaultValue int) int {
	if v.IsSet(key) {
		return v.GetInt(key)
	}
	if value := os.Getenv(key); value != "" {
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
			return n
		}
	}
	return defaultValue
}

func getBool(v *viper.Viper, key string, defaultValue bool) bool {
	if v.IsSet(key) {
		return v.GetBool(key)
	}
	if value := os.Getenv(key); value != "" {
		return strings.ToLower(value) == "true"
	}
	return defaultValue
}

func strictSetting(v *viper.Viper, key string) (string, bool) {
	if v.IsSet(key) {
		return strings.TrimSpace(v.GetString(key)), true
	}
	value, present := os.LookupEnv(key)
	return strings.TrimSpace(value), present
}

func strictIntegerSetting(v *viper.Viper, key string, defaultValue int) (int, error) {
	rawValue, present := strictSetting(v, key)
	if !present || rawValue == "" {
		return defaultValue, nil
	}
	parsedValue, parseError := strconv.Atoi(rawValue)
	if parseError != nil {
		return 0, fmt.Errorf("%s deve ser um número inteiro válido", key)
	}
	return parsedValue, nil
}

func strictFloatSetting(v *viper.Viper, key string, defaultValue float64) (float64, error) {
	rawValue, present := strictSetting(v, key)
	if !present || rawValue == "" {
		return defaultValue, nil
	}
	parsedValue, parseError := strconv.ParseFloat(rawValue, 64)
	if parseError != nil {
		return 0, fmt.Errorf("%s deve ser um número decimal válido", key)
	}
	return parsedValue, nil
}

func strictBooleanSetting(v *viper.Viper, key string, defaultValue bool) (bool, error) {
	rawValue, present := strictSetting(v, key)
	if !present || rawValue == "" {
		return defaultValue, nil
	}
	switch strings.ToLower(rawValue) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s deve ser true ou false", key)
	}
}

func strictDurationSetting(v *viper.Viper, key string, defaultValue time.Duration) (time.Duration, error) {
	rawValue, present := strictSetting(v, key)
	if !present || rawValue == "" {
		return defaultValue, nil
	}
	parsedValue, parseError := time.ParseDuration(rawValue)
	if parseError != nil {
		return 0, fmt.Errorf("%s deve ser uma duração válida", key)
	}
	return parsedValue, nil
}

func strictDurationOrSecondsSetting(v *viper.Viper, key string, defaultValue time.Duration) (time.Duration, error) {
	rawValue, present := strictSetting(v, key)
	if !present || rawValue == "" {
		return defaultValue, nil
	}
	if seconds, parseSecondsError := strconv.Atoi(rawValue); parseSecondsError == nil {
		return time.Duration(seconds) * time.Second, nil
	}
	parsedValue, parseDurationError := time.ParseDuration(rawValue)
	if parseDurationError != nil {
		return 0, fmt.Errorf("%s deve ser uma duração válida ou segundos inteiros", key)
	}
	return parsedValue, nil
}

func getDuration(v *viper.Viper, key string, defaultValue time.Duration) time.Duration {
	if v.IsSet(key) {
		return v.GetDuration(key)
	}
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}
