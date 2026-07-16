package api

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/handlers"
	v1 "github.com/prefeitura-rio/app-catalogo/internal/api/handlers/v1"
	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/config"
	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

type RouterDeps struct {
	SFSyncSvc                   *services.SalesForceSyncService
	DSManager                   *datasource.Manager
	SearchSvc                   *services.SearchService
	RecomSvc                    *services.RecommendationService
	CitizenSvc                  *services.CitizenProfileService
	ItemRepo                    *repository.CatalogItemRepository
	RateLimitStore              observability.RateLimitStore
	CatalogSearchClientVerifier observability.RateLimitClientIdentityVerifier
	JWTVerifier                 middleware.JWTClaimsVerifier
	WebhookSyncHook             v1.SalesForceWebhookSyncHook
}

func SetupRouter(cfg *config.AppConfig, db *pgxpool.Pool, deps RouterDeps) (*gin.Engine, error) {
	if cfg.App.IsDevelopment() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	userContextMiddleware, userContextError := middleware.NewUserContextMiddleware(
		deps.JWTVerifier,
		cfg.JWT.RoleClientID,
	)
	if userContextError != nil {
		return nil, fmt.Errorf("configure user authentication: %w", userContextError)
	}
	if trustedProxyError := configureTrustedProxies(r, cfg.Server.TrustedProxies); trustedProxyError != nil {
		return nil, fmt.Errorf("configure trusted proxies: %w", trustedProxyError)
	}
	r.Use(gin.Recovery())
	r.Use(otelgin.Middleware(cfg.Tracing.ServiceName))
	r.Use(observability.RequestLogger())
	r.Use(middleware.RequestID())
	r.Use(middleware.SearchID())
	r.Use(middleware.CORS())
	r.Use(userContextMiddleware)
	r.Use(middleware.Timeout(cfg.Server.RequestTimeout))

	healthHandler := handlers.NewHealthHandler(db)
	r.GET("/health", healthHandler.Health)
	r.GET("/ready", healthHandler.Ready)
	r.GET("/metrics", observability.MetricsHandler())

	rateLimitedRoutes := r.Group("")
	if rateLimitSettingsError := cfg.RateLimit.Validate(); rateLimitSettingsError != nil {
		return nil, fmt.Errorf("configure rate limiter: %w", rateLimitSettingsError)
	}
	if deps.CatalogSearchClientVerifier == nil {
		return nil, fmt.Errorf("configure rate limiter: catalog search client verifier is required")
	}
	rateLimitMiddleware, rateLimitMiddlewareError := observability.RateLimitMiddleware(
		deps.RateLimitStore,
		cfg.RateLimit.RequestsPerWindow,
		cfg.RateLimit.Window,
		cfg.RateLimit.RedisTimeout,
		cfg.RateLimit.KeySecret,
		deps.CatalogSearchClientVerifier,
	)
	if rateLimitMiddlewareError != nil {
		return nil, fmt.Errorf("configure rate limiter: %w", rateLimitMiddlewareError)
	}
	rateLimitedRoutes.Use(rateLimitMiddleware)

	// Webhook (auth própria — fora do Istio JWT)
	if cfg.SalesForce.Enabled() {
		if salesForceSettingsError := cfg.SalesForce.Validate(); salesForceSettingsError != nil {
			return nil, fmt.Errorf("configure Salesforce webhook: %w", salesForceSettingsError)
		}
		webhookHandler, webhookHandlerError := v1.NewWebhookHandler(
			deps.SFSyncSvc,
			cfg.SalesForce.WebhookSecret,
			deps.WebhookSyncHook,
		)
		if webhookHandlerError != nil {
			return nil, fmt.Errorf("configure Salesforce webhook: %w", webhookHandlerError)
		}
		rateLimitedRoutes.POST("/api/webhooks/salesforce", webhookHandler.SalesForce)
	}

	adminHandler := v1.NewAdminHandler(deps.ItemRepo, deps.DSManager)
	searchHandler := v1.NewSearchHandler(deps.SearchSvc)

	// API autenticada
	apiV1 := rateLimitedRoutes.Group("/api/v1")
	{
		apiV1.GET("/search", middleware.RequireAuth(), searchHandler.Search)
		apiV1.POST("/search", middleware.RequireAuth(), searchHandler.SearchJSON)
		apiV1.GET("/recommendations", middleware.RequireAuth(), v1.NewRecommendationHandler(deps.RecomSvc, deps.CitizenSvc).Authenticated)
		apiV1.GET("/catalog/:id", middleware.RequireAuth(), adminHandler.GetCatalogItem)

		admin := apiV1.Group("/admin", middleware.RequireAdmin())
		{
			admin.GET("/sync/status", adminHandler.SyncStatus)
			admin.POST("/sync/trigger", adminHandler.TriggerSync)
		}
	}

	// API pública
	pub := rateLimitedRoutes.Group("/api/public")
	{
		pub.GET("/search", searchHandler.Search)
		pub.POST("/search", searchHandler.SearchJSON)
		pub.GET("/recommendations", v1.NewRecommendationHandler(deps.RecomSvc, deps.CitizenSvc).Anonymous)
		pub.GET("/catalog/:id", adminHandler.GetPublicCatalogItem)
	}

	return r, nil
}

func configureTrustedProxies(router *gin.Engine, trustedProxies []string) error {
	router.TrustedPlatform = ""
	router.ForwardedByClientIP = len(trustedProxies) > 0
	if !router.ForwardedByClientIP {
		router.RemoteIPHeaders = nil
		return router.SetTrustedProxies(nil)
	}
	router.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
	return router.SetTrustedProxies(trustedProxies)
}
