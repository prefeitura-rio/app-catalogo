package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const (
	maximumAppGoAPIPages            = 10_000
	maximumAppGoAPIItemsPerVertical = 100_000
	maximumShortDescription         = 300
)

type appGoAPIClient interface {
	GetCourses(ctx context.Context, page int, updatedSince time.Time) ([]clients.Course, int, error)
	GetJobs(ctx context.Context, page int, updatedSince time.Time) ([]clients.Job, int, error)
	GetMEIOpportunities(ctx context.Context, page int, updatedSince time.Time) ([]clients.MEIOpportunity, int, error)
}

type appGoAPICatalogRepository interface {
	WithSourceSyncLease(
		ctx context.Context,
		source models.ItemSource,
		operation func(context.Context) (int, error),
	) (int, error)
	ReconcileSourceSnapshot(
		ctx context.Context,
		source models.ItemSource,
		items []*models.CatalogItem,
		expectedItemCount int,
		sourceUpdatedUpperBound *time.Time,
		snapshotStartedAt time.Time,
	) (int, int, error)
}

// AppGoAPIDataSource sincroniza cursos, vagas e MEI do app-go-api.
type AppGoAPIDataSource struct {
	client       appGoAPIClient
	repo         appGoAPICatalogRepository
	syncInterval time.Duration
	currentTime  func() time.Time
}

func NewAppGoAPIDataSource(
	client appGoAPIClient,
	repo appGoAPICatalogRepository,
	syncInterval time.Duration,
) *AppGoAPIDataSource {
	return &AppGoAPIDataSource{
		client:       client,
		repo:         repo,
		syncInterval: syncInterval,
		currentTime:  time.Now,
	}
}

func (s *AppGoAPIDataSource) Name() string                { return "app-go-api" }
func (s *AppGoAPIDataSource) Source() models.ItemSource   { return models.SourceAppGoAPI }
func (s *AppGoAPIDataSource) SyncInterval() time.Duration { return s.syncInterval }

// Sync sincroniza cursos, vagas e MEI. Sempre busca desde o início (sem cursor por ora).
// Retorna o total de itens processados (upsertados) nas três fontes.
func (s *AppGoAPIDataSource) Sync(ctx context.Context) (int, error) {
	startedAt := time.Now()
	total := 0
	var syncErrors []error

	processedCourses, coursesError := s.syncCourses(ctx)
	total += processedCourses
	if coursesError != nil {
		wrappedError := fmt.Errorf("cursos: %w", coursesError)
		syncErrors = append(syncErrors, wrappedError)
		log.Error().Err(wrappedError).Msg("appgoapi datasource: erro ao sincronizar cursos")
	}

	processedJobs, jobsError := s.syncJobs(ctx)
	total += processedJobs
	if jobsError != nil {
		wrappedError := fmt.Errorf("vagas: %w", jobsError)
		syncErrors = append(syncErrors, wrappedError)
		log.Error().Err(wrappedError).Msg("appgoapi datasource: erro ao sincronizar vagas")
	}

	processedMEI, meiError := s.syncMEI(ctx)
	total += processedMEI
	if meiError != nil {
		wrappedError := fmt.Errorf("mei: %w", meiError)
		syncErrors = append(syncErrors, wrappedError)
		log.Error().Err(wrappedError).Msg("appgoapi datasource: erro ao sincronizar MEI")
	}

	joinedError := errors.Join(syncErrors...)
	log.Info().
		Int("changed", total).
		Int("failed_verticals", len(syncErrors)).
		Dur("duration", time.Since(startedAt)).
		Msg("appgoapi datasource: sync concluído")
	return total, joinedError
}

func (s *AppGoAPIDataSource) syncCourses(ctx context.Context) (int, error) {
	return s.repo.WithSourceSyncLease(ctx, models.SourceCourses, s.syncCoursesWithLease)
}

func (s *AppGoAPIDataSource) syncCoursesWithLease(ctx context.Context) (int, error) {
	snapshotStartedAt := s.currentTime().UTC()
	allCourses, expectedItemCount, collectionError := collectCompleteAppGoAPISnapshot(
		ctx,
		"courses",
		s.client.GetCourses,
	)
	if collectionError != nil {
		return 0, collectionError
	}

	items := make([]*models.CatalogItem, 0, len(allCourses))
	for _, course := range allCourses {
		items = append(items, mapCourse(course))
	}
	changed, reconciliationError := s.reconcileSnapshot(
		ctx,
		models.SourceCourses,
		items,
		expectedItemCount,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		return 0, fmt.Errorf("reconcile courses: %w", reconciliationError)
	}
	log.Info().Int("changed", changed).Int("received", len(items)).Msg("appgoapi: cursos sincronizados")
	return changed, nil
}

func (s *AppGoAPIDataSource) syncJobs(ctx context.Context) (int, error) {
	return s.repo.WithSourceSyncLease(ctx, models.SourceJobs, s.syncJobsWithLease)
}

func (s *AppGoAPIDataSource) syncJobsWithLease(ctx context.Context) (int, error) {
	snapshotStartedAt := s.currentTime().UTC()
	allJobs, expectedItemCount, collectionError := collectCompleteAppGoAPISnapshot(
		ctx,
		"jobs",
		s.client.GetJobs,
	)
	if collectionError != nil {
		return 0, collectionError
	}

	items := make([]*models.CatalogItem, 0, len(allJobs))
	for _, job := range allJobs {
		items = append(items, mapJob(job))
	}
	changed, reconciliationError := s.reconcileSnapshot(
		ctx,
		models.SourceJobs,
		items,
		expectedItemCount,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		return 0, fmt.Errorf("reconcile jobs: %w", reconciliationError)
	}
	log.Info().Int("changed", changed).Int("received", len(items)).Msg("appgoapi: vagas sincronizadas")
	return changed, nil
}

func (s *AppGoAPIDataSource) syncMEI(ctx context.Context) (int, error) {
	return s.repo.WithSourceSyncLease(ctx, models.SourceMEI, s.syncMEIWithLease)
}

func (s *AppGoAPIDataSource) syncMEIWithLease(ctx context.Context) (int, error) {
	snapshotStartedAt := s.currentTime().UTC()
	allMEI, expectedItemCount, collectionError := collectCompleteAppGoAPISnapshot(
		ctx,
		"MEI opportunities",
		s.client.GetMEIOpportunities,
	)
	if collectionError != nil {
		return 0, collectionError
	}

	items := make([]*models.CatalogItem, 0, len(allMEI))
	for _, opportunity := range allMEI {
		items = append(items, mapMEI(opportunity))
	}
	changed, reconciliationError := s.reconcileSnapshot(
		ctx,
		models.SourceMEI,
		items,
		expectedItemCount,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		return 0, fmt.Errorf("reconcile MEI opportunities: %w", reconciliationError)
	}
	log.Info().Int("changed", changed).Int("received", len(items)).Msg("appgoapi: MEI sincronizado")
	return changed, nil
}

type appGoAPIPageFetcher[SourceRecord any] func(
	ctx context.Context,
	page int,
	updatedSince time.Time,
) ([]SourceRecord, int, error)

// collectCompleteAppGoAPISnapshot accepts a vertical only when every page
// reports the same total and the received count matches it exactly. An empty
// page before that boundary is a truncated snapshot, never completion.
func collectCompleteAppGoAPISnapshot[SourceRecord any](
	ctx context.Context,
	verticalName string,
	fetchPage appGoAPIPageFetcher[SourceRecord],
) ([]SourceRecord, int, error) {
	records := make([]SourceRecord, 0)
	expectedItemCount := -1
	for page := 1; ; page++ {
		if page > maximumAppGoAPIPages {
			return nil, 0, fmt.Errorf("%s pagination exceeded %d pages", verticalName, maximumAppGoAPIPages)
		}
		pageRecords, reportedItemCount, fetchError := fetchPage(ctx, page, time.Time{})
		if fetchError != nil {
			return nil, 0, fetchError
		}
		if reportedItemCount < 0 || reportedItemCount > maximumAppGoAPIItemsPerVertical {
			return nil, 0, fmt.Errorf(
				"%s pagination reported invalid total %d",
				verticalName,
				reportedItemCount,
			)
		}
		if expectedItemCount == -1 {
			expectedItemCount = reportedItemCount
		} else if reportedItemCount != expectedItemCount {
			return nil, 0, fmt.Errorf(
				"%s pagination total changed from %d to %d",
				verticalName,
				expectedItemCount,
				reportedItemCount,
			)
		}
		if len(records)+len(pageRecords) > expectedItemCount {
			return nil, 0, fmt.Errorf(
				"%s pagination returned more than its reported total %d",
				verticalName,
				expectedItemCount,
			)
		}
		records = append(records, pageRecords...)
		if len(records) == expectedItemCount {
			return records, expectedItemCount, nil
		}
		if len(pageRecords) == 0 {
			return nil, 0, fmt.Errorf(
				"%s pagination ended after %d of %d items",
				verticalName,
				len(records),
				expectedItemCount,
			)
		}
	}
}

func (s *AppGoAPIDataSource) reconcileSnapshot(
	ctx context.Context,
	source models.ItemSource,
	items []*models.CatalogItem,
	expectedItemCount int,
	snapshotStartedAt time.Time,
) (int, error) {
	if validationError := validateAppGoAPIItems(items); validationError != nil {
		return 0, validationError
	}
	sourceUpdatedUpperBound := maximumSourceUpdatedAt(items)
	if sourceUpdatedUpperBound == nil || sourceUpdatedUpperBound.Before(snapshotStartedAt) {
		sourceUpdatedUpperBound = &snapshotStartedAt
	}
	upserted, deactivated, reconciliationError := s.repo.ReconcileSourceSnapshot(
		ctx,
		source,
		items,
		expectedItemCount,
		sourceUpdatedUpperBound,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		return 0, reconciliationError
	}
	return upserted + deactivated, nil
}

func maximumSourceUpdatedAt(items []*models.CatalogItem) *time.Time {
	var maximumTimestamp time.Time
	for _, item := range items {
		if item != nil && item.SourceUpdatedAt != nil && item.SourceUpdatedAt.After(maximumTimestamp) {
			maximumTimestamp = *item.SourceUpdatedAt
		}
	}
	if maximumTimestamp.IsZero() {
		return nil
	}
	return &maximumTimestamp
}

func mapCourse(c clients.Course) *models.CatalogItem {
	sourceData, _ := json.Marshal(c)
	now := c.UpdatedAt

	// Tags: tema + categorias + carga horária + certificado
	var tags []string
	if c.Theme != "" {
		tags = append(tags, c.Theme)
	}
	for _, cat := range c.Categorias {
		if cat.Nome != "" && cat.Nome != c.Theme {
			tags = append(tags, cat.Nome)
		}
	}
	if c.Turno != "" && c.Turno != "LIVRE" {
		tags = append(tags, c.Turno)
	}
	if c.HasCertificate {
		tags = append(tags, "Com certificado")
	}

	// ShortDesc: público-alvo ou início da descrição
	shortDesc := c.TargetAudience
	if shortDesc == "" && len(c.Description) > 0 {
		shortDesc = c.Description
	}
	shortDesc = truncateByRunes(shortDesc, maximumShortDescription)

	return &models.CatalogItem{
		ExternalID:      string(c.ID),
		Source:          models.SourceCourses,
		Type:            models.TypeCourse,
		Title:           c.Title,
		Description:     c.Description,
		ShortDesc:       shortDesc,
		Organization:    c.Organization,
		URL:             c.URL,
		ImageURL:        c.ImageURL,
		Modalidade:      c.Modalidade,
		Status:          mapCourseStatus(c),
		Tags:            tags,
		SourceData:      sourceData,
		ValidUntil:      c.DataLimiteInscr,
		TargetAudience:  json.RawMessage("{}"),
		SourceUpdatedAt: &now,
	}
}

func mapJob(j clients.Job) *models.CatalogItem {
	sourceData, _ := json.Marshal(j)
	now := j.UpdatedAt

	bairros := []string{}
	if j.Bairro != "" {
		bairros = append(bairros, j.Bairro)
	}

	// Tags: regime, PCD
	var tags []string
	if j.RegimeContratacao.Descricao != "" {
		tags = append(tags, j.RegimeContratacao.Descricao)
	}
	if j.AcessibilidadePCD != "" && j.AcessibilidadePCD != "sem_restricao" {
		tags = append(tags, j.AcessibilidadePCD)
	}

	// TargetAudience: inclui informações de PCD no contrato canônico.
	targetAudienceData := models.TargetAudienceData{}
	if accessibility := strings.TrimSpace(j.AcessibilidadePCD); accessibility != "" && accessibility != "sem_restricao" {
		targetAudienceData.Deficiencia = []string{accessibility}
	}
	targetAudience, _ := json.Marshal(targetAudienceData)

	// Organização: nome fantasia do contratante
	org := j.Contratante.NomeFantasia
	if org == "" && j.OrgaoParceiro != nil {
		org = j.OrgaoParceiro.Name
	}

	shortDesc := truncateByRunes(j.Description, maximumShortDescription)

	return &models.CatalogItem{
		ExternalID:      j.ID,
		Source:          models.SourceJobs,
		Type:            models.TypeJob,
		Title:           j.Title,
		Description:     j.Description,
		ShortDesc:       shortDesc,
		Organization:    org,
		ImageURL:        j.Contratante.URLLogo,
		Modalidade:      j.ModeloTrabalho.Descricao,
		Bairros:         bairros,
		Status:          mapJobStatus(j.Status),
		Tags:            tags,
		TargetAudience:  targetAudience,
		SourceData:      sourceData,
		ValidUntil:      j.DataLimite,
		SourceUpdatedAt: &now,
	}
}

func mapMEI(m clients.MEIOpportunity) *models.CatalogItem {
	sourceData, _ := json.Marshal(m)
	now := m.UpdatedAt

	// Tags: CNAEs + forma de pagamento
	var tags []string
	tags = append(tags, m.CNAEIDs...)
	if m.FormaPagamento != "" {
		tags = append(tags, m.FormaPagamento)
	}

	// Bairros
	var bairros []string
	if m.Bairro != "" {
		bairros = append(bairros, m.Bairro)
	}

	shortDesc := truncateByRunes(m.Description, maximumShortDescription)

	return &models.CatalogItem{
		ExternalID:      string(m.ID),
		Source:          models.SourceMEI,
		Type:            models.TypeMEIOpportunity,
		Title:           m.Title,
		Description:     m.Description,
		ShortDesc:       shortDesc,
		Organization:    m.OrgaoID, // ID do órgão (sem resolução de nome por ora)
		ImageURL:        m.ImageURL,
		Bairros:         bairros,
		Status:          mapMEIStatus(m.Status),
		Tags:            tags,
		ValidUntil:      m.DataExpiracao,
		TargetAudience:  json.RawMessage("{}"),
		SourceData:      sourceData,
		SourceUpdatedAt: &now,
	}
}

func mapCourseStatus(course clients.Course) models.ItemStatus {
	if !course.IsVisible {
		return models.StatusInactive
	}
	switch strings.ToLower(strings.TrimSpace(course.Status)) {
	case "", "published", "approved", "opened":
		return models.StatusActive
	case "draft", "pending", "in_review":
		return models.StatusDraft
	default:
		return models.StatusInactive
	}
}

func mapJobStatus(status string) models.ItemStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "publicado_ativo":
		return models.StatusActive
	case "em_edicao", "em_aprovacao":
		return models.StatusDraft
	default:
		return models.StatusInactive
	}
}

func mapMEIStatus(status string) models.ItemStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active":
		return models.StatusActive
	case "draft":
		return models.StatusDraft
	default:
		return models.StatusInactive
	}
}

func truncateByRunes(text string, maximumRunes int) string {
	if maximumRunes <= 0 {
		return ""
	}
	textRunes := []rune(text)
	if len(textRunes) <= maximumRunes {
		return text
	}
	return string(textRunes[:maximumRunes])
}

func validateAppGoAPIItems(items []*models.CatalogItem) error {
	seenExternalIDs := make(map[string]struct{}, len(items))
	for itemIndex, item := range items {
		if item == nil {
			return fmt.Errorf("item %d is nil", itemIndex)
		}
		if strings.TrimSpace(item.ExternalID) == "" {
			return fmt.Errorf("item %d has an empty external id", itemIndex)
		}
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("item %q has an empty title", item.ExternalID)
		}
		if item.SourceUpdatedAt == nil || item.SourceUpdatedAt.IsZero() {
			return fmt.Errorf("item %q has no upstream update timestamp", item.ExternalID)
		}
		if _, duplicate := seenExternalIDs[item.ExternalID]; duplicate {
			return fmt.Errorf("duplicate external id %q", item.ExternalID)
		}
		seenExternalIDs[item.ExternalID] = struct{}{}
	}
	return nil
}
