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
	maximumTypesenseDocumentsPerSync = 100_000
	legacyTypesenseCursorVersion     = 1
	typesenseCursorVersion           = 2
)

var errEmptyTypesenseSnapshot = errors.New("typesense full snapshot must contain at least one document")

type typesenseExporter interface {
	ExportSince(ctx context.Context, since time.Time, process func(clients.TypesenseService) error) error
}

type typesenseSyncRepository interface {
	WithSourceSyncLease(
		ctx context.Context,
		source models.ItemSource,
		operation func(context.Context) (int, error),
	) (int, error)
	RecordSyncEvent(ctx context.Context, event *models.SyncEvent) (int64, error)
	UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errorMessage string, durationMilliseconds int) error
	UpdateSyncEventWithMetadata(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errorMessage string, durationMilliseconds int, metadata json.RawMessage) error
	GetLastCompletedSyncMetadata(ctx context.Context, source models.ItemSource) (json.RawMessage, bool, error)
	UpsertBatch(ctx context.Context, items []*models.CatalogItem) (int, error)
	ReconcileSourceSnapshot(
		ctx context.Context,
		source models.ItemSource,
		items []*models.CatalogItem,
		expectedItemCount int,
		sourceUpdatedUpperBound *time.Time,
		snapshotStartedAt time.Time,
	) (int, int, error)
}

type typesenseSyncMetadata struct {
	CursorVersion        int   `json:"cursor_version"`
	CursorUnix           int64 `json:"cursor_unix"`
	LastFullSnapshotUnix int64 `json:"last_full_snapshot_unix"`
}

// TypesenseDataSource sincroniza serviços da Prefeitura Rio a partir do Typesense.
// Solução temporária enquanto a migração para o SalesForce não é concluída.
type TypesenseDataSource struct {
	client           typesenseExporter
	repo             typesenseSyncRepository
	baseServiceURL   string
	syncInterval     time.Duration
	fullSyncInterval time.Duration
	currentTime      func() time.Time
}

func NewTypesenseDataSource(
	client typesenseExporter,
	repo typesenseSyncRepository,
	baseServiceURL string,
	syncInterval time.Duration,
	fullSyncInterval time.Duration,
) *TypesenseDataSource {
	return newTypesenseDataSource(
		client,
		repo,
		baseServiceURL,
		syncInterval,
		fullSyncInterval,
		time.Now,
	)
}

func newTypesenseDataSource(
	client typesenseExporter,
	repo typesenseSyncRepository,
	baseServiceURL string,
	syncInterval time.Duration,
	fullSyncInterval time.Duration,
	currentTime func() time.Time,
) *TypesenseDataSource {
	return &TypesenseDataSource{
		client:           client,
		repo:             repo,
		baseServiceURL:   baseServiceURL,
		syncInterval:     syncInterval,
		fullSyncInterval: fullSyncInterval,
		currentTime:      currentTime,
	}
}

func (s *TypesenseDataSource) Name() string                { return "typesense" }
func (s *TypesenseDataSource) Source() models.ItemSource   { return models.SourceTypesense }
func (s *TypesenseDataSource) SyncInterval() time.Duration { return s.syncInterval }

// Sync usa o maior last_update upstream concluído como cursor. O lote inteiro é
// validado antes do upsert para que JSONL truncado ou malformado não produza uma
// sincronização parcialmente confirmada.
func (s *TypesenseDataSource) Sync(ctx context.Context) (int, error) {
	return s.repo.WithSourceSyncLease(ctx, models.SourceTypesense, s.syncWithLease)
}

func (s *TypesenseDataSource) syncWithLease(ctx context.Context) (int, error) {
	currentTime := s.currentTime().UTC()
	since, eventType, lastFullSnapshotAt, cursorError := s.resolveCursor(ctx, currentTime)
	if cursorError != nil {
		return 0, cursorError
	}

	startedAt := time.Now()
	eventID, recordEventError := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceTypesense,
		EventType: eventType,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})
	if recordEventError != nil {
		return 0, fmt.Errorf("typesense: registrar início da sincronização: %w", recordEventError)
	}

	items := make([]*models.CatalogItem, 0)
	seenExternalIDs := make(map[string]struct{})
	upstreamCursor := since
	exportError := s.client.ExportSince(ctx, since, func(service clients.TypesenseService) error {
		externalID := strings.TrimSpace(service.ID)
		if externalID == "" {
			return errors.New("documento sem id")
		}
		if strings.TrimSpace(service.NomeServico) == "" {
			return fmt.Errorf("documento %q sem título", externalID)
		}
		if service.LastUpdate <= 0 {
			return fmt.Errorf("documento %q sem last_update válido", externalID)
		}
		if _, duplicate := seenExternalIDs[externalID]; duplicate {
			return fmt.Errorf("export contém id duplicado %q", externalID)
		}
		if len(items) >= maximumTypesenseDocumentsPerSync {
			return fmt.Errorf("export excedeu o limite de %d documentos", maximumTypesenseDocumentsPerSync)
		}

		seenExternalIDs[externalID] = struct{}{}
		items = append(items, mapTypesenseService(service, s.baseServiceURL))
		serviceCursor := time.Unix(service.LastUpdate, 0).UTC()
		if serviceCursor.After(upstreamCursor) {
			upstreamCursor = serviceCursor
		}
		return nil
	})
	if exportError == nil && eventType == models.SyncTypeFullSync && len(items) == 0 {
		exportError = errEmptyTypesenseSnapshot
	}
	if exportError != nil {
		return 0, s.failSyncEvent(ctx, eventID, len(items), exportError, startedAt)
	}

	changed := 0
	var persistenceError error
	if eventType == models.SyncTypeFullSync {
		snapshotUpperBound := currentTime
		if upstreamCursor.After(snapshotUpperBound) {
			snapshotUpperBound = upstreamCursor
		}
		upserted, deactivated, reconciliationError := s.repo.ReconcileSourceSnapshot(
			ctx,
			models.SourceTypesense,
			items,
			len(items),
			&snapshotUpperBound,
			currentTime,
		)
		changed = upserted + deactivated
		persistenceError = reconciliationError
		if persistenceError == nil {
			lastFullSnapshotAt = s.currentTime().UTC()
		}
	} else {
		changed, persistenceError = s.repo.UpsertBatch(ctx, items)
	}
	if persistenceError != nil {
		return 0, s.failSyncEvent(ctx, eventID, len(items), fmt.Errorf("typesense: persistir lote: %w", persistenceError), startedAt)
	}

	metadata, marshalError := json.Marshal(typesenseSyncMetadata{
		CursorVersion:        typesenseCursorVersion,
		CursorUnix:           upstreamCursor.Unix(),
		LastFullSnapshotUnix: lastFullSnapshotAt.Unix(),
	})
	if marshalError != nil {
		return changed, s.failSyncEvent(ctx, eventID, len(items), fmt.Errorf("typesense: serializar cursor: %w", marshalError), startedAt)
	}

	durationMilliseconds := int(time.Since(startedAt).Milliseconds())
	if updateEventError := s.repo.UpdateSyncEventWithMetadata(
		ctx,
		eventID,
		models.SyncStatusCompleted,
		len(items),
		0,
		"",
		durationMilliseconds,
		metadata,
	); updateEventError != nil {
		return changed, fmt.Errorf("typesense: concluir evento de sincronização: %w", updateEventError)
	}

	log.Info().
		Int("processed", len(items)).
		Int("changed", changed).
		Int("duration_ms", durationMilliseconds).
		Time("cursor", upstreamCursor).
		Msg("typesense datasource: sync concluído")

	return changed, nil
}

func (s *TypesenseDataSource) failSyncEvent(
	ctx context.Context,
	eventID int64,
	processed int,
	syncError error,
	startedAt time.Time,
) error {
	durationMilliseconds := int(time.Since(startedAt).Milliseconds())
	updateEventError := s.repo.UpdateSyncEvent(
		ctx,
		eventID,
		models.SyncStatusFailed,
		processed,
		1,
		syncError.Error(),
		durationMilliseconds,
	)
	log.Error().Err(syncError).Msg("typesense datasource: sync falhou")
	if updateEventError != nil {
		return errors.Join(syncError, fmt.Errorf("typesense: registrar falha da sincronização: %w", updateEventError))
	}
	return syncError
}

// resolveCursor lê o maior timestamp upstream confirmado. Eventos legados sem
// metadata fazem uma nova full sync segura em vez de usar o relógio local.
func (s *TypesenseDataSource) resolveCursor(
	ctx context.Context,
	currentTime time.Time,
) (time.Time, models.SyncEventType, time.Time, error) {
	if s.fullSyncInterval <= 0 {
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, errors.New("typesense full sync interval must be positive")
	}
	metadata, found, metadataError := s.repo.GetLastCompletedSyncMetadata(ctx, models.SourceTypesense)
	if metadataError != nil {
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, fmt.Errorf("typesense: ler cursor upstream: %w", metadataError)
	}
	if !found {
		log.Info().Msg("typesense datasource: sem cursor, executando full sync")
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, nil
	}

	var cursorMetadata typesenseSyncMetadata
	if unmarshalError := json.Unmarshal(metadata, &cursorMetadata); unmarshalError != nil {
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, fmt.Errorf("typesense: decodificar cursor upstream: %w", unmarshalError)
	}
	if cursorMetadata.CursorVersion == 0 && cursorMetadata.CursorUnix == 0 {
		log.Info().Msg("typesense datasource: evento legado sem cursor upstream, executando full sync")
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, nil
	}
	if cursorMetadata.CursorUnix <= 0 {
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, fmt.Errorf(
			"typesense: metadata de cursor inválida (versão %d)",
			cursorMetadata.CursorVersion,
		)
	}
	if cursorMetadata.CursorVersion == legacyTypesenseCursorVersion {
		log.Info().Msg("typesense datasource: cursor sem prova de full snapshot recente, executando full sync")
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, nil
	}
	if cursorMetadata.CursorVersion != typesenseCursorVersion || cursorMetadata.LastFullSnapshotUnix <= 0 {
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, fmt.Errorf(
			"typesense: metadata de cursor inválida (versão %d)",
			cursorMetadata.CursorVersion,
		)
	}

	cursor := time.Unix(cursorMetadata.CursorUnix, 0).UTC()
	lastFullSnapshotAt := time.Unix(cursorMetadata.LastFullSnapshotUnix, 0).UTC()
	if lastFullSnapshotAt.After(currentTime) {
		return time.Time{}, models.SyncTypeFullSync, time.Time{}, errors.New("typesense: full snapshot timestamp is in the future")
	}
	if !currentTime.Before(lastFullSnapshotAt.Add(s.fullSyncInterval)) {
		log.Info().Time("last_full_snapshot_at", lastFullSnapshotAt).Msg("typesense datasource: executando full sync periódico")
		return time.Time{}, models.SyncTypeFullSync, lastFullSnapshotAt, nil
	}
	log.Info().Time("since", cursor).Msg("typesense datasource: executando delta sync")
	return cursor, models.SyncTypeDeltaSync, lastFullSnapshotAt, nil
}

// mapTypesenseService converte um documento Typesense em CatalogItem.
func mapTypesenseService(svc clients.TypesenseService, baseURL string) *models.CatalogItem {
	sourceData, _ := json.Marshal(svc)

	lastUpdate := time.Unix(svc.LastUpdate, 0).UTC()
	var publishedAt *time.Time
	if svc.PublishedAt != nil && *svc.PublishedAt > 0 {
		t := time.Unix(*svc.PublishedAt, 0).UTC()
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
		}
	}

	raw, _ := json.Marshal(ta)
	return raw
}
