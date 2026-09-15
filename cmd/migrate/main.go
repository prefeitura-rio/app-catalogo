package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/config"
)

func main() {
	cfg, err := config.Get()
	if err != nil {
		fmt.Fprintf(os.Stderr, "falha ao carregar configurações: %v\n", err)
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.Database.DSN())
	if err != nil {
		log.Fatal().Err(err).Msg("falha ao abrir conexão para migrations")
	}
	defer db.Close()

	if err := db.PingContext(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("falha ao conectar ao banco para migrations")
	}

	goose.SetDialect("postgres")

	dir := "db/migrations"
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	if err := goose.RunContext(context.Background(), command, db, dir, os.Args[2:]...); err != nil {
		log.Fatal().Err(err).Str("command", command).Msg("falha ao executar migration")
	}
}
