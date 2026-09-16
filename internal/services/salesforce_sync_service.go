package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	cartaServicosCursorObjectType = "carta_servicos"
	defaultDetailConcurrency      = 8
	maxDetailConcurrency          = 32
	defaultListConcurrency        = 8
)

var htmlTagPattern = regexp.MustCompile(`(?i)<[^>]*>`)

type cartaServicosAPI interface {
	ListAllThemes(ctx context.Context, includeEmpty bool) ([]clients.CartaTheme, error)
	ListAllSubthemes(ctx context.Context, themeSlug string) ([]clients.CartaSubtheme, error)
	ListAllServicesBySubtheme(ctx context.Context, subthemeSlug string) ([]clients.CartaServiceListItem, error)
	GetService(ctx context.Context, slug string) (*clients.CartaServiceDetail, error)
	GetServiceCanonical(ctx context.Context, slug string) (*clients.CartaServiceDetail, string, error)
}

type cartaSyncRepository interface {
	RecordSyncEvent(ctx context.Context, event *models.SyncEvent) (int64, error)
	UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errMsg string, durationMs int) error
	GetSalesForceCursor(ctx context.Context, objectType string) (*models.SalesForceSyncCursor, error)
	UpsertSalesForceCursor(ctx context.Context, objectType string, lastSyncAt time.Time, deltaToken string) error
	Upsert(ctx context.Context, item *models.CatalogItem) error
	UpsertBatch(ctx context.Context, items []*models.CatalogItem) (int, error)
	SoftDelete(ctx context.Context, source models.ItemSource, externalID string) error
	SoftDeleteActiveNotIn(ctx context.Context, source models.ItemSource, keepExternalIDs []string) (int64, error)
}

// SalesForceSyncService sincroniza a Carta de Serviços (API CloudHub) para catalog_items.
type SalesForceSyncService struct {
	client            cartaServicosAPI
	repo              cartaSyncRepository
	baseServiceURL    string
	detailConcurrency int
}

func NewSalesForceSyncService(
	client *clients.CartaServicosClient,
	repo *repository.CatalogItemRepository,
	baseServiceURL string,
	detailConcurrency int,
) *SalesForceSyncService {
	return newSalesForceSyncService(client, repo, baseServiceURL, detailConcurrency)
}

func newSalesForceSyncService(
	client cartaServicosAPI,
	repo cartaSyncRepository,
	baseServiceURL string,
	detailConcurrency int,
) *SalesForceSyncService {
	if detailConcurrency <= 0 {
		detailConcurrency = defaultDetailConcurrency
	}
	if detailConcurrency > maxDetailConcurrency {
		detailConcurrency = maxDetailConcurrency
	}
	return &SalesForceSyncService{
		client:            client,
		repo:              repo,
		baseServiceURL:    strings.TrimRight(baseServiceURL, "/"),
		detailConcurrency: detailConcurrency,
	}
}

func (s *SalesForceSyncService) FullSync(ctx context.Context) error {
	return s.runSync(ctx, time.Time{}, models.SyncTypeFullSync)
}

func (s *SalesForceSyncService) DeltaSync(ctx context.Context) error {
	cursor, err := s.repo.GetSalesForceCursor(ctx, cartaServicosCursorObjectType)
	if err != nil || cursor == nil || cursor.LastSyncAt == nil {
		log.Info().Msg("salesforce: cursor não encontrado, executando full sync")
		return s.FullSync(ctx)
	}
	return s.runSync(ctx, *cursor.LastSyncAt, models.SyncTypeDeltaSync)
}

func (s *SalesForceSyncService) SyncRecord(ctx context.Context, slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return errors.New("salesforce: slug vazio")
	}

	detail, canonical, err := s.client.GetServiceCanonical(ctx, slug)
	if err != nil {
		if strings.Contains(err.Error(), "não encontrado") {
			return s.repo.SoftDelete(ctx, models.SourceSalesForce, slug)
		}
		return err
	}

	item := MapCartaServiceDetail(detail, s.baseServiceURL)
	if item == nil {
		return fmt.Errorf("salesforce: falha ao mapear serviço %q", canonical)
	}
	if err := s.repo.Upsert(ctx, item); err != nil {
		return err
	}

	if canonical != slug {
		if softErr := s.repo.SoftDelete(ctx, models.SourceSalesForce, slug); softErr != nil {
			log.Warn().Err(softErr).Str("old_slug", slug).Str("new_slug", canonical).
				Msg("salesforce: falha ao SoftDelete slug antigo após redirect")
		}
	}
	return nil
}

func (s *SalesForceSyncService) runSync(ctx context.Context, since time.Time, eventType models.SyncEventType) error {
	isFull := since.IsZero()
	startedAt := time.Now()

	eventID, _ := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceSalesForce,
		EventType: eventType,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})

	log.Info().
		Bool("full", isFull).
		Time("since", since).
		Msg("salesforce: iniciando sync")

	listed, err := s.listAllServiceSummaries(ctx)
	if err != nil {
		s.finishEvent(ctx, eventID, startedAt, models.SyncStatusFailed, 0, 0, err.Error())
		return err
	}

	toFetch := filterServicesNeedingDetail(listed, since)
	log.Info().
		Int("listed", len(listed)).
		Int("to_fetch", len(toFetch)).
		Msg("salesforce: listagem concluída")

	details, fetchFailed, fetchErr := s.fetchDetails(ctx, toFetch)
	if fetchErr != nil && len(details) == 0 {
		s.finishEvent(ctx, eventID, startedAt, models.SyncStatusFailed, 0, fetchFailed, fetchErr.Error())
		return fetchErr
	}

	items := make([]*models.CatalogItem, 0, len(details))
	for _, d := range details {
		if item := MapCartaServiceDetail(d, s.baseServiceURL); item != nil {
			items = append(items, item)
		}
	}

	processed, upsertErr := s.repo.UpsertBatch(ctx, items)
	failed := fetchFailed + (len(items) - processed)
	if upsertErr != nil {
		s.finishEvent(ctx, eventID, startedAt, models.SyncStatusFailed, processed, failed, upsertErr.Error())
		return upsertErr
	}

	var softErr error
	if isFull {
		seen := make([]string, 0, len(listed))
		for _, item := range listed {
			if item.Slug != "" {
				seen = append(seen, item.Slug)
			}
		}
		if len(seen) == 0 {
			log.Warn().Msg("salesforce: full sync listou 0 serviços; SoftDelete de órfãos ignorado")
		} else if deactivated, err := s.repo.SoftDeleteActiveNotIn(ctx, models.SourceSalesForce, seen); err != nil {
			softErr = err
			log.Error().Err(err).Msg("salesforce: SoftDelete de órfãos falhou")
		} else if deactivated > 0 {
			log.Info().Int64("deactivated", deactivated).Msg("salesforce: órfãos SoftDeleted")
		}
	}

	finalStatus := models.SyncStatusCompleted
	errMsg := ""
	var retErr error
	if softErr != nil {
		finalStatus = models.SyncStatusFailed
		errMsg = softErr.Error()
		retErr = softErr
	} else if fetchFailed > 0 {
		finalStatus = models.SyncStatusFailed
		errMsg = fmt.Sprintf("%d detalhe(s) falharam", fetchFailed)
		retErr = fmt.Errorf("salesforce: %s", errMsg)
	} else if fetchErr != nil {
		finalStatus = models.SyncStatusFailed
		errMsg = fetchErr.Error()
		retErr = fetchErr
	}

	if finalStatus == models.SyncStatusCompleted {
		_ = s.repo.UpsertSalesForceCursor(ctx, cartaServicosCursorObjectType, time.Now().UTC(), "")
	} else {
		log.Warn().
			Int("failed", failed).
			Str("status", string(finalStatus)).
			Msg("salesforce: cursor não avançado devido a falhas — próximo sync re-tentará itens afetados")
	}

	s.finishEvent(ctx, eventID, startedAt, finalStatus, processed, failed, errMsg)

	log.Info().
		Int("processed", processed).
		Int("failed", failed).
		Int("duration_ms", int(time.Since(startedAt).Milliseconds())).
		Msg("salesforce: sync concluído")

	return retErr
}

func (s *SalesForceSyncService) finishEvent(
	ctx context.Context,
	eventID int64,
	startedAt time.Time,
	status models.SyncEventStatus,
	processed, failed int,
	errMsg string,
) {
	_ = s.repo.UpdateSyncEvent(ctx, eventID, status, processed, failed, errMsg, int(time.Since(startedAt).Milliseconds()))
}

func (s *SalesForceSyncService) listAllServiceSummaries(ctx context.Context) ([]clients.CartaServiceListItem, error) {
	themes, err := s.client.ListAllThemes(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("listar temas: %w", err)
	}

	// Fase 1: subtemas por tema (paralelo).
	var (
		subMu    sync.Mutex
		subSlugs []string
	)
	themeGroup, themeCtx := errgroup.WithContext(ctx)
	themeGroup.SetLimit(defaultListConcurrency)

	for _, theme := range themes {
		theme := theme
		if theme.Slug == "" {
			continue
		}
		themeGroup.Go(func() error {
			subthemes, err := s.client.ListAllSubthemes(themeCtx, theme.Slug)
			if err != nil {
				return fmt.Errorf("listar subtemas de %q: %w", theme.Slug, err)
			}
			subMu.Lock()
			for _, st := range subthemes {
				if st.Slug != "" {
					subSlugs = append(subSlugs, st.Slug)
				}
			}
			subMu.Unlock()
			return nil
		})
	}
	if err := themeGroup.Wait(); err != nil {
		return nil, err
	}

	// Fase 2: serviços por subtema (paralelo).
	var (
		svcMu sync.Mutex
		seen  = make(map[string]struct{})
		all   []clients.CartaServiceListItem
	)
	subGroup, subCtx := errgroup.WithContext(ctx)
	subGroup.SetLimit(defaultListConcurrency)

	for _, subSlug := range subSlugs {
		subSlug := subSlug
		subGroup.Go(func() error {
			services, err := s.client.ListAllServicesBySubtheme(subCtx, subSlug)
			if err != nil {
				return fmt.Errorf("listar serviços de %q: %w", subSlug, err)
			}
			svcMu.Lock()
			for _, svc := range services {
				if svc.Slug == "" {
					continue
				}
				if _, ok := seen[svc.Slug]; ok {
					continue
				}
				seen[svc.Slug] = struct{}{}
				all = append(all, svc)
			}
			svcMu.Unlock()
			return nil
		})
	}
	if err := subGroup.Wait(); err != nil {
		return nil, err
	}

	return all, nil
}

func filterServicesNeedingDetail(listed []clients.CartaServiceListItem, since time.Time) []clients.CartaServiceListItem {
	if since.IsZero() {
		return listed
	}
	out := make([]clients.CartaServiceListItem, 0, len(listed)/4)
	for _, item := range listed {
		mod := item.EffectiveModifiedAt()
		if mod.IsZero() || mod.After(since) {
			out = append(out, item)
		}
	}
	return out
}

// fetchDetails busca detalhes em paralelo.
// Falha em um item não cancela o lote (degradação parcial): o errgroup só
// limita concorrência; as goroutines retornam nil de propósito.
func (s *SalesForceSyncService) fetchDetails(
	ctx context.Context,
	items []clients.CartaServiceListItem,
) ([]*clients.CartaServiceDetail, int, error) {
	if len(items) == 0 {
		return nil, 0, nil
	}

	var (
		mu       sync.Mutex
		details  []*clients.CartaServiceDetail
		failed   int
		firstErr error
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.detailConcurrency)

	for _, item := range items {
		item := item
		g.Go(func() error {
			detail, err := s.client.GetService(gctx, item.Slug)
			if err != nil {
				var redir *clients.ServiceRedirectError
				if errors.As(err, &redir) {
					detail, err = s.client.GetService(gctx, redir.ToSlug)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				log.Error().Err(err).Str("slug", item.Slug).Msg("salesforce: falha ao buscar detalhe")
				if firstErr == nil {
					firstErr = err
				}
				return nil
			}
			details = append(details, detail)
			return nil
		})
	}

	_ = g.Wait()
	return details, failed, firstErr
}

func MapCartaServiceDetail(detail *clients.CartaServiceDetail, baseServiceURL string) *models.CatalogItem {
	if detail == nil || detail.Slug == "" || detail.Name == "" {
		return nil
	}

	sourceData, _ := json.Marshal(detail)
	status := mapCartaArticleStatus(detail.ArticleStatus)

	var sourceUpdatedAt *time.Time
	if t := maxNonZeroTime(
		parseOptionalCartaTime(detail.LastModifiedDate),
		parseOptionalCartaTime(detail.LastPublishedDate),
	); t != nil {
		sourceUpdatedAt = t
	}

	var validFrom *time.Time
	if t := parseOptionalCartaTime(detail.LastPublishedDate); t != nil {
		validFrom = t
	} else if t := parseOptionalCartaTime(detail.CreatedDate); t != nil {
		validFrom = t
	}

	return &models.CatalogItem{
		ExternalID:      detail.Slug,
		Source:          models.SourceSalesForce,
		Type:            models.TypeService,
		Title:           detail.Name,
		Description:     buildCartaDescription(detail),
		ShortDesc:       stripHTML(detail.Info.Summary),
		Organization:    detail.ResponsibleOrgUnit,
		URL:             buildCartaServiceURL(detail, baseServiceURL),
		Modalidade:      inferCartaModalidade(detail.Channels),
		Status:          status,
		Tags:            buildCartaTags(detail),
		Bairros:         []string{},
		TargetAudience:  mapCartaTargetAudience(detail.Info.TargetAudience),
		SourceData:      sourceData,
		ValidFrom:       validFrom,
		SourceUpdatedAt: sourceUpdatedAt,
	}
}

func mapCartaArticleStatus(status string) models.ItemStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online", "published", "ativo", "active":
		return models.StatusActive
	case "draft", "rascunho":
		return models.StatusDraft
	default:
		if status == "" {
			return models.StatusActive
		}
		return models.StatusInactive
	}
}

func buildCartaServiceURL(detail *clients.CartaServiceDetail, baseServiceURL string) string {
	for _, b := range detail.Buttons {
		if b.Enabled && strings.TrimSpace(b.URL) != "" {
			return b.URL
		}
	}
	for _, b := range detail.Buttons {
		if strings.TrimSpace(b.URL) != "" {
			return b.URL
		}
	}
	if detail.Slug != "" && baseServiceURL != "" {
		return strings.TrimRight(baseServiceURL, "/") + "/servicos/" + detail.Slug
	}
	return ""
}

func inferCartaModalidade(channels []clients.CartaChannel) string {
	hasDigital, hasPresencial := false, false
	for _, ch := range channels {
		switch strings.ToLower(strings.TrimSpace(ch.Type)) {
		case "digital":
			hasDigital = true
		case "presencial":
			hasPresencial = true
		}
	}
	switch {
	case hasDigital && hasPresencial:
		return "hibrido"
	case hasDigital:
		return "digital"
	case hasPresencial:
		return "presencial"
	default:
		return ""
	}
}

func buildCartaTags(detail *clients.CartaServiceDetail) []string {
	tags := make([]string, 0, 4)
	if detail.ThemeName != "" {
		tags = append(tags, detail.ThemeName)
	}
	if detail.SubthemeName != "" {
		tags = append(tags, detail.SubthemeName)
	}
	if detail.Info.Cost != nil && *detail.Info.Cost != "" {
		tags = append(tags, *detail.Info.Cost)
	} else if detail.Info.IsFree {
		tags = append(tags, "Gratuito")
	}
	if detail.ResponsibleOrgUnitShortName != "" {
		tags = append(tags, detail.ResponsibleOrgUnitShortName)
	}
	return tags
}

func buildCartaDescription(detail *clients.CartaServiceDetail) string {
	summary := stripHTML(detail.Info.Summary)
	fullDesc := stripHTML(detail.Info.FullDescription)

	parts := make([]string, 0, 4)
	if fullDesc != "" && fullDesc != summary {
		parts = append(parts, fullDesc)
	}
	if d := stripHTML(detail.HowToRequest.Instructions); d != "" {
		parts = append(parts, d)
	}
	if d := stripHTML(detail.HowToRequest.ServiceResult); d != "" {
		parts = append(parts, d)
	}
	if len(detail.HowToRequest.RequiredDocs) > 0 {
		parts = append(parts, "Documentos: "+strings.Join(detail.HowToRequest.RequiredDocs, ", "))
	}
	return strings.Join(parts, "\n\n")
}

func mapCartaTargetAudience(audiences []string) json.RawMessage {
	if len(audiences) == 0 {
		return json.RawMessage("{}")
	}

	ta := models.TargetAudienceData{}
	for _, p := range audiences {
		pl := strings.ToLower(p)
		switch {
		case strings.Contains(pl, "pcd") ||
			strings.Contains(pl, "deficiência") ||
			strings.Contains(pl, "deficiencia"):
			ta.Deficiencia = append(ta.Deficiencia, p)
		case strings.Contains(pl, "idoso") ||
			strings.Contains(pl, "terceira_idade") ||
			strings.Contains(pl, "terceira idade"):
			ta.FaixaEtaria = append(ta.FaixaEtaria, "60+")
		case strings.Contains(pl, "criança") ||
			strings.Contains(pl, "crianca") ||
			strings.Contains(pl, "menor"):
			ta.FaixaEtaria = append(ta.FaixaEtaria, "menor-18")
		case strings.Contains(pl, "mulher") ||
			strings.Contains(pl, "feminino"):
			ta.Genero = append(ta.Genero, p)
		case strings.Contains(pl, "pret") ||
			strings.Contains(pl, "pard") ||
			strings.Contains(pl, "branc") ||
			strings.Contains(pl, "indígena") ||
			strings.Contains(pl, "indigena") ||
			strings.Contains(pl, "amarel") ||
			strings.Contains(pl, "etnia") ||
			strings.Contains(pl, "raça") ||
			strings.Contains(pl, "raca"):
			ta.Etnia = append(ta.Etnia, p)
		default:
			ta.Outros = append(ta.Outros, p)
		}
	}

	raw, err := json.Marshal(ta)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

func stripHTML(raw string) string {
	if raw == "" {
		return ""
	}
	plain := htmlTagPattern.ReplaceAllString(raw, " ")
	plain = html.UnescapeString(plain)
	plain = strings.Join(strings.Fields(plain), " ")
	return strings.TrimSpace(plain)
}

func parseOptionalCartaTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return &t
		}
	}
	return nil
}

func maxNonZeroTime(times ...*time.Time) *time.Time {
	var best *time.Time
	for _, t := range times {
		if t == nil {
			continue
		}
		if best == nil || t.After(*best) {
			best = t
		}
	}
	return best
}
