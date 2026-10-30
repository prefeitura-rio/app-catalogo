package config

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

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
	Auth        AuthSettings
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
	Worker      WorkerSettings
}

// WorkerSettings configura o processo worker (cmd/worker).
type WorkerSettings struct {
	// MetricsPort é a porta em que o worker expõe GET /metrics (0 desativa).
	MetricsPort int
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
	Host           string
	Port           int
	RequestTimeout int
	// TrustedProxies são os IPs/CIDRs dos proxies cujo X-Forwarded-For é
	// aceito para resolver o IP do cliente (rate limiting e log de acesso).
	// Vazia, o IP é sempre o da conexão.
	TrustedProxies []string
}

func (s *ServerSettings) Validate() error {
	if s.Host == "" {
		return errors.New("SERVER_HOST não pode estar vazio")
	}
	if s.Port <= 0 {
		return errors.New("SERVER_PORT deve ser maior que zero")
	}
	for _, p := range s.TrustedProxies {
		if !validProxyAddress(p) {
			return fmt.Errorf("TRUSTED_PROXIES: %q não é IP nem CIDR válido", p)
		}
	}
	return nil
}

// defaultTrustedProxies é o padrão de TRUSTED_PROXIES: loopback (sidecar do
// Istio, que entrega as requisições a partir de 127.0.0.6), redes privadas
// (gateway do Istio e pods) e as faixas de onde os proxies do load balancer
// HTTP(S) do Google se conectam aos backends. O IP do forwarding rule do load
// balancer, que ele também acrescenta ao X-Forwarded-For, é de cada ambiente e
// vem dos manifests.
const defaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7,35.191.0.0/16,130.211.0.0/22"

// parseCSV converte uma lista separada por vírgula, sem espaços, itens vazios
// nem repetidos.
func parseCSV(raw string) []string {
	var items []string
	seen := map[string]struct{}{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		items = append(items, p)
	}
	return items
}

// validProxyAddress diz se p é um IP ou um CIDR, o que o Gin aceita em
// SetTrustedProxies.
func validProxyAddress(p string) bool {
	if strings.Contains(p, "/") {
		_, _, err := net.ParseCIDR(p)
		return err == nil
	}
	return net.ParseIP(p) != nil
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

// AuthSettings define como o JWT de X-Auth-Request-Token é verificado.
type AuthSettings struct {
	JWTIssuer string
	JWKSURL   string
	// AuthorizedParties são os clients do Keycloak (claim azp) cujos tokens o
	// serviço aceita. Vazia, nenhum token é aceito (falha fechada).
	AuthorizedParties []string
}

// defaultJWTAuthorizedParties é o client do superapp, padrão de AUTH_JWT_AUTHORIZED_PARTIES.
const defaultJWTAuthorizedParties = "superapp"

// parseAuthorizedParties converte a lista separada por vírgula de
// AUTH_JWT_AUTHORIZED_PARTIES, sem espaços, itens vazios nem repetidos.
func parseAuthorizedParties(raw string) []string {
	return parseCSV(raw)
}

// keycloakIssuer monta o emissor do realm (KEYCLOAK_URL/realms/KEYCLOAK_REALM).
func keycloakIssuer(keycloakURL, realm string) string {
	keycloakURL = strings.TrimRight(strings.TrimSpace(keycloakURL), "/")
	realm = strings.TrimSpace(realm)
	if keycloakURL == "" || realm == "" {
		return ""
	}
	return keycloakURL + "/realms/" + realm
}

func defaultJWKSURL(issuer string) string {
	if issuer == "" {
		return ""
	}
	return strings.TrimRight(issuer, "/") + "/protocol/openid-connect/certs"
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
	// InstanceURL é a base URL da API CloudHub da Carta de Serviços
	// (ex: https://dev-....cloudhub.io). O path /api é acrescentado automaticamente.
	InstanceURL string
	// BaseServiceURL monta o fallback de URL pública do serviço (/servicos/{slug}).
	BaseServiceURL string
	WebhookSecret  string
	SyncInterval   time.Duration
	SyncEnabled    bool
	// DetailConcurrency limita fetches paralelos de GET /services/{slug}.
	DetailConcurrency int
}

type CitizenSyncSettings struct {
	StaleThreshold time.Duration
	SyncInterval   time.Duration
}

type CacheSettings struct {
	SearchTTL                      time.Duration
	SearchVectorsTTL               time.Duration
	RecommendationAuthenticatedTTL time.Duration
	RecommendationClusterTTL       time.Duration
}

type SwaggerSettings struct {
	Host string
}

type HeimdallSettings struct {
	BaseURL string
}

type TypesenseSettings struct {
	URL            string
	APIKey         string
	Collection     string
	BaseServiceURL string
	SyncInterval   time.Duration
	SyncEnabled    bool
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
}

const minimumCPFHashSecretBytes = 32

func validateCPFHashSecret(secret string) error {
	if len(secret) < minimumCPFHashSecretBytes {
		return fmt.Errorf("CPF_HASH_SALT deve conter pelo menos %d bytes", minimumCPFHashSecretBytes)
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

	cfg := &AppConfig{
		App: AppSettings{
			Environment: getEnv(v, "APP_ENV", "development"),
			Debug:       getBool(v, "APP_DEBUG", true),
			LogLevel:    getEnv(v, "LOG_LEVEL", "info"),
		},
		Database: DatabaseSettings{
			Host:         getEnv(v, "DB_HOST", "localhost"),
			Port:         getInt(v, "DB_PORT", 5432),
			User:         getEnv(v, "DB_USER", "catalogo"),
			Password:     getEnv(v, "DB_PASSWORD", "catalogo"),
			Name:         getEnv(v, "DB_NAME", "catalogo"),
			SSLMode:      getEnv(v, "DB_SSL_MODE", "disable"),
			Timezone:     getEnv(v, "DB_TIMEZONE", "America/Sao_Paulo"),
			MaxOpenConns: getInt(v, "DB_MAX_OPEN_CONNS", 25),
			MinConns:     getInt(v, "DB_MIN_CONNS", 5),
		},
		Server: ServerSettings{
			Host:           getEnv(v, "SERVER_HOST", "0.0.0.0"),
			Port:           getInt(v, "SERVER_PORT", 8080),
			RequestTimeout: getInt(v, "SERVER_REQUEST_TIMEOUT", 30),
			TrustedProxies: parseCSV(getEnv(v, "TRUSTED_PROXIES", defaultTrustedProxies)),
		},
		Worker: WorkerSettings{
			MetricsPort: getInt(v, "WORKER_METRICS_PORT", 9091),
		},
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
		RMI: RMISettings{
			BaseURL: getEnv(v, "RMI_BASE_URL", ""),
		},
		AppGoAPI: AppGoAPISettings{
			BaseURL:      getEnv(v, "APP_GO_API_BASE_URL", ""),
			SyncInterval: getDuration(v, "APP_GO_API_SYNC_INTERVAL", 30*time.Minute),
			SyncEnabled:  getBool(v, "APP_GO_API_SYNC_ENABLED", true),
		},
		SalesForce: SalesForceSettings{
			InstanceURL:       getEnv(v, "SALESFORCE_INSTANCE_URL", ""),
			BaseServiceURL:    getEnv(v, "SALESFORCE_BASE_SERVICE_URL", "https://prefeitura.rio"),
			WebhookSecret:     getEnv(v, "SALESFORCE_WEBHOOK_SECRET", ""),
			SyncInterval:      getDuration(v, "SALESFORCE_SYNC_INTERVAL", 15*time.Minute),
			SyncEnabled:       getBool(v, "SALESFORCE_SYNC_ENABLED", true),
			DetailConcurrency: getInt(v, "SALESFORCE_DETAIL_CONCURRENCY", 8),
		},
		CitizenSync: CitizenSyncSettings{
			StaleThreshold: getDuration(v, "CITIZEN_PROFILE_STALE_THRESHOLD", 1*time.Hour),
			SyncInterval:   getDuration(v, "CITIZEN_PROFILE_SYNC_INTERVAL", 24*time.Hour),
		},
		Cache: CacheSettings{
			SearchTTL:                      getDuration(v, "CACHE_SEARCH_TTL", 60*time.Second),
			SearchVectorsTTL:               getDuration(v, "CACHE_SEARCH_VECTORS_TTL", time.Hour),
			RecommendationAuthenticatedTTL: getDuration(v, "CACHE_RECOMMENDATION_AUTHENTICATED_TTL", 5*time.Minute),
			RecommendationClusterTTL:       getDuration(v, "CACHE_RECOMMENDATION_CLUSTER_TTL", 15*time.Minute),
		},
		CPFHashSalt: getEnv(v, "CPF_HASH_SALT", ""),
		Swagger: SwaggerSettings{
			Host: getEnv(v, "SWAGGER_HOST", "localhost:8080"),
		},
		Heimdall: HeimdallSettings{
			BaseURL: getEnv(v, "HEIMDALL_BASE_URL", ""),
		},
		Typesense: TypesenseSettings{
			URL:            getEnv(v, "TYPESENSE_URL", ""),
			APIKey:         getEnv(v, "TYPESENSE_API_KEY", ""),
			Collection:     getEnv(v, "TYPESENSE_COLLECTION", "prefrio_services_base"),
			BaseServiceURL: getEnv(v, "TYPESENSE_BASE_SERVICE_URL", "https://prefeitura.rio"),
			SyncInterval:   getDuration(v, "TYPESENSE_SYNC_INTERVAL", 30*time.Minute),
			SyncEnabled:    getBool(v, "TYPESENSE_SYNC_ENABLED", true),
		},
		Gemini: GeminiSettings{
			APIKey: getEnv(v, "GOOGLE_API_KEY", ""),
		},
		Reranker: RerankerSettings{
			URL:     getEnv(v, "RERANKER_URL", ""),
			Timeout: getDuration(v, "RERANKER_TIMEOUT", 2*time.Second),
		},
		Embedding: EmbeddingSettings{
			BackfillInterval: getDuration(v, "EMBEDDING_BACKFILL_INTERVAL", 5*time.Minute),
		},
	}

	cfg.Auth.JWTIssuer = getEnv(v, "AUTH_JWT_ISSUER", keycloakIssuer(cfg.Keycloak.URL, cfg.Keycloak.Realm))
	cfg.Auth.JWKSURL = getEnv(v, "AUTH_JWKS_URL", defaultJWKSURL(cfg.Auth.JWTIssuer))
	cfg.Auth.AuthorizedParties = parseAuthorizedParties(getEnv(v, "AUTH_JWT_AUTHORIZED_PARTIES", defaultJWTAuthorizedParties))

	if err := cfg.Database.Validate(); err != nil {
		return nil, fmt.Errorf("configuração de banco inválida: %w", err)
	}
	if err := cfg.Server.Validate(); err != nil {
		return nil, fmt.Errorf("configuração de servidor inválida: %w", err)
	}
	if err := validateCPFHashSecret(cfg.CPFHashSalt); err != nil {
		return nil, err
	}

	return cfg, nil
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
	if value := v.GetString(key); value != "" {
		return value
	}
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
