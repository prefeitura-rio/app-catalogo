package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/config"
	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
	"github.com/prefeitura-rio/app-catalogo/internal/db"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	itemRepo := repository.NewCatalogItemRepository(db.Pool)

	// -------------------------------------------------------------------------
	// Registrar fontes de dados no manager
	// Para adicionar uma nova fonte: implemente datasource.DataSource e chame
	// manager.Register(...) aqui.
	// -------------------------------------------------------------------------
	manager := datasource.NewManager()

	// SalesForce — Carta de Serviços
	if cfg.SalesForce.InstanceURL != "" {
		sfClient := clients.NewSalesForceClient(
			cfg.SalesForce.InstanceURL,
			cfg.SalesForce.ClientID,
			cfg.SalesForce.ClientSecret,
		)
		sfSyncSvc := services.NewSalesForceSyncService(sfClient, itemRepo, cfg.SalesForce.ObjectType)
		manager.Register(datasource.NewSalesForceDataSource(sfSyncSvc, cfg.SalesForce.SyncInterval))
	} else {
		log.Warn().Msg("worker: SalesForce não configurado (SALESFORCE_INSTANCE_URL vazia), fonte ignorada")
	}

	// app-go-api — Cursos, Vagas, MEI
	if cfg.AppGoAPI.BaseURL != "" && cfg.AppGoAPI.SyncEnabled {
		tokenManager := clients.NewKeycloakTokenManager(
			cfg.Keycloak.URL,
			cfg.Keycloak.Realm,
			cfg.Keycloak.ClientID,
			cfg.Keycloak.ClientSecret,
		)
		appGoAPIClient := clients.NewAppGoAPIClient(cfg.AppGoAPI.BaseURL, tokenManager)
		manager.Register(datasource.NewAppGoAPIDataSource(appGoAPIClient, itemRepo, cfg.AppGoAPI.SyncInterval))
	} else {
		log.Warn().Msg("worker: app-go-api não configurado ou desabilitado, fonte ignorada")
	}

	// Typesense — Carta de Serviços (temporário, até migração para SalesForce)
	if cfg.Typesense.URL != "" && cfg.Typesense.APIKey != "" && cfg.Typesense.SyncEnabled {
		tsClient := clients.NewTypesenseClient(
			cfg.Typesense.URL,
			cfg.Typesense.APIKey,
			cfg.Typesense.Collection,
		)
		manager.Register(datasource.NewTypesenseDataSource(
			tsClient,
			itemRepo,
			cfg.Typesense.BaseServiceURL,
			cfg.Typesense.SyncInterval,
		))
	} else {
		log.Warn().Msg("worker: Typesense não configurado ou desabilitado, fonte ignorada")
	}

	// Embedding backfill — gera vetores semânticos para itens sem embedding
	if cfg.Gemini.APIKey != "" {
		geminiClient, err := clients.NewGeminiEmbeddingClient(ctx, cfg.Gemini.APIKey)
		if err != nil {
			log.Warn().Err(err).Msg("worker: Gemini indisponível — backfill de embeddings desativado")
		} else {
			embeddingSvc := services.NewEmbeddingService(itemRepo, geminiClient)
			go func() {
				ticker := time.NewTicker(cfg.Embedding.BackfillInterval)
				defer ticker.Stop()
				log.Info().Dur("interval", cfg.Embedding.BackfillInterval).Msg("worker: iniciando backfill de embeddings")
				n := embeddingSvc.BackfillPass(ctx)
				log.Info().Int("processed", n).Msg("worker: backfill inicial concluído")
				for {
					select {
					case <-ticker.C:
						n := embeddingSvc.BackfillPass(ctx)
						if n > 0 {
							log.Info().Int("processed", n).Msg("worker: backfill de embeddings")
						}
					case <-ctx.Done():
						return
					}
				}
			}()
		}
	} else {
		log.Warn().Msg("worker: GOOGLE_API_KEY não configurado — backfill de embeddings desativado")
	}

	// -------------------------------------------------------------------------
	// Iniciar todos os workers
	// -------------------------------------------------------------------------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Info().Msg("worker: sinal recebido, encerrando...")
		cancel()
	}()

	manager.Start(ctx)
	log.Info().Msg("worker: encerrado")
}
