package api

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
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
	WebhookSvc    *services.SalesForceWebhookService
	DSManager     *datasource.Manager
	SearchSvc     *services.SearchService
	RecomSvc      *services.RecommendationService
	CitizenSvc    *services.CitizenProfileService
	ItemRepo      *repository.CatalogItemRepository
	CartaRepo     *repository.CartaRepository
	WebhookSecret string
	TokenVerifier middleware.TokenVerifier
}

func SetupRouter(cfg *config.AppConfig, db *pgxpool.Pool, deps RouterDeps) *gin.Engine {
	if cfg.App.IsDevelopment() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	configureClientIP(r, cfg.Server.TrustedProxies)
	r.Use(joinForwardedFor())
	r.Use(gin.Recovery())
	r.Use(otelgin.Middleware(cfg.Tracing.ServiceName))
	r.Use(observability.RequestLogger())
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS())
	r.Use(middleware.ExtractUserContext(deps.TokenVerifier))
	r.Use(middleware.Timeout(time.Duration(cfg.Server.RequestTimeout) * time.Second))
	r.Use(observability.RateLimitMiddleware(300)) // 300 req/min por IP

	healthHandler := handlers.NewHealthHandler(db)
	r.GET("/health", healthHandler.Health)
	r.GET("/ready", healthHandler.Ready)
	r.GET("/metrics", observability.MetricsHandler())

	// Webhook (auth própria por HMAC — fora da verificação do JWT)
	var webhookHandler *v1.WebhookHandler
	if deps.WebhookSvc != nil {
		webhookHandler = v1.NewWebhookHandler(deps.WebhookSvc, deps.WebhookSecret)
	} else {
		webhookHandler = v1.NewWebhookHandler(nil, deps.WebhookSecret)
	}
	r.POST("/api/webhooks/salesforce", webhookHandler.SalesForce)

	adminHandler := v1.NewAdminHandler(deps.ItemRepo, deps.DSManager)

	// API autenticada
	apiV1 := r.Group("/api/v1")
	{
		apiV1.GET("/search", middleware.RequireAuth(), v1.NewSearchHandler(deps.SearchSvc, deps.CitizenSvc).Search)
		apiV1.GET("/recommendations", middleware.RequireAuth(), v1.NewRecommendationHandler(deps.RecomSvc, deps.CitizenSvc).Authenticated)
		apiV1.GET("/catalog/:id", middleware.RequireAuth(), adminHandler.GetCatalogItem)

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
		pub.GET("/catalog/:id", adminHandler.GetPublicCatalogItem)

		// Navegação Carta de Serviços (base local)
		cartaHandler := v1.NewCartaHandler(deps.CartaRepo)
		pub.GET("/themes", cartaHandler.ListThemes)
		pub.GET("/themes/:slug/subthemes", cartaHandler.ListSubthemes)
		pub.GET("/subthemes/:slug/services", cartaHandler.ListServicesBySubtheme)
		pub.GET("/services/:slug", cartaHandler.GetService)
	}

	return r
}

// configureClientIP define como c.ClientIP() resolve o IP do cliente, que é a
// chave do rate limiting e o campo ip do log de acesso. Só o X-Forwarded-For é
// lido, e só quando a conexão vem de um proxy confiável; ele é percorrido da
// direita para a esquerda, pulando os proxies confiáveis, e o primeiro endereço
// que não é proxy é o cliente. Assim o que o cliente escreve no cabeçalho, que
// fica à esquerda do que os proxies acrescentam, não substitui o IP real.
// Sem a chamada, o Gin confia em qualquer origem (0.0.0.0/0) e devolve o
// endereço mais à esquerda, forjável.
func configureClientIP(r *gin.Engine, trustedProxies []string) {
	r.ForwardedByClientIP = true
	r.RemoteIPHeaders = []string{"X-Forwarded-For"}
	r.TrustedPlatform = ""
	if err := r.SetTrustedProxies(trustedProxies); err != nil {
		// A configuração já valida a lista; se mesmo assim falhar, nenhum
		// proxy é confiável e o IP é o da conexão (falha fechada).
		log.Error().Err(err).Msg("TRUSTED_PROXIES inválida — X-Forwarded-For ignorado")
		_ = r.SetTrustedProxies(nil)
	}
}

// joinForwardedFor junta várias linhas de X-Forwarded-For numa só, na ordem em
// que chegaram. O Gin lê só a primeira linha; sem isso, um cliente que mande o
// cabeçalho duas vezes poderia esconder, nas linhas seguintes, o que os proxies
// acrescentaram.
func joinForwardedFor() gin.HandlerFunc {
	return func(c *gin.Context) {
		if values := c.Request.Header.Values("X-Forwarded-For"); len(values) > 1 {
			c.Request.Header.Set("X-Forwarded-For", strings.Join(values, ","))
		}
		c.Next()
	}
}
