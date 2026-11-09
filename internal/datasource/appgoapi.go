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
	RecordSyncEvent(ctx context.Context, event *models.SyncEvent) (int64, error)
	UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errMsg string, durationMs int) error
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

// syncEventUpdateTimeout limita a gravação do desfecho do evento de sync, que roda
// mesmo com o contexto da sync já cancelado (para o evento não ficar em "started").
const syncEventUpdateTimeout = 5 * time.Second

// Sync sincroniza cursos, vagas e MEI. Sempre busca desde o início (sem cursor por ora),
// por isso o evento gravado em sync_events é um full_sync da fonte composta app-go-api,
// com os totais processados/falhados somados das três sub-syncs.
func (s *AppGoAPIDataSource) Sync(ctx context.Context) error {
	startedAt := time.Now()
	eventID, recordErr := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceAppGoAPI,
		EventType: models.SyncTypeFullSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
		// sync_events.metadata é JSONB NOT NULL e o pgx envia RawMessage nil como NULL.
		Metadata: json.RawMessage(`{}`),
	})
	if recordErr != nil {
		log.Warn().Err(recordErr).Msg("appgoapi datasource: falha ao registrar evento de sync")
	}

	var errs []error
	processed, failed := 0, 0
	subSyncs := []struct {
		label  string
		logMsg string
		run    func(context.Context) (int, int, error)
	}{
		{"cursos", "appgoapi datasource: erro ao sincronizar cursos", s.syncCourses},
		{"vagas", "appgoapi datasource: erro ao sincronizar vagas", s.syncJobs},
		{"mei", "appgoapi datasource: erro ao sincronizar MEI", s.syncMEI},
	}
	for _, sub := range subSyncs {
		p, f, err := sub.run(ctx)
		processed += p
		failed += f
		if err != nil {
			log.Error().Err(err).Msg(sub.logMsg)
			errs = append(errs, fmt.Errorf("%s: %w", sub.label, err))
		}
	}

	syncErr := joinSyncErrors(errs)
	finalStatus := models.SyncStatusCompleted
	errMsg := ""
	if syncErr != nil {
		finalStatus = models.SyncStatusFailed
		errMsg = syncErr.Error()
	}

	durationMs := int(time.Since(startedAt).Milliseconds())
	if recordErr == nil {
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), syncEventUpdateTimeout)
		if err := s.repo.UpdateSyncEvent(updateCtx, eventID, finalStatus, processed, failed, errMsg, durationMs); err != nil {
			log.Warn().Err(err).Int64("event_id", eventID).Msg("appgoapi datasource: falha ao atualizar evento de sync")
		}
		cancel()
	}

	log.Info().
		Int("processed", processed).
		Int("failed", failed).
		Int("duration_ms", durationMs).
		Msg("appgoapi datasource: sync concluído")
	return syncErr
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

// syncCourses retorna os itens gravados, os que falharam ao gravar e o erro da sub-sync.
func (s *AppGoAPIDataSource) syncCourses(ctx context.Context) (processed, failed int, err error) {
	allCourses, err := s.fetchAllCourses(ctx)
	if err != nil {
		return 0, 0, err
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

	processed, err = s.repo.UpsertBatch(ctx, items)
	if err != nil {
		return 0, len(items), err
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
			return processed, 0, err
		}
	}

	log.Info().
		Int("processed", processed).
		Int("skipped_non_indexable", skipped).
		Int64("deactivated_orphans", orphans).
		Msg("appgoapi: cursos sincronizados")
	return processed, 0, nil
}

// syncJobs retorna os itens gravados, os que falharam ao gravar e o erro da sub-sync.
func (s *AppGoAPIDataSource) syncJobs(ctx context.Context) (processed, failed int, err error) {
	allJobs, err := s.fetchAllJobs(ctx)
	if err != nil {
		return 0, 0, err
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

	processed, err = s.repo.UpsertBatch(ctx, items)
	if err != nil {
		return 0, len(items), err
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
			return processed, 0, err
		}
	}

	log.Info().
		Int("processed", processed).
		Int("skipped_non_indexable", skipped).
		Int64("deactivated_orphans", orphans).
		Msg("appgoapi: vagas sincronizadas")
	return processed, 0, nil
}

// syncMEI retorna os itens gravados, os que falharam ao gravar e o erro da sub-sync.
func (s *AppGoAPIDataSource) syncMEI(ctx context.Context) (processed, failed int, err error) {
	allMEI, err := s.fetchAllMEI(ctx)
	if err != nil {
		return 0, 0, err
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

	processed, err = s.repo.UpsertBatch(ctx, items)
	if err != nil {
		return 0, len(items), err
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
			return processed, 0, err
		}
	}

	log.Info().
		Int("processed", processed).
		Int("skipped_non_indexable", skipped).
		Int64("deactivated_orphans", orphans).
		Msg("appgoapi: MEI sincronizado")
	return processed, 0, nil
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
