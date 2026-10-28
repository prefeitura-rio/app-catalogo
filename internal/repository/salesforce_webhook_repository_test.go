package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func testWebhookRepo(t *testing.T) (*SalesForceWebhookRepository, *pgxpool.Pool, string) {
	t.Helper()
	pool := testCatalogPool(t)

	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'salesforce_webhook_events'
		)
	`).Scan(&exists); err != nil || !exists {
		t.Skip("salesforce_webhook_events ausente; rode migrations antes dos testes de integração")
	}

	prefix := fmt.Sprintf("test-webhook-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM salesforce_webhook_events WHERE slug LIKE $1`, prefix+"%")
	})
	return NewSalesForceWebhookRepository(pool), pool, prefix
}

// eventKey gera uma chave de 64 caracteres única por teste.
func eventKey(prefix, suffix string) string {
	k := fmt.Sprintf("%x", []byte(prefix+suffix))
	if len(k) >= 64 {
		return k[len(k)-64:]
	}
	return strings.Repeat("0", 64-len(k)) + k
}

type webhookRow struct {
	status    string
	attempts  int
	lastError *string
	due       bool
}

func readWebhookRow(t *testing.T, pool *pgxpool.Pool, id int64) webhookRow {
	t.Helper()
	var r webhookRow
	if err := pool.QueryRow(context.Background(), `
		SELECT status, attempts, last_error, next_attempt_at <= NOW()
		FROM salesforce_webhook_events WHERE id = $1
	`, id).Scan(&r.status, &r.attempts, &r.lastError, &r.due); err != nil {
		t.Fatalf("ler evento %d: %v", id, err)
	}
	return r
}

func claimedIDs(events []*models.SalesForceWebhookEvent) map[int64]*models.SalesForceWebhookEvent {
	out := make(map[int64]*models.SalesForceWebhookEvent, len(events))
	for _, ev := range events {
		out[ev.ID] = ev
	}
	return out
}

func TestWebhookRepo_EnqueueIsIdempotentByEventKey(t *testing.T) {
	repo, pool, prefix := testWebhookRepo(t)
	ctx := context.Background()
	key := eventKey(prefix, "a")

	ev, created, err := repo.Enqueue(ctx, key, prefix+"-slug", "updated", 2*time.Minute)
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	if ev.Status != models.WebhookEventPending || ev.Attempts != 1 || ev.ClaimedAt.Before(ev.ReceivedAt) {
		t.Fatalf("evento inesperado: %+v", ev)
	}
	if !ev.NextAttemptAt.After(ev.ReceivedAt.Add(time.Minute)) {
		t.Errorf("lease da 1ª tentativa deveria segurar o evento: next=%v received=%v", ev.NextAttemptAt, ev.ReceivedAt)
	}

	again, created, err := repo.Enqueue(ctx, key, prefix+"-slug", "updated", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if created || again != nil {
		t.Fatalf("mesma chave não deveria criar evento: created=%v ev=%v", created, again)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM salesforce_webhook_events WHERE event_key = $1`, key).Scan(&n); err != nil || n != 1 {
		t.Fatalf("esperava 1 linha para a chave, got %d (err %v)", n, err)
	}
}

func TestWebhookRepo_FailedEventIsClaimedAgainWhenDue(t *testing.T) {
	repo, pool, prefix := testWebhookRepo(t)
	ctx := context.Background()

	ev, _, err := repo.Enqueue(ctx, eventKey(prefix, "b"), prefix+"-slug", "updated", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Lease vigente: não é assumido.
	claimed, err := repo.ClaimDue(ctx, 100, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := claimedIDs(claimed)[ev.ID]; ok {
		t.Fatal("evento com lease vigente não deveria ser assumido")
	}

	// Falha com nova tentativa imediata: fica pendente e vencido.
	if err := repo.Fail(ctx, ev, "carta indisponível", 0); err != nil {
		t.Fatal(err)
	}
	row := readWebhookRow(t, pool, ev.ID)
	if row.status != "pending" || !row.due || row.lastError == nil || *row.lastError != "carta indisponível" {
		t.Fatalf("após falha: %+v", row)
	}

	claimed, err = repo.ClaimDue(ctx, 100, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := claimedIDs(claimed)[ev.ID]
	if !ok || second.Attempts != 2 {
		t.Fatalf("esperava o evento assumido na 2ª tentativa, got %+v", second)
	}

	// Falha atrasada da 1ª tentativa não mexe na 2ª.
	if err := repo.Fail(ctx, ev, "falha atrasada", 0); err != nil {
		t.Fatal(err)
	}
	row = readWebhookRow(t, pool, ev.ID)
	if row.due || *row.lastError != "carta indisponível" {
		t.Fatalf("falha de tentativa antiga não deveria valer: %+v", row)
	}

	// Falha da 2ª tentativa agenda para depois: não é assumido agora.
	if err := repo.Fail(ctx, second, strings.Repeat("é", 2000), time.Hour); err != nil {
		t.Fatalf("erro longo com multibyte deveria ser truncado sem falhar: %v", err)
	}
	claimed, err = repo.ClaimDue(ctx, 100, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := claimedIDs(claimed)[ev.ID]; ok {
		t.Fatal("evento agendado para daqui a 1h não deveria ser assumido")
	}
	if row := readWebhookRow(t, pool, ev.ID); row.attempts != 2 || len(*row.lastError) > maxWebhookErrorLength {
		t.Fatalf("estado final inesperado: attempts=%d len(last_error)=%d", row.attempts, len(*row.lastError))
	}
}

func TestWebhookRepo_ConcurrentClaimsDoNotShareEvent(t *testing.T) {
	repo, _, prefix := testWebhookRepo(t)
	ctx := context.Background()

	ev, _, err := repo.Enqueue(ctx, eventKey(prefix, "c"), prefix+"-slug", "updated", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, ev, "x", 0); err != nil {
		t.Fatal(err)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		hits int
	)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := repo.ClaimDue(ctx, 100, 2*time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if _, ok := claimedIDs(claimed)[ev.ID]; ok {
				mu.Lock()
				hits++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if hits != 1 {
		t.Fatalf("o evento deveria ser assumido por exatamente um processador, got %d", hits)
	}
}

func TestWebhookRepo_CompleteCoversEarlierPendingOfSameSlugOnce(t *testing.T) {
	repo, pool, prefix := testWebhookRepo(t)
	ctx := context.Background()
	slug := prefix + "-slug"

	older, _, err := repo.Enqueue(ctx, eventKey(prefix, "d1"), slug, "updated", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := repo.Enqueue(ctx, eventKey(prefix, "d2"), prefix+"-outro", "updated", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	newer, _, err := repo.Enqueue(ctx, eventKey(prefix, "d3"), slug, "updated", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	n, err := repo.Complete(ctx, newer)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("esperava concluir o evento e o anterior do mesmo slug (2), got %d", n)
	}
	if row := readWebhookRow(t, pool, older.ID); row.status != "completed" {
		t.Errorf("evento anterior do slug deveria estar concluído: %+v", row)
	}
	if row := readWebhookRow(t, pool, other.ID); row.status != "pending" {
		t.Errorf("evento de outro slug não deveria ser tocado: %+v", row)
	}

	// Reprocessar o mesmo evento não tem efeito.
	if n, err := repo.Complete(ctx, newer); err != nil || n != 0 {
		t.Fatalf("concluir de novo: n=%d err=%v", n, err)
	}
	// Falha tardia não reabre evento concluído.
	if err := repo.Fail(ctx, newer, "tarde", 0); err != nil {
		t.Fatal(err)
	}
	if row := readWebhookRow(t, pool, newer.ID); row.status != "completed" || row.lastError != nil {
		t.Fatalf("evento concluído não deveria voltar: %+v", row)
	}
	claimed, err := repo.ClaimDue(ctx, 100, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := claimedIDs(claimed)[newer.ID]; ok {
		t.Fatal("evento concluído não deveria ser assumido")
	}

	// Retenção remove os concluídos antigos e preserva os pendentes.
	if _, err := repo.DeleteCompletedBefore(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM salesforce_webhook_events WHERE slug LIKE $1`, prefix+"%").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("esperava só o pendente de outro slug após a retenção, got %d", left)
	}
}
