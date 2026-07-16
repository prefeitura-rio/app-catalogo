package api

import (
	"time"

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
	SFSyncSvc     *services.SalesForceSyncService
	DSManager     *datasource.Manager
	SearchSvc     *services.SearchService
	RecomSvc      *services.RecommendationService
	CitizenSvc    *services.CitizenProfileService
	ItemRepo      *repository.CatalogItemRepository
	WebhookSecret string
}

func SetupRouter(cfg *config.AppConfig, db *pgxpool.Pool, deps RouterDeps) *gin.Engine {
	if cfg.App.IsDevelopment() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(otelgin.Middleware(cfg.Tracing.ServiceName))
	r.Use(observability.RequestLogger())
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS())
	r.Use(middleware.ExtractUserContext())
	r.Use(middleware.Timeout(time.Duration(cfg.Server.RequestTimeout) * time.Second))
	r.Use(observability.RateLimitMiddleware(300)) // 300 req/min por IP

	healthHandler := handlers.NewHealthHandler(db)
	r.GET("/health", healthHandler.Health)
	r.GET("/ready", healthHandler.Ready)
	r.GET("/metrics", observability.MetricsHandler())

	// Webhook (auth própria — fora do Istio JWT)
	webhookHandler := v1.NewWebhookHandler(deps.SFSyncSvc, deps.WebhookSecret)
	r.POST("/api/webhooks/salesforce", webhookHandler.SalesForce)

	adminHandler := v1.NewAdminHandler(deps.ItemRepo, deps.DSManager)

	// API autenticada
	apiV1 := r.Group("/api/v1")
	{
		apiV1.GET("/search", v1.NewSearchHandler(deps.SearchSvc, deps.CitizenSvc).Search)
		apiV1.GET("/recommendations", middleware.RequireAuth(), v1.NewRecommendationHandler(deps.RecomSvc, deps.CitizenSvc).Authenticated)
		apiV1.GET("/catalog/:id", adminHandler.GetCatalogItem)

		admin := apiV1.Group("/admin", middleware.RequireAdmin())
		{
			admin.GET("/sync/status", adminHandler.SyncStatus)
			admin.POST("/sync/trigger", adminHandler.TriggerSync)
		}
	}

	// API pública
	pub := r.Group("/api/public")
	{
		pub.GET("/search", v1.NewSearchHandler(deps.SearchSvc, deps.CitizenSvc).Search)
		pub.GET("/recommendations", v1.NewRecommendationHandler(deps.RecomSvc, deps.CitizenSvc).Anonymous)
		pub.GET("/catalog/:id", adminHandler.GetCatalogItem)
	}

	return r
}
