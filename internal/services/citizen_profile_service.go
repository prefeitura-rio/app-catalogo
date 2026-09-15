package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type CitizenProfileService struct {
	rmiClient      *clients.RMIClient
	profileRepo    *repository.CitizenProfileRepository
	cpfHashSalt    string
	staleThreshold time.Duration
}

func NewCitizenProfileService(
	rmiClient *clients.RMIClient,
	profileRepo *repository.CitizenProfileRepository,
	cpfHashSalt string,
	staleThreshold time.Duration,
) *CitizenProfileService {
	return &CitizenProfileService{
		rmiClient:      rmiClient,
		profileRepo:    profileRepo,
		cpfHashSalt:    cpfHashSalt,
		staleThreshold: staleThreshold,
	}
}

// HashCPF calcula SHA-256(CPF + salt). CPF nunca é armazenado diretamente.
func (s *CitizenProfileService) HashCPF(cpf string) string {
	h := sha256.Sum256([]byte(cpf + s.cpfHashSalt))
	return fmt.Sprintf("%x", h)
}

// GetOrSync retorna o perfil do cidadão, sincronizando do RMI se necessário.
// CPF é usado apenas em memória para busca no RMI — nunca persiste.
func (s *CitizenProfileService) GetOrSync(ctx context.Context, cpf string) (*models.CitizenProfile, error) {
	hash := s.HashCPF(cpf)

	profile, err := s.profileRepo.GetByCPFHash(ctx, hash)
	if err == nil {
		// Verificar se está stale
		if time.Since(profile.LastSyncedAt) < s.staleThreshold {
			return profile, nil
		}
		// Atualizar em background e retornar o dado existente
		go s.syncFromRMI(context.Background(), cpf, hash)
		return profile, nil
	}

	if !errors.Is(err, repository.ErrProfileNotFound) {
		return nil, err
	}

	// Perfil não existe — buscar do RMI de forma síncrona
	return s.syncFromRMI(ctx, cpf, hash)
}

func (s *CitizenProfileService) syncFromRMI(ctx context.Context, cpf, hash string) (*models.CitizenProfile, error) {
	citizen, err := s.rmiClient.GetCitizen(ctx, cpf)
	if err != nil {
		log.Warn().Err(err).Msg("citizen profile: falha ao buscar do RMI")
		return nil, err
	}
	if citizen == nil {
		return nil, nil
	}

	profile := &models.CitizenProfile{
		CPFHash:       hash,
		Bairro:        citizen.Address.Bairro,
		Cidade:        citizen.Address.Cidade,
		Estado:        citizen.Address.Estado,
		CEP:           citizen.Address.CEP,
		Escolaridade:  citizen.Escolaridade,
		RendaFamiliar: citizen.RendaFamiliar,
		Deficiencia:   citizen.Deficiencia,
		Etnia:         citizen.Etnia,
		Genero:        citizen.Genero,
		FaixaEtaria:   calcFaixaEtaria(citizen.BirthDate),
	}

	if err := s.profileRepo.Upsert(ctx, profile); err != nil {
		log.Warn().Err(err).Msg("citizen profile: falha ao persistir perfil")
	}

	return profile, nil
}

// calcFaixaEtaria calcula a faixa etária a partir da data de nascimento (YYYY-MM-DD).
func calcFaixaEtaria(birthDate string) string {
	if len(birthDate) < 4 {
		return ""
	}
	year, err := strconv.Atoi(birthDate[:4])
	if err != nil {
		return ""
	}
	age := time.Now().Year() - year
	switch {
	case age < 18:
		return "menor-18"
	case age <= 24:
		return "18-24"
	case age <= 34:
		return "25-34"
	case age <= 44:
		return "35-44"
	case age <= 59:
		return "45-59"
	default:
		return "60+"
	}
}
