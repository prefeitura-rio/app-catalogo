package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
)

// semanticVectorsTimeout limita a geração do embedding da query e do HyDE.
const semanticVectorsTimeout = 4 * time.Second

type semanticEncoder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	GenerateHyDE(ctx context.Context, query string) (string, error)
}

type vectorStore interface {
	Get(ctx context.Context, key string, dest interface{}) error
	SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)
}

// searchVectors são os literais pgvector de uma busca. HyDE vazio significa
// busca híbrida sem HyDE.
type searchVectors struct {
	Query string `json:"query"`
	HyDE  string `json:"hyde,omitempty"`
}

// semanticVectors devolve os vetores da busca por q. O HyDE é gerado por LLM e
// muda a cada chamada, e cada página é uma requisição própria: os vetores são
// gerados uma vez por texto de busca e guardados no Redis, para que todas as
// páginas usem a mesma lista RRF e o mesmo total (DEV-143). O SETNX faz
// requisições concorrentes convergirem no primeiro valor gravado. Com o Redis
// indisponível a busca segue com os vetores recém-gerados.
func (s *SearchService) semanticVectors(ctx context.Context, q string) (searchVectors, error) {
	key := searchVectorsCacheKey(q)

	var cached searchVectors
	err := s.vectorStore.Get(ctx, key, &cached)
	if err == nil && cached.Query != "" {
		return cached, nil
	}
	if err != nil && !cache.IsMiss(err) {
		log.Warn().Err(err).Msg("search: falha ao ler vetores do cache")
	}

	generated, err := s.generateSearchVectors(ctx, q)
	if err != nil {
		return searchVectors{}, err
	}

	stored, err := s.vectorStore.SetNX(ctx, key, generated, s.vectorsTTL)
	if err != nil {
		log.Warn().Err(err).Msg("search: falha ao salvar vetores no cache")
		return generated, nil
	}
	if !stored {
		var winner searchVectors
		if err := s.vectorStore.Get(ctx, key, &winner); err == nil && winner.Query != "" {
			return winner, nil
		}
	}
	return generated, nil
}

// generateSearchVectors gera em paralelo o embedding da query e o do documento
// HyDE. Falha do embedding da query é erro (a busca cai para FTS); falha do
// HyDE só deixa o HyDE vazio.
func (s *SearchService) generateSearchVectors(ctx context.Context, q string) (searchVectors, error) {
	type vecResult struct {
		vec []float32
		err error
	}

	parallelCtx, cancel := context.WithTimeout(ctx, semanticVectorsTimeout)
	defer cancel()

	embedCh := make(chan vecResult, 1)
	hydeCh := make(chan vecResult, 1)

	go func() {
		v, err := s.encoder.EmbedQuery(parallelCtx, q)
		embedCh <- vecResult{v, err}
	}()
	go func() {
		hydeDoc, err := s.encoder.GenerateHyDE(parallelCtx, q)
		if err != nil {
			hydeCh <- vecResult{nil, err}
			return
		}
		v, err := s.encoder.EmbedQuery(parallelCtx, hydeDoc)
		hydeCh <- vecResult{v, err}
	}()

	embedRes := <-embedCh
	hydeRes := <-hydeCh

	if embedRes.err != nil {
		return searchVectors{}, fmt.Errorf("embedding da query: %w", embedRes.err)
	}
	queryVec := clients.VectorLiteral(embedRes.vec)
	if queryVec == "" {
		return searchVectors{}, errors.New("embedding da query vazio")
	}

	vecs := searchVectors{Query: queryVec}
	if hydeRes.err != nil {
		log.Debug().Err(hydeRes.err).Msg("search: hyde indisponível, usando hybrid sem hyde")
		return vecs, nil
	}
	vecs.HyDE = clients.VectorLiteral(hydeRes.vec)
	return vecs, nil
}

func searchVectorsCacheKey(q string) string {
	h := sha256.Sum256([]byte("search-vectors:" + q))
	return fmt.Sprintf("catalogo:search:vectors:%x", h[:16])
}
