// Package datasource define a interface para integração de fontes de dados no catálogo.
//
// Para adicionar uma nova fonte de dados:
//  1. Crie um arquivo em internal/datasource/<nome>.go
//  2. Implemente a interface DataSource
//  3. Registre a fonte no cmd/worker/main.go com manager.Register(...)
//
// Exemplo mínimo:
//
//	type MinhaFonte struct { ... }
//
//	func (f *MinhaFonte) Name() string                 { return "minha-fonte" }
//	func (f *MinhaFonte) Source() models.ItemSource    { return models.ItemSource("minha-fonte") }
//	func (f *MinhaFonte) SyncInterval() time.Duration  { return 30 * time.Minute }
//	func (f *MinhaFonte) Sync(ctx context.Context) error { ... }
package datasource

import (
	"context"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// DataSource é a interface que toda fonte de dados deve implementar.
type DataSource interface {
	// Name retorna o identificador legível da fonte (ex: "salesforce", "app-go-api").
	Name() string

	// Source retorna o enum item_source correspondente.
	Source() models.ItemSource

	// SyncInterval define com que frequência a sincronização periódica ocorre.
	SyncInterval() time.Duration

	// Sync executa a sincronização. Deve ser incremental (delta) por padrão;
	// na primeira execução (sem cursor), deve executar full sync automaticamente.
	Sync(ctx context.Context) error
}
