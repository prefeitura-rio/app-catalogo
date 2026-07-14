package services

import (
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

func TestCatalogSnapshotCacheTTLStopsBeforeTemporalBoundary(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	nextTransition := observedAt.Add(3500 * time.Microsecond)
	snapshotVersion := repository.CatalogSnapshotVersion{
		Revision:                  "catalog-v2:1:window-until-1783771200003500",
		ObservedAt:                observedAt,
		NextEligibilityTransition: &nextTransition,
	}

	if cacheTTL := catalogSnapshotCacheTTL(time.Minute, snapshotVersion); cacheTTL != 3*time.Millisecond {
		t.Fatalf("cache TTL = %s, want 3ms", cacheTTL)
	}
	if cacheTTL := catalogSnapshotCacheTTL(2*time.Millisecond, snapshotVersion); cacheTTL != 2*time.Millisecond {
		t.Fatalf("configured cache TTL = %s, want 2ms", cacheTTL)
	}
}

func TestCatalogSnapshotCacheTTLSkipsUnsafeOrUnrepresentableWindows(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name           string
		configuredTTL  time.Duration
		observedAt     time.Time
		nextTransition *time.Time
	}{
		{name: "non-positive configured TTL", configuredTTL: 0, observedAt: observedAt},
		{name: "missing observation", configuredTTL: time.Minute, nextTransition: timePointer(observedAt.Add(time.Second))},
		{name: "elapsed transition", configuredTTL: time.Minute, observedAt: observedAt, nextTransition: timePointer(observedAt)},
		{name: "sub-millisecond transition", configuredTTL: time.Minute, observedAt: observedAt, nextTransition: timePointer(observedAt.Add(time.Microsecond))},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			snapshotVersion := repository.CatalogSnapshotVersion{
				Revision:                  "catalog-v2:1:window-until-test",
				ObservedAt:                testCase.observedAt,
				NextEligibilityTransition: testCase.nextTransition,
			}
			if cacheTTL := catalogSnapshotCacheTTL(testCase.configuredTTL, snapshotVersion); cacheTTL != 0 {
				t.Fatalf("cache TTL = %s, want 0", cacheTTL)
			}
		})
	}

	if cacheTTL := catalogSnapshotCacheTTL(1500*time.Microsecond, repository.CatalogSnapshotVersion{
		Revision: "catalog-v2:1:window-until-infinity",
	}); cacheTTL != time.Millisecond {
		t.Fatalf("infinite-window TTL = %s, want 1ms", cacheTTL)
	}
}

func timePointer(timestamp time.Time) *time.Time {
	return &timestamp
}
