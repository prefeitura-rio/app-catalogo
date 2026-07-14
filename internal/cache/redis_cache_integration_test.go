package cache

import (
	"context"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRedisRateLimitCounterIsAtomicAndExpiring(t *testing.T) {
	redisAddress := os.Getenv("APP_CATALOGO_RATE_LIMIT_TEST_REDIS_ADDR")
	if redisAddress == "" {
		t.Skip("APP_CATALOGO_RATE_LIMIT_TEST_REDIS_ADDR is not set")
	}
	host, rawPort, splitError := net.SplitHostPort(redisAddress)
	if splitError != nil {
		t.Fatalf("parse Redis test address: %v", splitError)
	}
	port, portError := strconv.Atoi(rawPort)
	if portError != nil {
		t.Fatalf("parse Redis test port: %v", portError)
	}

	redisCache := NewRedisCache(host, port, "", 0, 4, 0)
	t.Cleanup(func() {
		if closeError := redisCache.client.Close(); closeError != nil {
			t.Errorf("close Redis test client: %v", closeError)
		}
	})
	testContext, cancelTest := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelTest()
	if pingError := redisCache.Ping(testContext); pingError != nil {
		t.Fatalf("ping Redis test service: %v", pingError)
	}

	rateLimitKey := "catalogo:rate-limit:test:" + uuid.NewString()
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
		defer cancelCleanup()
		if deleteError := redisCache.Del(cleanupContext, rateLimitKey); deleteError != nil {
			t.Errorf("delete Redis test key: %v", deleteError)
		}
	})

	const concurrentIncrements = 32
	window := 10 * time.Second
	counts := make([]int64, concurrentIncrements)
	remainingWindows := make([]time.Duration, concurrentIncrements)
	var waitGroup sync.WaitGroup
	for incrementIndex := range concurrentIncrements {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			count, remainingWindow, incrementError := redisCache.IncrementRateLimit(testContext, rateLimitKey, window)
			if incrementError != nil {
				t.Errorf("increment Redis rate-limit counter: %v", incrementError)
				return
			}
			counts[incrementIndex] = count
			remainingWindows[incrementIndex] = remainingWindow
		}()
	}
	waitGroup.Wait()

	sort.Slice(counts, func(leftIndex, rightIndex int) bool {
		return counts[leftIndex] < counts[rightIndex]
	})
	for countIndex, count := range counts {
		expectedCount := int64(countIndex + 1)
		if count != expectedCount {
			t.Fatalf("sorted count[%d] = %d, want %d", countIndex, count, expectedCount)
		}
	}
	for _, remainingWindow := range remainingWindows {
		if remainingWindow <= 0 || remainingWindow > window {
			t.Fatalf("remaining Redis window = %s, want within (0, %s]", remainingWindow, window)
		}
	}
}
