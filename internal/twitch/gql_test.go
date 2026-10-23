package twitch

import (
	"context"
	"testing"
	"time"
)

func TestMergeDataPrefersPrimary(t *testing.T) {
	primary := map[string]any{
		"name":  "inventory",
		"self":  map[string]any{"isAccountConnected": true},
		"drops": []any{"from-inventory"},
	}
	secondary := map[string]any{
		"name":  "details",
		"game":  "Rust",
		"self":  map[string]any{"isAccountConnected": false, "extra": 1},
		"drops": []any{"from-details"},
	}
	got := MergeData(primary, secondary)
	if got["name"] != "inventory" || got["game"] != "Rust" {
		t.Fatalf("unexpected merge: %v", got)
	}
	self := got["self"].(map[string]any)
	if self["isAccountConnected"] != true || self["extra"] != 1 {
		t.Fatalf("nested objects should merge key by key: %v", self)
	}
	if got["drops"].([]any)[0] != "from-inventory" {
		t.Fatal("lists come from the primary side")
	}
}

func TestNullifyPath(t *testing.T) {
	r := map[string]any{"data": map[string]any{"user": map[string]any{"stream": "broken", "id": "1"}}}
	nullifyPath(r, []any{"user", "stream"})
	user := Dig(r, "data", "user").(map[string]any)
	if user["stream"] != nil || user["id"] != "1" {
		t.Fatalf("only the failed field should be cleared: %v", user)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	r := NewRateLimiter(2, 100*time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	for range 3 {
		release, err := r.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("third request started after %v, want it to wait for the window", elapsed)
	}
}

func TestBackoffCaps(t *testing.T) {
	b := NewBackoff(5 * time.Second)
	var last time.Duration
	for range 10 {
		last = b.Next()
	}
	if last != 5*time.Second {
		t.Fatalf("backoff should cap at 5s, got %v", last)
	}
}
