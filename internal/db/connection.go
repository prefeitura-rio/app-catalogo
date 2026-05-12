package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

var Pool *pgxpool.Pool

const (
	maxRetries    = 5
	retryBaseWait = 2 * time.Second
)

func Connect(ctx context.Context, cfg PoolConfig) error {
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s&TimeZone=%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Name, cfg.SSLMode, cfg.Timezone,
	)

	poolCfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("falha ao parsear configuração do banco: %w", err)
	}

	poolCfg.MaxConns = int32(cfg.MaxOpenConns)
	poolCfg.MinConns = int32(cfg.MinConns)

	wait := retryBaseWait
	for attempt := 1; attempt <= maxRetries; attempt++ {
		var pool *pgxpool.Pool
		pool, err = pgxpool.NewWithConfig(ctx, poolCfg)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr != nil {
				pool.Close()
				err = fmt.Errorf("falha ao conectar ao banco: %w", pingErr)
			} else {
				Pool = pool
				return nil
			}
		}

		if attempt == maxRetries {
			break
		}

		log.Warn().Err(err).
			Int("attempt", attempt).
			Int("max", maxRetries).
			Dur("retry_in", wait).
			Msg("db: conexão falhou, tentando novamente...")

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}

	return err
}

func Close() {
	if Pool != nil {
		Pool.Close()
	}
}

type PoolConfig struct {
	Host         string
	Port         int
	User         string
	Password     string
	Name         string
	SSLMode      string
	Timezone     string
	MaxOpenConns int
	MinConns     int
}
