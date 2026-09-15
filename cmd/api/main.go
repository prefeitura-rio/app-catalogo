// @title           app-catalogo
// @version         1.0
// @description     Discovery layer unificada da Prefeitura do Rio de Janeiro. Indexa serviços públicos, cursos, vagas e oportunidades MEI com busca full-text e recomendação personalizada por perfil de cidadão.
// @contact.name    Prefeitura do Rio de Janeiro
// @contact.url     https://github.com/prefeitura-rio/app-catalogo
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     JWT injetado pelo Istio via header X-Auth-Request-Token

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/api"
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
	if err := redisCache.Ping(ctx); err != nil {
		log.Warn().Err(err).Msg("redis indisponível — cache desativado (dados servidos sem cache)")
	}

	// Repositórios
	itemRepo := repository.NewCatalogItemRepository(db.Pool)
	searchRepo := repository.NewSearchRepository(db.Pool)
	profileRepo := repository.NewCitizenProfileRepository(db.Pool)

	// Clients externos
	tokenManager := clients.NewKeycloakTokenManager(
		cfg.Keycloak.URL,
		cfg.Keycloak.Realm,
		cfg.Keycloak.ClientID,
		cfg.Keycloak.ClientSecret,
	)
	rmiClient := clients.NewRMIClient(cfg.RMI.BaseURL, tokenManager)

	sfClient := clients.NewSalesForceClient(
		cfg.SalesForce.InstanceURL,
		cfg.SalesForce.ClientID,
		cfg.SalesForce.ClientSecret,
	)

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
		rerankerClient = clients.NewRerankerClient(cfg.Reranker.URL, cfg.Reranker.Timeout)
		log.Info().Str("url", cfg.Reranker.URL).Msg("reranker cross-encoder ativado")
	}

	// Serviços
	sfSyncSvc := services.NewSalesForceSyncService(sfClient, itemRepo, cfg.SalesForce.ObjectType)
	searchSvc := services.NewSearchService(searchRepo, redisCache, cfg.Cache.SearchTTL, geminiClient, rerankerClient)
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
		cfg.Cache.RecommendationClusterTTL,
	)

	// Manager com fontes registradas (espelha o worker, mas sem tickers — só para TriggerSync)
	dsManager := datasource.NewManager()
	if cfg.SalesForce.InstanceURL != "" {
		sfDataSource := datasource.NewSalesForceDataSource(sfSyncSvc, cfg.SalesForce.SyncInterval)
		dsManager.Register(sfDataSource)
	}
	if cfg.AppGoAPI.BaseURL != "" && cfg.AppGoAPI.SyncEnabled {
		appGoAPIClient := clients.NewAppGoAPIClient(cfg.AppGoAPI.BaseURL, tokenManager)
		appGoAPIDs := datasource.NewAppGoAPIDataSource(appGoAPIClient, itemRepo, cfg.AppGoAPI.SyncInterval)
		dsManager.Register(appGoAPIDs)
	}

	router := api.SetupRouter(cfg, db.Pool, api.RouterDeps{
		SFSyncSvc:     sfSyncSvc,
		DSManager:     dsManager,
		SearchSvc:     searchSvc,
		RecomSvc:      recomSvc,
		CitizenSvc:    citizenSvc,
		ItemRepo:      itemRepo,
		WebhookSecret: cfg.SalesForce.WebhookSecret,
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal().Err(err).Msg("encerramento forçado")
	}
	log.Info().Msg("servidor encerrado")
}
