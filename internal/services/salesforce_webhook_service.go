package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

// WebhookEventStore é a fila durável dos eventos do webhook SalesForce.
type WebhookEventStore interface {
	Enqueue(ctx context.Context, eventKey, slug, eventType string, lease time.Duration) (*models.SalesForceWebhookEvent, bool, error)
	ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]*models.SalesForceWebhookEvent, error)
	Complete(ctx context.Context, ev *models.SalesForceWebhookEvent) (int64, error)
	Fail(ctx context.Context, ev *models.SalesForceWebhookEvent, errMsg string, retryIn time.Duration) error
	DeleteCompletedBefore(ctx context.Context, before time.Time) (int64, error)
}

// RecordSyncer sincroniza um serviço da Carta pelo slug. Precisa ser idempotente:
// o mesmo evento pode ser processado mais de uma vez.
type RecordSyncer interface {
	SyncRecord(ctx context.Context, slug string) error
}

// WebhookConfig ajusta a fila do webhook SalesForce.
type WebhookConfig struct {
	// Lease é por quanto tempo uma tentativa é dona do evento; vencido, outro
	// processador o retoma (cobre o pod que reiniciou no meio do processamento).
	Lease time.Duration
	// ProcessTimeout limita uma tentativa; menor que Lease.
	ProcessTimeout time.Duration
	// RetryBaseDelay é a espera depois da 1ª falha; dobra a cada falha.
	RetryBaseDelay time.Duration
	// RetryMaxDelay limita a espera entre tentativas.
	RetryMaxDelay time.Duration
	// BatchSize é quantos eventos vencidos cada passada do reprocessador assume.
	BatchSize int
	// Retention é por quanto tempo eventos concluídos ficam na tabela.
	Retention time.Duration
}

// DefaultWebhookConfig: tentativas em 30s, 1min, 2min… até 1h, sem desistir.
var DefaultWebhookConfig = WebhookConfig{
	Lease:          2 * time.Minute,
	ProcessTimeout: 90 * time.Second,
	RetryBaseDelay: 30 * time.Second,
	RetryMaxDelay:  time.Hour,
	BatchSize:      20,
	Retention:      7 * 24 * time.Hour,
}

// maxWebhookEventTypeChars é o limite da coluna event_type (informativa: é truncada).
const maxWebhookEventTypeChars = 64

// truncateRunes corta s em no máximo n caracteres, sem partir um caractere UTF-8.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Resultados do webhook no label result de catalogo_salesforce_webhook_events_total.
const (
	webhookResultEnqueued  = "enqueued"
	webhookResultDuplicate = "duplicate"
	webhookResultProcessed = "processed"
	webhookResultFailed    = "failed"
)

// SalesForceWebhookService grava os eventos do webhook antes de responder e os
// processa com novas tentativas até a sincronização do registro dar certo.
type SalesForceWebhookService struct {
	store  WebhookEventStore
	syncer RecordSyncer
	cfg    WebhookConfig
}

func NewSalesForceWebhookService(store WebhookEventStore, syncer RecordSyncer, cfg WebhookConfig) *SalesForceWebhookService {
	def := DefaultWebhookConfig
	if cfg.Lease <= 0 {
		cfg.Lease = def.Lease
	}
	if cfg.ProcessTimeout <= 0 || cfg.ProcessTimeout >= cfg.Lease {
		cfg.ProcessTimeout = cfg.Lease * 3 / 4
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = def.RetryBaseDelay
	}
	if cfg.RetryMaxDelay < cfg.RetryBaseDelay {
		cfg.RetryMaxDelay = cfg.RetryBaseDelay
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = def.BatchSize
	}
	if cfg.Retention <= 0 {
		cfg.Retention = def.Retention
	}
	return &SalesForceWebhookService{store: store, syncer: syncer, cfg: cfg}
}

// WebhookEventKey identifica o evento pelo SHA-256 do body assinado.
func WebhookEventKey(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// uniqueWebhookEventKey gera uma chave que não colide com nenhuma reentrega.
func uniqueWebhookEventKey(body []byte) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(body)
	h.Write(nonce)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Enqueue grava o evento, já assumido pela primeira tentativa. created = false
// quando o mesmo body já foi recebido (reentrega): nada a processar de novo.
// A reentrega só é reconhecida quando o evento traz o horário de criação
// (eventCreated): sem ele, duas mudanças distintas do mesmo serviço teriam o
// mesmo body, e tratá-las como uma só perderia a segunda.
func (s *SalesForceWebhookService) Enqueue(
	ctx context.Context,
	body []byte,
	slug, eventType, eventCreated string,
) (*models.SalesForceWebhookEvent, bool, error) {
	key := WebhookEventKey(body)
	if strings.TrimSpace(eventCreated) == "" {
		unique, err := uniqueWebhookEventKey(body)
		if err != nil {
			return nil, false, err
		}
		key = unique
	}
	ev, created, err := s.store.Enqueue(ctx, key, slug, truncateRunes(eventType, maxWebhookEventTypeChars), s.cfg.Lease)
	if err != nil {
		return nil, false, err
	}
	if !created {
		observability.SalesForceWebhookEventsTotal.WithLabelValues(webhookResultDuplicate).Inc()
		return nil, false, nil
	}
	observability.SalesForceWebhookEventsTotal.WithLabelValues(webhookResultEnqueued).Inc()
	return ev, true, nil
}

// Process executa a tentativa assumida: sincroniza o registro e marca o evento
// como concluído, ou registra a falha e agenda a próxima tentativa.
func (s *SalesForceWebhookService) Process(ctx context.Context, ev *models.SalesForceWebhookEvent) error {
	if ev == nil {
		return nil
	}
	syncCtx, cancel := context.WithTimeout(ctx, s.cfg.ProcessTimeout)
	syncErr := s.syncer.SyncRecord(syncCtx, ev.Slug)
	cancel()

	// A gravação do resultado não pode depender do context da tentativa (que pode
	// ter estourado): sem ela, o evento só volta quando o lease vencer.
	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelWrite()

	if syncErr != nil {
		observability.SalesForceWebhookEventsTotal.WithLabelValues(webhookResultFailed).Inc()
		retryIn := s.retryDelay(ev.Attempts)
		log.Error().Err(syncErr).
			Int64("event_id", ev.ID).
			Str("slug", ev.Slug).
			Int("attempt", ev.Attempts).
			Dur("retry_in", retryIn).
			Msg("webhook: falha ao sincronizar registro; evento mantido pendente")
		if err := s.store.Fail(writeCtx, ev, syncErr.Error(), retryIn); err != nil {
			log.Error().Err(err).Int64("event_id", ev.ID).
				Msg("webhook: falha ao registrar erro do evento; ele volta quando o lease vencer")
		}
		return syncErr
	}

	completed, err := s.store.Complete(writeCtx, ev)
	if err != nil {
		log.Error().Err(err).Int64("event_id", ev.ID).
			Msg("webhook: registro sincronizado, mas falhou marcar o evento; reprocessá-lo é seguro (SyncRecord é idempotente)")
		return err
	}
	observability.SalesForceWebhookEventsTotal.WithLabelValues(webhookResultProcessed).Inc()
	log.Info().
		Int64("event_id", ev.ID).
		Str("slug", ev.Slug).
		Int("attempt", ev.Attempts).
		Int64("events_completed", completed).
		Msg("webhook: registro sincronizado")
	return nil
}

// ProcessDue assume os eventos pendentes vencidos e os processa em sequência.
// Devolve quantos foram assumidos.
func (s *SalesForceWebhookService) ProcessDue(ctx context.Context) (int, error) {
	events, err := s.store.ClaimDue(ctx, s.cfg.BatchSize, s.cfg.Lease)
	if err != nil {
		return 0, err
	}
	for _, ev := range events {
		if ctx.Err() != nil {
			break
		}
		_ = s.Process(ctx, ev)
	}
	return len(events), nil
}

// Run reprocessa os eventos pendentes a cada interval até o context ser cancelado,
// e remove periodicamente os concluídos fora da retenção.
func (s *SalesForceWebhookService) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Info().Dur("interval", interval).Msg("webhook: reprocessador de eventos iniciado")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastCleanup := time.Time{}

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("webhook: reprocessador de eventos encerrado")
			return
		case <-ticker.C:
			if n, err := s.ProcessDue(ctx); err != nil {
				log.Error().Err(err).Msg("webhook: falha ao buscar eventos pendentes")
			} else if n > 0 {
				log.Info().Int("events", n).Msg("webhook: eventos pendentes reprocessados")
			}
			if time.Since(lastCleanup) >= time.Hour {
				lastCleanup = time.Now()
				if n, err := s.store.DeleteCompletedBefore(ctx, time.Now().Add(-s.cfg.Retention)); err != nil {
					log.Warn().Err(err).Msg("webhook: falha ao remover eventos concluídos antigos")
				} else if n > 0 {
					log.Info().Int64("deleted", n).Msg("webhook: eventos concluídos antigos removidos")
				}
			}
		}
	}
}

// retryDelay devolve a espera depois da falha da tentativa attempt (começa em 1).
func (s *SalesForceWebhookService) retryDelay(attempt int) time.Duration {
	d := s.cfg.RetryBaseDelay
	for i := 1; i < attempt && d < s.cfg.RetryMaxDelay; i++ {
		d *= 2
	}
	if d > s.cfg.RetryMaxDelay {
		d = s.cfg.RetryMaxDelay
	}
	return d
}
