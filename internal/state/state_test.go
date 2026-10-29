package state

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPutRemove(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "nested", "parked.json")}
	if e, err := s.Load(); err != nil || len(e) != 0 {
		t.Fatalf("empty Load = %v, %v", e, err)
	}
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if err := s.Put(Entry{SessionID: "a", Name: "one", ParkedAt: at, Pending: true}); err != nil {
		t.Fatal(err)
	}
	// Put replaces the entry for the same session.
	if err := s.Put(Entry{SessionID: "a", Name: "one", ParkedAt: at}); err != nil {
		t.Fatal(err)
	}
	_ = s.Put(Entry{SessionID: "b"})
	e, _ := s.Load()
	if len(e) != 2 || e[0].Pending || !e[0].ParkedAt.Equal(at) {
		t.Fatalf("entries = %+v", e)
	}
	_ = s.Remove("a", "missing")
	if e, _ := s.Load(); len(e) != 1 || e[0].SessionID != "b" {
		t.Fatalf("after Remove = %+v", e)
	}
}

func TestConcurrentPuts(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "parked.json")}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(Entry{SessionID: string(rune('a' + i))}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if e, _ := s.Load(); len(e) != 20 {
		t.Fatalf("got %d entries, want 20 (lost updates)", len(e))
	}
}
