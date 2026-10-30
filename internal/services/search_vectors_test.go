package services

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
)

// varyingEncoder imita o Gemini da busca: o embedding depende só do texto,
// mas o documento HyDE muda a cada chamada, como a geração por LLM.
type varyingEncoder struct {
	mu        sync.Mutex
	hydeCalls int
	embedErr  error
	hydeErr   error
	hydeVec   func(call int) []float32
	queryVec  func(text string) []float32
}

func (e *varyingEncoder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.embedErr != nil {
		return nil, e.embedErr
	}
	if len(text) > 5 && text[:5] == "hyde:" {
		call, err := strconv.Atoi(text[5:])
		if err != nil {
			return nil, err
		}
		if e.hydeVec != nil {
			return e.hydeVec(call), nil
		}
		return []float32{0, float32(call)}, nil
	}
	if e.queryVec != nil {
		return e.queryVec(text), nil
	}
	return []float32{float32(len(text)), 1}, nil
}

func (e *varyingEncoder) GenerateHyDE(_ context.Context, _ string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hydeErr != nil {
		return "", e.hydeErr
	}
	e.hydeCalls++
	return "hyde:" + strconv.Itoa(e.hydeCalls), nil
}

func (e *varyingEncoder) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hydeCalls
}

// memoryStore guarda JSON como o RedisCache. beforeSetNX simula outra
// requisição gravando a chave entre a leitura e a escrita.
type memoryStore struct {
	mu          sync.Mutex
	data        map[string][]byte
	ttls        map[string]time.Duration
	err         error
	beforeSetNX func(m *memoryStore, key string)
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (m *memoryStore) Get(_ context.Context, key string, dest interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	raw, ok := m.data[key]
	if !ok {
		return cache.ErrCacheMiss
	}
	return json.Unmarshal(raw, dest)
}

func (m *memoryStore) SetNX(_ context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	if m.beforeSetNX != nil {
		m.beforeSetNX(m, key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	if _, ok := m.data[key]; ok {
		return false, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	m.data[key] = raw
	m.ttls[key] = ttl
	return true, nil
}

func newVectorsTestService(enc semanticEncoder, store vectorStore) *SearchService {
	return &SearchService{encoder: enc, vectorStore: store, vectorsTTL: 30 * time.Minute}
}

// Cada página é uma requisição própria e o HyDE muda a cada geração: a mesma
// busca precisa reutilizar os vetores da primeira página (DEV-143).
func TestSemanticVectors_ReusesFirstGenerationAcrossPages(t *testing.T) {
	enc := &varyingEncoder{}
	store := newMemoryStore()
	svc := newVectorsTestService(enc, store)
	ctx := context.Background()

	first, err := svc.semanticVectors(ctx, "curso")
	if err != nil {
		t.Fatalf("1ª página: %v", err)
	}
	if first.Query == "" || first.HyDE == "" {
		t.Fatalf("vetores incompletos: %+v", first)
	}
	for page := 2; page <= 5; page++ {
		got, err := svc.semanticVectors(ctx, "curso")
		if err != nil {
			t.Fatalf("página %d: %v", page, err)
		}
		if got != first {
			t.Fatalf("página %d com vetores diferentes da 1ª: %+v != %+v", page, got, first)
		}
	}
	if enc.calls() != 1 {
		t.Fatalf("HyDE gerado %d vezes; esperado 1", enc.calls())
	}
	if ttl := store.ttls[searchVectorsCacheKey("curso")]; ttl != 30*time.Minute {
		t.Fatalf("TTL dos vetores = %s; esperado 30m", ttl)
	}

	other, err := svc.semanticVectors(ctx, "vaga")
	if err != nil {
		t.Fatalf("outra busca: %v", err)
	}
	if other.Query == first.Query || other.HyDE == first.HyDE {
		t.Fatalf("buscas diferentes compartilharam vetores: %+v", other)
	}
}

// Sem o HyDE na 1ª página, as seguintes também seguem sem ele, mesmo que o
// HyDE volte: misturar listas com e sem HyDE muda o total entre páginas.
func TestSemanticVectors_CachesHyDEFailureAsHybridWithoutHyDE(t *testing.T) {
	enc := &varyingEncoder{hydeErr: context.DeadlineExceeded}
	svc := newVectorsTestService(enc, newMemoryStore())
	ctx := context.Background()

	first, err := svc.semanticVectors(ctx, "curso")
	if err != nil {
		t.Fatalf("falha do HyDE não pode virar erro: %v", err)
	}
	if first.Query == "" || first.HyDE != "" {
		t.Fatalf("esperado só o vetor da query: %+v", first)
	}

	enc.mu.Lock()
	enc.hydeErr = nil
	enc.mu.Unlock()
	second, err := svc.semanticVectors(ctx, "curso")
	if err != nil {
		t.Fatalf("2ª página: %v", err)
	}
	if second != first {
		t.Fatalf("2ª página mudou de lista: %+v != %+v", second, first)
	}
}

// Sem o embedding da query a busca cai para FTS; nada é guardado, e a próxima
// requisição tenta gerar de novo.
func TestSemanticVectors_QueryEmbeddingFailureIsNotCached(t *testing.T) {
	enc := &varyingEncoder{embedErr: errors.New("gemini indisponível")}
	store := newMemoryStore()
	svc := newVectorsTestService(enc, store)
	ctx := context.Background()

	if _, err := svc.semanticVectors(ctx, "curso"); err == nil {
		t.Fatal("falha do embedding da query deveria virar erro")
	}
	if len(store.data) != 0 {
		t.Fatalf("falha não pode ir para o cache: %v", store.data)
	}

	enc.mu.Lock()
	enc.embedErr = nil
	enc.mu.Unlock()
	got, err := svc.semanticVectors(ctx, "curso")
	if err != nil || got.Query == "" {
		t.Fatalf("depois da falha, esperado gerar os vetores: %+v, %v", got, err)
	}
}

// Duas páginas pedidas ao mesmo tempo geram vetores diferentes; a que grava
// depois descarta os seus e usa os já gravados.
func TestSemanticVectors_ConcurrentWriterWins(t *testing.T) {
	winner := searchVectors{Query: "[9,9]", HyDE: "[8,8]"}
	store := newMemoryStore()
	store.beforeSetNX = func(m *memoryStore, key string) {
		raw, _ := json.Marshal(winner)
		m.mu.Lock()
		m.data[key] = raw
		m.mu.Unlock()
	}
	svc := newVectorsTestService(&varyingEncoder{}, store)

	got, err := svc.semanticVectors(context.Background(), "curso")
	if err != nil {
		t.Fatalf("semanticVectors: %v", err)
	}
	if got != winner {
		t.Fatalf("esperado o valor já gravado %+v, obtido %+v", winner, got)
	}
}

// Redis fora do ar não derruba a busca: os vetores recém-gerados são usados.
func TestSemanticVectors_StoreUnavailableUsesGeneratedVectors(t *testing.T) {
	store := newMemoryStore()
	store.err = errors.New("dial tcp: connection refused")
	svc := newVectorsTestService(&varyingEncoder{}, store)

	got, err := svc.semanticVectors(context.Background(), "curso")
	if err != nil {
		t.Fatalf("Redis indisponível não pode virar erro: %v", err)
	}
	if got.Query == "" || got.HyDE == "" {
		t.Fatalf("esperado os vetores gerados: %+v", got)
	}
}

func TestNewSearchService_VectorsDefaults(t *testing.T) {
	svc := NewSearchService(nil, nil, time.Minute, 0, nil, nil)
	if svc.vectorsTTL != defaultSearchVectorsTTL {
		t.Fatalf("TTL não positivo deveria cair no padrão: %s", svc.vectorsTTL)
	}
	if svc.encoder != nil {
		t.Fatal("sem cliente Gemini a busca semântica deve ficar desativada (encoder nil)")
	}
	if svc := NewSearchService(nil, nil, time.Minute, 10*time.Minute, nil, nil); svc.vectorsTTL != 10*time.Minute {
		t.Fatalf("TTL configurado ignorado: %s", svc.vectorsTTL)
	}
}

func TestSearchVectorsCacheKey(t *testing.T) {
	a, b := searchVectorsCacheKey("curso"), searchVectorsCacheKey("Curso")
	if a == b {
		t.Fatal("textos diferentes geram o embedding de textos diferentes: chaves devem diferir")
	}
	if a != searchVectorsCacheKey("curso") {
		t.Fatal("chave deve ser determinística")
	}
	if len(a) != len("catalogo:search:vectors:")+32 {
		t.Fatalf("chave inesperada: %q", a)
	}
}
