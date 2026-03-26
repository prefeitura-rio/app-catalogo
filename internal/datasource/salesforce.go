package datasource

import (
	"context"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

// SalesForceDataSource adapta o SalesForceSyncService para a interface DataSource.
type SalesForceDataSource struct {
	syncSvc      *services.SalesForceSyncService
	syncInterval time.Duration
}

func NewSalesForceDataSource(syncSvc *services.SalesForceSyncService, syncInterval time.Duration) *SalesForceDataSource {
	return &SalesForceDataSource{
		syncSvc:      syncSvc,
		syncInterval: syncInterval,
	}
}

func (s *SalesForceDataSource) Name() string               { return "salesforce" }
func (s *SalesForceDataSource) Source() models.ItemSource  { return models.SourceSalesForce }
func (s *SalesForceDataSource) SyncInterval() time.Duration { return s.syncInterval }

func (s *SalesForceDataSource) Sync(ctx context.Context) error {
	return s.syncSvc.DeltaSync(ctx)
}
