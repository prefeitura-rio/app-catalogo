package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

type AppConfig struct {
	App          AppSettings
	Database     DatabaseSettings
	Server       ServerSettings
	Redis        RedisSettings
	Tracing      TracingSettings
	Migrations   MigrationSettings
	Keycloak     KeycloakSettings
	RMI          RMISettings
	AppGoAPI     AppGoAPISettings
	SalesForce   SalesForceSettings
	CitizenSync  CitizenSyncSettings
	Cache        CacheSettings
	CPFHashSalt  string
	Swagger      SwaggerSettings
	Heimdall     HeimdallSettings
	Typesense    TypesenseSettings
	Gemini       GeminiSettings
	Reranker     RerankerSettings
	Embedding    EmbeddingSettings
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
	Host        string
	Port        int
	User        string
	Password    string
	Name        string
	SSLMode     string
	Timezone    string
	MaxOpenConns int
	MinConns    int
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
}

func (s *ServerSettings) Validate() error {
	if s.Host == "" {
		return errors.New("SERVER_HOST não pode estar vazio")
	}
	if s.Port <= 0 {
		return errors.New("SERVER_PORT deve ser maior que zero")
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

type RMISettings struct {
	BaseURL string
}

type AppGoAPISettings struct {
	BaseURL      string
	SyncInterval time.Duration
	SyncEnabled  bool
}

type SalesForceSettings struct {
	InstanceURL    string
	ClientID       string
	ClientSecret   string
	WebhookSecret  string
	SyncInterval   time.Duration
	ObjectType     string
}

type CitizenSyncSettings struct {
	StaleThreshold time.Duration
	SyncInterval   time.Duration
}

type CacheSettings struct {
	SearchTTL                    time.Duration
	RecommendationAuthenticatedTTL time.Duration
	RecommendationClusterTTL     time.Duration
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
			InstanceURL:   getEnv(v, "SALESFORCE_INSTANCE_URL", ""),
			ClientID:      getEnv(v, "SALESFORCE_CLIENT_ID", ""),
			ClientSecret:  getEnv(v, "SALESFORCE_CLIENT_SECRET", ""),
			WebhookSecret: getEnv(v, "SALESFORCE_WEBHOOK_SECRET", ""),
			SyncInterval:  getDuration(v, "SALESFORCE_SYNC_INTERVAL", 15*time.Minute),
			ObjectType:    getEnv(v, "SALESFORCE_OBJECT_TYPE", "Service__c"),
		},
		CitizenSync: CitizenSyncSettings{
			StaleThreshold: getDuration(v, "CITIZEN_PROFILE_STALE_THRESHOLD", 1*time.Hour),
			SyncInterval:   getDuration(v, "CITIZEN_PROFILE_SYNC_INTERVAL", 24*time.Hour),
		},
		Cache: CacheSettings{
			SearchTTL:                    getDuration(v, "CACHE_SEARCH_TTL", 60*time.Second),
			RecommendationAuthenticatedTTL: getDuration(v, "CACHE_RECOMMENDATION_AUTHENTICATED_TTL", 5*time.Minute),
			RecommendationClusterTTL:     getDuration(v, "CACHE_RECOMMENDATION_CLUSTER_TTL", 15*time.Minute),
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

	if err := cfg.Database.Validate(); err != nil {
		return nil, fmt.Errorf("configuração de banco inválida: %w", err)
	}
	if err := cfg.Server.Validate(); err != nil {
		return nil, fmt.Errorf("configuração de servidor inválida: %w", err)
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
