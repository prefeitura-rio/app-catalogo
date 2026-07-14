package repository

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCatalogSnapshotRevisionIncludesContentAndEligibilityWindow(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 123456000, time.UTC)
	nextTransition := observedAt.Add(5*time.Minute + 789*time.Microsecond)
	snapshotVersion := newCatalogSnapshotVersion(42, observedAt, &nextTransition)

	expectedRevision := "catalog-v2:42:window-until-" + formatUnixMicro(nextTransition)
	if snapshotVersion.Revision != expectedRevision {
		t.Fatalf("revision = %q, want %q", snapshotVersion.Revision, expectedRevision)
	}
	if snapshotVersion.ContentRevision != 42 || !snapshotVersion.ObservedAt.Equal(observedAt) ||
		snapshotVersion.NextEligibilityTransition == nil ||
		!snapshotVersion.NextEligibilityTransition.Equal(nextTransition) {
		t.Fatalf("snapshot version = %#v", snapshotVersion)
	}

	infiniteVersion := newCatalogSnapshotVersion(43, observedAt, nil)
	if infiniteVersion.Revision != "catalog-v2:43:window-until-infinity" {
		t.Fatalf("infinite revision = %q", infiniteVersion.Revision)
	}
}

func TestCatalogSnapshotQueryUsesDatabaseClockAndBothEligibilityBoundaries(t *testing.T) {
	t.Parallel()

	for _, requiredFragment := range []string{
		"transaction_timestamp()",
		"ci.valid_from",
		"ci.valid_until",
		"ci.valid_from > catalog_clock.observed_at",
		"ci.valid_until > catalog_clock.observed_at",
		"ci.valid_from < 'infinity'::timestamptz",
		"ci.valid_until < 'infinity'::timestamptz",
		"catalog_state.revision",
	} {
		if !strings.Contains(catalogSnapshotVersionQuery, requiredFragment) {
			t.Errorf("catalog snapshot query is missing %q", requiredFragment)
		}
	}
}

func formatUnixMicro(timestamp time.Time) string {
	return strconv.FormatInt(timestamp.UnixMicro(), 10)
}
