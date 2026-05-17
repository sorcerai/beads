package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoardCache_SingleflightCollapsesConcurrent(t *testing.T) {
	var calls int32
	bc := newBoardCache(50*time.Millisecond, func(_ context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		return []byte(`{"projects":[]}`), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = bc.get(context.Background()) }()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("singleflight should collapse 25 concurrent callers to 1, got %d", got)
	}
}

func TestBoardCache_LastGoodOnError(t *testing.T) {
	fail := false
	bc := newBoardCache(time.Millisecond, func(_ context.Context) ([]byte, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return []byte(`{"ok":true}`), nil
	})
	if _, stale, err := bc.get(context.Background()); err != nil || stale {
		t.Fatalf("first call should be fresh: stale=%v err=%v", stale, err)
	}
	time.Sleep(2 * time.Millisecond)
	fail = true
	body, stale, err := bc.get(context.Background())
	if err != nil || !stale || string(body) != `{"ok":true}` {
		t.Fatalf("on backend error: want last-good + stale, got body=%q stale=%v err=%v", body, stale, err)
	}
}

func TestBoardCache_GoodTimestampIsFetchTimeNotNow(t *testing.T) {
	fail := false
	bc := newBoardCache(time.Millisecond, func(_ context.Context) ([]byte, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return []byte(`{"ok":true}`), nil
	})
	if _, _, err := bc.get(context.Background()); err != nil {
		t.Fatalf("seed fetch: %v", err)
	}
	fetchedAt := bc.goodTimestamp()
	if fetchedAt.IsZero() {
		t.Fatal("goodTimestamp must be set after a successful fetch")
	}
	time.Sleep(15 * time.Millisecond)
	fail = true
	if _, stale, _ := bc.get(context.Background()); !stale {
		t.Fatal("expected stale after backend error")
	}
	// The stale banner must report the original fetch time, not "now".
	if !bc.goodTimestamp().Equal(fetchedAt) {
		t.Fatalf("goodTimestamp moved: was %v now %v (banner would mislead)", fetchedAt, bc.goodTimestamp())
	}
	if time.Since(bc.goodTimestamp()) < 10*time.Millisecond {
		t.Fatal("goodTimestamp should reflect the older fetch, not the recent failed attempt")
	}
}
