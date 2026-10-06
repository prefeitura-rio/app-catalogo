package datasource

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// appGoAPIFetcher é o contrato HTTP usado pelo sync (mockável em testes).
type appGoAPIFetcher interface {
	GetCourses(ctx context.Context, page int, updatedSince time.Time) ([]clients.Course, int, error)
	GetJobs(ctx context.Context, page int, updatedSince time.Time) ([]clients.Job, int, error)
	GetMEIOpportunities(ctx context.Context, page int, updatedSince time.Time) ([]clients.MEIOpportunity, int, error)
}

// appGoAPIItemRepo é o contrato de persistência usado pelo sync (mockável em testes).
type appGoAPIItemRepo interface {
	UpsertBatch(ctx context.Context, items []*models.CatalogItem) (int, error)
	SoftDeleteActiveNotIn(ctx context.Context, source models.ItemSource, keepExternalIDs []string) (int64, error)
}

// AppGoAPIDataSource sincroniza cursos, vagas e MEI do app-go-api.
type AppGoAPIDataSource struct {
	client       appGoAPIFetcher
	repo         appGoAPIItemRepo
	syncInterval time.Duration
}

func NewAppGoAPIDataSource(
	client appGoAPIFetcher,
	repo appGoAPIItemRepo,
	syncInterval time.Duration,
) *AppGoAPIDataSource {
	return &AppGoAPIDataSource{
		client:       client,
		repo:         repo,
		syncInterval: syncInterval,
	}
}

func (s *AppGoAPIDataSource) Name() string              { return "app-go-api" }
func (s *AppGoAPIDataSource) Source() models.ItemSource { return models.SourceAppGoAPI }
func (s *AppGoAPIDataSource) SyncInterval() time.Duration {
	return s.syncInterval
}

// Sync sincroniza cursos, vagas e MEI. Sempre busca desde o início (sem cursor por ora).
func (s *AppGoAPIDataSource) Sync(ctx context.Context) error {
	startedAt := time.Now()

	var errs []error
	if err := s.syncCourses(ctx); err != nil {
		log.Error().Err(err).Msg("appgoapi datasource: erro ao sincronizar cursos")
		errs = append(errs, fmt.Errorf("cursos: %w", err))
	}
	if err := s.syncJobs(ctx); err != nil {
		log.Error().Err(err).Msg("appgoapi datasource: erro ao sincronizar vagas")
		errs = append(errs, fmt.Errorf("vagas: %w", err))
	}
	if err := s.syncMEI(ctx); err != nil {
		log.Error().Err(err).Msg("appgoapi datasource: erro ao sincronizar MEI")
		errs = append(errs, fmt.Errorf("mei: %w", err))
	}

	log.Info().Dur("duration", time.Since(startedAt)).Msg("appgoapi datasource: sync concluído")
	return joinSyncErrors(errs)
}

func joinSyncErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msg := errs[0].Error()
	for _, e := range errs[1:] {
		msg += "; " + e.Error()
	}
	return fmt.Errorf("appgoapi sync: %s", msg)
}

func (s *AppGoAPIDataSource) syncCourses(ctx context.Context) error {
	allCourses, err := s.fetchAllCourses(ctx)
	if err != nil {
		return err
	}

	items := make([]*models.CatalogItem, 0, len(allCourses))
	keepIDs := make([]string, 0, len(allCourses))
	skipped := 0
	for _, c := range allCourses {
		if !courseIsIndexable(c) {
			skipped++
			continue
		}
		keepIDs = append(keepIDs, string(c.ID))
		items = append(items, mapCourse(c))
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	if err != nil {
		return err
	}

	var orphans int64
	if len(allCourses) == 0 || len(keepIDs) == 0 {
		log.Warn().
			Int("listed", len(allCourses)).
			Int("indexable", len(keepIDs)).
			Msg("appgoapi: SoftDelete de órfãos de cursos ignorado (listagem/indexáveis vazios)")
	} else {
		orphans, err = s.repo.SoftDeleteActiveNotIn(ctx, models.SourceCourses, keepIDs)
		if err != nil {
			return err
		}
	}

	log.Info().
		Int("processed", processed).
		Int("skipped_non_indexable", skipped).
		Int64("deactivated_orphans", orphans).
		Msg("appgoapi: cursos sincronizados")
	return nil
}

func (s *AppGoAPIDataSource) syncJobs(ctx context.Context) error {
	allJobs, err := s.fetchAllJobs(ctx)
	if err != nil {
		return err
	}

	items := make([]*models.CatalogItem, 0, len(allJobs))
	keepIDs := make([]string, 0, len(allJobs))
	skipped := 0
	for _, j := range allJobs {
		if !jobIsIndexable(j) {
			skipped++
			continue
		}
		keepIDs = append(keepIDs, j.ID)
		items = append(items, mapJob(j))
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	if err != nil {
		return err
	}

	var orphans int64
	if len(allJobs) == 0 || len(keepIDs) == 0 {
		log.Warn().
			Int("listed", len(allJobs)).
			Int("indexable", len(keepIDs)).
			Msg("appgoapi: SoftDelete de órfãos de vagas ignorado (listagem/indexáveis vazios)")
	} else {
		orphans, err = s.repo.SoftDeleteActiveNotIn(ctx, models.SourceJobs, keepIDs)
		if err != nil {
			return err
		}
	}

	log.Info().
		Int("processed", processed).
		Int("skipped_non_indexable", skipped).
		Int64("deactivated_orphans", orphans).
		Msg("appgoapi: vagas sincronizadas")
	return nil
}

func (s *AppGoAPIDataSource) syncMEI(ctx context.Context) error {
	allMEI, err := s.fetchAllMEI(ctx)
	if err != nil {
		return err
	}

	items := make([]*models.CatalogItem, 0, len(allMEI))
	keepIDs := make([]string, 0, len(allMEI))
	skipped := 0
	for _, m := range allMEI {
		if !meiIsIndexable(m) {
			skipped++
			continue
		}
		keepIDs = append(keepIDs, string(m.ID))
		items = append(items, mapMEI(m))
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	if err != nil {
		return err
	}

	var orphans int64
	if len(allMEI) == 0 || len(keepIDs) == 0 {
		log.Warn().
			Int("listed", len(allMEI)).
			Int("indexable", len(keepIDs)).
			Msg("appgoapi: SoftDelete de órfãos de MEI ignorado (listagem/indexáveis vazios)")
	} else {
		orphans, err = s.repo.SoftDeleteActiveNotIn(ctx, models.SourceMEI, keepIDs)
		if err != nil {
			return err
		}
	}

	log.Info().
		Int("processed", processed).
		Int("skipped_non_indexable", skipped).
		Int64("deactivated_orphans", orphans).
		Msg("appgoapi: MEI sincronizado")
	return nil
}

func (s *AppGoAPIDataSource) fetchAllCourses(ctx context.Context) ([]clients.Course, error) {
	var all []clients.Course
	page := 1
	for {
		courses, total, err := s.client.GetCourses(ctx, page, time.Time{})
		if err != nil {
			return nil, err
		}
		all = append(all, courses...)
		if len(all) >= total || len(courses) == 0 {
			break
		}
		page++
	}
	return all, nil
}

func (s *AppGoAPIDataSource) fetchAllJobs(ctx context.Context) ([]clients.Job, error) {
	var all []clients.Job
	page := 1
	for {
		jobs, total, err := s.client.GetJobs(ctx, page, time.Time{})
		if err != nil {
			return nil, err
		}
		all = append(all, jobs...)
		if len(all) >= total || len(jobs) == 0 {
			break
		}
		page++
	}
	return all, nil
}

func (s *AppGoAPIDataSource) fetchAllMEI(ctx context.Context) ([]clients.MEIOpportunity, error) {
	var all []clients.MEIOpportunity
	page := 1
	for {
		oportunidades, total, err := s.client.GetMEIOpportunities(ctx, page, time.Time{})
		if err != nil {
			return nil, err
		}
		all = append(all, oportunidades...)
		if len(all) >= total || len(oportunidades) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// courseIsIndexable retorna true para cursos visíveis e publicáveis.
// O app-go-api deriva status a partir de "published" (scheduled, accepting_enrollments,
// in_progress). Esses derivados ainda são ofertas públicas e devem permanecer no catálogo.
// Terminais / não públicos (finished, closed, canceled, draft, …) são excluídos.
func courseIsIndexable(c clients.Course) bool {
	if !c.IsVisible {
		return false
	}
	switch c.Status {
	case "published", "approved", "opened", "",
		"scheduled", "accepting_enrollments", "in_progress":
		return true
	default:
		return false
	}
}

// jobIsIndexable mantém apenas vagas publicadas e ativas no catálogo.
func jobIsIndexable(j clients.Job) bool {
	switch j.Status {
	case "publicado_ativo", "":
		return true
	default:
		return false
	}
}

// meiIsIndexable mantém apenas oportunidades MEI ativas.
func meiIsIndexable(m clients.MEIOpportunity) bool {
	switch m.Status {
	case "active", "":
		return true
	default:
		return false
	}
}

func mapCourse(c clients.Course) *models.CatalogItem {
	sourceData, _ := json.Marshal(c)
	now := c.UpdatedAt

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

	shortDesc := c.TargetAudience
	if shortDesc == "" && len(c.Description) > 0 {
		shortDesc = c.Description
	}
	if len(shortDesc) > 300 {
		shortDesc = shortDesc[:300]
	}

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
		Status:          models.StatusActive,
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

	var tags []string
	if j.RegimeContratacao.Descricao != "" {
		tags = append(tags, j.RegimeContratacao.Descricao)
	}
	if j.AcessibilidadePCD != "" && j.AcessibilidadePCD != "sem_restricao" {
		tags = append(tags, j.AcessibilidadePCD)
	}

	targetAudience, _ := json.Marshal(map[string]interface{}{
		"pcd": j.AcessibilidadePCD,
	})

	org := j.Contratante.NomeFantasia
	if org == "" && j.OrgaoParceiro != nil {
		org = j.OrgaoParceiro.Name
	}

	shortDesc := j.Description
	if len(shortDesc) > 300 {
		shortDesc = shortDesc[:300]
	}

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

	var tags []string
	tags = append(tags, m.CNAEIDs...)
	if m.FormaPagamento != "" {
		tags = append(tags, m.FormaPagamento)
	}

	var bairros []string
	if m.Bairro != "" {
		bairros = append(bairros, m.Bairro)
	}

	shortDesc := m.Description
	if len(shortDesc) > 300 {
		shortDesc = shortDesc[:300]
	}

	return &models.CatalogItem{
		ExternalID:      string(m.ID),
		Source:          models.SourceMEI,
		Type:            models.TypeMEIOpportunity,
		Title:           m.Title,
		Description:     m.Description,
		ShortDesc:       shortDesc,
		Organization:    m.OrgaoID,
		ImageURL:        m.ImageURL,
		Bairros:         bairros,
		Status:          models.StatusActive,
		Tags:            tags,
		ValidUntil:      m.DataExpiracao,
		TargetAudience:  json.RawMessage("{}"),
		SourceData:      sourceData,
		SourceUpdatedAt: &now,
	}
}
