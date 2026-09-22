package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// --- stubs ------------------------------------------------------------------

type stubCartaClient struct {
	themes    []clients.CartaTheme
	subthemes map[string][]clients.CartaSubtheme
	services  map[string][]clients.CartaServiceListItem
	details   map[string]*clients.CartaServiceDetail
	redirects map[string]string
	listErr   error
	detailErr error
	getCalls  []string
	mu        sync.Mutex
}

func (s *stubCartaClient) ListAllThemes(ctx context.Context, includeEmpty bool) ([]clients.CartaTheme, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.themes, nil
}

func (s *stubCartaClient) ListAllSubthemes(ctx context.Context, themeSlug string) ([]clients.CartaSubtheme, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.subthemes[themeSlug], nil
}

func (s *stubCartaClient) ListAllServicesBySubtheme(ctx context.Context, subthemeSlug string) ([]clients.CartaServiceListItem, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.services[subthemeSlug], nil
}

func (s *stubCartaClient) GetService(ctx context.Context, slug string) (*clients.CartaServiceDetail, error) {
	s.mu.Lock()
	s.getCalls = append(s.getCalls, slug)
	s.mu.Unlock()

	if s.detailErr != nil {
		return nil, s.detailErr
	}
	if to, ok := s.redirects[slug]; ok {
		return nil, &clients.ServiceRedirectError{FromSlug: slug, ToSlug: to}
	}
	d, ok := s.details[slug]
	if !ok {
		return nil, fmt.Errorf("%w: %s", clients.ErrServiceNotFound, slug)
	}
	return d, nil
}

func (s *stubCartaClient) GetServiceCanonical(ctx context.Context, slug string) (*clients.CartaServiceDetail, string, error) {
	seen := map[string]struct{}{}
	current := slug
	for hop := 0; hop < 3; hop++ {
		if _, dup := seen[current]; dup {
			return nil, "", fmt.Errorf("redirect cycle")
		}
		seen[current] = struct{}{}
		d, err := s.GetService(ctx, current)
		if err == nil {
			return d, d.Slug, nil
		}
		var redir *clients.ServiceRedirectError
		if !errors.As(err, &redir) {
			return nil, "", err
		}
		current = redir.ToSlug
	}
	return nil, "", fmt.Errorf("muitos redirects")
}

type stubSyncRepo struct {
	mu            sync.Mutex
	cursor        *models.SalesForceSyncCursor
	cursorErr     error
	upserted      []*models.CatalogItem
	upsertErr     error
	softDeleted   []string
	softDeleteErr error
	orphanKept    []string
	orphanCount   int64
	orphanErr     error
	events        []*models.SyncEvent
	eventUpdates  []models.SyncEventStatus
}

func (r *stubSyncRepo) RecordSyncEvent(ctx context.Context, event *models.SyncEvent) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return int64(len(r.events)), nil
}

func (r *stubSyncRepo) UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errMsg string, durationMs int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventUpdates = append(r.eventUpdates, status)
	return nil
}

func (r *stubSyncRepo) GetSalesForceCursor(ctx context.Context, objectType string) (*models.SalesForceSyncCursor, error) {
	if r.cursorErr != nil {
		return nil, r.cursorErr
	}
	if r.cursor == nil {
		return nil, errors.New("no rows")
	}
	return r.cursor, nil
}

func (r *stubSyncRepo) UpsertSalesForceCursor(ctx context.Context, objectType string, lastSyncAt time.Time, deltaToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cursor = &models.SalesForceSyncCursor{ObjectType: objectType, LastSyncAt: &lastSyncAt, LastDeltaToken: deltaToken}
	return nil
}

func (r *stubSyncRepo) Upsert(ctx context.Context, item *models.CatalogItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.upsertErr != nil {
		return r.upsertErr
	}
	r.upserted = append(r.upserted, item)
	return nil
}

func (r *stubSyncRepo) UpsertBatch(ctx context.Context, items []*models.CatalogItem) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.upsertErr != nil {
		return 0, r.upsertErr
	}
	r.upserted = append(r.upserted, items...)
	return len(items), nil
}

func (r *stubSyncRepo) SoftDelete(ctx context.Context, source models.ItemSource, externalID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.softDeleteErr != nil {
		return r.softDeleteErr
	}
	r.softDeleted = append(r.softDeleted, externalID)
	return nil
}

func (r *stubSyncRepo) SoftDeleteActiveNotIn(ctx context.Context, source models.ItemSource, keepExternalIDs []string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.orphanErr != nil {
		return 0, r.orphanErr
	}
	r.orphanKept = append([]string{}, keepExternalIDs...)
	return r.orphanCount, nil
}

func sampleDetail(slug, name string) *clients.CartaServiceDetail {
	cost := "Gratuito"
	return &clients.CartaServiceDetail{
		Slug:                        slug,
		Name:                        name,
		ArticleStatus:               "Online",
		LastModifiedDate:            "2026-08-20T04:56:30.000Z",
		LastPublishedDate:           "2026-08-14T23:40:16.000Z",
		ThemeSlug:                   "tributos",
		ThemeName:                   "Tributos",
		SubthemeSlug:                "iptu",
		SubthemeName:                "IPTU",
		ResponsibleOrgUnit:          "SMFP",
		ResponsibleOrgUnitShortName: "SMFP",
		Info: clients.CartaServiceInfo{
			Summary:         "<p>Resumo do serviço</p>",
			FullDescription: "<h2>O que é</h2><p>Descrição completa</p>",
			TargetAudience:  []string{"Publico_em_geral"},
			Cost:            &cost,
			IsFree:          true,
		},
		HowToRequest: clients.CartaHowToRequest{
			Instructions: "<p>Instruções</p>",
			RequiredDocs: []string{"RG", "CPF"},
		},
		Channels: []clients.CartaChannel{
			{Type: "Digital", Enabled: true},
			{Type: "Presencial", Enabled: true},
		},
		Buttons: []clients.CartaButton{
			{Title: "Acessar", URL: "https://example.com/svc", Enabled: true},
		},
	}
}

func sampleListItem(slug string, modified string) clients.CartaServiceListItem {
	return clients.CartaServiceListItem{
		Slug:             slug,
		Name:             slug,
		ArticleStatus:    "Online",
		LastModifiedDate: modified,
	}
}

type stubHierarchyRepo struct {
	mu             sync.Mutex
	themes         []*models.CartaTheme
	subthemes      []*models.CartaSubtheme
	themeKeep      []string
	subthemeKeep   []string
	refreshCalls   int
	upsertThemeErr error
	upsertSubErr   error
	softThemeErr   error
	softSubErr     error
	refreshErr     error
}

func (h *stubHierarchyRepo) UpsertTheme(ctx context.Context, theme *models.CartaTheme) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.upsertThemeErr != nil {
		return h.upsertThemeErr
	}
	cp := *theme
	h.themes = append(h.themes, &cp)
	return nil
}

func (h *stubHierarchyRepo) UpsertSubtheme(ctx context.Context, st *models.CartaSubtheme) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.upsertSubErr != nil {
		return h.upsertSubErr
	}
	cp := *st
	h.subthemes = append(h.subthemes, &cp)
	return nil
}

func (h *stubHierarchyRepo) SoftDeleteThemesNotIn(ctx context.Context, keep []string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.softThemeErr != nil {
		return 0, h.softThemeErr
	}
	h.themeKeep = append([]string{}, keep...)
	return 1, nil
}

func (h *stubHierarchyRepo) SoftDeleteSubthemesNotIn(ctx context.Context, keep []string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.softSubErr != nil {
		return 0, h.softSubErr
	}
	h.subthemeKeep = append([]string{}, keep...)
	return 1, nil
}

func (h *stubHierarchyRepo) RefreshHierarchyCounts(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refreshCalls++
	return h.refreshErr
}

func newTestSync(client *stubCartaClient, repo *stubSyncRepo) *SalesForceSyncService {
	return newSalesForceSyncService(client, repo, nil, "https://prefeitura.rio", 4)
}

func newTestSyncWithHierarchy(client *stubCartaClient, repo *stubSyncRepo, hier *stubHierarchyRepo) *SalesForceSyncService {
	return newSalesForceSyncService(client, repo, hier, "https://prefeitura.rio", 4)
}

// --- mapper -----------------------------------------------------------------

func TestMapCartaServiceDetail_Happy(t *testing.T) {
	item := MapCartaServiceDetail(sampleDetail("iptu-cobranca", "IPTU cobrança"), "https://prefeitura.rio")
	if item == nil {
		t.Fatal("nil item")
	}
	if item.ExternalID != "iptu-cobranca" || item.Source != models.SourceSalesForce || item.Type != models.TypeService {
		t.Fatalf("identity fields: %+v", item)
	}
	if item.Status != models.StatusActive {
		t.Fatalf("status=%s", item.Status)
	}
	if item.Modalidade != "hibrido" {
		t.Fatalf("modalidade=%s", item.Modalidade)
	}
	if item.URL != "https://example.com/svc" {
		t.Fatalf("url=%s", item.URL)
	}
	if !strings.Contains(item.ShortDesc, "Resumo") || strings.Contains(item.ShortDesc, "<p>") {
		t.Fatalf("short_desc should be stripped HTML: %q", item.ShortDesc)
	}
	if !strings.Contains(item.Description, "Descrição completa") {
		t.Fatalf("description missing content: %q", item.Description)
	}
	if !strings.Contains(item.Description, "Documentos: RG, CPF") {
		t.Fatalf("description missing docs: %q", item.Description)
	}
	if item.SourceUpdatedAt == nil {
		t.Fatal("expected SourceUpdatedAt")
	}
	tags := strings.Join(item.Tags, ",")
	if !strings.Contains(tags, "Tributos") || !strings.Contains(tags, "IPTU") {
		t.Fatalf("tags=%v", item.Tags)
	}
	if item.ThemeSlug != "tributos" || item.SubthemeSlug != "iptu" {
		t.Fatalf("hierarchy slugs: theme=%q subtheme=%q", item.ThemeSlug, item.SubthemeSlug)
	}
}

func TestMapCartaServiceDetail_DraftAndURLFallback(t *testing.T) {
	d := sampleDetail("meu-servico", "Meu Serviço")
	d.ArticleStatus = "Draft"
	d.Buttons = nil
	item := MapCartaServiceDetail(d, "https://prefeitura.rio")
	if item.Status != models.StatusDraft {
		t.Fatalf("status=%s", item.Status)
	}
	if item.URL != "https://prefeitura.rio/servicos/meu-servico" {
		t.Fatalf("url=%s", item.URL)
	}
}

func TestMapCartaServiceDetail_NilOrEmpty(t *testing.T) {
	if MapCartaServiceDetail(nil, "") != nil {
		t.Fatal("nil detail should return nil")
	}
	if MapCartaServiceDetail(&clients.CartaServiceDetail{Name: "x"}, "") != nil {
		t.Fatal("empty slug should return nil")
	}
	if MapCartaServiceDetail(&clients.CartaServiceDetail{Slug: "x"}, "") != nil {
		t.Fatal("empty name should return nil")
	}
}

func TestStripHTML(t *testing.T) {
	got := stripHTML(`<p>Olá <b>mundo</b></p>`)
	if got != "Olá mundo" {
		t.Fatalf("got %q", got)
	}
	if stripHTML("") != "" {
		t.Fatal("empty")
	}
}

func TestFilterServicesNeedingDetail(t *testing.T) {
	listed := []clients.CartaServiceListItem{
		sampleListItem("old", "2026-01-01T00:00:00.000Z"),
		sampleListItem("new", "2026-08-20T00:00:00.000Z"),
		sampleListItem("nodate", ""),
	}
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	full := filterServicesNeedingDetail(listed, time.Time{})
	if len(full) != 3 {
		t.Fatalf("full want 3 got %d", len(full))
	}

	delta := filterServicesNeedingDetail(listed, since)
	slugs := map[string]bool{}
	for _, i := range delta {
		slugs[i.Slug] = true
	}
	if !slugs["new"] || !slugs["nodate"] || slugs["old"] {
		t.Fatalf("delta filter unexpected: %v", slugs)
	}
}

// --- sync -------------------------------------------------------------------

func TestFullSync_HappyPath(t *testing.T) {
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "tributos", Name: "Tributos"}},
		subthemes: map[string][]clients.CartaSubtheme{"tributos": {{Slug: "iptu", Name: "IPTU"}}},
		services: map[string][]clients.CartaServiceListItem{
			"iptu": {sampleListItem("svc-a", "2026-08-20T00:00:00.000Z")},
		},
		details: map[string]*clients.CartaServiceDetail{
			"svc-a": sampleDetail("svc-a", "Serviço A"),
		},
	}
	repo := &stubSyncRepo{orphanCount: 2}
	svc := newTestSync(client, repo)

	if err := svc.FullSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.upserted) != 1 || repo.upserted[0].ExternalID != "svc-a" {
		t.Fatalf("upserted=%v", repo.upserted)
	}
	if repo.cursor == nil || repo.cursor.LastSyncAt == nil {
		t.Fatal("cursor not advanced")
	}
	if len(repo.orphanKept) != 1 || repo.orphanKept[0] != "svc-a" {
		t.Fatalf("orphanKept=%v", repo.orphanKept)
	}
	if len(repo.eventUpdates) == 0 || repo.eventUpdates[len(repo.eventUpdates)-1] != models.SyncStatusCompleted {
		t.Fatalf("eventUpdates=%v", repo.eventUpdates)
	}
}

func TestDeltaSync_FetchesOnlyChanged(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "tributos", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"tributos": {{Slug: "iptu", Name: "I"}}},
		services: map[string][]clients.CartaServiceListItem{
			"iptu": {
				sampleListItem("old-svc", "2026-01-01T00:00:00.000Z"),
				sampleListItem("new-svc", "2026-08-20T00:00:00.000Z"),
			},
		},
		details: map[string]*clients.CartaServiceDetail{
			"new-svc": sampleDetail("new-svc", "Novo"),
			"old-svc": sampleDetail("old-svc", "Velho"),
		},
	}
	repo := &stubSyncRepo{
		cursor: &models.SalesForceSyncCursor{LastSyncAt: &since},
	}
	svc := newTestSync(client, repo)

	if err := svc.DeltaSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.getCalls) != 1 || client.getCalls[0] != "new-svc" {
		t.Fatalf("expected only new-svc detail fetch, got %v", client.getCalls)
	}
	if len(repo.upserted) != 1 || repo.upserted[0].ExternalID != "new-svc" {
		t.Fatalf("upserted=%v", len(repo.upserted))
	}
	// Delta não soft-delete órfãos
	if len(repo.orphanKept) != 0 {
		t.Fatalf("delta should not SoftDeleteActiveNotIn, got %v", repo.orphanKept)
	}
}

func TestDeltaSync_NoCursorFallsBackToFull(t *testing.T) {
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "t", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"t": {{Slug: "s", Name: "S"}}},
		services:  map[string][]clients.CartaServiceListItem{"s": {sampleListItem("a", "2026-08-20T00:00:00.000Z")}},
		details:   map[string]*clients.CartaServiceDetail{"a": sampleDetail("a", "A")},
	}
	repo := &stubSyncRepo{cursorErr: errors.New("no rows")}
	svc := newTestSync(client, repo)

	if err := svc.DeltaSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.orphanKept) != 1 {
		t.Fatal("full sync should soft-delete orphans")
	}
}

func TestFullSync_ListError(t *testing.T) {
	client := &stubCartaClient{listErr: errors.New("boom")}
	repo := &stubSyncRepo{}
	svc := newTestSync(client, repo)

	err := svc.FullSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want list error, got %v", err)
	}
	if repo.eventUpdates[len(repo.eventUpdates)-1] != models.SyncStatusFailed {
		t.Fatalf("status=%v", repo.eventUpdates)
	}
}

func TestFullSync_PartialDetailFailure(t *testing.T) {
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "t", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"t": {{Slug: "s", Name: "S"}}},
		services: map[string][]clients.CartaServiceListItem{
			"s": {
				sampleListItem("ok", "2026-08-20T00:00:00.000Z"),
				sampleListItem("missing", "2026-08-20T00:00:00.000Z"),
			},
		},
		details: map[string]*clients.CartaServiceDetail{
			"ok": sampleDetail("ok", "OK"),
		},
	}
	repo := &stubSyncRepo{}
	svc := newTestSync(client, repo)

	err := svc.FullSync(context.Background())
	if err == nil {
		t.Fatal("expected failure due to missing detail")
	}
	if len(repo.upserted) != 1 {
		t.Fatalf("should upsert successful ones, got %d", len(repo.upserted))
	}
	// Cursor não avança em falha
	if repo.cursor != nil {
		t.Fatal("cursor should not advance on failed sync")
	}
}

func TestFullSync_UpsertError(t *testing.T) {
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "t", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"t": {{Slug: "s", Name: "S"}}},
		services:  map[string][]clients.CartaServiceListItem{"s": {sampleListItem("a", "2026-08-20T00:00:00.000Z")}},
		details:   map[string]*clients.CartaServiceDetail{"a": sampleDetail("a", "A")},
	}
	repo := &stubSyncRepo{upsertErr: errors.New("db down")}
	svc := newTestSync(client, repo)

	if err := svc.FullSync(context.Background()); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("want upsert error, got %v", err)
	}
}

func TestFullSync_EmptyListingSkipsOrphanDelete(t *testing.T) {
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "t", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"t": {}},
	}
	repo := &stubSyncRepo{orphanCount: 99}
	svc := newTestSync(client, repo)

	if err := svc.FullSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.orphanKept) != 0 {
		t.Fatal("should skip SoftDelete when listing is empty")
	}
}

func TestFullSync_DeduplicatesSlugAcrossSubthemes(t *testing.T) {
	client := &stubCartaClient{
		themes: []clients.CartaTheme{{Slug: "t", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{
			"t": {{Slug: "s1"}, {Slug: "s2"}},
		},
		services: map[string][]clients.CartaServiceListItem{
			"s1": {sampleListItem("shared", "2026-08-20T00:00:00.000Z")},
			"s2": {sampleListItem("shared", "2026-08-20T00:00:00.000Z")},
		},
		details: map[string]*clients.CartaServiceDetail{
			"shared": sampleDetail("shared", "Shared"),
		},
	}
	repo := &stubSyncRepo{}
	svc := newTestSync(client, repo)

	if err := svc.FullSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.getCalls) != 1 {
		t.Fatalf("want 1 detail fetch, got %v", client.getCalls)
	}
}

func TestSyncRecord_HappyAndRedirect(t *testing.T) {
	client := &stubCartaClient{
		redirects: map[string]string{"old": "new"},
		details:   map[string]*clients.CartaServiceDetail{"new": sampleDetail("new", "Novo")},
	}
	repo := &stubSyncRepo{}
	svc := newTestSync(client, repo)

	if err := svc.SyncRecord(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if len(repo.upserted) != 1 || repo.upserted[0].ExternalID != "new" {
		t.Fatalf("upserted=%v", repo.upserted)
	}
	if len(repo.softDeleted) != 1 || repo.softDeleted[0] != "old" {
		t.Fatalf("softDeleted=%v", repo.softDeleted)
	}
}

func TestSyncRecord_NotFoundSoftDeletes(t *testing.T) {
	client := &stubCartaClient{details: map[string]*clients.CartaServiceDetail{}}
	repo := &stubSyncRepo{}
	svc := newTestSync(client, repo)

	if err := svc.SyncRecord(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	if len(repo.softDeleted) != 1 || repo.softDeleted[0] != "gone" {
		t.Fatalf("softDeleted=%v", repo.softDeleted)
	}
}

func TestSyncRecord_EmptySlug(t *testing.T) {
	svc := newTestSync(&stubCartaClient{}, &stubSyncRepo{})
	if err := svc.SyncRecord(context.Background(), "  "); err == nil {
		t.Fatal("expected error")
	}
}

func TestFullSync_PersistsHierarchy(t *testing.T) {
	client := &stubCartaClient{
		themes: []clients.CartaTheme{
			{Slug: "tributos", Name: "Tributos", SubthemesCount: 1, PublishedServices: 1},
			{Slug: "saude", Name: "Saúde", SubthemesCount: 0, PublishedServices: 0},
		},
		subthemes: map[string][]clients.CartaSubtheme{
			"tributos": {{Slug: "iptu", Name: "IPTU", PublishedServices: 1}},
			"saude":     {},
		},
		services: map[string][]clients.CartaServiceListItem{
			"iptu": {sampleListItem("svc-a", "2026-08-20T00:00:00.000Z")},
		},
		details: map[string]*clients.CartaServiceDetail{
			"svc-a": sampleDetail("svc-a", "Serviço A"),
		},
	}
	repo := &stubSyncRepo{}
	hier := &stubHierarchyRepo{}
	svc := newTestSyncWithHierarchy(client, repo, hier)

	if err := svc.FullSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hier.themes) != 2 {
		t.Fatalf("themes upserted=%d", len(hier.themes))
	}
	if len(hier.subthemes) != 1 || hier.subthemes[0].Slug != "iptu" || hier.subthemes[0].ThemeSlug != "tributos" {
		t.Fatalf("subthemes=%v", hier.subthemes)
	}
	if len(hier.themeKeep) != 2 {
		t.Fatalf("themeKeep=%v", hier.themeKeep)
	}
	if len(hier.subthemeKeep) != 1 || hier.subthemeKeep[0] != "iptu" {
		t.Fatalf("subthemeKeep=%v", hier.subthemeKeep)
	}
	if hier.refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", hier.refreshCalls)
	}
	if len(repo.upserted) != 1 || repo.upserted[0].ThemeSlug != "tributos" {
		t.Fatalf("catalog theme_slug=%v", repo.upserted)
	}
}

func TestFullSync_HierarchyUpsertError(t *testing.T) {
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "tributos", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"tributos": {{Slug: "iptu", Name: "I"}}},
		services:  map[string][]clients.CartaServiceListItem{},
	}
	repo := &stubSyncRepo{}
	hier := &stubHierarchyRepo{upsertThemeErr: errors.New("theme db down")}
	svc := newTestSyncWithHierarchy(client, repo, hier)

	err := svc.FullSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "theme db down") {
		t.Fatalf("err=%v", err)
	}
}

func TestDeltaSync_SkipsHierarchySoftDelete(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := &stubCartaClient{
		themes:    []clients.CartaTheme{{Slug: "tributos", Name: "T"}},
		subthemes: map[string][]clients.CartaSubtheme{"tributos": {{Slug: "iptu", Name: "I"}}},
		services: map[string][]clients.CartaServiceListItem{
			"iptu": {sampleListItem("new-svc", "2026-08-20T00:00:00.000Z")},
		},
		details: map[string]*clients.CartaServiceDetail{
			"new-svc": sampleDetail("new-svc", "Novo"),
		},
	}
	repo := &stubSyncRepo{cursor: &models.SalesForceSyncCursor{LastSyncAt: &since}}
	hier := &stubHierarchyRepo{}
	svc := newTestSyncWithHierarchy(client, repo, hier)

	if err := svc.DeltaSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hier.themes) == 0 || len(hier.subthemes) == 0 {
		t.Fatal("expected hierarchy upserts on delta")
	}
	if hier.themeKeep != nil || hier.subthemeKeep != nil {
		t.Fatalf("delta must not soft-delete hierarchy: themes=%v subs=%v", hier.themeKeep, hier.subthemeKeep)
	}
	if hier.refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", hier.refreshCalls)
	}
}

func TestSyncRecord_UpsertsHierarchy(t *testing.T) {
	client := &stubCartaClient{
		details: map[string]*clients.CartaServiceDetail{
			"svc-a": sampleDetail("svc-a", "Serviço A"),
		},
	}
	repo := &stubSyncRepo{}
	hier := &stubHierarchyRepo{}
	svc := newTestSyncWithHierarchy(client, repo, hier)

	if err := svc.SyncRecord(context.Background(), "svc-a"); err != nil {
		t.Fatal(err)
	}
	if len(hier.themes) != 1 || hier.themes[0].Slug != "tributos" {
		t.Fatalf("themes=%v", hier.themes)
	}
	if len(hier.subthemes) != 1 || hier.subthemes[0].Slug != "iptu" {
		t.Fatalf("subthemes=%v", hier.subthemes)
	}
}

func TestMapCartaTargetAudience_EdgeCases(t *testing.T) {
	raw := mapCartaTargetAudience([]string{"Pessoa PCD", "Idoso", "Mulher", "Publico_em_geral", "População preta"})
	var ta models.TargetAudienceData
	if err := json.Unmarshal(raw, &ta); err != nil {
		t.Fatal(err)
	}
	if len(ta.Deficiencia) == 0 || len(ta.FaixaEtaria) == 0 || len(ta.Genero) == 0 {
		t.Fatalf("audience mapping incomplete: %+v", ta)
	}
	if len(ta.Outros) != 1 || ta.Outros[0] != "Publico_em_geral" {
		t.Fatalf("expected Publico_em_geral in Outros, got %+v", ta.Outros)
	}
	if len(ta.Etnia) != 1 || ta.Etnia[0] != "População preta" {
		t.Fatalf("expected etnia mapping, got %+v", ta.Etnia)
	}
	empty := mapCartaTargetAudience(nil)
	if string(empty) != "{}" {
		t.Fatalf("empty=%s", empty)
	}
}

func TestBuildCartaDescription_SkipsDuplicateSummary(t *testing.T) {
	same := "<p>Mesmo texto</p>"
	detail := &clients.CartaServiceDetail{
		Info: clients.CartaServiceInfo{
			Summary:         same,
			FullDescription: same,
		},
		HowToRequest: clients.CartaHowToRequest{
			Instructions: "<p>Passo a passo</p>",
			RequiredDocs: []string{"RG"},
		},
	}
	desc := buildCartaDescription(detail)
	if strings.Count(desc, "Mesmo texto") != 0 {
		t.Fatalf("full description should be omitted when equal to summary: %q", desc)
	}
	if !strings.Contains(desc, "Passo a passo") || !strings.Contains(desc, "Documentos: RG") {
		t.Fatalf("expected howto/docs in description: %q", desc)
	}
}
