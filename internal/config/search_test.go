package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestLoadSearchSettings(t *testing.T) {
	t.Run("padrão", func(t *testing.T) {
		got, err := loadSearchSettings(viper.New())
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if got.SemanticMinSimilarity != defaultSemanticMinSimilarity || got.HyDEMinSimilarity != defaultHyDEMinSimilarity {
			t.Errorf("padrão = %+v, want %v/%v", got, defaultSemanticMinSimilarity, defaultHyDEMinSimilarity)
		}
	})

	t.Run("valores", func(t *testing.T) {
		t.Setenv("SEARCH_SEMANTIC_MIN_SIMILARITY", "0.72")
		t.Setenv("SEARCH_HYDE_MIN_SIMILARITY", " 0 ")
		got, err := loadSearchSettings(viper.New())
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if got.SemanticMinSimilarity != 0.72 || got.HyDEMinSimilarity != 0 {
			t.Errorf("lido = %+v, want 0.72/0", got)
		}
	})

	for _, bad := range []string{"0,7", "abc", "-0.1", "1", "1.5", "NaN"} {
		t.Run("inválido "+bad, func(t *testing.T) {
			t.Setenv("SEARCH_HYDE_MIN_SIMILARITY", bad)
			if _, err := loadSearchSettings(viper.New()); err == nil {
				t.Errorf("SEARCH_HYDE_MIN_SIMILARITY=%q: esperado erro", bad)
			}
		})
	}
}
