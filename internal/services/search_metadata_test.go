package services

import (
	"encoding/json"
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestSanitizedSearchMetadata_CartaThemeFields(t *testing.T) {
	source := json.RawMessage(`{
		"slug": "iptu-2026",
		"themeSlug": "tributos",
		"themeName": "Tributos",
		"secret": "drop-me"
	}`)
	got := sanitizedSearchMetadata(models.TypeService, source)
	var meta map[string]string
	if err := json.Unmarshal(got, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta["slug"] != "iptu-2026" {
		t.Fatalf("slug=%q", meta["slug"])
	}
	if meta["themeSlug"] != "tributos" || meta["themeName"] != "Tributos" {
		t.Fatalf("theme metadata missing: %v", meta)
	}
	if _, ok := meta["secret"]; ok {
		t.Fatal("secret must not leak into metadata")
	}
}

func TestSanitizedSearchMetadata_LegacyTemaKeys(t *testing.T) {
	source := json.RawMessage(`{"slug":"x","tema_geral":"Tributos","Tema__c":"IPTU"}`)
	got := sanitizedSearchMetadata(models.TypeService, source)
	var meta map[string]string
	if err := json.Unmarshal(got, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["tema_geral"] != "Tributos" || meta["Tema__c"] != "IPTU" {
		t.Fatalf("legacy keys: %v", meta)
	}
}
