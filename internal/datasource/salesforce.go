package datasource

import (
	"context"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

// SalesForceDataSource adapta o SalesForceSyncService (Carta CloudHub) para DataSource.
type SalesForceDataSource struct {
	syncSvc          *services.SalesForceSyncService
	syncInterval     time.Duration
	fullSyncInterval time.Duration
}

func NewSalesForceDataSource(
	syncSvc *services.SalesForceSyncService,
	syncInterval time.Duration,
	fullSyncInterval time.Duration,
) *SalesForceDataSource {
	return &SalesForceDataSource{
		syncSvc:          syncSvc,
		syncInterval:     syncInterval,
		fullSyncInterval: fullSyncInterval,
	}
}

func (s *SalesForceDataSource) Name() string                { return "salesforce" }
func (s *SalesForceDataSource) Source() models.ItemSource   { return models.SourceSalesForce }
func (s *SalesForceDataSource) SyncInterval() time.Duration { return s.syncInterval }

// FullSyncInterval define o ciclo do full sync agendado (SALESFORCE_FULL_SYNC_INTERVAL).
func (s *SalesForceDataSource) FullSyncInterval() time.Duration { return s.fullSyncInterval }

func (s *SalesForceDataSource) Sync(ctx context.Context) error {
	return s.syncSvc.DeltaSync(ctx)
}

// FullSync reprocessa a Carta inteira (detalhe de todos os serviços), ignorando o cursor.
func (s *SalesForceDataSource) FullSync(ctx context.Context) error {
	return s.syncSvc.FullSync(ctx)
}
