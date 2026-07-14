package repository

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const serviceIntelligenceMigrationPath = "../../db/migrations/000007_service_intelligence.sql"

func TestServiceIntelligenceMigrationContract(t *testing.T) {
	migrationBytes, readError := os.ReadFile(serviceIntelligenceMigrationPath)
	if readError != nil {
		t.Fatalf("read service intelligence migration: %v", readError)
	}
	migrationSQL := string(migrationBytes)
	for _, requiredFragment := range []string{
		"-- +goose Up",
		"-- +goose Down",
		"ADD COLUMN reason",
		"ADD COLUMN theme",
		"ADD COLUMN migration_origin",
		"ON CONFLICT (from_external_id, from_source, to_external_id, to_source) DO NOTHING",
		"CREATE CONSTRAINT TRIGGER trg_catalog_item_journeys_revision",
		"EXECUTE FUNCTION bump_catalog_revision()",
		"DELETE FROM catalog_item_journeys",
		"DROP TRIGGER IF EXISTS trg_catalog_item_journeys_revision",
		"DROP COLUMN theme",
		"DROP COLUMN reason",
	} {
		if !strings.Contains(migrationSQL, requiredFragment) {
			t.Errorf("service intelligence migration is missing %q", requiredFragment)
		}
	}
	if strings.Contains(migrationSQL, "-- +goose NO TRANSACTION") {
		t.Error("service intelligence migration must remain atomic")
	}
}

func TestServiceIntelligenceMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv(catalogMigrationTestDatabaseURLVariable)
	if databaseURL == "" {
		t.Skip(catalogMigrationTestDatabaseURLVariable + " is not configured")
	}
	databaseConfiguration, parseError := pgxpool.ParseConfig(databaseURL)
	if parseError != nil {
		t.Fatalf("parse migration test database configuration: %v", parseError)
	}
	if !strings.Contains(strings.ToLower(databaseConfiguration.ConnConfig.Database), "test") {
		t.Fatalf("migration round-trip requires a database whose name contains test")
	}

	testContext, cancelTest := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelTest()
	database, openError := sql.Open("pgx", databaseURL)
	if openError != nil {
		t.Fatalf("open migration test database: %v", openError)
	}
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelCleanup()
		currentVersion, versionError := goose.GetDBVersionContext(cleanupContext, database)
		if versionError == nil && currentVersion < latestCatalogMigrationVersion {
			if restoreError := goose.UpToContext(cleanupContext, database, "../../db/migrations", latestCatalogMigrationVersion); restoreError != nil {
				t.Errorf("restore service intelligence migration: %v", restoreError)
			}
		}
		if closeError := database.Close(); closeError != nil {
			t.Errorf("close migration test database: %v", closeError)
		}
	})
	if pingError := database.PingContext(testContext); pingError != nil {
		t.Fatalf("ping migration test database: %v", pingError)
	}
	if dialectError := goose.SetDialect("postgres"); dialectError != nil {
		t.Fatalf("configure Goose PostgreSQL dialect: %v", dialectError)
	}
	startingVersion, versionError := goose.GetDBVersionContext(testContext, database)
	if versionError != nil {
		t.Fatalf("read starting migration version: %v", versionError)
	}
	if startingVersion != latestCatalogMigrationVersion {
		t.Fatalf("service intelligence round-trip requires schema version %d, got %d", latestCatalogMigrationVersion, startingVersion)
	}
	assertServiceIntelligenceColumns(t, testContext, database, true)
	if downError := goose.DownToContext(testContext, database, "../../db/migrations", latestCatalogMigrationVersion-1); downError != nil {
		t.Fatalf("roll back service intelligence migration: %v", downError)
	}
	assertServiceIntelligenceColumns(t, testContext, database, false)
	if upError := goose.UpToContext(testContext, database, "../../db/migrations", latestCatalogMigrationVersion); upError != nil {
		t.Fatalf("reapply service intelligence migration: %v", upError)
	}
	assertServiceIntelligenceColumns(t, testContext, database, true)
}

func assertServiceIntelligenceColumns(
	testingInstance *testing.T,
	testContext context.Context,
	database *sql.DB,
	expectedToExist bool,
) {
	testingInstance.Helper()
	var columnsExist bool
	if scanError := database.QueryRowContext(testContext, `
		SELECT COUNT(*) = 3
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'catalog_item_journeys'
		  AND column_name IN ('reason', 'theme', 'migration_origin')
	`).Scan(&columnsExist); scanError != nil {
		testingInstance.Fatalf("inspect service intelligence columns: %v", scanError)
	}
	if columnsExist != expectedToExist {
		testingInstance.Fatalf("service intelligence columns exist = %t, want %t", columnsExist, expectedToExist)
	}
}
