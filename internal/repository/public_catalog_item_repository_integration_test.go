package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestGetPublicByIDRequiresActiveCurrentItem(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	repository := NewCatalogItemRepository(databasePool)
	testContext, cancelTest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelTest()
	fixturePrefix := "public-detail-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)

	activeID := uuid.New()
	futureID := uuid.New()
	_, insertionError := databasePool.Exec(testContext, `
		INSERT INTO catalog_items (id, external_id, source, type, title, status, valid_from)
		VALUES
			($1, $2, 'typesense', 'service', 'Active item', 'active', NULL),
			($3, $4, 'typesense', 'service', 'Future item', 'active', NOW() + INTERVAL '1 day')
	`, activeID, fixturePrefix+"-active", futureID, fixturePrefix+"-future")
	if insertionError != nil {
		t.Fatalf("insert public detail fixtures: %v", insertionError)
	}

	if _, getError := repository.GetPublicByID(testContext, activeID); getError != nil {
		t.Fatalf("GetPublicByID(active) error = %v", getError)
	}
	if _, getError := repository.GetPublicByID(testContext, futureID); !errors.Is(getError, pgx.ErrNoRows) {
		t.Fatalf("GetPublicByID(future) error = %v, want pgx.ErrNoRows", getError)
	}
}
