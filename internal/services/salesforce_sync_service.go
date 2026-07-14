package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type SalesForceSyncService struct {
	client     salesForceSyncClient
	repo       salesForceSyncRepository
	objectType string
}

type salesForceSyncClient interface {
	Query(ctx context.Context, soql string) ([]map[string]interface{}, error)
	QueryAll(ctx context.Context, objectType string) ([]map[string]interface{}, error)
	QueryModifiedSince(ctx context.Context, objectType string, since time.Time) ([]map[string]interface{}, error)
}

type salesForceSyncRepository interface {
	GetSalesForceCursor(ctx context.Context, objectType string) (*models.SalesForceSyncCursor, error)
	RecordSyncEvent(ctx context.Context, event *models.SyncEvent) (int64, error)
	UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errorMessage string, durationMilliseconds int) error
	ReconcileSalesForceSnapshot(ctx context.Context, objectType string, items []*models.CatalogItem, lastSyncAt time.Time) (int, int, error)
	UpsertSalesForceDelta(ctx context.Context, objectType string, items []*models.CatalogItem, lastSyncAt time.Time) (int, error)
	SoftDelete(ctx context.Context, source models.ItemSource, externalID string) error
	Upsert(ctx context.Context, item *models.CatalogItem) error
}

const (
	maximumSalesForceObjectTypeLength = 100
	shortSalesForceRecordIDLength     = 15
	longSalesForceRecordIDLength      = 18
	// Re-read a bounded window because upstream timestamps have finite
	// granularity and indexed changes can become visible after propagation lag.
	salesForceDeltaOverlap = time.Minute
)

var salesForceObjectTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func NewSalesForceSyncService(
	client salesForceSyncClient,
	repo salesForceSyncRepository,
	objectType string,
) *SalesForceSyncService {
	return &SalesForceSyncService{
		client:     client,
		repo:       repo,
		objectType: objectType,
	}
}

// FullSync sincroniza todos os registros do SalesForce.
// Retorna o número de itens alterados, incluindo desativações.
func (s *SalesForceSyncService) FullSync(ctx context.Context) (int, error) {
	startedAt := time.Now()
	eventID, _ := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceSalesForce,
		EventType: models.SyncTypeFullSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})

	objectType, err := validatedSalesForceObjectType(s.objectType)
	if err != nil {
		s.failSyncEvent(ctx, eventID, startedAt, 0, 0, err)
		return 0, err
	}

	log.Info().Str("object_type", objectType).Msg("salesforce: iniciando full sync")

	records, err := s.client.QueryAll(ctx, objectType)
	if err != nil {
		s.failSyncEvent(ctx, eventID, startedAt, 0, 0, err)
		return 0, err
	}
	if len(records) == 0 {
		err := repository.ErrEmptySalesForceSnapshot
		s.failSyncEvent(ctx, eventID, startedAt, 0, 0, err)
		return 0, err
	}

	items, err := s.mapRecords(records, objectType)
	if err != nil {
		s.failSyncEvent(ctx, eventID, startedAt, 0, len(records), err)
		return 0, err
	}
	upstreamCursor, err := maximumSalesForceSourceUpdatedAt(items)
	if err != nil {
		s.failSyncEvent(ctx, eventID, startedAt, 0, len(records), err)
		return 0, err
	}

	upserted, deactivated, err := s.repo.ReconcileSalesForceSnapshot(ctx, objectType, items, upstreamCursor)
	durationMs := int(time.Since(startedAt).Milliseconds())

	if err != nil {
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusFailed, 0, len(items), err.Error(), durationMs)
		return 0, err
	}

	changed := upserted + deactivated
	_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusCompleted, changed, 0, "", durationMs)

	log.Info().
		Time("cursor", upstreamCursor).
		Int("upserted", upserted).
		Int("deactivated", deactivated).
		Int("changed", changed).
		Int("duration_ms", durationMs).
		Msg("salesforce: full sync concluído")

	return changed, nil
}

// DeltaSync sincroniza apenas os registros modificados desde a última sync.
// Retorna o número de itens processados (upsertados).
func (s *SalesForceSyncService) DeltaSync(ctx context.Context) (int, error) {
	objectType, err := validatedSalesForceObjectType(s.objectType)
	if err != nil {
		return 0, err
	}
	cursor, err := s.repo.GetSalesForceCursor(ctx, objectType)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (cursor == nil || cursor.LastSyncAt == nil)) {
		log.Info().Msg("salesforce: cursor não encontrado, executando full sync")
		return s.FullSync(ctx)
	}
	if err != nil {
		return 0, fmt.Errorf("salesforce: read sync cursor: %w", err)
	}

	startedAt := time.Now()
	eventID, _ := s.repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceSalesForce,
		EventType: models.SyncTypeDeltaSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})

	log.Info().
		Time("cursor", *cursor.LastSyncAt).
		Time("since", cursor.LastSyncAt.Add(-salesForceDeltaOverlap)).
		Str("object_type", objectType).
		Msg("salesforce: iniciando delta sync")

	records, err := s.client.QueryModifiedSince(ctx, objectType, cursor.LastSyncAt.Add(-salesForceDeltaOverlap))
	if err != nil {
		s.failSyncEvent(ctx, eventID, startedAt, 0, 0, err)
		return 0, err
	}

	items, err := s.mapRecords(records, objectType)
	if err != nil {
		s.failSyncEvent(ctx, eventID, startedAt, 0, len(records), err)
		return 0, err
	}
	upstreamCursor := *cursor.LastSyncAt
	if len(items) > 0 {
		batchCursor, cursorError := maximumSalesForceSourceUpdatedAt(items)
		if cursorError != nil {
			s.failSyncEvent(ctx, eventID, startedAt, 0, len(records), cursorError)
			return 0, cursorError
		}
		if batchCursor.After(upstreamCursor) {
			upstreamCursor = batchCursor
		}
	}

	processed, err := s.repo.UpsertSalesForceDelta(ctx, objectType, items, upstreamCursor)
	durationMs := int(time.Since(startedAt).Milliseconds())

	if err != nil {
		_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusFailed, 0, len(items), err.Error(), durationMs)
		return 0, err
	}

	_ = s.repo.UpdateSyncEvent(ctx, eventID, models.SyncStatusCompleted, processed, 0, "", durationMs)
	if len(records) == 0 {
		log.Debug().Msg("salesforce: sem registros novos no delta sync")
		return 0, nil
	}

	log.Info().
		Time("cursor", upstreamCursor).
		Int("items", processed).
		Int("duration_ms", durationMs).
		Msg("salesforce: delta sync concluído")

	return processed, nil
}

// SyncRecord sincroniza um único registro (para uso em webhooks).
func (s *SalesForceSyncService) SyncRecord(ctx context.Context, externalID string) error {
	objectType, err := validatedSalesForceObjectType(s.objectType)
	if err != nil {
		return err
	}
	externalID, err = validatedSalesForceRecordID(externalID)
	if err != nil {
		return err
	}
	soql := "SELECT Id, Name, Description__c, ShortDescription__c, Organization__c, URL__c, Status__c, Theme__c, Channel__c, Neighborhood__c, Tags__c, ValidFrom__c, ValidUntil__c, LastModifiedDate FROM " + objectType + " WHERE Id = '" + externalID + "' LIMIT 1"
	records, err := s.client.Query(ctx, soql)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return s.repo.SoftDelete(ctx, models.SourceSalesForce, externalID)
	}
	item, err := s.mapRecord(records[0], objectType)
	if err != nil {
		return err
	}
	return s.repo.Upsert(ctx, item)
}

// mapRecord converte um record do SalesForce para CatalogItem.
// Os campos são flexíveis — tudo que não é mapeado vai para source_data.
func (s *SalesForceSyncService) mapRecord(rec map[string]interface{}, objectType string) (*models.CatalogItem, error) {
	id, _ := rec["Id"].(string)
	if id == "" {
		return nil, errors.New("salesforce record has no id")
	}

	title := stringField(rec, "Name")
	if title == "" {
		return nil, errors.New("salesforce record has no name")
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

	sourceUpdatedAt, err := requiredSalesForceLastModifiedDate(rec)
	if err != nil {
		return nil, err
	}

	// Tags: campo separado por vírgula ou array JSON
	tags := parseTags(rec, "Tags__c")

	// Bairros: campo Neighborhood__c pode ser separado por vírgula
	bairros := parseTags(rec, "Neighborhood__c")

	// Target audience vazio por padrão para SalesForce (definido no conteúdo)
	targetAudience := json.RawMessage("{}")

	sourceRecord := make(map[string]interface{}, len(rec)+1)
	for sourceField, sourceValue := range rec {
		sourceRecord[sourceField] = sourceValue
	}
	sourceRecord[models.SalesForceObjectTypeSourceDataKey] = objectType
	sourceData, err := json.Marshal(sourceRecord)
	if err != nil {
		return nil, fmt.Errorf("salesforce record source data: %w", err)
	}
	theme := stringField(rec, "Theme__c")
	if theme != "" {
		tags = append(tags, theme)
	}

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
		Tags:            tags,
		Bairros:         bairros,
		TargetAudience:  targetAudience,
		SourceData:      sourceData,
		ValidFrom:       validFrom,
		ValidUntil:      validUntil,
		SourceUpdatedAt: &sourceUpdatedAt,
	}, nil
}

func (s *SalesForceSyncService) mapRecords(records []map[string]interface{}, objectType string) ([]*models.CatalogItem, error) {
	items := make([]*models.CatalogItem, 0, len(records))
	for recordIndex, record := range records {
		item, err := s.mapRecord(record, objectType)
		if err != nil {
			return nil, fmt.Errorf("salesforce snapshot record %d: %w", recordIndex, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func maximumSalesForceSourceUpdatedAt(items []*models.CatalogItem) (time.Time, error) {
	var maximumSourceUpdatedAt time.Time
	for itemIndex, item := range items {
		if item == nil || item.SourceUpdatedAt == nil || item.SourceUpdatedAt.IsZero() {
			return time.Time{}, fmt.Errorf("salesforce item %d has no valid LastModifiedDate", itemIndex)
		}
		if item.SourceUpdatedAt.After(maximumSourceUpdatedAt) {
			maximumSourceUpdatedAt = *item.SourceUpdatedAt
		}
	}
	if maximumSourceUpdatedAt.IsZero() {
		return time.Time{}, errors.New("salesforce records have no valid LastModifiedDate")
	}
	return maximumSourceUpdatedAt, nil
}

func requiredSalesForceLastModifiedDate(record map[string]interface{}) (time.Time, error) {
	rawLastModifiedDate, ok := record["LastModifiedDate"].(string)
	if !ok || strings.TrimSpace(rawLastModifiedDate) == "" {
		return time.Time{}, errors.New("salesforce record has no LastModifiedDate")
	}
	lastModifiedDate, err := time.Parse(time.RFC3339, rawLastModifiedDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("salesforce record has invalid LastModifiedDate: %w", err)
	}
	return lastModifiedDate.UTC(), nil
}

func (s *SalesForceSyncService) failSyncEvent(
	ctx context.Context,
	eventID int64,
	startedAt time.Time,
	processed int,
	failed int,
	syncError error,
) {
	_ = s.repo.UpdateSyncEvent(
		ctx,
		eventID,
		models.SyncStatusFailed,
		processed,
		failed,
		syncError.Error(),
		int(time.Since(startedAt).Milliseconds()),
	)
}

func validatedSalesForceObjectType(objectType string) (string, error) {
	objectType = strings.TrimSpace(objectType)
	if len(objectType) > maximumSalesForceObjectTypeLength || !salesForceObjectTypePattern.MatchString(objectType) {
		return "", errors.New("salesforce object type is invalid")
	}
	return objectType, nil
}

func validatedSalesForceRecordID(externalID string) (string, error) {
	if len(externalID) != shortSalesForceRecordIDLength && len(externalID) != longSalesForceRecordIDLength {
		return "", errors.New("salesforce record id is invalid")
	}
	for characterIndex := 0; characterIndex < len(externalID); characterIndex++ {
		character := externalID[characterIndex]
		if (character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return "", errors.New("salesforce record id is invalid")
		}
	}
	return externalID, nil
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
