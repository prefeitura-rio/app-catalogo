package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestCatalogPersistenceRejectsInvalidItemsBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	repositoryWithoutDatabase := NewCatalogItemRepository(nil)
	invalidItem := &models.CatalogItem{
		ExternalID: "invalid-1",
		Source:     models.SourceCourses,
		Type:       models.TypeCourse,
		Title:      "",
		Status:     models.StatusActive,
	}
	if upsertError := repositoryWithoutDatabase.Upsert(context.Background(), invalidItem); upsertError == nil {
		t.Fatal("Upsert reached the database with an invalid catalog item")
	}
	if _, batchError := repositoryWithoutDatabase.UpsertBatch(
		context.Background(),
		[]*models.CatalogItem{invalidItem},
	); batchError == nil {
		t.Fatal("UpsertBatch reached the database with an invalid catalog item")
	}

	snapshotTimestamp := time.Now().UTC()
	invalidItem.SourceUpdatedAt = &snapshotTimestamp
	if _, _, reconciliationError := repositoryWithoutDatabase.ReconcileSourceSnapshot(
		context.Background(),
		models.SourceCourses,
		[]*models.CatalogItem{invalidItem},
		1,
		&snapshotTimestamp,
		snapshotTimestamp,
	); reconciliationError == nil {
		t.Fatal("ReconcileSourceSnapshot reached the database with an invalid catalog item")
	}

	objectType := "Service__c"
	invalidSalesForceItem := &models.CatalogItem{
		ExternalID:      "001000000000001AAA",
		Source:          models.SourceSalesForce,
		Type:            models.TypeService,
		Status:          models.StatusActive,
		SourceData:      json.RawMessage(`{"_catalog_object_type":"Service__c"}`),
		SourceUpdatedAt: &snapshotTimestamp,
	}
	if _, _, reconciliationError := repositoryWithoutDatabase.ReconcileSalesForceSnapshot(
		context.Background(),
		objectType,
		[]*models.CatalogItem{invalidSalesForceItem},
		snapshotTimestamp,
	); reconciliationError == nil {
		t.Fatal("ReconcileSalesForceSnapshot reached the database with an invalid catalog item")
	}
	if _, deltaError := repositoryWithoutDatabase.UpsertSalesForceDelta(
		context.Background(),
		objectType,
		[]*models.CatalogItem{invalidSalesForceItem},
		snapshotTimestamp,
	); deltaError == nil {
		t.Fatal("UpsertSalesForceDelta reached the database with an invalid catalog item")
	}
}
