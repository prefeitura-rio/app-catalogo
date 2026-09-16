package datasource

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

// TypesenseDataSource sincroniza serviços da Prefeitura Rio a partir do Typesense.
// Solução temporária enquanto a migração para o SalesForce não é concluída.
//
// Limitation (delta sync): ExportSince only returns published docs
// (awaiting_approval=false && status>=1). Unpublished/removed docs are therefore
// invisible to delta sync. SoftDelete of missing IDs runs on full sync only.
type TypesenseDataSource struct {
	client         *clients.TypesenseClient
	repo           *repository.CatalogItemRepository
	baseServiceURL string
	syncInterval   time.Duration
}

func NewTypesenseDataSource(
	client *clients.TypesenseClient,
	repo *repository.CatalogItemRepository,
	baseServiceURL string,
	syncInterval time.Duration,
) *TypesenseDataSource {
	return &TypesenseDataSource{
		client:         client,
		repo:           repo,
		baseServiceURL: baseServiceURL,
		syncInterval:   syncInterval,
	}
}

func (s *TypesenseDataSource) Name() string                { return "typesense" }
func (s *TypesenseDataSource) Source() models.ItemSource   { return models.SourceTypesense }
func (s *TypesenseDataSource) SyncInterval() time.Duration { return s.syncInterval }

// Sync determina o cursor pelo último sync completo e executa delta ou full sync.
func (s *TypesenseDataSource) Sync(ctx context.Context) error {
	since, eventType := s.resolveCursor(ctx)
	isFullSync := since.IsZero()

	startedAt := time.Now()
	eventID, _ := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceTypesense,
		EventType: eventType,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})

	processed, failed := 0, 0
	var lastErr string
	exportedIDs := make([]string, 0)

	err := s.client.ExportSince(ctx, since, func(svc clients.TypesenseService) error {
		item := mapTypesenseService(svc, s.baseServiceURL)
		if upsertErr := s.repo.Upsert(ctx, item); upsertErr != nil {
			failed++
			lastErr = upsertErr.Error()
			log.Error().Err(upsertErr).Str("id", svc.ID).Msg("typesense: erro ao upsert")
			return nil // continua os demais documentos
		}
		processed++
		exportedIDs = append(exportedIDs, svc.ID)
		return nil
	})

	finalStatus := models.SyncStatusCompleted
	if err != nil {
		finalStatus = models.SyncStatusFailed
		lastErr = err.Error()
		log.Error().Err(err).Msg("typesense datasource: sync falhou")
	} else if failed > 0 {
		finalStatus = models.SyncStatusFailed
		err = fmt.Errorf("typesense: %d upsert(s) falharam", failed)
		if lastErr == "" {
			lastErr = err.Error()
		}
		log.Error().Err(err).Msg("typesense datasource: sync incompleto por falhas de upsert")
	} else if isFullSync {
		// Soft-delete active typesense items missing from a complete full export.
		if len(exportedIDs) == 0 {
			log.Warn().Msg("typesense datasource: full sync retornou 0 docs; SoftDelete de órfãos ignorado")
		} else if deactivated, softErr := s.repo.SoftDeleteActiveNotIn(ctx, models.SourceTypesense, exportedIDs); softErr != nil {
			finalStatus = models.SyncStatusFailed
			err = softErr
			lastErr = softErr.Error()
			log.Error().Err(softErr).Msg("typesense datasource: falha ao SoftDelete itens ausentes")
		} else if deactivated > 0 {
			log.Info().Int64("deactivated", deactivated).Msg("typesense datasource: itens ausentes do export SoftDeleted")
		}
	}

	durationMs := int(time.Since(startedAt).Milliseconds())
	_ = s.repo.UpdateSyncEvent(ctx, eventID, finalStatus, processed, failed, lastErr, durationMs)

	log.Info().
		Int("processed", processed).
		Int("failed", failed).
		Int("duration_ms", durationMs).
		Msg("typesense datasource: sync concluído")

	return err
}

// resolveCursor retorna o timestamp do último sync completo.
// Se não há histórico, retorna zero (full sync).
func (s *TypesenseDataSource) resolveCursor(ctx context.Context) (time.Time, models.SyncEventType) {
	statuses, err := s.repo.GetLastSyncEvents(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("typesense: não foi possível ler histórico de sync, executando full sync")
		return time.Time{}, models.SyncTypeFullSync
	}

	for _, st := range statuses {
		if st.Source == models.SourceTypesense &&
			st.LastStatus == models.SyncStatusCompleted &&
			st.LastCompletedAt != nil {
			log.Info().Time("since", *st.LastCompletedAt).Msg("typesense datasource: executando delta sync")
			return *st.LastCompletedAt, models.SyncTypeDeltaSync
		}
	}

	log.Info().Msg("typesense datasource: sem cursor, executando full sync")
	return time.Time{}, models.SyncTypeFullSync
}

// mapTypesenseService converte um documento Typesense em CatalogItem.
func mapTypesenseService(svc clients.TypesenseService, baseURL string) *models.CatalogItem {
	sourceData, _ := json.Marshal(svc)

	lastUpdate := time.Unix(svc.LastUpdate, 0)
	var publishedAt *time.Time
	if svc.PublishedAt != nil && *svc.PublishedAt > 0 {
		t := time.Unix(*svc.PublishedAt, 0)
		publishedAt = &t
	}

	status := models.StatusActive
	if svc.AwaitingApproval || svc.Status < 1 {
		status = models.StatusDraft
	}

	return &models.CatalogItem{
		ExternalID:      svc.ID,
		Source:          models.SourceTypesense,
		Type:            models.TypeService,
		Title:           svc.NomeServico,
		Description:     svc.DescricaoCompleta,
		ShortDesc:       svc.Resumo,
		Organization:    strings.Join(svc.OrgaoGestor, ", "),
		URL:             buildTypesenseServiceURL(svc, baseURL),
		Modalidade:      inferTypesenseModalidade(svc),
		Status:          status,
		Tags:            buildTypesenseTags(svc),
		TargetAudience:  mapTypesenseTargetAudience(svc),
		SourceData:      sourceData,
		ValidFrom:       publishedAt,
		SourceUpdatedAt: &lastUpdate,
	}
}

func buildTypesenseServiceURL(svc clients.TypesenseService, baseURL string) string {
	// Primeiro botão habilitado com URL
	var buttons []struct {
		URLService string `json:"url_service"`
		IsEnabled  bool   `json:"is_enabled"`
	}
	if len(svc.Buttons) > 0 {
		_ = json.Unmarshal(svc.Buttons, &buttons)
		for _, b := range buttons {
			if b.IsEnabled && b.URLService != "" {
				return b.URLService
			}
		}
	}
	// Fallback: slug
	if svc.Slug != "" && baseURL != "" {
		return strings.TrimRight(baseURL, "/") + "/servicos/" + svc.Slug
	}
	return ""
}

func inferTypesenseModalidade(svc clients.TypesenseService) string {
	hasDigital := len(svc.CanaisDigitais) > 0
	hasPresencial := len(svc.CanaisPresenciais) > 0
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

func buildTypesenseTags(svc clients.TypesenseService) []string {
	var tags []string
	if svc.TemaGeral != "" {
		tags = append(tags, svc.TemaGeral)
	}
	if svc.SubCategoria != "" {
		tags = append(tags, svc.SubCategoria)
	}
	if svc.CustoServico != "" {
		tags = append(tags, svc.CustoServico)
	}
	if tags == nil {
		return []string{}
	}
	return tags
}

func mapTypesenseTargetAudience(svc clients.TypesenseService) json.RawMessage {
	if len(svc.PublicoEspecifico) == 0 {
		return json.RawMessage("{}")
	}

	ta := models.TargetAudienceData{}
	for _, p := range svc.PublicoEspecifico {
		pl := strings.ToLower(p)
		switch {
		case strings.Contains(pl, "pcd") ||
			strings.Contains(pl, "deficiência") ||
			strings.Contains(pl, "deficiencia"):
			ta.Deficiencia = append(ta.Deficiencia, p)
		case strings.Contains(pl, "idoso") ||
			strings.Contains(pl, "terceira idade"):
			ta.FaixaEtaria = append(ta.FaixaEtaria, "60+")
		case strings.Contains(pl, "criança") ||
			strings.Contains(pl, "crianca") ||
			strings.Contains(pl, "menor de idade"):
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

	raw, _ := json.Marshal(ta)
	return raw
}
