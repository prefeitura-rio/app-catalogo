package datasource

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type salesForceSyncServiceStub struct {
	mutex       sync.Mutex
	fullCalls   int
	deltaCalls  int
	fullError   error
	fullStarted chan struct{}
	releaseFull chan struct{}
}

func (service *salesForceSyncServiceStub) FullSync(context.Context) (int, error) {
	service.mutex.Lock()
	service.fullCalls++
	fullError := service.fullError
	fullStarted := service.fullStarted
	releaseFull := service.releaseFull
	service.mutex.Unlock()
	if fullStarted != nil {
		fullStarted <- struct{}{}
	}
	if releaseFull != nil {
		<-releaseFull
	}
	return 1, fullError
}

func (service *salesForceSyncServiceStub) DeltaSync(context.Context) (int, error) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.deltaCalls++
	return 2, nil
}

func (service *salesForceSyncServiceStub) callCounts() (int, int) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.fullCalls, service.deltaCalls
}

func TestSalesForceDataSourceRunsInitialAndPeriodicFullSyncs(t *testing.T) {
	currentTime := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	fullSyncInterval := 24 * time.Hour
	service := &salesForceSyncServiceStub{}
	dataSource := newSalesForceDataSource(
		service,
		15*time.Minute,
		fullSyncInterval,
		func() time.Time { return currentTime },
	)

	if changed, err := dataSource.Sync(context.Background()); err != nil || changed != 1 {
		t.Fatalf("initial Sync() = %d, %v; want successful full sync", changed, err)
	}
	currentTime = currentTime.Add(fullSyncInterval - time.Second)
	if changed, err := dataSource.Sync(context.Background()); err != nil || changed != 2 {
		t.Fatalf("intermediate Sync() = %d, %v; want delta sync", changed, err)
	}
	currentTime = currentTime.Add(time.Second)
	if changed, err := dataSource.Sync(context.Background()); err != nil || changed != 1 {
		t.Fatalf("periodic Sync() = %d, %v; want full sync", changed, err)
	}

	fullCalls, deltaCalls := service.callCounts()
	if fullCalls != 2 || deltaCalls != 1 {
		t.Fatalf("calls = full %d, delta %d; want full 2, delta 1", fullCalls, deltaCalls)
	}
}

func TestSalesForceDataSourceRetriesInitialFullSyncAfterFailure(t *testing.T) {
	currentTime := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	service := &salesForceSyncServiceStub{fullError: errors.New("upstream unavailable")}
	dataSource := newSalesForceDataSource(
		service,
		time.Minute,
		time.Hour,
		func() time.Time { return currentTime },
	)

	if _, err := dataSource.Sync(context.Background()); err == nil {
		t.Fatal("initial Sync accepted a failed full sync")
	}
	service.mutex.Lock()
	service.fullError = nil
	service.mutex.Unlock()
	if _, err := dataSource.Sync(context.Background()); err != nil {
		t.Fatalf("retry Sync returned error: %v", err)
	}

	fullCalls, deltaCalls := service.callCounts()
	if fullCalls != 2 || deltaCalls != 0 {
		t.Fatalf("calls = full %d, delta %d; want two full attempts", fullCalls, deltaCalls)
	}
}

func TestSalesForceDataSourceSerializesConcurrentSyncCalls(t *testing.T) {
	service := &salesForceSyncServiceStub{
		fullStarted: make(chan struct{}, 1),
		releaseFull: make(chan struct{}),
	}
	dataSource := newSalesForceDataSource(service, time.Minute, time.Hour, time.Now)
	firstCompleted := make(chan struct{})
	secondCompleted := make(chan struct{})

	go func() {
		defer close(firstCompleted)
		_, _ = dataSource.Sync(context.Background())
	}()
	select {
	case <-service.fullStarted:
	case <-time.After(time.Second):
		t.Fatal("first full sync did not start")
	}
	go func() {
		defer close(secondCompleted)
		_, _ = dataSource.Sync(context.Background())
	}()

	select {
	case <-secondCompleted:
		t.Fatal("second sync completed while first sync still held the local lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(service.releaseFull)
	select {
	case <-firstCompleted:
	case <-time.After(time.Second):
		t.Fatal("first sync did not complete")
	}
	select {
	case <-secondCompleted:
	case <-time.After(time.Second):
		t.Fatal("second sync did not complete after local lock was released")
	}

	fullCalls, deltaCalls := service.callCounts()
	if fullCalls != 1 || deltaCalls != 1 {
		t.Fatalf("calls = full %d, delta %d; want serialized full then delta", fullCalls, deltaCalls)
	}
}
