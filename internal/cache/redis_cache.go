package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

var ErrCacheMiss = errors.New("cache miss")

const SearchKeyPrefix = "catalogo:search:v2:"

var incrementRateLimitScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
local ttl = redis.call("PTTL", KEYS[1])
if count == 1 or ttl < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {count, ttl}
`)

type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(host string, port int, password string, db int, poolSize, minIdleConns int) *RedisCache {
	client := redis.NewClient(&redis.Options{
		Addr:         host + ":" + itoa(port),
		Password:     password,
		DB:           db,
		PoolSize:     poolSize,
		MinIdleConns: minIdleConns,
	})
	return &RedisCache{client: client}
}

func (c *RedisCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *RedisCache) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

// IncrementRateLimit atomically increments a fixed-window counter and returns
// its current value and remaining lifetime.
func (c *RedisCache) IncrementRateLimit(
	ctx context.Context,
	key string,
	window time.Duration,
) (int64, time.Duration, error) {
	if c == nil || c.client == nil {
		return 0, 0, errors.New("redis rate-limit store is not configured")
	}
	if key == "" || window <= 0 {
		return 0, 0, errors.New("redis rate-limit key and window must be valid")
	}
	windowMilliseconds := int64((window-1)/time.Millisecond + 1)

	counterValues, scriptError := incrementRateLimitScript.Run(
		ctx,
		c.client,
		[]string{key},
		windowMilliseconds,
	).Int64Slice()
	if scriptError != nil {
		return 0, 0, scriptError
	}
	if len(counterValues) != 2 || counterValues[0] <= 0 || counterValues[1] < 0 {
		return 0, 0, fmt.Errorf("redis returned an invalid rate-limit counter: %v", counterValues)
	}
	remainingMilliseconds := counterValues[1]
	if remainingMilliseconds == 0 {
		remainingMilliseconds = 1
	}
	return counterValues[0], time.Duration(remainingMilliseconds) * time.Millisecond, nil
}

// Get desserializa o valor do cache na variável dest.
// Retorna ErrCacheMiss se a chave não existe.
func (c *RedisCache) Get(ctx context.Context, key string, dest interface{}) error {
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return ErrCacheMiss
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

// Set serializa e armazena o valor com TTL.
func (c *RedisCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := marshalCacheValue(key, value)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, data, ttl).Err()
}

func marshalCacheValue(key string, value any) ([]byte, error) {
	encodedValue, encodeError := json.Marshal(value)
	if encodeError != nil {
		return nil, encodeError
	}
	if strings.HasPrefix(key, SearchKeyPrefix) && len(encodedValue) > models.MaximumPublicSearchResponseBytes {
		return nil, errors.New("search cache value exceeds the public response byte limit")
	}
	return encodedValue, nil
}

// Del remove uma chave do cache.
func (c *RedisCache) Del(ctx context.Context, keys ...string) error {
	return c.client.Del(ctx, keys...).Err()
}

// DelByPrefix remove todas as chaves que começam com prefix usando SCAN.
func (c *RedisCache) DelByPrefix(ctx context.Context, prefix string) (int64, error) {
	if c == nil || c.client == nil {
		return 0, nil
	}

	var cursor uint64
	var deleted int64
	batch := make([]string, 0, 100)
	pattern := prefix + "*"

	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return deleted, err
		}
		batch = append(batch, keys...)

		if len(batch) >= 100 {
			n, err := c.client.Del(ctx, batch...).Result()
			deleted += n
			if err != nil {
				return deleted, err
			}
			batch = batch[:0]
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if len(batch) > 0 {
		n, err := c.client.Del(ctx, batch...).Result()
		deleted += n
		if err != nil {
			return deleted, err
		}
	}

	return deleted, nil
}

// IsMiss verifica se o erro é cache miss.
func IsMiss(err error) bool {
	return errors.Is(err, ErrCacheMiss)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
