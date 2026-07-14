package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestCitizenProfileRepositoryIgnoresAndPreservesLegacyClusterColumns(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	citizenProfileHashDigest := sha256.Sum256([]byte(uuid.NewString()))
	citizenProfileHash := fmt.Sprintf("%x", citizenProfileHashDigest)
	legacyClusterIdentifier := 741
	legacyClusterUpdatedAt := time.Date(2025, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	initialLastSyncedAt := time.Date(2025, time.February, 3, 4, 5, 6, 234567000, time.UTC)

	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupError := databasePool.Exec(
			cleanupContext,
			`DELETE FROM citizen_profiles WHERE cpf_hash = $1`,
			citizenProfileHash,
		); cleanupError != nil {
			t.Errorf("cleanup citizen profile fixture: %v", cleanupError)
		}
	})

	_, insertionError := databasePool.Exec(testContext, `
		INSERT INTO citizen_profiles (
			cpf_hash, bairro, cidade, estado, cep,
			escolaridade, renda_familiar, deficiencia,
			etnia, genero, faixa_etaria,
			cluster_id, cluster_updated_at, last_synced_at
		) VALUES (
			$1, 'Centro', 'Rio de Janeiro', 'RJ', '20000000',
			'ensino-medio', '2-4-sm', 'nenhuma',
			'nao-informada', 'nao-informado', '25-34',
			$2, $3, $4
		)
	`, citizenProfileHash, legacyClusterIdentifier, legacyClusterUpdatedAt, initialLastSyncedAt)
	if insertionError != nil {
		t.Fatalf("insert citizen profile fixture with legacy cluster values: %v", insertionError)
	}

	citizenProfileRepository := NewCitizenProfileRepository(databasePool)
	storedProfile, retrievalError := citizenProfileRepository.GetByCPFHash(testContext, citizenProfileHash)
	if retrievalError != nil {
		t.Fatalf("GetByCPFHash with legacy cluster values: %v", retrievalError)
	}
	if storedProfile.CPFHash != citizenProfileHash || storedProfile.Bairro != "Centro" ||
		!storedProfile.LastSyncedAt.Equal(initialLastSyncedAt) {
		t.Fatalf("stored citizen profile = %#v", storedProfile)
	}

	updatedProfile := &models.CitizenProfile{
		CPFHash:       citizenProfileHash,
		Bairro:        "Tijuca",
		Cidade:        "Rio de Janeiro",
		Estado:        "RJ",
		CEP:           "20500000",
		Escolaridade:  "superior-completo",
		RendaFamiliar: "4-8-sm",
		Deficiencia:   "nenhuma",
		Etnia:         "nao-informada",
		Genero:        "nao-informado",
		FaixaEtaria:   "35-44",
	}
	if upsertError := citizenProfileRepository.Upsert(testContext, updatedProfile); upsertError != nil {
		t.Fatalf("Upsert citizen profile with legacy cluster values: %v", upsertError)
	}

	var retainedClusterIdentifier int
	var retainedClusterUpdatedAt time.Time
	legacyColumnsQueryError := databasePool.QueryRow(testContext, `
		SELECT cluster_id, cluster_updated_at
		FROM citizen_profiles
		WHERE cpf_hash = $1
	`, citizenProfileHash).Scan(&retainedClusterIdentifier, &retainedClusterUpdatedAt)
	if legacyColumnsQueryError != nil {
		t.Fatalf("read retained legacy cluster values: %v", legacyColumnsQueryError)
	}
	if retainedClusterIdentifier != legacyClusterIdentifier ||
		!retainedClusterUpdatedAt.Equal(legacyClusterUpdatedAt) {
		t.Fatalf(
			"legacy cluster values changed to identifier=%d updated_at=%s",
			retainedClusterIdentifier,
			retainedClusterUpdatedAt,
		)
	}

	reloadedProfile, reloadError := citizenProfileRepository.GetByCPFHash(testContext, citizenProfileHash)
	if reloadError != nil {
		t.Fatalf("GetByCPFHash after Upsert: %v", reloadError)
	}
	if reloadedProfile.Bairro != updatedProfile.Bairro || reloadedProfile.CEP != updatedProfile.CEP ||
		reloadedProfile.Escolaridade != updatedProfile.Escolaridade {
		t.Fatalf("reloaded citizen profile = %#v, want updated runtime fields", reloadedProfile)
	}
}
