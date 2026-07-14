package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const catalogSnapshotRevisionPrefix = "catalog-v2:"

const catalogSnapshotVersionQuery = `
	WITH catalog_clock AS MATERIALIZED (
		SELECT transaction_timestamp() AS observed_at
	),
	next_transition AS (
		SELECT MIN(eligibility_boundaries.transition_at) AS transition_at
		FROM (
			SELECT MIN(ci.valid_from) AS transition_at
			FROM catalog_items ci
			CROSS JOIN catalog_clock
			WHERE ci.status = 'active'
			  AND ci.deleted_at IS NULL
			  AND ci.valid_from > catalog_clock.observed_at
			  AND ci.valid_from < 'infinity'::timestamptz
			  AND (ci.valid_until IS NULL OR ci.valid_until > ci.valid_from)
			UNION ALL
			SELECT MIN(ci.valid_until) AS transition_at
			FROM catalog_items ci
			CROSS JOIN catalog_clock
			WHERE ci.status = 'active'
			  AND ci.deleted_at IS NULL
			  AND ci.valid_until > catalog_clock.observed_at
			  AND ci.valid_until < 'infinity'::timestamptz
			  AND (ci.valid_from IS NULL OR ci.valid_from < ci.valid_until)
		) eligibility_boundaries
	)
	SELECT
		catalog_state.revision,
		catalog_clock.observed_at,
		next_transition.transition_at
	FROM catalog_state
	CROSS JOIN catalog_clock
	CROSS JOIN next_transition
	WHERE catalog_state.singleton = TRUE
`

// CatalogSnapshotVersion identifies one stable catalog eligibility window.
// ObservedAt and NextEligibilityTransition come from PostgreSQL's transaction
// clock so callers never mix database eligibility with an application clock.
type CatalogSnapshotVersion struct {
	ContentRevision           int64
	Revision                  string
	ObservedAt                time.Time
	NextEligibilityTransition *time.Time
}

type catalogSnapshotQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// readCatalogSnapshotVersion must run on the same queryer (and therefore the
// same transaction snapshot) as the catalog rows whose version it describes.
func readCatalogSnapshotVersion(
	queryContext context.Context,
	queryer catalogSnapshotQueryer,
) (CatalogSnapshotVersion, error) {
	var contentRevision int64
	var observedAt time.Time
	var nextEligibilityTransition *time.Time
	queryError := queryer.QueryRow(queryContext, catalogSnapshotVersionQuery).Scan(
		&contentRevision,
		&observedAt,
		&nextEligibilityTransition,
	)
	if queryError != nil {
		return CatalogSnapshotVersion{}, fmt.Errorf("catalog snapshot version: %w", queryError)
	}
	return newCatalogSnapshotVersion(contentRevision, observedAt, nextEligibilityTransition), nil
}

func newCatalogSnapshotVersion(
	contentRevision int64,
	observedAt time.Time,
	nextEligibilityTransition *time.Time,
) CatalogSnapshotVersion {
	transitionToken := "infinity"
	if nextEligibilityTransition != nil {
		transitionToken = fmt.Sprintf("%d", nextEligibilityTransition.UnixMicro())
	}
	return CatalogSnapshotVersion{
		ContentRevision:           contentRevision,
		Revision:                  fmt.Sprintf("%s%d:window-until-%s", catalogSnapshotRevisionPrefix, contentRevision, transitionToken),
		ObservedAt:                observedAt,
		NextEligibilityTransition: nextEligibilityTransition,
	}
}
