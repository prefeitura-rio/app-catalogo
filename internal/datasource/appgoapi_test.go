package datasource

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestCourseIsIndexable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		visible bool
		want    bool
	}{
		{"published visible", "published", true, true},
		{"approved visible", "approved", true, true},
		{"opened visible", "opened", true, true},
		{"empty status visible", "", true, true},
		{"scheduled derived", "scheduled", true, true},
		{"accepting_enrollments derived", "accepting_enrollments", true, true},
		{"in_progress derived", "in_progress", true, true},
		{"finished terminal", "finished", true, false},
		{"draft", "draft", true, false},
		{"canceled", "canceled", true, false},
		{"published invisible", "published", false, false},
		{"accepting invisible", "accepting_enrollments", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := courseIsIndexable(clients.Course{
				Status:    tt.status,
				IsVisible: tt.visible,
			})
			if got != tt.want {
				t.Fatalf("courseIsIndexable(status=%q, visible=%v) = %v, want %v",
					tt.status, tt.visible, got, tt.want)
			}
		})
	}
}

func TestJobIsIndexable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   bool
	}{
		{"publicado_ativo", true},
		{"", true},
		{"publicado_expirado", false},
		{"vaga_congelada", false},
		{"vaga_descontinuada", false},
		{"em_edicao", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			got := jobIsIndexable(clients.Job{Status: tt.status})
			if got != tt.want {
				t.Fatalf("jobIsIndexable(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestMEIIsIndexable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   bool
	}{
		{"active", true},
		{"", true},
		{"draft", false},
		{"expired", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			got := meiIsIndexable(clients.MEIOpportunity{Status: tt.status})
			if got != tt.want {
				t.Fatalf("meiIsIndexable(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

// --- stubs ------------------------------------------------------------------

type stubAppGoAPIFetcher struct {
	courses    []clients.Course
	coursesTot int
	coursesErr error

	jobs    []clients.Job
	jobsTot int
	jobsErr error

	mei    []clients.MEIOpportunity
	meiTot int
	meiErr error

	coursePages int
	jobPages    int
	meiPages    int
}

func (s *stubAppGoAPIFetcher) GetCourses(_ context.Context, page int, _ time.Time) ([]clients.Course, int, error) {
	s.coursePages++
	if s.coursesErr != nil {
		return nil, 0, s.coursesErr
	}
	if page > 1 {
		return nil, s.coursesTot, nil
	}
	tot := s.coursesTot
	if tot == 0 {
		tot = len(s.courses)
	}
	return s.courses, tot, nil
}

func (s *stubAppGoAPIFetcher) GetJobs(_ context.Context, page int, _ time.Time) ([]clients.Job, int, error) {
	s.jobPages++
	if s.jobsErr != nil {
		return nil, 0, s.jobsErr
	}
	if page > 1 {
		return nil, s.jobsTot, nil
	}
	tot := s.jobsTot
	if tot == 0 {
		tot = len(s.jobs)
	}
	return s.jobs, tot, nil
}

func (s *stubAppGoAPIFetcher) GetMEIOpportunities(_ context.Context, page int, _ time.Time) ([]clients.MEIOpportunity, int, error) {
	s.meiPages++
	if s.meiErr != nil {
		return nil, 0, s.meiErr
	}
	if page > 1 {
		return nil, s.meiTot, nil
	}
	tot := s.meiTot
	if tot == 0 {
		tot = len(s.mei)
	}
	return s.mei, tot, nil
}

type stubAppGoAPIRepo struct {
	upserted    []*models.CatalogItem
	upsertErr   error
	orphanKept  []string
	orphanSrc   models.ItemSource
	orphanCount int64
	orphanErr   error
	orphanCalls int
}

func (r *stubAppGoAPIRepo) UpsertBatch(_ context.Context, items []*models.CatalogItem) (int, error) {
	if r.upsertErr != nil {
		return 0, r.upsertErr
	}
	r.upserted = append(r.upserted, items...)
	return len(items), nil
}

func (r *stubAppGoAPIRepo) SoftDeleteActiveNotIn(_ context.Context, source models.ItemSource, keepExternalIDs []string) (int64, error) {
	r.orphanCalls++
	r.orphanSrc = source
	r.orphanKept = append([]string{}, keepExternalIDs...)
	if r.orphanErr != nil {
		return 0, r.orphanErr
	}
	return r.orphanCount, nil
}

func TestSyncCourses_FiltersAndOrphanDelete(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		courses: []clients.Course{
			{ID: "1", Title: "Keep published", Status: "published", IsVisible: true},
			{ID: "2", Title: "Keep accepting", Status: "accepting_enrollments", IsVisible: true},
			{ID: "3", Title: "Skip finished", Status: "finished", IsVisible: true},
			{ID: "4", Title: "Skip invisible", Status: "published", IsVisible: false},
		},
	}
	repo := &stubAppGoAPIRepo{orphanCount: 2}
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.syncCourses(context.Background()); err != nil {
		t.Fatalf("syncCourses: %v", err)
	}

	if len(repo.upserted) != 2 {
		t.Fatalf("upserted=%d want 2", len(repo.upserted))
	}
	gotIDs := []string{repo.upserted[0].ExternalID, repo.upserted[1].ExternalID}
	if gotIDs[0] != "1" || gotIDs[1] != "2" {
		t.Fatalf("upserted ids=%v want [1 2]", gotIDs)
	}
	if repo.orphanSrc != models.SourceCourses {
		t.Fatalf("orphan source=%s", repo.orphanSrc)
	}
	if len(repo.orphanKept) != 2 || repo.orphanKept[0] != "1" || repo.orphanKept[1] != "2" {
		t.Fatalf("orphan keep=%v want [1 2]", repo.orphanKept)
	}
	if repo.orphanCalls != 1 {
		t.Fatalf("orphanCalls=%d", repo.orphanCalls)
	}
}

func TestSyncJobs_FiltersAndOrphanDelete(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		jobs: []clients.Job{
			{ID: "j-active", Title: "Ativa", Status: "publicado_ativo"},
			{ID: "j-expired", Title: "Expirada", Status: "publicado_expirado"},
			{ID: "j-frozen", Title: "Congelada", Status: "vaga_congelada"},
		},
	}
	repo := &stubAppGoAPIRepo{orphanCount: 1}
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.syncJobs(context.Background()); err != nil {
		t.Fatalf("syncJobs: %v", err)
	}

	if len(repo.upserted) != 1 || repo.upserted[0].ExternalID != "j-active" {
		t.Fatalf("upserted=%v", repo.upserted)
	}
	if repo.orphanSrc != models.SourceJobs {
		t.Fatalf("orphan source=%s", repo.orphanSrc)
	}
	if len(repo.orphanKept) != 1 || repo.orphanKept[0] != "j-active" {
		t.Fatalf("orphan keep=%v", repo.orphanKept)
	}
}

func TestSyncMEI_FiltersAndOrphanDelete(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		mei: []clients.MEIOpportunity{
			{ID: "m1", Title: "Ativa", Status: "active"},
			{ID: "m2", Title: "Expirada", Status: "expired"},
			{ID: "m3", Title: "Draft", Status: "draft"},
		},
	}
	repo := &stubAppGoAPIRepo{}
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.syncMEI(context.Background()); err != nil {
		t.Fatalf("syncMEI: %v", err)
	}

	if len(repo.upserted) != 1 || repo.upserted[0].ExternalID != "m1" {
		t.Fatalf("upserted=%v", repo.upserted)
	}
	if repo.orphanSrc != models.SourceMEI || len(repo.orphanKept) != 1 || repo.orphanKept[0] != "m1" {
		t.Fatalf("orphan src=%s keep=%v", repo.orphanSrc, repo.orphanKept)
	}
}

func TestSyncCourses_SkipsOrphanDeleteOnUpsertError(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		courses: []clients.Course{
			{ID: "1", Title: "Ok", Status: "published", IsVisible: true},
		},
	}
	repo := &stubAppGoAPIRepo{upsertErr: errors.New("db down")}
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	err := ds.syncCourses(context.Background())
	if err == nil {
		t.Fatal("expected upsert error")
	}
	if repo.orphanCalls != 0 {
		t.Fatalf("SoftDeleteActiveNotIn must not run after upsert failure, calls=%d", repo.orphanCalls)
	}
}

func TestSyncCourses_PropagatesFetchError(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{coursesErr: errors.New("upstream 500")}
	repo := &stubAppGoAPIRepo{}
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.syncCourses(context.Background()); err == nil {
		t.Fatal("expected fetch error")
	}
	if len(repo.upserted) != 0 || repo.orphanCalls != 0 {
		t.Fatalf("no writes on fetch error: upserted=%d orphans=%d", len(repo.upserted), repo.orphanCalls)
	}
}

func TestSync_JoinsPartialErrors(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		coursesErr: errors.New("courses down"),
		jobs:       []clients.Job{{ID: "j1", Status: "publicado_ativo", Title: "ok"}},
		meiErr:     errors.New("mei down"),
	}
	repo := &stubAppGoAPIRepo{}
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	err := ds.Sync(context.Background())
	if err == nil {
		t.Fatal("expected joined error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cursos") || !strings.Contains(msg, "mei") {
		t.Fatalf("joined error missing parts: %s", msg)
	}
	// jobs should still have succeeded
	if len(repo.upserted) != 1 {
		t.Fatalf("jobs upserted=%d want 1 despite other failures", len(repo.upserted))
	}
}
