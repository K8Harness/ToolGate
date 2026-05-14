package main

import (
	"sync"
	"testing"
)

func TestSessionRegistryCreateGetDelete(t *testing.T) {
	var registry SessionRegistry

	session := registry.Create()
	if session == nil {
		t.Fatalf("Create() = nil, want session")
	}
	if session.ID == "" {
		t.Fatalf("Create() ID = empty, want generated ID")
	}
	if session.CreatedAt.IsZero() {
		t.Fatalf("Create() CreatedAt = zero, want creation timestamp")
	}

	stored, ok := registry.Get(session.ID)
	if !ok {
		t.Fatalf("Get(%q) ok = false, want true", session.ID)
	}
	if stored == nil {
		t.Fatalf("Get(%q) session = nil, want stored session", session.ID)
	}
	if stored.ID != session.ID {
		t.Fatalf("Get(%q) ID = %q, want %q", session.ID, stored.ID, session.ID)
	}
	if !stored.CreatedAt.Equal(session.CreatedAt) {
		t.Fatalf("Get(%q) CreatedAt = %s, want %s", session.ID, stored.CreatedAt, session.CreatedAt)
	}

	registry.Delete(session.ID)
	if stored, ok := registry.Get(session.ID); ok || stored != nil {
		t.Fatalf("Get(%q) after Delete() = (%#v, %t), want (nil, false)", session.ID, stored, ok)
	}
}

func TestSessionRegistryConcurrentCreateReturnsDistinctIDs(t *testing.T) {
	var registry SessionRegistry
	const workers = 10

	ids := make(chan string, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			session := registry.Create()
			if session == nil {
				t.Errorf("Create() = nil, want session")
				return
			}
			if session.ID == "" {
				t.Errorf("Create() ID = empty, want generated ID")
				return
			}
			ids <- session.ID
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]struct{}, workers)
	for id := range ids {
		if _, exists := seen[id]; exists {
			t.Fatalf("Create() generated duplicate ID %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != workers {
		t.Fatalf("Create() generated %d IDs, want %d", len(seen), workers)
	}
}
