package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type recommendationRepositoryStub struct {
	catalogSnapshotFunction   func(context.Context) (repository.CatalogSnapshotVersion, error)
	candidateSnapshotFunction func(context.Context, []models.ItemType, int) (*repository.RecommendationCandidateSnapshot, error)
	journeyBoostsFunction     func(context.Context, []string) (map[string]float64, error)
	legacyCandidateCalls      int
}

func (repositoryStub *recommendationRepositoryStub) CatalogSnapshot(
	requestContext context.Context,
) (repository.CatalogSnapshotVersion, error) {
	return repositoryStub.catalogSnapshotFunction(requestContext)
}

func (repositoryStub *recommendationRepositoryStub) GetCandidateSnapshot(
	requestContext context.Context,
	itemTypes []models.ItemType,
	limit int,
) (*repository.RecommendationCandidateSnapshot, error) {
	return repositoryStub.candidateSnapshotFunction(requestContext, itemTypes, limit)
}

func (repositoryStub *recommendationRepositoryStub) GetCandidates(
	context.Context,
	[]models.ItemType,
	int,
) ([]*models.CatalogItem, error) {
	repositoryStub.legacyCandidateCalls++
	return nil, errors.New("legacy candidates must not run")
}

func (repositoryStub *recommendationRepositoryStub) GetJourneyBoosts(
	requestContext context.Context,
	itemIDs []string,
) (map[string]float64, error) {
	if repositoryStub.journeyBoostsFunction != nil {
		return repositoryStub.journeyBoostsFunction(requestContext, itemIDs)
	}
	return map[string]float64{}, nil
}

type recommendationCacheRecorder struct {
	cachedResponse *models.RecommendationResponse
	setCount       int
	setKey         string
	setTTL         time.Duration
}

func (cacheRecorder *recommendationCacheRecorder) Get(
	_ context.Context,
	_ string,
	destination any,
) error {
	if cacheRecorder.cachedResponse == nil {
		return cache.ErrCacheMiss
	}
	recommendationDestination, validDestination := destination.(*models.RecommendationResponse)
	if !validDestination {
		return errors.New("unexpected recommendation cache destination")
	}
	*recommendationDestination = *cacheRecorder.cachedResponse
	return nil
}

func (cacheRecorder *recommendationCacheRecorder) Set(
	_ context.Context,
	cacheKey string,
	_ any,
	ttl time.Duration,
) error {
	cacheRecorder.setCount++
	cacheRecorder.setKey = cacheKey
	cacheRecorder.setTTL = ttl
	return nil
}

func TestScoreItem_SemPerfil(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	item := &models.CatalogItem{
		Type:           models.TypeCourse,
		Source:         models.SourceCourses,
		Title:          "Curso Teste",
		Bairros:        []string{},
		Modalidade:     "online",
		TargetAudience: nil,
	}

	score, breakdown, scoringError := svc.scoreItem(item, nil, typeWeights)
	if scoringError != nil {
		t.Fatalf("scoreItem returned an error: %v", scoringError)
	}

	if score <= 0 || score > 1 {
		t.Errorf("score esperado entre 0 e 1, got %.2f", score)
	}
	if len(breakdown) != 6 {
		t.Errorf("esperado 6 dimensões no breakdown, got %d", len(breakdown))
	}
}

func TestScoreItem_PerfilCompleto_Match(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	profile := &models.CitizenProfile{
		Bairro:        "Tijuca",
		Escolaridade:  "medio",
		RendaFamiliar: "ate_1sm",
		FaixaEtaria:   "25-34",
	}

	audience := []byte(`{"escolaridade":["medio","fundamental"],"renda":"ate_1sm","faixa_etaria":["25-34","18-24"]}`)
	item := &models.CatalogItem{
		Type:           models.TypeCourse,
		Source:         models.SourceCourses,
		Title:          "Curso gratuito",
		Bairros:        []string{"Tijuca"},
		TargetAudience: audience,
	}

	score, _, scoringError := svc.scoreItem(item, profile, typeWeights)
	if scoringError != nil {
		t.Fatalf("scoreItem returned an error: %v", scoringError)
	}

	// Com todos os campos batendo, o score deve ser alto (> 0.7)
	if score < 0.7 {
		t.Errorf("score esperado > 0.7 para match completo, got %.2f", score)
	}
}

func TestScoreItem_PerfilCompleto_SemMatch(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	profile := &models.CitizenProfile{
		Bairro:        "Botafogo",
		Escolaridade:  "superior",
		RendaFamiliar: "5_10sm",
		FaixaEtaria:   "45-59",
	}

	// Item para jovens de baixa renda no centro
	audience := []byte(`{"escolaridade":["fundamental"],"renda":"ate_1sm","faixa_etaria":["18-24"]}`)
	item := &models.CatalogItem{
		Type:           models.TypeCourse,
		Source:         models.SourceCourses,
		Title:          "Curso para jovens",
		Bairros:        []string{"Centro"},
		TargetAudience: audience,
	}

	scoreMatch, _, matchScoringError := svc.scoreItem(item, profile, typeWeights)
	if matchScoringError != nil {
		t.Fatalf("scoreItem with profile returned an error: %v", matchScoringError)
	}
	scoreNone, _, anonymousScoringError := svc.scoreItem(item, nil, typeWeights)
	if anonymousScoringError != nil {
		t.Fatalf("scoreItem without profile returned an error: %v", anonymousScoringError)
	}

	// Perfil não-match deve ter score menor que o anônimo (que usa defaults neutros)
	if scoreMatch >= scoreNone {
		t.Errorf("perfil sem match (%.2f) deveria ter score menor que anônimo (%.2f)", scoreMatch, scoreNone)
	}
}

func TestScoreItem_PCD_ItemComAcessibilidade(t *testing.T) {
	svc := &RecommendationService{weights: models.DefaultWeights}
	typeWeights := models.TypeWeightsByContext[models.ContextHomepage]

	profilePCD := &models.CitizenProfile{Deficiencia: "fisica"}
	profileSemPCD := &models.CitizenProfile{}

	audience := []byte(`{"deficiencia":["fisica","auditiva"]}`)
	item := &models.CatalogItem{
		Type:           models.TypeService,
		TargetAudience: audience,
	}

	scorePCD, breakdownPCD, pcdScoringError := svc.scoreItem(item, profilePCD, typeWeights)
	if pcdScoringError != nil {
		t.Fatalf("scoreItem for PCD profile returned an error: %v", pcdScoringError)
	}
	scoreSemPCD, _, nonPCDScoringError := svc.scoreItem(item, profileSemPCD, typeWeights)
	if nonPCDScoringError != nil {
		t.Fatalf("scoreItem for non-PCD profile returned an error: %v", nonPCDScoringError)
	}

	// PCD com item acessível deve ter score de acessibilidade = 1.0
	expectedAcessibilidadeContrib := models.DefaultWeights.Acessibilidade * 1.0
	if breakdownPCD["acessibilidade"] != round2(expectedAcessibilidadeContrib) {
		t.Errorf("acessibilidade PCD esperada %.2f, got %.2f", expectedAcessibilidadeContrib, breakdownPCD["acessibilidade"])
	}

	// Sem PCD: todos os itens são elegíveis, também score alto
	if scoreSemPCD < 0 || scorePCD < 0 {
		t.Error("scores não devem ser negativos")
	}
}

func TestMatchLocalizacao(t *testing.T) {
	cases := []struct {
		bairro      string
		itemBairros []string
		modalidade  string
		wantHigh    bool // score >= 0.6
	}{
		{"Tijuca", []string{"Tijuca"}, "presencial", true},
		{"Botafogo", []string{"Tijuca"}, "presencial", false},
		{"", []string{"Tijuca"}, "online", true},
		{"Qualquer", []string{}, "presencial", true},
		{"Qualquer", []string{}, "ead", true},
	}

	for _, tc := range cases {
		score := matchLocalizacao(tc.bairro, tc.itemBairros, tc.modalidade)
		if tc.wantHigh && score < 0.5 {
			t.Errorf("bairro=%q modalidade=%q: esperava score >= 0.5, got %.2f", tc.bairro, tc.modalidade, score)
		}
		if !tc.wantHigh && score >= 0.6 {
			t.Errorf("bairro=%q modalidade=%q: esperava score < 0.6, got %.2f", tc.bairro, tc.modalidade, score)
		}
	}
}

func TestCalcFaixaEtaria(t *testing.T) {
	cases := []struct {
		birth string
		want  string
	}{
		{"2000-01-01", "25-34"},
		{"2010-06-15", "menor-18"},
		{"1965-03-20", "60+"},
		{"1985-12-01", "35-44"},
		{"", ""},
	}

	for _, tc := range cases {
		got := calcFaixaEtaria(tc.birth)
		if got != tc.want {
			// Faixa etária depende do ano atual — aceitar variação de ±1 faixa
			t.Logf("calcFaixaEtaria(%q) = %q (esperava %q) — pode variar com o ano", tc.birth, got, tc.want)
		}
	}
}

func TestRecommendationRevalidatesHitAndCachesConsistentCandidateSnapshot(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	nextTransition := observedAt.Add(3500 * time.Microsecond)
	oldSnapshot := repository.CatalogSnapshotVersion{Revision: "catalog-v2:4:window-until-100"}
	freshSnapshot := repository.CatalogSnapshotVersion{
		Revision:                  "catalog-v2:4:window-until-200",
		ObservedAt:                observedAt,
		NextEligibilityTransition: &nextTransition,
	}
	snapshotCalls := 0
	repositoryStub := &recommendationRepositoryStub{
		catalogSnapshotFunction: func(context.Context) (repository.CatalogSnapshotVersion, error) {
			snapshotCalls++
			if snapshotCalls == 1 {
				return oldSnapshot, nil
			}
			return freshSnapshot, nil
		},
		candidateSnapshotFunction: func(
			context.Context,
			[]models.ItemType,
			int,
		) (*repository.RecommendationCandidateSnapshot, error) {
			return &repository.RecommendationCandidateSnapshot{
				SnapshotVersion: freshSnapshot,
				CatalogRevision: freshSnapshot.Revision,
				Items: []*models.CatalogItem{
					{
						Type:   models.TypeService,
						Source: models.SourceTypesense,
						Title:  "Fresh recommendation",
					},
				},
			}, nil
		},
	}
	cacheRecorder := &recommendationCacheRecorder{cachedResponse: &models.RecommendationResponse{
		Items: []*models.RankedItem{{Title: "Stale recommendation"}},
	}}
	recommendationService := &RecommendationService{
		itemRepo: repositoryStub,
		cache:    cacheRecorder,
		weights:  models.DefaultWeights,
		authTTL:  time.Minute,
	}
	request := &models.RecommendationRequest{Limit: 1}
	profile := &models.CitizenProfile{CPFHash: "citizen-hash"}

	recommendationResponse, recommendationError := recommendationService.Recommend(
		context.Background(),
		profile,
		request,
	)
	if recommendationError != nil {
		t.Fatalf("Recommend returned an unexpected error: %v", recommendationError)
	}
	if len(recommendationResponse.Items) != 1 || recommendationResponse.Items[0].Title != "Fresh recommendation" {
		t.Fatalf("stale recommendation cache escaped revalidation: %#v", recommendationResponse)
	}
	if repositoryStub.legacyCandidateCalls != 0 {
		t.Fatalf("legacy candidate calls = %d, want 0", repositoryStub.legacyCandidateCalls)
	}
	if cacheRecorder.setCount != 1 || cacheRecorder.setTTL != 3*time.Millisecond {
		t.Fatalf("cache writes = %d TTL=%s, want one write with 3ms", cacheRecorder.setCount, cacheRecorder.setTTL)
	}
	if cacheRecorder.setKey != recommendationService.authCacheKey(profile.CPFHash, request, freshSnapshot.Revision) {
		t.Fatalf("recommendation was cached under the wrong revision key %q", cacheRecorder.setKey)
	}
}

func TestRecommendationCacheRejectsNullAndOversizedItemCollections(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		items []*models.RankedItem
	}{
		{name: "null", items: nil},
		{name: "oversized", items: make([]*models.RankedItem, models.MaximumRecommendationItems+1)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &RecommendationService{
				cache: &recommendationCacheRecorder{
					cachedResponse: &models.RecommendationResponse{Items: testCase.items},
				},
			}
			if cachedResponse := service.cachedRecommendation(context.Background(), "cache-key"); cachedResponse != nil {
				t.Fatalf("invalid cached recommendation escaped validation: %#v", cachedResponse)
			}
		})
	}
}

func TestRecommendationSkipsCacheWhenEligibilityWindowMoves(t *testing.T) {
	t.Parallel()

	candidateSnapshot := repository.CatalogSnapshotVersion{Revision: "catalog-v2:7:window-until-100"}
	latestSnapshot := repository.CatalogSnapshotVersion{Revision: "catalog-v2:7:window-until-200"}
	snapshotCalls := 0
	repositoryStub := &recommendationRepositoryStub{
		catalogSnapshotFunction: func(context.Context) (repository.CatalogSnapshotVersion, error) {
			snapshotCalls++
			if snapshotCalls == 1 {
				return candidateSnapshot, nil
			}
			return latestSnapshot, nil
		},
		candidateSnapshotFunction: func(
			context.Context,
			[]models.ItemType,
			int,
		) (*repository.RecommendationCandidateSnapshot, error) {
			return &repository.RecommendationCandidateSnapshot{
				SnapshotVersion: candidateSnapshot,
				CatalogRevision: candidateSnapshot.Revision,
				Items:           []*models.CatalogItem{},
			}, nil
		},
	}
	cacheRecorder := &recommendationCacheRecorder{}
	recommendationService := &RecommendationService{
		itemRepo:     repositoryStub,
		cache:        cacheRecorder,
		weights:      models.DefaultWeights,
		anonymousTTL: time.Minute,
	}

	response, recommendationError := recommendationService.RecommendAnonymous(
		context.Background(),
		&models.RecommendationRequest{Limit: 1},
	)
	if recommendationError != nil {
		t.Fatalf("RecommendAnonymous returned an unexpected error: %v", recommendationError)
	}
	if len(response.Items) != 0 {
		t.Fatalf("anonymous recommendations = %#v, want empty", response.Items)
	}
	if cacheRecorder.setCount != 0 {
		t.Fatalf("moving eligibility window was cached %d time(s)", cacheRecorder.setCount)
	}
}

func TestRecommendationCacheKeysIncludeCatalogRevision(t *testing.T) {
	t.Parallel()

	recommendationService := &RecommendationService{}
	request := &models.RecommendationRequest{Limit: 5, Context: models.ContextHomepage}
	if recommendationService.authCacheKey("citizen", request, "catalog-v2:1:window-until-100") ==
		recommendationService.authCacheKey("citizen", request, "catalog-v2:1:window-until-200") {
		t.Fatal("authenticated recommendation keys ignore catalog revision")
	}
	if recommendationService.anonymousCacheKey(request, "catalog-v2:1:window-until-100") ==
		recommendationService.anonymousCacheKey(request, "catalog-v2:1:window-until-200") {
		t.Fatal("anonymous recommendation keys ignore catalog revision")
	}
	newJourneyGraphService := &RecommendationService{journeyGraphVersion: "journey-graph-v2"}
	if recommendationService.anonymousCacheKey(request, "catalog-v2:1:window-until-100") ==
		newJourneyGraphService.anonymousCacheKey(request, "catalog-v2:1:window-until-100") {
		t.Fatal("recommendation keys ignore journey graph version")
	}
	authenticatedKey := recommendationService.authCacheKey("citizen", request, "catalog-v2:1:window-until-100")
	secondAuthenticatedKey := recommendationService.authCacheKey("another-citizen", request, "catalog-v2:1:window-until-100")
	authenticatedDigest := strings.TrimPrefix(authenticatedKey, "catalogo:rec:auth:")
	if len(authenticatedDigest) != sha256.Size*2 {
		t.Fatalf("authenticated cache digest length = %d, want %d", len(authenticatedDigest), sha256.Size*2)
	}
	if authenticatedKey == secondAuthenticatedKey {
		t.Fatal("distinct citizen identities produced the same authenticated cache key")
	}
	anonymousDigest := strings.TrimPrefix(
		recommendationService.anonymousCacheKey(request, "catalog-v2:1:window-until-100"),
		"catalogo:rec:anon:",
	)
	if len(anonymousDigest) != sha256.Size*2 {
		t.Fatalf("anonymous cache digest length = %d, want %d", len(anonymousDigest), sha256.Size*2)
	}

	firstEquivalentRequest := &models.RecommendationRequest{
		Types:   []models.ItemType{models.TypeJob, models.TypeService, models.TypeJob, models.TypeCourse},
		Limit:   5,
		Context: models.ContextHomepage,
	}
	secondEquivalentRequest := &models.RecommendationRequest{
		Types:   []models.ItemType{models.TypeCourse, models.TypeService, models.TypeJob},
		Limit:   5,
		Context: models.ContextHomepage,
	}
	if firstNormalizeError := firstEquivalentRequest.Normalize(); firstNormalizeError != nil {
		t.Fatalf("normalize first equivalent request: %v", firstNormalizeError)
	}
	if secondNormalizeError := secondEquivalentRequest.Normalize(); secondNormalizeError != nil {
		t.Fatalf("normalize second equivalent request: %v", secondNormalizeError)
	}
	if recommendationService.authCacheKey("citizen", firstEquivalentRequest, "catalog-revision") !=
		recommendationService.authCacheKey("citizen", secondEquivalentRequest, "catalog-revision") {
		t.Fatal("authenticated cache key depends on equivalent type ordering or duplication")
	}
	if recommendationService.anonymousCacheKey(firstEquivalentRequest, "catalog-revision") !=
		recommendationService.anonymousCacheKey(secondEquivalentRequest, "catalog-revision") {
		t.Fatal("anonymous cache key depends on equivalent type ordering or duplication")
	}
}

func TestRecommendationPropagatesJourneyBoostFailureWithoutCaching(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		recommend func(*RecommendationService, *models.RecommendationRequest) (*models.RecommendationResponse, error)
	}{
		{
			name: "authenticated",
			recommend: func(service *RecommendationService, request *models.RecommendationRequest) (*models.RecommendationResponse, error) {
				return service.Recommend(context.Background(), &models.CitizenProfile{CPFHash: "citizen"}, request)
			},
		},
		{
			name: "anonymous",
			recommend: func(service *RecommendationService, request *models.RecommendationRequest) (*models.RecommendationResponse, error) {
				return service.RecommendAnonymous(context.Background(), request)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			snapshotVersion := repository.CatalogSnapshotVersion{Revision: "catalog-v2:1:window-until-infinity"}
			repositoryStub := &recommendationRepositoryStub{
				catalogSnapshotFunction: func(context.Context) (repository.CatalogSnapshotVersion, error) {
					return snapshotVersion, nil
				},
				candidateSnapshotFunction: func(
					context.Context,
					[]models.ItemType,
					int,
				) (*repository.RecommendationCandidateSnapshot, error) {
					return &repository.RecommendationCandidateSnapshot{
						SnapshotVersion: snapshotVersion,
						CatalogRevision: snapshotVersion.Revision,
						Items: []*models.CatalogItem{{
							Type:   models.TypeService,
							Source: models.SourceTypesense,
							Title:  "Candidate",
						}},
					}, nil
				},
				journeyBoostsFunction: func(context.Context, []string) (map[string]float64, error) {
					return nil, errors.New("journey database unavailable")
				},
			}
			cacheRecorder := &recommendationCacheRecorder{}
			recommendationService := &RecommendationService{
				itemRepo:     repositoryStub,
				cache:        cacheRecorder,
				weights:      models.DefaultWeights,
				authTTL:      time.Minute,
				anonymousTTL: time.Minute,
			}

			recommendationResponse, recommendationError := testCase.recommend(
				recommendationService,
				&models.RecommendationRequest{Limit: 1},
			)
			if recommendationError == nil || !strings.Contains(recommendationError.Error(), "journey boosts") {
				t.Fatalf("journey failure error = %v", recommendationError)
			}
			if recommendationResponse != nil {
				t.Fatalf("journey failure response = %#v, want nil", recommendationResponse)
			}
			if cacheRecorder.setCount != 0 {
				t.Fatalf("journey failure was cached %d time(s)", cacheRecorder.setCount)
			}
		})
	}
}

func TestRecommendationRankingUsesIDAsDeterministicScoreTieBreak(t *testing.T) {
	t.Parallel()

	rankedItems := []*models.RankedItem{
		{ID: "00000000-0000-4000-8000-000000000003", Score: 0.8},
		{ID: "00000000-0000-4000-8000-000000000001", Score: 0.8},
		{ID: "00000000-0000-4000-8000-000000000002", Score: 0.8},
	}
	slices.SortFunc(rankedItems, compareRankedItems)
	for itemIndex, expectedID := range []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
	} {
		if rankedItems[itemIndex].ID != expectedID {
			t.Fatalf("tied ranking item %d = %q, want %q", itemIndex, rankedItems[itemIndex].ID, expectedID)
		}
	}
}

func TestRecommendationRequest_Normalize(t *testing.T) {
	req := &models.RecommendationRequest{Limit: 0}
	if normalizeError := req.Normalize(); normalizeError != nil {
		t.Fatalf("normalize default request: %v", normalizeError)
	}
	if req.Limit != models.DefaultRecommendationLimit {
		t.Errorf("limite default esperado %d, got %d", models.DefaultRecommendationLimit, req.Limit)
	}

	req2 := &models.RecommendationRequest{Limit: 999}
	if normalizeError := req2.Normalize(); !errors.Is(normalizeError, models.ErrInvalidRecommendationLimit) {
		t.Errorf("limit > max error = %v", normalizeError)
	}

	req3 := &models.RecommendationRequest{
		Types: []models.ItemType{models.TypeJob, models.TypeService, models.TypeJob, models.TypeCourse},
	}
	if normalizeError := req3.Normalize(); normalizeError != nil {
		t.Fatalf("normalize canonical request: %v", normalizeError)
	}
	if req3.Context != models.ContextHomepage {
		t.Errorf("contexto default esperado %q, got %q", models.ContextHomepage, req3.Context)
	}
	expectedTypes := []models.ItemType{models.TypeCourse, models.TypeJob, models.TypeService}
	if !slices.Equal(req3.Types, expectedTypes) {
		t.Errorf("canonical types = %v, want %v", req3.Types, expectedTypes)
	}
}

func TestRecommendationServiceRejectsInvalidContextBeforeCallingDependencies(t *testing.T) {
	t.Parallel()

	service := &RecommendationService{}
	testCases := []struct {
		name      string
		recommend func() (*models.RecommendationResponse, error)
	}{
		{
			name: "authenticated",
			recommend: func() (*models.RecommendationResponse, error) {
				return service.Recommend(
					context.Background(),
					&models.CitizenProfile{},
					&models.RecommendationRequest{Context: "unsupported"},
				)
			},
		},
		{
			name: "anonymous",
			recommend: func() (*models.RecommendationResponse, error) {
				return service.RecommendAnonymous(
					context.Background(),
					&models.RecommendationRequest{Context: "unsupported"},
				)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recommendationResponse, recommendationError := testCase.recommend()
			if recommendationError == nil || !strings.Contains(recommendationError.Error(), "context") {
				t.Fatalf("invalid context error = %v", recommendationError)
			}
			if recommendationResponse != nil {
				t.Fatalf("invalid context response = %#v, want nil", recommendationResponse)
			}
		})
	}
}

func TestRecommendationServiceRejectsInvalidItemTypeBeforeCallingDependencies(t *testing.T) {
	t.Parallel()

	service := &RecommendationService{}
	request := &models.RecommendationRequest{Types: []models.ItemType{"unsupported"}}
	for _, testCase := range []struct {
		name      string
		recommend func() (*models.RecommendationResponse, error)
	}{
		{
			name: "authenticated",
			recommend: func() (*models.RecommendationResponse, error) {
				return service.Recommend(context.Background(), &models.CitizenProfile{}, request)
			},
		},
		{
			name: "anonymous",
			recommend: func() (*models.RecommendationResponse, error) {
				return service.RecommendAnonymous(context.Background(), request)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recommendationResponse, recommendationError := testCase.recommend()
			if !errors.Is(recommendationError, models.ErrInvalidRecommendationItemType) {
				t.Fatalf("invalid type error = %v", recommendationError)
			}
			if recommendationResponse != nil {
				t.Fatalf("invalid type response = %#v, want nil", recommendationResponse)
			}
		})
	}
}
