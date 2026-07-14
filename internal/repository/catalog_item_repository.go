package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type CatalogItemRepository struct {
	db *pgxpool.Pool
}

// ErrEmptySalesForceSnapshot prevents a successful-but-empty upstream response
// from being interpreted as an instruction to deactivate an entire object.
var ErrEmptySalesForceSnapshot = errors.New("salesforce snapshot must contain at least one item")

// ErrIncompleteSourceSnapshot prevents a partial or ambiguously scoped source
// response from being interpreted as an instruction to deactivate catalog rows.
var ErrIncompleteSourceSnapshot = errors.New("source snapshot is incomplete")

const sourceSyncLeaseCleanupTimeout = 5 * time.Second

// EmbeddingClaim owns a set of catalog items for one worker pass. The token is
// required to complete or release work and prevents writes from superseded claims.
type EmbeddingClaim struct {
	Token uuid.UUID
	Items []*models.CatalogItem
}

// EmbeddingCompletion contains the vector and provenance written atomically
// after a claimed catalog item has been embedded.
type EmbeddingCompletion struct {
	ItemID        uuid.UUID
	ClaimToken    uuid.UUID
	VectorLiteral string
	SourceHash    string
	Metadata      models.EmbeddingMetadata
}

func NewCatalogItemRepository(db *pgxpool.Pool) *CatalogItemRepository {
	return &CatalogItemRepository{db: db}
}

// WithSourceSyncLease serializes one source's complete remote fetch and local
// persistence across all replicas. The PostgreSQL session advisory lock lives
// on a dedicated connection outside the application pool, so remote I/O holds
// neither a transaction nor a pooled query connection. Closing the dedicated
// connection is a final lock-release guarantee if explicit unlock fails.
func (r *CatalogItemRepository) WithSourceSyncLease(
	ctx context.Context,
	source models.ItemSource,
	operation func(context.Context) (int, error),
) (changed int, syncError error) {
	if strings.TrimSpace(string(source)) == "" {
		return 0, errors.New("source sync lease requires a source")
	}
	if operation == nil {
		return 0, errors.New("source sync lease requires an operation")
	}
	if r.db == nil {
		return 0, errors.New("source sync lease requires a database pool")
	}

	connectionConfiguration := r.db.Config().ConnConfig.Copy()
	leaseConnection, connectionError := pgx.ConnectConfig(ctx, connectionConfiguration)
	if connectionError != nil {
		return 0, fmt.Errorf("connect source sync lease for %q: %w", source, connectionError)
	}
	lockAcquired := false
	defer func() {
		if lockAcquired {
			unlockContext, cancelUnlock := context.WithTimeout(context.Background(), sourceSyncLeaseCleanupTimeout)
			var unlocked bool
			unlockError := leaseConnection.QueryRow(unlockContext, `
				SELECT pg_advisory_unlock(
					hashtextextended('app-catalogo:source-sync:' || $1, 0)
				)
			`, string(source)).Scan(&unlocked)
			cancelUnlock()
			if unlockError != nil {
				syncError = errors.Join(syncError, fmt.Errorf("unlock source sync lease for %q: %w", source, unlockError))
			} else if !unlocked {
				syncError = errors.Join(syncError, fmt.Errorf("source sync lease for %q was not held", source))
			}
		}

		closeContext, cancelClose := context.WithTimeout(context.Background(), sourceSyncLeaseCleanupTimeout)
		closeError := leaseConnection.Close(closeContext)
		cancelClose()
		if closeError != nil {
			syncError = errors.Join(syncError, fmt.Errorf("close source sync lease connection for %q: %w", source, closeError))
		}
	}()

	if _, lockError := leaseConnection.Exec(ctx, `
		SELECT pg_advisory_lock(
			hashtextextended('app-catalogo:source-sync:' || $1, 0)
		)
	`, string(source)); lockError != nil {
		return 0, fmt.Errorf("acquire source sync lease for %q: %w", source, lockError)
	}
	lockAcquired = true

	return operation(ctx)
}

// Upsert insere ou atualiza um item do catálogo baseado em (source, external_id).
func (r *CatalogItemRepository) Upsert(ctx context.Context, item *models.CatalogItem) error {
	if validationError := models.ValidateCatalogItem(item); validationError != nil {
		return fmt.Errorf("validate catalog item: %w", validationError)
	}
	targetAudience := item.TargetAudience
	if len(targetAudience) == 0 {
		targetAudience = json.RawMessage("{}")
	}

	sourceData := item.SourceData
	if len(sourceData) == 0 {
		sourceData = json.RawMessage("{}")
	}

	tags := item.Tags
	if tags == nil {
		tags = []string{}
	}
	bairros := item.Bairros
	if bairros == nil {
		bairros = []string{}
	}

	_, err := r.db.Exec(ctx, `
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
			deleted_at       = NULL,
			updated_at       = NOW()
		WHERE (
			(
				EXCLUDED.source = 'salesforce'
				AND (
					EXCLUDED.source_updated_at IS NOT NULL
					AND (
						catalog_items.source_updated_at IS NULL
						OR EXCLUDED.source_updated_at > catalog_items.source_updated_at
					)
				)
			) OR (
				EXCLUDED.source <> 'salesforce'
				AND (
					EXCLUDED.source_updated_at IS NULL
					OR catalog_items.source_updated_at IS NULL
					OR EXCLUDED.source_updated_at >= catalog_items.source_updated_at
				)
			)
		) AND (
			catalog_items.source_data IS DISTINCT FROM EXCLUDED.source_data
			OR catalog_items.type IS DISTINCT FROM EXCLUDED.type
			OR catalog_items.title IS DISTINCT FROM EXCLUDED.title
			OR catalog_items.description IS DISTINCT FROM EXCLUDED.description
			OR catalog_items.short_desc IS DISTINCT FROM EXCLUDED.short_desc
			OR catalog_items.organization IS DISTINCT FROM EXCLUDED.organization
			OR catalog_items.url IS DISTINCT FROM EXCLUDED.url
			OR catalog_items.image_url IS DISTINCT FROM EXCLUDED.image_url
			OR catalog_items.target_audience IS DISTINCT FROM EXCLUDED.target_audience
			OR catalog_items.bairros IS DISTINCT FROM EXCLUDED.bairros
			OR catalog_items.modalidade IS DISTINCT FROM EXCLUDED.modalidade
			OR catalog_items.tags IS DISTINCT FROM EXCLUDED.tags
			OR catalog_items.status IS DISTINCT FROM EXCLUDED.status
			OR catalog_items.valid_from IS DISTINCT FROM EXCLUDED.valid_from
			OR catalog_items.valid_until IS DISTINCT FROM EXCLUDED.valid_until
			OR catalog_items.source_updated_at IS DISTINCT FROM EXCLUDED.source_updated_at
			OR catalog_items.deleted_at IS NOT NULL
		)
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
// Retorna quantos itens foram de fato inseridos ou tiveram algum dado alterado —
// uma sync que reprocessa itens já idênticos ao registrado não conta como
// alteração, o que evita invalidar o cache de busca sem necessidade real.
func (r *CatalogItemRepository) UpsertBatch(ctx context.Context, items []*models.CatalogItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	if validationError := validateCatalogItems(items); validationError != nil {
		return 0, validationError
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	count, err := upsertCatalogItems(ctx, tx, items, nil)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func upsertCatalogItems(
	ctx context.Context,
	tx pgx.Tx,
	items []*models.CatalogItem,
	existingUpdatedBefore *time.Time,
) (int, error) {
	count := 0
	for itemIndex, item := range items {
		if validationError := models.ValidateCatalogItem(item); validationError != nil {
			return 0, fmt.Errorf("catalog item %d: %w", itemIndex, validationError)
		}
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

		var id uuid.UUID
		err := tx.QueryRow(ctx, `
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
				deleted_at       = NULL,
				updated_at       = NOW()
			WHERE (
				(
					EXCLUDED.source = 'salesforce'
					AND (
					EXCLUDED.source_updated_at IS NOT NULL
					AND (
						catalog_items.source_updated_at IS NULL
						OR EXCLUDED.source_updated_at > catalog_items.source_updated_at
					)
					)
				) OR (
					EXCLUDED.source <> 'salesforce'
					AND (
						EXCLUDED.source_updated_at IS NULL
						OR catalog_items.source_updated_at IS NULL
						OR EXCLUDED.source_updated_at >= catalog_items.source_updated_at
					)
				)
			) AND (
				$19::timestamptz IS NULL
				OR catalog_items.updated_at <= $19
			) AND (
				catalog_items.source_data IS DISTINCT FROM EXCLUDED.source_data
				OR catalog_items.type IS DISTINCT FROM EXCLUDED.type
				OR catalog_items.title IS DISTINCT FROM EXCLUDED.title
				OR catalog_items.description IS DISTINCT FROM EXCLUDED.description
				OR catalog_items.short_desc IS DISTINCT FROM EXCLUDED.short_desc
				OR catalog_items.organization IS DISTINCT FROM EXCLUDED.organization
				OR catalog_items.url IS DISTINCT FROM EXCLUDED.url
				OR catalog_items.image_url IS DISTINCT FROM EXCLUDED.image_url
				OR catalog_items.target_audience IS DISTINCT FROM EXCLUDED.target_audience
				OR catalog_items.bairros IS DISTINCT FROM EXCLUDED.bairros
				OR catalog_items.modalidade IS DISTINCT FROM EXCLUDED.modalidade
				OR catalog_items.tags IS DISTINCT FROM EXCLUDED.tags
				OR catalog_items.status IS DISTINCT FROM EXCLUDED.status
				OR catalog_items.valid_from IS DISTINCT FROM EXCLUDED.valid_from
				OR catalog_items.valid_until IS DISTINCT FROM EXCLUDED.valid_until
				OR catalog_items.source_updated_at IS DISTINCT FROM EXCLUDED.source_updated_at
				OR catalog_items.deleted_at IS NOT NULL
			)
			RETURNING id
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
			existingUpdatedBefore,
		).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Conflito existente cujo source_data é idêntico ao já
				// registrado — WHERE bloqueou o UPDATE, nada mudou de fato.
				continue
			}
			return 0, err
		}
		count++
	}

	return count, nil
}

// ReconcileSourceSnapshot atomically upserts a proven-complete snapshot and
// soft-deletes rows from the same source that disappeared. Callers must supply
// the independently reported upstream count. A source timestamp upper bound
// protects rows written by a newer concurrent sync, including for an explicitly
// empty snapshot. The snapshot start boundary additionally prevents an older,
// slower fetch from overwriting or deactivating rows committed by a newer fetch.
func (r *CatalogItemRepository) ReconcileSourceSnapshot(
	ctx context.Context,
	source models.ItemSource,
	items []*models.CatalogItem,
	expectedItemCount int,
	sourceUpdatedUpperBound *time.Time,
	snapshotStartedAt time.Time,
) (int, int, error) {
	expectedItemType, supportedSource := snapshotItemType(source)
	if !supportedSource {
		return 0, 0, fmt.Errorf("source %q does not support snapshot reconciliation", source)
	}
	if expectedItemCount < 0 || expectedItemCount != len(items) {
		return 0, 0, fmt.Errorf(
			"%w: source %q reported %d items but supplied %d",
			ErrIncompleteSourceSnapshot,
			source,
			expectedItemCount,
			len(items),
		)
	}
	if sourceUpdatedUpperBound == nil || sourceUpdatedUpperBound.IsZero() {
		return 0, 0, fmt.Errorf("%w: source %q has no update upper bound", ErrIncompleteSourceSnapshot, source)
	}
	if snapshotStartedAt.IsZero() {
		return 0, 0, fmt.Errorf("%w: source %q has no snapshot start boundary", ErrIncompleteSourceSnapshot, source)
	}

	externalIDs := make([]string, 0, len(items))
	seenExternalIDs := make(map[string]struct{}, len(items))
	for itemIndex, item := range items {
		if item == nil {
			return 0, 0, fmt.Errorf("source snapshot item %d cannot be nil", itemIndex)
		}
		if validationError := models.ValidateCatalogItem(item); validationError != nil {
			return 0, 0, fmt.Errorf("source snapshot item %d: %w", itemIndex, validationError)
		}
		if item.Source != source {
			return 0, 0, fmt.Errorf(
				"source snapshot item %d belongs to %q instead of %q",
				itemIndex,
				item.Source,
				source,
			)
		}
		if item.Type != expectedItemType {
			return 0, 0, fmt.Errorf(
				"source snapshot item %d has type %q instead of %q",
				itemIndex,
				item.Type,
				expectedItemType,
			)
		}
		if strings.TrimSpace(item.ExternalID) == "" {
			return 0, 0, fmt.Errorf("source snapshot item %d has an empty external id", itemIndex)
		}
		if item.SourceUpdatedAt == nil || item.SourceUpdatedAt.IsZero() {
			return 0, 0, fmt.Errorf("source snapshot item %q has no upstream update timestamp", item.ExternalID)
		}
		if item.SourceUpdatedAt.After(*sourceUpdatedUpperBound) {
			return 0, 0, fmt.Errorf("source snapshot item %q is newer than its upper bound", item.ExternalID)
		}
		if _, duplicate := seenExternalIDs[item.ExternalID]; duplicate {
			return 0, 0, fmt.Errorf("source snapshot contains duplicate external id %q", item.ExternalID)
		}
		seenExternalIDs[item.ExternalID] = struct{}{}
		externalIDs = append(externalIDs, item.ExternalID)
	}

	transaction, beginError := r.db.Begin(ctx)
	if beginError != nil {
		return 0, 0, beginError
	}
	defer transaction.Rollback(ctx)
	if lockError := lockSourceSnapshot(ctx, transaction, source); lockError != nil {
		return 0, 0, lockError
	}

	upserted, upsertError := upsertCatalogItems(ctx, transaction, items, &snapshotStartedAt)
	if upsertError != nil {
		return 0, 0, upsertError
	}
	deactivationTag, deactivationError := transaction.Exec(ctx, `
		UPDATE catalog_items
		SET status = 'inactive',
			deleted_at = COALESCE(deleted_at, NOW())
		WHERE source = $1
		  AND NOT (external_id = ANY($2::text[]))
		  AND (source_updated_at IS NULL OR source_updated_at <= $3)
		  AND updated_at <= $4
		  AND (status <> 'inactive' OR deleted_at IS NULL)
	`, string(source), externalIDs, sourceUpdatedUpperBound, snapshotStartedAt)
	if deactivationError != nil {
		return 0, 0, deactivationError
	}
	if commitError := transaction.Commit(ctx); commitError != nil {
		return 0, 0, commitError
	}
	return upserted, int(deactivationTag.RowsAffected()), nil
}

func snapshotItemType(source models.ItemSource) (models.ItemType, bool) {
	switch source {
	case models.SourceTypesense:
		return models.TypeService, true
	case models.SourceCourses:
		return models.TypeCourse, true
	case models.SourceJobs:
		return models.TypeJob, true
	case models.SourceMEI:
		return models.TypeMEIOpportunity, true
	default:
		return "", false
	}
}

func lockSourceSnapshot(ctx context.Context, transaction pgx.Tx, source models.ItemSource) error {
	_, lockError := transaction.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended('app-catalogo:source-snapshot:' || $1, 0)
		)
	`, string(source))
	if lockError != nil {
		return fmt.Errorf("lock source snapshot for %q: %w", source, lockError)
	}
	return nil
}

// ReconcileSalesForceSnapshot atomically upserts a complete object snapshot,
// deactivates records from the same Salesforce object that disappeared, and
// advances the delta cursor. Empty or mixed-scope snapshots are rejected before
// the transaction starts to prevent accidental cross-object deletion.
func (r *CatalogItemRepository) ReconcileSalesForceSnapshot(
	ctx context.Context,
	objectType string,
	items []*models.CatalogItem,
	lastSyncAt time.Time,
) (int, int, error) {
	objectType = strings.TrimSpace(objectType)
	if objectType == "" {
		return 0, 0, errors.New("salesforce object type cannot be empty")
	}
	if len(items) == 0 {
		return 0, 0, ErrEmptySalesForceSnapshot
	}
	if lastSyncAt.IsZero() {
		return 0, 0, errors.New("salesforce snapshot cursor cannot be zero")
	}

	externalIDs := make([]string, 0, len(items))
	seenExternalIDs := make(map[string]struct{}, len(items))
	for itemIndex, item := range items {
		if err := validateSalesForceSnapshotItem(item, objectType); err != nil {
			return 0, 0, fmt.Errorf("salesforce snapshot item %d: %w", itemIndex, err)
		}
		if item.SourceUpdatedAt.After(lastSyncAt) {
			return 0, 0, fmt.Errorf("salesforce snapshot item %d is newer than its cursor", itemIndex)
		}
		if _, duplicate := seenExternalIDs[item.ExternalID]; duplicate {
			return 0, 0, fmt.Errorf("salesforce snapshot contains duplicate external id %q", item.ExternalID)
		}
		seenExternalIDs[item.ExternalID] = struct{}{}
		externalIDs = append(externalIDs, item.ExternalID)
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	if err := lockSalesForceSync(ctx, tx, objectType); err != nil {
		return 0, 0, err
	}

	upserted, err := upsertCatalogItems(ctx, tx, items, nil)
	if err != nil {
		return 0, 0, err
	}

	deactivationTag, err := tx.Exec(ctx, `
		UPDATE catalog_items
		SET status = 'inactive',
			deleted_at = COALESCE(deleted_at, NOW())
		WHERE source = 'salesforce'
		  AND lower(COALESCE(
			NULLIF(source_data->>$1, ''),
			source_data#>>'{attributes,type}',
			''
		  )) = lower($2)
		  AND NOT (external_id = ANY($3::text[]))
		  AND (source_updated_at IS NULL OR source_updated_at <= $4)
		  AND (status <> 'inactive' OR deleted_at IS NULL)
	`, models.SalesForceObjectTypeSourceDataKey, objectType, externalIDs, lastSyncAt)
	if err != nil {
		return 0, 0, err
	}

	if err := upsertSalesForceCursor(ctx, tx, objectType, lastSyncAt, ""); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return upserted, int(deactivationTag.RowsAffected()), nil
}

// UpsertSalesForceDelta atomically applies a delta batch and advances its
// cursor. The cursor is an upstream LastModifiedDate and never a local clock.
func (r *CatalogItemRepository) UpsertSalesForceDelta(
	ctx context.Context,
	objectType string,
	items []*models.CatalogItem,
	lastSyncAt time.Time,
) (int, error) {
	objectType = strings.TrimSpace(objectType)
	if objectType == "" {
		return 0, errors.New("salesforce object type cannot be empty")
	}
	if lastSyncAt.IsZero() {
		return 0, errors.New("salesforce delta cursor cannot be zero")
	}
	for itemIndex, item := range items {
		if err := validateSalesForceSnapshotItem(item, objectType); err != nil {
			return 0, fmt.Errorf("salesforce delta item %d: %w", itemIndex, err)
		}
		if item.SourceUpdatedAt.After(lastSyncAt) {
			return 0, fmt.Errorf("salesforce delta item %d is newer than its cursor", itemIndex)
		}
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := lockSalesForceSync(ctx, tx, objectType); err != nil {
		return 0, err
	}

	upserted, err := upsertCatalogItems(ctx, tx, items, nil)
	if err != nil {
		return 0, err
	}
	if err := upsertSalesForceCursor(ctx, tx, objectType, lastSyncAt, ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return upserted, nil
}

func upsertSalesForceCursor(
	ctx context.Context,
	executor pgx.Tx,
	objectType string,
	lastSyncAt time.Time,
	deltaToken string,
) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO salesforce_sync_cursor (object_type, last_sync_at, last_delta_token)
		VALUES ($1, $2, $3)
		ON CONFLICT (object_type) DO UPDATE SET
			last_sync_at = CASE
				WHEN salesforce_sync_cursor.last_sync_at IS NULL THEN EXCLUDED.last_sync_at
				ELSE GREATEST(salesforce_sync_cursor.last_sync_at, EXCLUDED.last_sync_at)
			END,
			last_delta_token = CASE
				WHEN salesforce_sync_cursor.last_sync_at IS NULL
					OR EXCLUDED.last_sync_at > salesforce_sync_cursor.last_sync_at
					OR (
						EXCLUDED.last_sync_at = salesforce_sync_cursor.last_sync_at
						AND EXCLUDED.last_delta_token <> ''
					)
				THEN EXCLUDED.last_delta_token
				ELSE salesforce_sync_cursor.last_delta_token
			END,
			updated_at = NOW()
	`, objectType, lastSyncAt, deltaToken)
	return err
}

func lockSalesForceSync(ctx context.Context, transaction pgx.Tx, objectType string) error {
	_, err := transaction.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended('app-catalogo:salesforce:' || lower($1), 0)
		)
	`, objectType)
	if err != nil {
		return fmt.Errorf("lock salesforce sync for %q: %w", objectType, err)
	}
	return nil
}

func validateSalesForceSnapshotItem(item *models.CatalogItem, objectType string) error {
	if validationError := models.ValidateCatalogItem(item); validationError != nil {
		return validationError
	}
	if item.Source != models.SourceSalesForce {
		return fmt.Errorf("source must be %q", models.SourceSalesForce)
	}
	if strings.TrimSpace(item.ExternalID) == "" {
		return errors.New("external id cannot be empty")
	}
	if item.SourceUpdatedAt == nil || item.SourceUpdatedAt.IsZero() {
		return errors.New("source updated at is required")
	}

	var sourceData map[string]any
	if err := json.Unmarshal(item.SourceData, &sourceData); err != nil {
		return fmt.Errorf("invalid source data: %w", err)
	}
	itemObjectType, ok := sourceData[models.SalesForceObjectTypeSourceDataKey].(string)
	if !ok || !strings.EqualFold(strings.TrimSpace(itemObjectType), objectType) {
		return errors.New("source data object type does not match snapshot scope")
	}
	return nil
}

func validateCatalogItems(items []*models.CatalogItem) error {
	for itemIndex, item := range items {
		if validationError := models.ValidateCatalogItem(item); validationError != nil {
			return fmt.Errorf("catalog item %d: %w", itemIndex, validationError)
		}
	}
	return nil
}

// ClaimItemsForEmbedding atomically claims active items whose vector is absent
// or incompatible with the requested embedding contract. An old claim becomes
// eligible for takeover; its token stops working once another worker reclaims
// the row. SKIP LOCKED lets concurrent workers make disjoint progress.
func (r *CatalogItemRepository) ClaimItemsForEmbedding(
	ctx context.Context,
	metadata models.EmbeddingMetadata,
	limit int,
	claimTimeout time.Duration,
) (*EmbeddingClaim, error) {
	if limit <= 0 {
		return &EmbeddingClaim{Items: []*models.CatalogItem{}}, nil
	}
	if claimTimeout <= 0 {
		return nil, errors.New("embedding claim timeout must be positive")
	}

	claimToken := uuid.New()
	rows, err := r.db.Query(ctx, `
		WITH embedding_candidates AS (
			SELECT id
			FROM catalog_items
			WHERE deleted_at IS NULL
			  AND status = 'active'
			  AND (valid_from IS NULL OR valid_from <= NOW())
			  AND (valid_until IS NULL OR valid_until > NOW())
			  AND (
				embedding IS NULL
				OR embedding_model IS DISTINCT FROM $1
				OR embedding_model_version IS DISTINCT FROM $2
				OR embedding_dimensions IS DISTINCT FROM $3
				OR embedding_task_type IS DISTINCT FROM $4
				OR embedding_document_version IS DISTINCT FROM $5
				OR embedding_source_hash IS NULL
				OR embedding_generated_at IS NULL
			  )
			  AND (
				embedding_claim_token IS NULL
				OR embedding_claimed_at < NOW() - ($6::double precision * INTERVAL '1 second')
			  )
			ORDER BY updated_at, id
			LIMIT $7
			FOR UPDATE SKIP LOCKED
		)
		UPDATE catalog_items AS catalog_item
		SET embedding_claim_token = $8,
			embedding_claimed_at = NOW()
		FROM embedding_candidates
		WHERE catalog_item.id = embedding_candidates.id
		RETURNING
			catalog_item.id,
			catalog_item.external_id,
			catalog_item.source,
			catalog_item.type,
			catalog_item.title,
			COALESCE(catalog_item.description, ''),
			COALESCE(catalog_item.short_desc, ''),
			COALESCE(catalog_item.organization, ''),
			COALESCE(catalog_item.url, ''),
			COALESCE(catalog_item.image_url, ''),
			catalog_item.target_audience,
			catalog_item.bairros,
			COALESCE(catalog_item.modalidade, ''),
			catalog_item.status,
			catalog_item.tags,
			catalog_item.source_data,
			catalog_item.valid_from,
			catalog_item.valid_until,
			catalog_item.source_updated_at,
			catalog_item.created_at,
			catalog_item.updated_at
	`,
		metadata.Model,
		metadata.Version,
		metadata.Dimensions,
		metadata.DocumentTaskType,
		metadata.DocumentVersion,
		claimTimeout.Seconds(),
		limit,
		claimToken,
	)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &EmbeddingClaim{Token: claimToken, Items: items}, nil
}

// CompleteEmbedding writes a vector and all provenance metadata atomically.
// A false return means another worker superseded the claim or a source update
// invalidated it.
func (r *CatalogItemRepository) CompleteEmbedding(
	ctx context.Context,
	completion EmbeddingCompletion,
) (bool, error) {
	commandTag, err := r.db.Exec(ctx, `
		UPDATE catalog_items
		SET embedding = $3::vector,
			embedding_model = $4,
			embedding_model_version = $5,
			embedding_dimensions = $6,
			embedding_task_type = $7,
			embedding_document_version = $8,
			embedding_source_hash = $9,
			embedding_generated_at = NOW(),
			embedding_claim_token = NULL,
			embedding_claimed_at = NULL
		WHERE id = $1
		  AND embedding_claim_token = $2
		  AND deleted_at IS NULL
		  AND status = 'active'
		  AND (valid_from IS NULL OR valid_from <= NOW())
		  AND (valid_until IS NULL OR valid_until > NOW())
	`,
		completion.ItemID,
		completion.ClaimToken,
		completion.VectorLiteral,
		completion.Metadata.Model,
		completion.Metadata.Version,
		completion.Metadata.Dimensions,
		completion.Metadata.DocumentTaskType,
		completion.Metadata.DocumentVersion,
		completion.SourceHash,
	)
	if err != nil {
		return false, err
	}
	return commandTag.RowsAffected() == 1, nil
}

// ReleaseEmbeddingClaim makes unfinished work immediately available again.
func (r *CatalogItemRepository) ReleaseEmbeddingClaim(ctx context.Context, claimToken uuid.UUID) error {
	if claimToken == uuid.Nil {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		UPDATE catalog_items
		SET embedding_claim_token = NULL,
			embedding_claimed_at = NULL
		WHERE embedding_claim_token = $1
	`, claimToken)
	return err
}

// GetJourneyBoosts retorna um mapa de item_id → boost para itens que são vizinhos
// de jornada dos itemIDs fornecidos. O boost é `weight * 0.20`.
func (r *CatalogItemRepository) GetJourneyBoosts(ctx context.Context, fromItemIDs []string) (map[string]float64, error) {
	if len(fromItemIDs) == 0 {
		return map[string]float64{}, nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT ci.id::text, MAX(j.weight) * 0.20 AS boost
		FROM catalog_item_journeys j
		JOIN catalog_items from_ci
			ON from_ci.external_id = j.from_external_id
			AND from_ci.source::text = j.from_source
		JOIN catalog_items ci
			ON ci.external_id = j.to_external_id
			AND ci.source::text = j.to_source
		WHERE from_ci.id::text = ANY($1)
		  AND ci.deleted_at IS NULL
		  AND ci.status = 'active'
		GROUP BY ci.id
	`, fromItemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	boosts := make(map[string]float64)
	for rows.Next() {
		var id string
		var boost float64
		if err := rows.Scan(&id, &boost); err != nil {
			return nil, err
		}
		boosts[id] = boost
	}
	return boosts, rows.Err()
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
		SELECT id, external_id, source, type, title,
			COALESCE(description, ''), COALESCE(short_desc, ''),
			COALESCE(organization, ''), COALESCE(url, ''), COALESCE(image_url, ''),
			target_audience, bairros,
			COALESCE(modalidade, ''), status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE source = $1 AND external_id = $2 AND deleted_at IS NULL
	`, string(source), externalID)

	return scanCatalogItem(row)
}

// RecommendationCandidateSnapshot binds recommendation candidates to the
// repeatable-read catalog eligibility window that produced them.
type RecommendationCandidateSnapshot struct {
	SnapshotVersion CatalogSnapshotVersion
	CatalogRevision string
	Items           []*models.CatalogItem
}

// CatalogSnapshot returns the content and temporal eligibility version visible
// to a new PostgreSQL statement.
func (r *CatalogItemRepository) CatalogSnapshot(ctx context.Context) (CatalogSnapshotVersion, error) {
	return readCatalogSnapshotVersion(ctx, r.db)
}

// GetCandidateSnapshot reads the version and candidates from the same
// repeatable-read transaction so recommendation caches cannot mislabel rows.
func (r *CatalogItemRepository) GetCandidateSnapshot(
	ctx context.Context,
	types []models.ItemType,
	limit int,
) (*RecommendationCandidateSnapshot, error) {
	transaction, beginError := r.db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("recommendation candidate transaction: %w", beginError)
	}
	defer transaction.Rollback(ctx)

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(ctx, transaction)
	if snapshotError != nil {
		return nil, fmt.Errorf("recommendation candidate catalog snapshot: %w", snapshotError)
	}
	candidates, candidatesError := queryRecommendationCandidates(ctx, transaction, types, limit)
	if candidatesError != nil {
		return nil, candidatesError
	}
	if commitError := transaction.Commit(ctx); commitError != nil {
		return nil, fmt.Errorf("recommendation candidate commit: %w", commitError)
	}
	return &RecommendationCandidateSnapshot{
		SnapshotVersion: snapshotVersion,
		CatalogRevision: snapshotVersion.Revision,
		Items:           candidates,
	}, nil
}

// GetCandidates preserves the legacy repository contract. New recommendation
// callers should use GetCandidateSnapshot for cache-safe provenance.
func (r *CatalogItemRepository) GetCandidates(ctx context.Context, types []models.ItemType, limit int) ([]*models.CatalogItem, error) {
	return queryRecommendationCandidates(ctx, r.db, types, limit)
}

type recommendationCandidateQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func queryRecommendationCandidates(
	ctx context.Context,
	queryer recommendationCandidateQueryer,
	types []models.ItemType,
	limit int,
) ([]*models.CatalogItem, error) {
	typeStrs := make([]string, len(types))
	for i, t := range types {
		typeStrs[i] = string(t)
	}

	rows, err := queryer.Query(ctx, `
		SELECT id, external_id, source, type, title,
			COALESCE(description, ''), COALESCE(short_desc, ''),
			COALESCE(organization, ''), COALESCE(url, ''), COALESCE(image_url, ''),
			target_audience, bairros,
			COALESCE(modalidade, ''), status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE status = 'active'
		  AND deleted_at IS NULL
		  AND (cardinality($1::text[]) = 0 OR type = ANY($1::item_type[]))
		  AND (valid_from IS NULL OR valid_from <= NOW())
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
		SELECT id, external_id, source, type, title,
			COALESCE(description, ''), COALESCE(short_desc, ''),
			COALESCE(organization, ''), COALESCE(url, ''), COALESCE(image_url, ''),
			target_audience, bairros,
			COALESCE(modalidade, ''), status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	return scanCatalogItem(row)
}

// GetPublicByID returns only an item that is currently eligible for public
// discovery. The transport layer converts the result to an allowlisted DTO.
func (r *CatalogItemRepository) GetPublicByID(ctx context.Context, id uuid.UUID) (*models.CatalogItem, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, external_id, source, type, title,
			COALESCE(description, ''), COALESCE(short_desc, ''),
			COALESCE(organization, ''), COALESCE(url, ''), COALESCE(image_url, ''),
			target_audience, bairros,
			COALESCE(modalidade, ''), status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE id = $1
		  AND status = 'active'
		  AND deleted_at IS NULL
		  AND (valid_from IS NULL OR valid_from <= NOW())
		  AND (valid_until IS NULL OR valid_until > NOW())
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
	metadata := event.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
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
		metadata,
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

// UpdateSyncEventWithMetadata conclui um evento e persiste metadados de cursor
// na mesma operação. Isso impede que um evento seja marcado como concluído
// sem o cursor upstream necessário para a próxima sincronização.
func (r *CatalogItemRepository) UpdateSyncEventWithMetadata(
	ctx context.Context,
	id int64,
	status models.SyncEventStatus,
	processed int,
	failed int,
	errorMessage string,
	durationMilliseconds int,
	metadata json.RawMessage,
) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
	completedAt := time.Now()
	commandTag, updateError := r.db.Exec(ctx, `
		UPDATE sync_events
		SET status = $2, items_processed = $3, items_failed = $4,
		    error_message = $5, duration_ms = $6, completed_at = $7,
		    metadata = $8
		WHERE id = $1
	`, id, string(status), processed, failed, errorMessage, durationMilliseconds, completedAt, metadata)
	if updateError != nil {
		return updateError
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("sync event %d was not found", id)
	}
	return nil
}

// GetLastCompletedSyncMetadata retorna os metadados do evento concluído mais
// recente da fonte. O booleano distingue ausência de cursor de falha no banco.
func (r *CatalogItemRepository) GetLastCompletedSyncMetadata(
	ctx context.Context,
	source models.ItemSource,
) (json.RawMessage, bool, error) {
	var metadata json.RawMessage
	queryError := r.db.QueryRow(ctx, `
		SELECT metadata
		FROM sync_events
		WHERE source = $1
		  AND status = $2
		  AND completed_at IS NOT NULL
		ORDER BY completed_at DESC, id DESC
		LIMIT 1
	`, string(source), string(models.SyncStatusCompleted)).Scan(&metadata)
	if errors.Is(queryError, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if queryError != nil {
		return nil, false, queryError
	}
	return metadata, true, nil
}

// GetLastSyncEvents retorna os últimos eventos de sincronização por fonte.
func (r *CatalogItemRepository) GetLastSyncEvents(ctx context.Context) ([]*models.SyncStatus, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT ON (source)
			source, event_type, status, started_at, completed_at,
			items_processed, items_failed, COALESCE(error_message, '')
		FROM sync_events
		ORDER BY source, started_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := make([]*models.SyncStatus, 0)
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
		SELECT id, object_type, last_sync_at, COALESCE(last_delta_token, ''), updated_at
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
