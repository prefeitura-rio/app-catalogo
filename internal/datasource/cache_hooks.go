package datasource

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
)

// NewSearchCacheInvalidationHook limpa resultados de busca após uma sync bem-sucedida.
func NewSearchCacheInvalidationHook(searchCache *cache.RedisCache) SyncHook {
	return func(ctx context.Context, source DataSource) error {
		deleted, err := searchCache.DelByPrefix(ctx, cache.SearchKeyPrefix)
		if err != nil {
			return err
		}
		if deleted > 0 {
			log.Info().
				Str("source", source.Name()).
				Int64("deleted", deleted).
				Msg("datasource: cache de busca invalidado")
		}
		return nil
	}
}
