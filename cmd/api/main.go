// @title           app-catalogo
// @version         1.0
// @description     Discovery layer unificada da Prefeitura do Rio de Janeiro. Indexa serviços públicos, cursos, vagas e oportunidades MEI com busca full-text e recomendação personalizada por perfil de cidadão.
// @contact.name    Prefeitura do Rio de Janeiro
// @contact.url     https://github.com/prefeitura-rio/app-catalogo
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            X-Auth-Request-Token
// @description     JWT injetado pelo Istio via header X-Auth-Request-Token

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/api"
	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/config"
	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
	"github.com/prefeitura-rio/app-catalogo/internal/db"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

func main() {
	cfg, err := config.Get()
	if err != nil {
		fmt.Fprintf(os.Stderr, "falha ao carregar configurações: %v\n", err)
		os.Exit(1)
	}

	level, err := zerolog.ParseLevel(cfg.App.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	if cfg.App.IsDevelopment() {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	ctx := context.Background()
	jwtVerifier, jwtVerifierError := middleware.NewJWTVerifier(middleware.JWTVerifierConfig{
		JWKSURL:                   cfg.JWT.JWKSURL,
		Issuer:                    cfg.JWT.Issuer,
		Audience:                  cfg.JWT.Audience,
		AuthorizedParty:           cfg.JWT.AuthorizedParty,
		ClockSkew:                 cfg.JWT.ClockSkew,
		JWKSCacheTTL:              cfg.JWT.JWKSCacheTTL,
		UnknownKeyRefreshInterval: cfg.JWT.UnknownKeyRefreshInterval,
		HTTPTimeout:               cfg.JWT.HTTPTimeout,
		Now:                       time.Now,
	})
	if jwtVerifierError != nil {
		log.Fatal().Err(jwtVerifierError).Msg("failed to configure JWT verification")
	}
	catalogSearchClientVerifier, verifierError := middleware.NewCatalogSearchClientVerifier(
		cfg.InternalAPI.Key,
		cfg.InternalAPI.CatalogSearchSignatureSkew,
		time.Now,
	)
	if verifierError != nil {
		log.Fatal().Err(verifierError).Msg("failed to configure catalog search client verification")
	}

	if cfg.Tracing.Enabled {
		shutdown, err := observability.InitTracer(
			ctx,
			cfg.Tracing.Endpoint,
			cfg.Tracing.ServiceName,
			cfg.Tracing.ServiceVersion,
		)
		if err != nil {
			log.Fatal().Err(err).Msg("falha ao inicializar tracer")
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(shutdownCtx); err != nil {
				log.Error().Err(err).Msg("erro ao encerrar tracer")
			}
		}()
	}

	if err := db.Connect(ctx, db.PoolConfig{
		Host:         cfg.Database.Host,
		Port:         cfg.Database.Port,
		User:         cfg.Database.User,
		Password:     cfg.Database.Password,
		Name:         cfg.Database.Name,
		SSLMode:      cfg.Database.SSLMode,
		Timezone:     cfg.Database.Timezone,
		MaxOpenConns: cfg.Database.MaxOpenConns,
		MinConns:     cfg.Database.MinConns,
	}); err != nil {
		log.Fatal().Err(err).Msg("falha ao conectar ao banco de dados")
	}
	defer db.Close()

	redisCache := cache.NewRedisCache(
		cfg.Redis.Host,
		cfg.Redis.Port,
		cfg.Redis.Password,
		cfg.Redis.DB,
		cfg.Redis.PoolSize,
		cfg.Redis.MinIdleConns,
	)
	defer func() {
		if closeError := redisCache.Close(); closeError != nil {
			log.Error().Err(closeError).Msg("erro ao encerrar cliente Redis de cache")
		}
	}()
	searchCache := redisCache
	if err := redisCache.Ping(ctx); err != nil {
		log.Warn().Err(err).Msg("redis de cache indisponível — cache de busca desativado")
		searchCache = nil
	}

	rateLimitRedis := cache.NewRedisCache(
		cfg.Redis.Host,
		cfg.Redis.Port,
		cfg.Redis.Password,
		cfg.Redis.DB,
		cfg.Redis.PoolSize,
		cfg.Redis.MinIdleConns,
	)
	defer func() {
		if closeError := rateLimitRedis.Close(); closeError != nil {
			log.Error().Err(closeError).Msg("erro ao encerrar cliente Redis de rate limit")
		}
	}()
	if pingError := rateLimitRedis.Ping(ctx); pingError != nil {
		log.Warn().Err(pingError).Msg("redis de rate limit indisponível — proteção local ativada")
	}

	// Repositórios
	itemRepo := repository.NewCatalogItemRepository(db.Pool)
	searchRepo := repository.NewSearchRepository(db.Pool)
	profileRepo := repository.NewCitizenProfileRepository(db.Pool)

	// Clients externos
	tokenManager, tokenManagerError := clients.NewKeycloakTokenManager(
		cfg.Keycloak.URL,
		cfg.Keycloak.Realm,
		cfg.Keycloak.ClientID,
		cfg.Keycloak.ClientSecret,
	)
	if tokenManagerError != nil {
		log.Fatal().Err(tokenManagerError).Msg("invalid Keycloak service-account configuration")
	}
	rmiClient := clients.NewRMIClient(cfg.RMI.BaseURL, tokenManager)

	var salesForceSyncService *services.SalesForceSyncService
	if cfg.SalesForce.Enabled() {
		salesForceClient, salesForceClientError := clients.NewSalesForceClient(
			cfg.SalesForce.InstanceURL,
			cfg.SalesForce.ClientID,
			cfg.SalesForce.ClientSecret,
		)
		if salesForceClientError != nil {
			log.Fatal().Err(salesForceClientError).Msg("invalid Salesforce client configuration")
		}
		salesForceSyncService = services.NewSalesForceSyncService(
			salesForceClient,
			itemRepo,
			cfg.SalesForce.ObjectType,
		)
	}

	// Clients opcionais — busca semântica e reranking
	var geminiClient *clients.GeminiEmbeddingClient
	if cfg.Gemini.APIKey != "" {
		gc, err := clients.NewGeminiEmbeddingClient(ctx, cfg.Gemini.APIKey)
		if err != nil {
			log.Warn().Err(err).Msg("Gemini indisponível — busca semântica desativada")
		} else {
			geminiClient = gc
			log.Info().Msg("busca semântica (Gemini) ativada")
		}
	}

	var rerankerClient *clients.RerankerClient
	if cfg.Reranker.URL != "" {
		configuredRerankerClient, rerankerError := clients.NewRerankerClient(cfg.Reranker.URL, cfg.Reranker.Timeout)
		if rerankerError != nil {
			log.Fatal().Err(rerankerError).Msg("failed to configure reranker")
		}
		rerankerClient = configuredRerankerClient
		log.Info().Str("url", cfg.Reranker.URL).Msg("reranker cross-encoder ativado")
	}

	var facilitaSearchClient *clients.FacilitaSearchClient
	if cfg.Facilita.Enabled() {
		configuredFacilitaClient, facilitaClientError := clients.NewFacilitaSearchClient(
			cfg.Facilita.BaseURL,
			cfg.Facilita.InternalAPIKey,
			cfg.Facilita.Timeout,
		)
		if facilitaClientError != nil {
			log.Fatal().Err(facilitaClientError).Msg("failed to configure Facilita search candidates")
		}
		facilitaSearchClient = configuredFacilitaClient
		log.Info().Msg("Facilita service candidates enabled")
	}

	// Serviços
	searchSvc := services.NewSearchService(
		searchRepo,
		searchCache,
		cfg.Cache.SearchTTL,
		geminiClient,
		rerankerClient,
		services.SearchRuntimeConfig{
			RankerVersion:           cfg.Search.RankerVersion,
			RerankerVersion:         cfg.Search.RerankerVersion,
			CandidatePoolSize:       cfg.Search.CandidatePoolSize,
			SemanticOverfetchFactor: cfg.Search.SemanticOverfetchFactor,
			MaximumSemanticDistance: cfg.Search.MaximumSemanticDistance,
			SemanticTimeout:         cfg.Search.SemanticTimeout,
			HyDEEnabled:             cfg.Search.HyDEEnabled,
			Weights: repository.RetrievalWeights{
				Exact:    cfg.Search.ExactWeight,
				FullText: cfg.Search.FullTextWeight,
				Trigram:  cfg.Search.TrigramWeight,
				Semantic: cfg.Search.SemanticWeight,
				HyDE:     cfg.Search.HyDEWeight,
				Facilita: cfg.Search.FacilitaWeight,
			},
		},
		facilitaSearchClient,
	)
	citizenSvc := services.NewCitizenProfileService(
		rmiClient,
		profileRepo,
		cfg.CPFHashSalt,
		cfg.CitizenSync.StaleThreshold,
	)
	recomSvc := services.NewRecommendationService(
		itemRepo,
		redisCache,
		models.DefaultWeights,
		cfg.Cache.RecommendationAuthenticatedTTL,
		cfg.Cache.RecommendationAnonymousTTL,
	)

	// Manager com fontes registradas (espelha o worker, mas sem tickers — só para TriggerSync)
	dsManager := datasource.NewManager()
	dsManager.AddSyncHook(datasource.NewSearchCacheInvalidationHook(redisCache))
	if cfg.SalesForce.Enabled() {
		sfDataSource := datasource.NewSalesForceDataSource(
			salesForceSyncService,
			cfg.SalesForce.SyncInterval,
			cfg.SalesForce.FullSyncInterval,
		)
		dsManager.Register(sfDataSource)
	}
	if cfg.AppGoAPI.BaseURL != "" && cfg.AppGoAPI.SyncEnabled {
		appGoAPIClient := clients.NewAppGoAPIClient(cfg.AppGoAPI.BaseURL, tokenManager)
		appGoAPIDs := datasource.NewAppGoAPIDataSource(appGoAPIClient, itemRepo, cfg.AppGoAPI.SyncInterval)
		dsManager.Register(appGoAPIDs)
	}
	registerConfiguredTypesenseDataSource(dsManager, cfg.Typesense, itemRepo)

	router, routerError := api.SetupRouter(cfg, db.Pool, api.RouterDeps{
		SFSyncSvc:                   salesForceSyncService,
		DSManager:                   dsManager,
		SearchSvc:                   searchSvc,
		RecomSvc:                    recomSvc,
		CitizenSvc:                  citizenSvc,
		ItemRepo:                    itemRepo,
		RateLimitStore:              rateLimitRedis,
		CatalogSearchClientVerifier: catalogSearchClientVerifier,
		JWTVerifier:                 jwtVerifier,
		WebhookSyncHook: func(webhookContext context.Context) error {
			deleted, invalidationError := redisCache.DelByPrefix(webhookContext, cache.SearchKeyPrefix)
			if invalidationError == nil && deleted > 0 {
				log.Info().Int64("deleted", deleted).Msg("webhook: cache de busca invalidado")
			}
			return invalidationError
		},
	})
	if routerError != nil {
		log.Fatal().Err(routerError).Msg("falha ao configurar roteador HTTP")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := newHTTPServer(addr, router, cfg.Server)

	go func() {
		log.Info().Str("addr", addr).Msg("servidor iniciado")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("falha no servidor HTTP")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("encerrando servidor...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal().Err(err).Msg("encerramento forçado")
	}
	log.Info().Msg("servidor encerrado")
}

func registerConfiguredTypesenseDataSource(
	manager *datasource.Manager,
	settings config.TypesenseSettings,
	itemRepository *repository.CatalogItemRepository,
) bool {
	if strings.TrimSpace(settings.URL) == "" || strings.TrimSpace(settings.APIKey) == "" || !settings.SyncEnabled {
		return false
	}
	typesenseClient := clients.NewTypesenseClient(settings.URL, settings.APIKey, settings.Collection)
	manager.Register(datasource.NewTypesenseDataSource(
		typesenseClient,
		itemRepository,
		settings.BaseServiceURL,
		settings.SyncInterval,
		settings.FullSyncInterval,
	))
	return true
}

func newHTTPServer(address string, handler http.Handler, settings config.ServerSettings) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: settings.ReadHeaderTimeout,
		ReadTimeout:       settings.ReadTimeout,
		WriteTimeout:      settings.WriteTimeout,
		IdleTimeout:       settings.IdleTimeout,
		MaxHeaderBytes:    settings.MaxHeaderBytes,
	}
}
