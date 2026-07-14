package services

import (
	"strings"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const redisExpirationResolution = time.Millisecond

// catalogSnapshotCacheTTL floors the TTL to Redis' expiration resolution. A
// zero result means the value cannot be cached without crossing an eligibility
// boundary (or its configured lifetime is not representable safely).
func catalogSnapshotCacheTTL(
	configuredTTL time.Duration,
	snapshotVersion repository.CatalogSnapshotVersion,
) time.Duration {
	if configuredTTL <= 0 {
		return 0
	}
	effectiveTTL := configuredTTL
	if snapshotVersion.NextEligibilityTransition != nil {
		if snapshotVersion.ObservedAt.IsZero() {
			return 0
		}
		transitionTTL := snapshotVersion.NextEligibilityTransition.Sub(snapshotVersion.ObservedAt)
		if transitionTTL <= 0 {
			return 0
		}
		effectiveTTL = min(effectiveTTL, transitionTTL)
	}
	return effectiveTTL.Truncate(redisExpirationResolution)
}

func validCatalogSnapshotVersion(snapshotVersion repository.CatalogSnapshotVersion) bool {
	return strings.TrimSpace(snapshotVersion.Revision) != ""
}
