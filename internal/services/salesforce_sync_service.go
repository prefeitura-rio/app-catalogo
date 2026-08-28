package services

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type SalesForceSyncService struct {
	client     *clients.SalesForceClient
	repo       *repository.CatalogItemRepository
	objectType string
}

func NewSalesForceSyncService(
	client *clients.SalesForceClient,
	repo *repository.CatalogItemRepository,
	objectType string,
) *SalesForceSyncService {
	return &SalesForceSyncService{
		client:     client,
		repo:       repo,
		objectType: objectType,
	}
}

// FullSync sincroniza todos os registros do SalesForce.
func (s *SalesForceSyncService) FullSync(ctx context.Context) error {
	startedAt := time.Now()
	eventID, _ := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceSalesForce,
		EventType: models.SyncTypeFullSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})

	log.Info().Str("object_type", s.objectType).Msg("salesforce: iniciando full sync")

	records, err := s.client.QueryAll(ctx, s.objectType)
	if err != nil {
		errMsg := err.Error()
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusFailed, 0, 0, errMsg, int(time.Since(startedAt).Milliseconds()))
		return err
	}

	items := make([]*models.CatalogItem, 0, len(records))
	for _, rec := range records {
		item := s.mapRecord(rec)
		if item != nil {
			items = append(items, item)
		}
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	durationMs := int(time.Since(startedAt).Milliseconds())

	if err != nil {
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusFailed, processed, len(items)-processed, err.Error(), durationMs)
		return err
	}

	now := time.Now()
	_ = s.repo.UpsertSalesForceCursor(ctx, s.objectType, now, "")
	_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusCompleted, processed, 0, "", durationMs)

	log.Info().
		Int("items", processed).
		Int("duration_ms", durationMs).
		Msg("salesforce: full sync concluído")

	return nil
}

// DeltaSync sincroniza apenas os registros modificados desde a última sync.
func (s *SalesForceSyncService) DeltaSync(ctx context.Context) error {
	cursor, err := s.repo.GetSalesForceCursor(ctx, s.objectType)
	if err != nil || cursor.LastSyncAt == nil {
		log.Info().Msg("salesforce: cursor não encontrado, executando full sync")
		return s.FullSync(ctx)
	}

	startedAt := time.Now()
	eventID, _ := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceSalesForce,
		EventType: models.SyncTypeDeltaSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})

	log.Info().
		Time("since", *cursor.LastSyncAt).
		Str("object_type", s.objectType).
		Msg("salesforce: iniciando delta sync")

	records, err := s.client.QueryModifiedSince(ctx, s.objectType, *cursor.LastSyncAt)
	if err != nil {
		errMsg := err.Error()
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusFailed, 0, 0, errMsg, int(time.Since(startedAt).Milliseconds()))
		return err
	}

	if len(records) == 0 {
		durationMs := int(time.Since(startedAt).Milliseconds())
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusCompleted, 0, 0, "", durationMs)
		log.Debug().Msg("salesforce: sem registros novos no delta sync")
		return nil
	}

	items := make([]*models.CatalogItem, 0, len(records))
	for _, rec := range records {
		item := s.mapRecord(rec)
		if item != nil {
			items = append(items, item)
		}
	}

	processed, err := s.repo.UpsertBatch(ctx, items)
	durationMs := int(time.Since(startedAt).Milliseconds())

	if err != nil {
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusFailed, processed, len(items)-processed, err.Error(), durationMs)
		return err
	}

	now := time.Now()
	_ = s.repo.UpsertSalesForceCursor(ctx, s.objectType, now, "")
	_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusCompleted, processed, 0, "", durationMs)

	log.Info().
		Int("items", processed).
		Int("duration_ms", durationMs).
		Msg("salesforce: delta sync concluído")

	return nil
}

// SyncRecord sincroniza um único registro (para uso em webhooks).
func (s *SalesForceSyncService) SyncRecord(ctx context.Context, externalID string) error {
	soql := "SELECT Id, Name, Description__c, ShortDescription__c, Organization__c, URL__c, Status__c, Theme__c, Channel__c, Neighborhood__c, Tags__c, ValidFrom__c, ValidUntil__c, LastModifiedDate FROM " + s.objectType + " WHERE Id = '" + externalID + "' LIMIT 1"
	records, err := s.client.Query(ctx, soql)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return s.repo.SoftDelete(ctx, models.SourceSalesForce, externalID)
	}
	item := s.mapRecord(records[0])
	if item == nil {
		return nil
	}
	return s.repo.Upsert(ctx, item)
}

// mapRecord converte um record do SalesForce para CatalogItem.
// Os campos são flexíveis — tudo que não é mapeado vai para source_data.
func (s *SalesForceSyncService) mapRecord(rec map[string]interface{}) *models.CatalogItem {
	id, _ := rec["Id"].(string)
	if id == "" {
		return nil
	}

	title := stringField(rec, "Name")
	if title == "" {
		return nil
	}

	status := models.StatusActive
	if sfStatus, ok := rec["Status__c"].(string); ok {
		switch strings.ToLower(sfStatus) {
		case "inactive", "inativo", "rascunho":
			status = models.StatusInactive
		case "draft":
			status = models.StatusDraft
		}
	}

	var validFrom, validUntil *time.Time
	if v := parseTime(rec, "ValidFrom__c"); v != nil {
		validFrom = v
	}
	if v := parseTime(rec, "ValidUntil__c"); v != nil {
		validUntil = v
	}

	var sourceUpdatedAt *time.Time
	if v := parseTime(rec, "LastModifiedDate"); v != nil {
		sourceUpdatedAt = v
	}

	// Tags: campo separado por vírgula ou array JSON
	tags := parseTags(rec, "Tags__c")

	// Bairros: campo Neighborhood__c pode ser separado por vírgula
	bairros := parseTags(rec, "Neighborhood__c")

	// Target audience vazio por padrão para SalesForce (definido no conteúdo)
	targetAudience := json.RawMessage("{}")

	sourceData, _ := json.Marshal(rec)

	return &models.CatalogItem{
		ExternalID:      id,
		Source:          models.SourceSalesForce,
		Type:            models.TypeService,
		Title:           title,
		Description:     stringField(rec, "Description__c"),
		ShortDesc:       stringField(rec, "ShortDescription__c"),
		Organization:    stringField(rec, "Organization__c"),
		URL:             stringField(rec, "URL__c"),
		Modalidade:      stringField(rec, "Channel__c"),
		Status:          status,
		Tags:            append(tags, stringField(rec, "Theme__c")),
		Bairros:         bairros,
		TargetAudience:  targetAudience,
		SourceData:      sourceData,
		ValidFrom:       validFrom,
		ValidUntil:      validUntil,
		SourceUpdatedAt: sourceUpdatedAt,
	}
}

func stringField(rec map[string]interface{}, key string) string {
	if v, ok := rec[key].(string); ok {
		return v
	}
	return ""
}

func parseTime(rec map[string]interface{}, key string) *time.Time {
	s := stringField(rec, key)
	if s == "" {
		return nil
	}
	formats := []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02"}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return &t
		}
	}
	return nil
}

func parseTags(rec map[string]interface{}, key string) []string {
	raw := stringField(rec, key)
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	var result []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}
