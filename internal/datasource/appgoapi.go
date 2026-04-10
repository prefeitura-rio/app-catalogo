package datasource

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

// AppGoAPIDataSource sincroniza cursos, vagas e MEI do app-go-api.
type AppGoAPIDataSource struct {
	client       *clients.AppGoAPIClient
	repo         *repository.CatalogItemRepository
	syncInterval time.Duration
}

func NewAppGoAPIDataSource(
	client *clients.AppGoAPIClient,
	repo *repository.CatalogItemRepository,
	syncInterval time.Duration,
) *AppGoAPIDataSource {
	return &AppGoAPIDataSource{
		client:       client,
		repo:         repo,
		syncInterval: syncInterval,
	}
}

func (s *AppGoAPIDataSource) Name() string               { return "app-go-api" }
func (s *AppGoAPIDataSource) Source() models.ItemSource  { return models.SourceAppGoAPI }
func (s *AppGoAPIDataSource) SyncInterval() time.Duration { return s.syncInterval }

// Sync sincroniza cursos, vagas e MEI. Sempre busca desde o início (sem cursor por ora).
func (s *AppGoAPIDataSource) Sync(ctx context.Context) error {
	startedAt := time.Now()

	if err := s.syncCourses(ctx); err != nil {
		log.Error().Err(err).Msg("appgoapi datasource: erro ao sincronizar cursos")
	}
	if err := s.syncJobs(ctx); err != nil {
		log.Error().Err(err).Msg("appgoapi datasource: erro ao sincronizar vagas")
	}
	if err := s.syncMEI(ctx); err != nil {
		log.Error().Err(err).Msg("appgoapi datasource: erro ao sincronizar MEI")
	}

	log.Info().Dur("duration", time.Since(startedAt)).Msg("appgoapi datasource: sync concluído")
	return nil
}

func (s *AppGoAPIDataSource) syncCourses(ctx context.Context) error {
	var allCourses []clients.Course
	page := 1
	for {
		courses, total, err := s.client.GetCourses(ctx, page, time.Time{})
		if err != nil {
			return err
		}
		allCourses = append(allCourses, courses...)
		if len(allCourses) >= total || len(courses) == 0 {
			break
		}
		page++
	}

	items := make([]*models.CatalogItem, 0, len(allCourses))
	for _, c := range allCourses {
		items = append(items, mapCourse(c))
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	log.Info().Int("processed", processed).Msg("appgoapi: cursos sincronizados")
	return err
}

func (s *AppGoAPIDataSource) syncJobs(ctx context.Context) error {
	var allJobs []clients.Job
	page := 1
	for {
		jobs, total, err := s.client.GetJobs(ctx, page, time.Time{})
		if err != nil {
			return err
		}
		allJobs = append(allJobs, jobs...)
		if len(allJobs) >= total || len(jobs) == 0 {
			break
		}
		page++
	}

	items := make([]*models.CatalogItem, 0, len(allJobs))
	for _, j := range allJobs {
		items = append(items, mapJob(j))
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	log.Info().Int("processed", processed).Msg("appgoapi: vagas sincronizadas")
	return err
}

func (s *AppGoAPIDataSource) syncMEI(ctx context.Context) error {
	var allMEI []clients.MEIOpportunity
	page := 1
	for {
		oportunidades, total, err := s.client.GetMEIOpportunities(ctx, page, time.Time{})
		if err != nil {
			return err
		}
		allMEI = append(allMEI, oportunidades...)
		if len(allMEI) >= total || len(oportunidades) == 0 {
			break
		}
		page++
	}

	items := make([]*models.CatalogItem, 0, len(allMEI))
	for _, m := range allMEI {
		items = append(items, mapMEI(m))
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	log.Info().Int("processed", processed).Msg("appgoapi: MEI sincronizado")
	return err
}

func mapCourse(c clients.Course) *models.CatalogItem {
	sourceData, _ := json.Marshal(c)
	now := c.UpdatedAt
	tags := []string{}
	if c.Theme != "" {
		tags = append(tags, c.Theme)
	}
	return &models.CatalogItem{
		ExternalID:      string(c.ID),
		Source:          models.SourceCourses,
		Type:            models.TypeCourse,
		Title:           c.Title,
		Description:     c.Description,
		Organization:    c.Organization,
		URL:             c.URL,
		ImageURL:        c.ImageURL,
		Modalidade:      c.Modalidade,
		Status:          models.StatusActive,
		Tags:            tags,
		SourceData:      sourceData,
		TargetAudience:  json.RawMessage("{}"),
		SourceUpdatedAt: &now,
	}
}

func mapJob(j clients.Job) *models.CatalogItem {
	sourceData, _ := json.Marshal(j)
	now := j.UpdatedAt
	bairros := []string{}
	if j.Bairro != "" {
		bairros = append(bairros, string(j.Bairro))
	}
	targetAudience, _ := json.Marshal(map[string]interface{}{
		"pcd": j.PCD,
	})
	tags := []string{}
	if j.RegimeContratacao != "" {
		tags = append(tags, string(j.RegimeContratacao))
	}
	return &models.CatalogItem{
		ExternalID:      j.ID,
		Source:          models.SourceJobs,
		Type:            models.TypeJob,
		Title:           j.Title,
		Description:     j.Description,
		Organization:    j.Company,
		URL:             j.URL,
		Modalidade:      string(j.ModeloTrabalho),
		Bairros:         bairros,
		Status:          models.StatusActive,
		Tags:            tags,
		TargetAudience:  targetAudience,
		SourceData:      sourceData,
		SourceUpdatedAt: &now,
	}
}

func mapMEI(m clients.MEIOpportunity) *models.CatalogItem {
	sourceData, _ := json.Marshal(m)
	now := m.UpdatedAt
	tags := []string{}
	if m.Segmento != "" {
		tags = append(tags, m.Segmento)
	}
	return &models.CatalogItem{
		ExternalID:      string(m.ID),
		Source:          models.SourceMEI,
		Type:            models.TypeMEIOpportunity,
		Title:           m.Title,
		Description:     m.Description,
		Organization:    m.Organization,
		ImageURL:        m.ImageURL,
		Status:          models.StatusActive,
		Tags:            tags,
		TargetAudience:  json.RawMessage("{}"),
		SourceData:      sourceData,
		SourceUpdatedAt: &now,
	}
}
