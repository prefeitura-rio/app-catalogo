package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

var ErrProfileNotFound = errors.New("perfil não encontrado")

type CitizenProfileRepository struct {
	db *pgxpool.Pool
}

func NewCitizenProfileRepository(db *pgxpool.Pool) *CitizenProfileRepository {
	return &CitizenProfileRepository{db: db}
}

// GetByCPFHash busca um perfil pelo hash do CPF.
func (r *CitizenProfileRepository) GetByCPFHash(ctx context.Context, cpfHash string) (*models.CitizenProfile, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, bairro, cidade, estado, cep, escolaridade, renda_familiar,
		       deficiencia, etnia, genero, faixa_etaria, cluster_id, last_synced_at
		FROM citizen_profiles
		WHERE cpf_hash = $1
	`, cpfHash)

	var p models.CitizenProfile
	p.CPFHash = cpfHash

	err := row.Scan(
		&p.ID, &p.Bairro, &p.Cidade, &p.Estado, &p.CEP,
		&p.Escolaridade, &p.RendaFamiliar, &p.Deficiencia,
		&p.Etnia, &p.Genero, &p.FaixaEtaria,
		&p.ClusterID, &p.LastSyncedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProfileNotFound
	}
	return &p, err
}

// Upsert cria ou atualiza um perfil do cidadão.
func (r *CitizenProfileRepository) Upsert(ctx context.Context, p *models.CitizenProfile) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO citizen_profiles (
			cpf_hash, bairro, cidade, estado, cep,
			escolaridade, renda_familiar, deficiencia,
			etnia, genero, faixa_etaria, last_synced_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (cpf_hash) DO UPDATE SET
			bairro          = EXCLUDED.bairro,
			cidade          = EXCLUDED.cidade,
			estado          = EXCLUDED.estado,
			cep             = EXCLUDED.cep,
			escolaridade    = EXCLUDED.escolaridade,
			renda_familiar  = EXCLUDED.renda_familiar,
			deficiencia     = EXCLUDED.deficiencia,
			etnia           = EXCLUDED.etnia,
			genero          = EXCLUDED.genero,
			faixa_etaria    = EXCLUDED.faixa_etaria,
			last_synced_at  = EXCLUDED.last_synced_at,
			updated_at      = NOW()
	`,
		p.CPFHash, p.Bairro, p.Cidade, p.Estado, p.CEP,
		p.Escolaridade, p.RendaFamiliar, p.Deficiencia,
		p.Etnia, p.Genero, p.FaixaEtaria, time.Now(),
	)
	return err
}

// GetStaleProfiles retorna perfis não sincronizados há mais de staleThreshold.
func (r *CitizenProfileRepository) GetStaleProfiles(ctx context.Context, staleThreshold time.Duration, limit int) ([]string, error) {
	cutoff := time.Now().Add(-staleThreshold)
	rows, err := r.db.Query(ctx, `
		SELECT cpf_hash FROM citizen_profiles
		WHERE last_synced_at < $1
		ORDER BY last_synced_at ASC
		LIMIT $2
	`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}
