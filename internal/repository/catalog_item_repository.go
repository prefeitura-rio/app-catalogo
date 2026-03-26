package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type CatalogItemRepository struct {
	db *pgxpool.Pool
}

func NewCatalogItemRepository(db *pgxpool.Pool) *CatalogItemRepository {
	return &CatalogItemRepository{db: db}
}

// Upsert insere ou atualiza um item do catálogo baseado em (source, external_id).
func (r *CatalogItemRepository) Upsert(ctx context.Context, item *models.CatalogItem) error {
	targetAudience, err := json.Marshal(item.TargetAudience)
	if err != nil || len(targetAudience) == 0 {
		targetAudience = []byte("{}")
	}

	sourceData, err := json.Marshal(item.SourceData)
	if err != nil || len(sourceData) == 0 {
		sourceData = []byte("{}")
	}

	tags := item.Tags
	if tags == nil {
		tags = []string{}
	}
	bairros := item.Bairros
	if bairros == nil {
		bairros = []string{}
	}

	_, err = r.db.Exec(ctx, `
		INSERT INTO catalog_items (
			external_id, source, type, title, description, short_desc,
			organization, url, image_url, target_audience, bairros,
			modalidade, status, tags, source_data,
			valid_from, valid_until, source_updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18
		)
		ON CONFLICT (source, external_id) DO UPDATE SET
			type             = EXCLUDED.type,
			title            = EXCLUDED.title,
			description      = EXCLUDED.description,
			short_desc       = EXCLUDED.short_desc,
			organization     = EXCLUDED.organization,
			url              = EXCLUDED.url,
			image_url        = EXCLUDED.image_url,
			target_audience  = EXCLUDED.target_audience,
			bairros          = EXCLUDED.bairros,
			modalidade       = EXCLUDED.modalidade,
			status           = EXCLUDED.status,
			tags             = EXCLUDED.tags,
			source_data      = EXCLUDED.source_data,
			valid_from       = EXCLUDED.valid_from,
			valid_until      = EXCLUDED.valid_until,
			source_updated_at = EXCLUDED.source_updated_at,
			updated_at       = NOW()
	`,
		item.ExternalID,
		string(item.Source),
		string(item.Type),
		item.Title,
		item.Description,
		item.ShortDesc,
		item.Organization,
		item.URL,
		item.ImageURL,
		targetAudience,
		bairros,
		item.Modalidade,
		string(item.Status),
		tags,
		sourceData,
		item.ValidFrom,
		item.ValidUntil,
		item.SourceUpdatedAt,
	)
	return err
}

// UpsertBatch executa upserts em lote dentro de uma única transação.
func (r *CatalogItemRepository) UpsertBatch(ctx context.Context, items []*models.CatalogItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	count := 0
	for _, item := range items {
		tags := item.Tags
		if tags == nil {
			tags = []string{}
		}
		bairros := item.Bairros
		if bairros == nil {
			bairros = []string{}
		}

		targetAudience := item.TargetAudience
		if len(targetAudience) == 0 {
			targetAudience = json.RawMessage("{}")
		}
		sourceData := item.SourceData
		if len(sourceData) == 0 {
			sourceData = json.RawMessage("{}")
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO catalog_items (
				external_id, source, type, title, description, short_desc,
				organization, url, image_url, target_audience, bairros,
				modalidade, status, tags, source_data,
				valid_from, valid_until, source_updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10, $11,
				$12, $13, $14, $15,
				$16, $17, $18
			)
			ON CONFLICT (source, external_id) DO UPDATE SET
				type             = EXCLUDED.type,
				title            = EXCLUDED.title,
				description      = EXCLUDED.description,
				short_desc       = EXCLUDED.short_desc,
				organization     = EXCLUDED.organization,
				url              = EXCLUDED.url,
				image_url        = EXCLUDED.image_url,
				target_audience  = EXCLUDED.target_audience,
				bairros          = EXCLUDED.bairros,
				modalidade       = EXCLUDED.modalidade,
				status           = EXCLUDED.status,
				tags             = EXCLUDED.tags,
				source_data      = EXCLUDED.source_data,
				valid_from       = EXCLUDED.valid_from,
				valid_until      = EXCLUDED.valid_until,
				source_updated_at = EXCLUDED.source_updated_at,
				updated_at       = NOW()
		`,
			item.ExternalID,
			string(item.Source),
			string(item.Type),
			item.Title,
			item.Description,
			item.ShortDesc,
			item.Organization,
			item.URL,
			item.ImageURL,
			targetAudience,
			bairros,
			item.Modalidade,
			string(item.Status),
			tags,
			sourceData,
			item.ValidFrom,
			item.ValidUntil,
			item.SourceUpdatedAt,
		)
		if err != nil {
			return count, err
		}
		count++
	}

	return count, tx.Commit(ctx)
}

// SoftDelete marca um item como deletado.
func (r *CatalogItemRepository) SoftDelete(ctx context.Context, source models.ItemSource, externalID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE catalog_items SET deleted_at = NOW(), status = 'inactive' WHERE source = $1 AND external_id = $2`,
		string(source), externalID,
	)
	return err
}

// GetBySourceAndExternalID busca um item pelo source + external_id.
func (r *CatalogItemRepository) GetBySourceAndExternalID(ctx context.Context, source models.ItemSource, externalID string) (*models.CatalogItem, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, external_id, source, type, title, description, short_desc,
			organization, url, image_url, target_audience, bairros,
			modalidade, status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE source = $1 AND external_id = $2 AND deleted_at IS NULL
	`, string(source), externalID)

	return scanCatalogItem(row)
}

// GetCandidates retorna itens ativos de tipos específicos para scoring de recomendação.
func (r *CatalogItemRepository) GetCandidates(ctx context.Context, types []models.ItemType, limit int) ([]*models.CatalogItem, error) {
	typeStrs := make([]string, len(types))
	for i, t := range types {
		typeStrs[i] = string(t)
	}

	rows, err := r.db.Query(ctx, `
		SELECT id, external_id, source, type, title, description, short_desc,
			organization, url, image_url, target_audience, bairros,
			modalidade, status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE status = 'active'
		  AND deleted_at IS NULL
		  AND (cardinality($1::text[]) = 0 OR type = ANY($1::item_type[]))
		  AND (valid_until IS NULL OR valid_until > NOW())
		ORDER BY created_at DESC
		LIMIT $2
	`, typeStrs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*models.CatalogItem
	for rows.Next() {
		item, err := scanCatalogItemFromRows(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetByID busca um item pelo ID.
func (r *CatalogItemRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.CatalogItem, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, external_id, source, type, title, description, short_desc,
			organization, url, image_url, target_audience, bairros,
			modalidade, status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	return scanCatalogItem(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCatalogItem(row rowScanner) (*models.CatalogItem, error) {
	var item models.CatalogItem
	var source, itemType, status string
	err := row.Scan(
		&item.ID, &item.ExternalID, &source, &itemType,
		&item.Title, &item.Description, &item.ShortDesc,
		&item.Organization, &item.URL, &item.ImageURL,
		&item.TargetAudience, &item.Bairros,
		&item.Modalidade, &status, &item.Tags,
		&item.SourceData, &item.ValidFrom, &item.ValidUntil,
		&item.SourceUpdatedAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	item.Source = models.ItemSource(source)
	item.Type = models.ItemType(itemType)
	item.Status = models.ItemStatus(status)
	return &item, nil
}

type pgRows interface {
	Scan(dest ...any) error
}

func scanCatalogItemFromRows(rows pgRows) (*models.CatalogItem, error) {
	return scanCatalogItem(rows)
}

// RecordSyncEvent registra um evento de sincronização no banco.
func (r *CatalogItemRepository) RecordSyncEvent(ctx context.Context, event *models.SyncEvent) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO sync_events (source, event_type, status, items_processed, items_failed, error_message, duration_ms, started_at, completed_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`,
		string(event.Source),
		string(event.EventType),
		string(event.Status),
		event.ItemsProcessed,
		event.ItemsFailed,
		event.ErrorMessage,
		event.DurationMs,
		event.StartedAt,
		event.CompletedAt,
		event.Metadata,
	).Scan(&id)
	return id, err
}

// UpdateSyncEvent atualiza o status de um evento de sincronização.
func (r *CatalogItemRepository) UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errMsg string, durationMs int) error {
	now := time.Now()
	_, err := r.db.Exec(ctx, `
		UPDATE sync_events
		SET status = $2, items_processed = $3, items_failed = $4,
		    error_message = $5, duration_ms = $6, completed_at = $7
		WHERE id = $1
	`, id, string(status), processed, failed, errMsg, durationMs, now)
	return err
}

// GetLastSyncEvents retorna os últimos eventos de sincronização por fonte.
func (r *CatalogItemRepository) GetLastSyncEvents(ctx context.Context) ([]*models.SyncStatus, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT ON (source)
			source, event_type, status, started_at, completed_at,
			items_processed, items_failed, error_message
		FROM sync_events
		ORDER BY source, started_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []*models.SyncStatus
	for rows.Next() {
		var s models.SyncStatus
		var source, eventType, status string
		err := rows.Scan(
			&source, &eventType, &status,
			&s.LastStartedAt, &s.LastCompletedAt,
			&s.ItemsProcessed, &s.ItemsFailed, &s.ErrorMessage,
		)
		if err != nil {
			return nil, err
		}
		s.Source = models.ItemSource(source)
		s.LastEventType = models.SyncEventType(eventType)
		s.LastStatus = models.SyncEventStatus(status)
		statuses = append(statuses, &s)
	}
	return statuses, rows.Err()
}

// GetSalesForceCursor retorna o cursor de sincronização do SalesForce.
func (r *CatalogItemRepository) GetSalesForceCursor(ctx context.Context, objectType string) (*models.SalesForceSyncCursor, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, object_type, last_sync_at, last_delta_token, updated_at
		FROM salesforce_sync_cursor
		WHERE object_type = $1
	`, objectType)

	var cursor models.SalesForceSyncCursor
	err := row.Scan(&cursor.ID, &cursor.ObjectType, &cursor.LastSyncAt, &cursor.LastDeltaToken, &cursor.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cursor, nil
}

// UpsertSalesForceCursor salva ou atualiza o cursor de sincronização.
func (r *CatalogItemRepository) UpsertSalesForceCursor(ctx context.Context, objectType string, lastSyncAt time.Time, deltaToken string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO salesforce_sync_cursor (object_type, last_sync_at, last_delta_token)
		VALUES ($1, $2, $3)
		ON CONFLICT (object_type) DO UPDATE SET
			last_sync_at = EXCLUDED.last_sync_at,
			last_delta_token = EXCLUDED.last_delta_token,
			updated_at = NOW()
	`, objectType, lastSyncAt, deltaToken)
	return err
}
