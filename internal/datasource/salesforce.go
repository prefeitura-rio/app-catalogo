package datasource

import (
	"context"
	"sync"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

// SalesForceDataSource adapta o SalesForceSyncService para a interface DataSource.
type SalesForceDataSource struct {
	syncService          salesForceSyncService
	syncInterval         time.Duration
	fullSyncInterval     time.Duration
	currentTime          func() time.Time
	mutex                sync.Mutex
	lastSuccessfulFullAt time.Time
}

type salesForceSyncService interface {
	FullSync(ctx context.Context) (int, error)
	DeltaSync(ctx context.Context) (int, error)
}

func NewSalesForceDataSource(
	syncService *services.SalesForceSyncService,
	syncInterval time.Duration,
	fullSyncInterval time.Duration,
) *SalesForceDataSource {
	return newSalesForceDataSource(syncService, syncInterval, fullSyncInterval, time.Now)
}

func newSalesForceDataSource(
	syncService salesForceSyncService,
	syncInterval time.Duration,
	fullSyncInterval time.Duration,
	currentTime func() time.Time,
) *SalesForceDataSource {
	return &SalesForceDataSource{
		syncService:      syncService,
		syncInterval:     syncInterval,
		fullSyncInterval: fullSyncInterval,
		currentTime:      currentTime,
	}
}

func (s *SalesForceDataSource) Name() string                { return "salesforce" }
func (s *SalesForceDataSource) Source() models.ItemSource   { return models.SourceSalesForce }
func (s *SalesForceDataSource) SyncInterval() time.Duration { return s.syncInterval }

func (s *SalesForceDataSource) Sync(ctx context.Context) (int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	currentTime := s.currentTime()
	if s.lastSuccessfulFullAt.IsZero() || !currentTime.Before(s.lastSuccessfulFullAt.Add(s.fullSyncInterval)) {
		changed, err := s.syncService.FullSync(ctx)
		if err == nil {
			s.lastSuccessfulFullAt = s.currentTime()
		}
		return changed, err
	}
	return s.syncService.DeltaSync(ctx)
}
