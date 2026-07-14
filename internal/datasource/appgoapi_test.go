package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type appGoAPIClientStub struct {
	courses      []clients.Course
	jobs         []clients.Job
	mei          []clients.MEIOpportunity
	coursePages  map[int][]clients.Course
	jobPages     map[int][]clients.Job
	meiPages     map[int][]clients.MEIOpportunity
	courseTotals map[int]int
	jobTotals    map[int]int
	meiTotals    map[int]int
	coursesErr   error
	jobsErr      error
	meiErr       error
	courseCalls  int
	jobCalls     int
	meiCalls     int
}

func (client *appGoAPIClientStub) GetCourses(_ context.Context, page int, _ time.Time) ([]clients.Course, int, error) {
	client.courseCalls++
	if client.coursePages != nil {
		return client.coursePages[page], client.courseTotals[page], client.coursesErr
	}
	return client.courses, len(client.courses), client.coursesErr
}

func (client *appGoAPIClientStub) GetJobs(_ context.Context, page int, _ time.Time) ([]clients.Job, int, error) {
	client.jobCalls++
	if client.jobPages != nil {
		return client.jobPages[page], client.jobTotals[page], client.jobsErr
	}
	return client.jobs, len(client.jobs), client.jobsErr
}

func (client *appGoAPIClientStub) GetMEIOpportunities(
	_ context.Context,
	page int,
	_ time.Time,
) ([]clients.MEIOpportunity, int, error) {
	client.meiCalls++
	if client.meiPages != nil {
		return client.meiPages[page], client.meiTotals[page], client.meiErr
	}
	return client.mei, len(client.mei), client.meiErr
}

type appGoAPIRepositoryStub struct {
	items               []*models.CatalogItem
	calls               []appGoAPIReconciliationCall
	deactivatedBySource map[models.ItemSource]int
	reconciliationError error
	leaseError          error
	leasedSources       []models.ItemSource
}

type appGoAPIReconciliationCall struct {
	source                  models.ItemSource
	expectedItemCount       int
	sourceUpdatedUpperBound *time.Time
	snapshotStartedAt       time.Time
	items                   []*models.CatalogItem
}

func (repository *appGoAPIRepositoryStub) WithSourceSyncLease(
	ctx context.Context,
	source models.ItemSource,
	operation func(context.Context) (int, error),
) (int, error) {
	repository.leasedSources = append(repository.leasedSources, source)
	if repository.leaseError != nil {
		return 0, repository.leaseError
	}
	return operation(ctx)
}

func (repository *appGoAPIRepositoryStub) ReconcileSourceSnapshot(
	_ context.Context,
	source models.ItemSource,
	items []*models.CatalogItem,
	expectedItemCount int,
	sourceUpdatedUpperBound *time.Time,
	snapshotStartedAt time.Time,
) (int, int, error) {
	repository.calls = append(repository.calls, appGoAPIReconciliationCall{
		source:                  source,
		expectedItemCount:       expectedItemCount,
		sourceUpdatedUpperBound: sourceUpdatedUpperBound,
		snapshotStartedAt:       snapshotStartedAt,
		items:                   append([]*models.CatalogItem(nil), items...),
	})
	if repository.reconciliationError != nil {
		return 0, 0, repository.reconciliationError
	}
	repository.items = append(repository.items, items...)
	return len(items), repository.deactivatedBySource[source], nil
}

func TestMapJobPreservesSlugInSourceData(t *testing.T) {
	job := clients.Job{
		ID:          "42",
		Slug:        "analista-de-dados-42",
		Title:       "Analista de dados",
		Description: "Vaga para analista de dados",
		UpdatedAt:   time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
	}

	item := mapJob(job)

	var sourceData struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(item.SourceData, &sourceData); err != nil {
		t.Fatalf("source_data inválido: %v", err)
	}
	if sourceData.Slug != job.Slug {
		t.Fatalf("slug em source_data = %q, esperava %q", sourceData.Slug, job.Slug)
	}
}

func TestMapJobOmitsEmptySlugFromSourceData(t *testing.T) {
	job := clients.Job{
		ID:          "43",
		Title:       "Vaga sem slug",
		Description: "Vaga ainda sem slug atribuído",
		UpdatedAt:   time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
	}

	item := mapJob(job)

	var sourceData map[string]json.RawMessage
	if err := json.Unmarshal(item.SourceData, &sourceData); err != nil {
		t.Fatalf("source_data inválido: %v", err)
	}
	if _, present := sourceData["slug"]; present {
		t.Fatalf("source_data não deveria conter a chave \"slug\" quando o slug é vazio")
	}
}

func TestAppGoAPISyncPropagatesPartialVerticalFailure(t *testing.T) {
	jobsError := errors.New("jobs unavailable")
	updatedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	client := &appGoAPIClientStub{
		courses: []clients.Course{{
			ID:        "course-1",
			Title:     "Curso",
			IsVisible: true,
			Status:    "published",
			UpdatedAt: updatedAt,
		}},
		jobsErr: jobsError,
		mei: []clients.MEIOpportunity{{
			ID:        "mei-1",
			Title:     "Oportunidade",
			Status:    "active",
			UpdatedAt: updatedAt,
		}},
	}
	repository := &appGoAPIRepositoryStub{}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 2 {
		t.Fatalf("Sync changed = %d, want 2 successful vertical changes", changed)
	}
	if !errors.Is(syncError, jobsError) {
		t.Fatalf("Sync error = %v, want wrapped jobs error", syncError)
	}
	if len(repository.items) != 2 {
		t.Fatalf("persisted items = %d, want 2", len(repository.items))
	}
}

func TestAppGoAPIStatusMappingFailsClosed(t *testing.T) {
	testCases := []struct {
		name       string
		got        models.ItemStatus
		wantStatus models.ItemStatus
	}{
		{name: "visible published course", got: mapCourseStatus(clients.Course{IsVisible: true, Status: "published"}), wantStatus: models.StatusActive},
		{name: "hidden published course", got: mapCourseStatus(clients.Course{IsVisible: false, Status: "published"}), wantStatus: models.StatusInactive},
		{name: "canceled course", got: mapCourseStatus(clients.Course{IsVisible: true, Status: "canceled"}), wantStatus: models.StatusInactive},
		{name: "active job", got: mapJobStatus("publicado_ativo"), wantStatus: models.StatusActive},
		{name: "expired job", got: mapJobStatus("publicado_expirado"), wantStatus: models.StatusInactive},
		{name: "unknown job", got: mapJobStatus("unexpected"), wantStatus: models.StatusInactive},
		{name: "active mei", got: mapMEIStatus("active"), wantStatus: models.StatusActive},
		{name: "expired mei", got: mapMEIStatus("expired"), wantStatus: models.StatusInactive},
		{name: "unknown mei", got: mapMEIStatus("unexpected"), wantStatus: models.StatusInactive},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.wantStatus {
				t.Fatalf("status = %q, want %q", testCase.got, testCase.wantStatus)
			}
		})
	}
}

func TestMapJobUsesCanonicalPCDAudienceAndValidity(t *testing.T) {
	validUntil := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	job := clients.Job{
		ID:                "job-1",
		Title:             "Vaga inclusiva",
		Status:            "publicado_ativo",
		AcessibilidadePCD: "exclusivo_pcd",
		DataLimite:        &validUntil,
		UpdatedAt:         time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC),
	}

	item := mapJob(job)

	var audience models.TargetAudienceData
	if unmarshalError := json.Unmarshal(item.TargetAudience, &audience); unmarshalError != nil {
		t.Fatalf("target_audience inválido: %v", unmarshalError)
	}
	if len(audience.Deficiencia) != 1 || audience.Deficiencia[0] != "exclusivo_pcd" {
		t.Fatalf("deficiencia = %v, want [exclusivo_pcd]", audience.Deficiencia)
	}
	if item.ValidUntil == nil || !item.ValidUntil.Equal(validUntil) {
		t.Fatalf("valid_until = %v, want %s", item.ValidUntil, validUntil)
	}
}

func TestShortDescriptionTruncationPreservesUTF8(t *testing.T) {
	description := strings.Repeat("á", maximumShortDescription+1)
	truncated := truncateByRunes(description, maximumShortDescription)

	if !utf8.ValidString(truncated) {
		t.Fatal("truncated description is not valid UTF-8")
	}
	if utf8.RuneCountInString(truncated) != maximumShortDescription {
		t.Fatalf(
			"truncated rune count = %d, want %d",
			utf8.RuneCountInString(truncated),
			maximumShortDescription,
		)
	}
}

func TestAppGoAPISyncRejectsDuplicateExternalIDsBeforePersisting(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	client := &appGoAPIClientStub{courses: []clients.Course{
		{ID: "duplicate", Title: "Primeiro curso", IsVisible: true, Status: "published", UpdatedAt: updatedAt},
		{ID: "duplicate", Title: "Segundo curso", IsVisible: true, Status: "published", UpdatedAt: updatedAt},
	}}
	repository := &appGoAPIRepositoryStub{}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || syncError == nil || !strings.Contains(syncError.Error(), "duplicate external id") {
		t.Fatalf("Sync = %d, %v, want duplicate id failure", changed, syncError)
	}
	if len(repository.items) != 0 {
		t.Fatalf("invalid batch persisted %d items", len(repository.items))
	}
}

func TestAppGoAPISyncReconcilesOnlyAfterCompleteStablePagination(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	client := &appGoAPIClientStub{
		coursePages: map[int][]clients.Course{
			1: {{ID: "course-1", Title: "First course", IsVisible: true, Status: "published", UpdatedAt: updatedAt}},
			2: {{ID: "course-2", Title: "Second course", IsVisible: true, Status: "published", UpdatedAt: updatedAt}},
		},
		courseTotals: map[int]int{1: 2, 2: 2},
	}
	repository := &appGoAPIRepositoryStub{}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if changed != 2 {
		t.Fatalf("Sync changed = %d, want 2", changed)
	}
	courseCall, found := findAppGoAPIReconciliationCall(repository.calls, models.SourceCourses)
	if !found {
		t.Fatal("complete course snapshot was not reconciled")
	}
	if courseCall.expectedItemCount != 2 || len(courseCall.items) != 2 {
		t.Fatalf(
			"course reconciliation = expected %d, received %d; want 2, 2",
			courseCall.expectedItemCount,
			len(courseCall.items),
		)
	}
	if courseCall.sourceUpdatedUpperBound == nil || courseCall.sourceUpdatedUpperBound.Before(updatedAt) {
		t.Fatalf("course snapshot upper bound = %v, want at least %s", courseCall.sourceUpdatedUpperBound, updatedAt)
	}
}

func TestAppGoAPISyncRejectsTruncatedSnapshotBeforeReconciliation(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	client := &appGoAPIClientStub{
		coursePages: map[int][]clients.Course{
			1: {{ID: "course-1", Title: "Only course", IsVisible: true, Status: "published", UpdatedAt: updatedAt}},
			2: {},
		},
		courseTotals: map[int]int{1: 2, 2: 2},
	}
	repository := &appGoAPIRepositoryStub{}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || syncError == nil || !strings.Contains(syncError.Error(), "ended after 1 of 2 items") {
		t.Fatalf("Sync = %d, %v; want truncated snapshot failure", changed, syncError)
	}
	if _, found := findAppGoAPIReconciliationCall(repository.calls, models.SourceCourses); found {
		t.Fatal("truncated course snapshot reached reconciliation")
	}
}

func TestAppGoAPISyncRejectsPaginationTotalChangeBeforeReconciliation(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	client := &appGoAPIClientStub{
		jobPages: map[int][]clients.Job{
			1: {{ID: "job-1", Title: "First job", Status: "publicado_ativo", UpdatedAt: updatedAt}},
			2: {{ID: "job-2", Title: "Second job", Status: "publicado_ativo", UpdatedAt: updatedAt}},
		},
		jobTotals: map[int]int{1: 2, 2: 3},
	}
	repository := &appGoAPIRepositoryStub{}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || syncError == nil || !strings.Contains(syncError.Error(), "total changed from 2 to 3") {
		t.Fatalf("Sync = %d, %v; want unstable total failure", changed, syncError)
	}
	if _, found := findAppGoAPIReconciliationCall(repository.calls, models.SourceJobs); found {
		t.Fatal("unstable job snapshot reached reconciliation")
	}
}

func TestAppGoAPISyncCountsSoftDeactivationsAsCatalogChanges(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	client := &appGoAPIClientStub{courses: []clients.Course{{
		ID:        "course-1",
		Title:     "Retained course",
		IsVisible: true,
		Status:    "published",
		UpdatedAt: updatedAt,
	}}}
	repository := &appGoAPIRepositoryStub{
		deactivatedBySource: map[models.ItemSource]int{models.SourceCourses: 2},
	}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if changed != 3 {
		t.Fatalf("Sync changed = %d, want one upsert plus two soft-deactivations", changed)
	}
}

func TestAppGoAPISyncTreatsExplicitZeroTotalsAsBoundedCompleteSnapshots(t *testing.T) {
	repository := &appGoAPIRepositoryStub{}
	source := NewAppGoAPIDataSource(&appGoAPIClientStub{}, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if changed != 0 {
		t.Fatalf("Sync changed = %d, want 0", changed)
	}
	if len(repository.calls) != 3 {
		t.Fatalf("empty vertical reconciliation calls = %d, want 3", len(repository.calls))
	}
	expectedLeasedSources := []models.ItemSource{models.SourceCourses, models.SourceJobs, models.SourceMEI}
	if len(repository.leasedSources) != len(expectedLeasedSources) {
		t.Fatalf("leased sources = %v, want %v", repository.leasedSources, expectedLeasedSources)
	}
	for sourceIndex, expectedSource := range expectedLeasedSources {
		if repository.leasedSources[sourceIndex] != expectedSource {
			t.Fatalf("leased sources = %v, want %v", repository.leasedSources, expectedLeasedSources)
		}
	}
	for _, reconciliationCall := range repository.calls {
		if reconciliationCall.expectedItemCount != 0 || len(reconciliationCall.items) != 0 {
			t.Fatalf("empty %q snapshot had expected=%d items=%d", reconciliationCall.source, reconciliationCall.expectedItemCount, len(reconciliationCall.items))
		}
		if reconciliationCall.sourceUpdatedUpperBound == nil || reconciliationCall.sourceUpdatedUpperBound.IsZero() {
			t.Fatalf("empty %q snapshot has no concurrency boundary", reconciliationCall.source)
		}
		if reconciliationCall.snapshotStartedAt.IsZero() {
			t.Fatalf("empty %q snapshot has no start boundary", reconciliationCall.source)
		}
	}
}

func TestAppGoAPISyncDoesNotFetchWithoutGlobalVerticalLeases(t *testing.T) {
	leaseError := errors.New("lease unavailable")
	client := &appGoAPIClientStub{}
	repository := &appGoAPIRepositoryStub{leaseError: leaseError}
	source := NewAppGoAPIDataSource(client, repository, time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || syncError == nil || !errors.Is(syncError, leaseError) {
		t.Fatalf("Sync = %d, %v; want lease failures", changed, syncError)
	}
	if client.courseCalls != 0 || client.jobCalls != 0 || client.meiCalls != 0 {
		t.Fatalf(
			"app-go-api fetched without leases: courses=%d jobs=%d mei=%d",
			client.courseCalls,
			client.jobCalls,
			client.meiCalls,
		)
	}
}

func findAppGoAPIReconciliationCall(
	calls []appGoAPIReconciliationCall,
	source models.ItemSource,
) (appGoAPIReconciliationCall, bool) {
	for _, reconciliationCall := range calls {
		if reconciliationCall.source == source {
			return reconciliationCall, true
		}
	}
	return appGoAPIReconciliationCall{}, false
}
