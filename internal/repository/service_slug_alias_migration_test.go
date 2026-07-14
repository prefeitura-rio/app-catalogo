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

const serviceSlugAliasMigrationPath = "../../db/migrations/000006_service_slug_aliases.sql"

func TestServiceSlugAliasMigrationContract(t *testing.T) {
	migrationBytes, readError := os.ReadFile(serviceSlugAliasMigrationPath)
	if readError != nil {
		t.Fatalf("read service slug alias migration: %v", readError)
	}
	migrationSQL := string(migrationBytes)
	for _, requiredFragment := range []string{
		"-- +goose Up",
		"-- +goose Down",
		"CREATE TABLE IF NOT EXISTS catalog_item_slug_aliases",
		"REFERENCES catalog_items(id) ON DELETE CASCADE",
		"PRIMARY KEY (catalog_item_id, slug)",
		"catalog_item_slug_aliases_slug_format",
		"idx_catalog_item_slug_aliases_lookup",
		"idx_catalog_item_slug_aliases_one_canonical",
		"jsonb_array_elements_text",
		"DROP TABLE IF EXISTS catalog_item_slug_aliases;",
	} {
		if !strings.Contains(migrationSQL, requiredFragment) {
			t.Errorf("service slug alias migration is missing %q", requiredFragment)
		}
	}
	if strings.Contains(migrationSQL, "-- +goose NO TRANSACTION") {
		t.Error("service slug alias migration must remain atomic")
	}
}

func TestServiceSlugAliasMigrationRoundTrip(t *testing.T) {
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
				t.Errorf("restore service slug alias migration: %v", restoreError)
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
		t.Fatalf("service slug alias round-trip requires schema version %d, got %d", latestCatalogMigrationVersion, startingVersion)
	}
	assertServiceSlugAliasTable(t, testContext, database, true)
	if downError := goose.DownToContext(testContext, database, "../../db/migrations", latestCatalogMigrationVersion-1); downError != nil {
		t.Fatalf("roll back service slug alias migration: %v", downError)
	}
	assertServiceSlugAliasTable(t, testContext, database, false)
	if upError := goose.UpToContext(testContext, database, "../../db/migrations", latestCatalogMigrationVersion); upError != nil {
		t.Fatalf("reapply service slug alias migration: %v", upError)
	}
	assertServiceSlugAliasTable(t, testContext, database, true)
}

func assertServiceSlugAliasTable(
	testingInstance *testing.T,
	testContext context.Context,
	database *sql.DB,
	expectedToExist bool,
) {
	testingInstance.Helper()
	var tableExists bool
	if scanError := database.QueryRowContext(testContext, `
		SELECT to_regclass('public.catalog_item_slug_aliases') IS NOT NULL
	`).Scan(&tableExists); scanError != nil {
		testingInstance.Fatalf("inspect service slug alias table: %v", scanError)
	}
	if tableExists != expectedToExist {
		testingInstance.Fatalf("service slug alias table exists = %t, want %t", tableExists, expectedToExist)
	}
}
