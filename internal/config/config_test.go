package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestOpWithMergesNestedVariables(t *testing.T) {
	op := OpGameDirectory.With(map[string]any{
		"slug":    "rust",
		"options": map[string]any{"systemFilters": []any{"DROPS_ENABLED"}},
	})
	opts := op.Variables["options"].(map[string]any)
	if op.Variables["slug"] != "rust" || opts["sort"] != "RELEVANCE" {
		t.Fatalf("defaults lost while merging: %v", op.Variables)
	}
	if f := opts["systemFilters"].([]any); len(f) != 1 {
		t.Fatalf("override not applied: %v", opts)
	}
	if base := OpGameDirectory.Variables["options"].(map[string]any)["systemFilters"].([]any); len(base) != 0 {
		t.Fatal("With must not modify the shared operation")
	}
}

func TestStorePatchValidatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Patch([]byte(`{"games_to_watch":[" Rust ","rust","","Apex Legends"],"connection_quality":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Rust", "Apex Legends"}; !slices.Equal(s.GamesToWatch, want) {
		t.Fatalf("games = %v, want %v", s.GamesToWatch, want)
	}
	if s.ConnectionQuality != 6 {
		t.Fatalf("quality should clamp to 6, got %d", s.ConnectionQuality)
	}

	if _, err := st.Patch([]byte(`{"proxy":"ftp://nope"}`)); err == nil {
		t.Fatal("unsupported proxy scheme should be rejected")
	}
	if st.Get().Proxy != "" {
		t.Fatal("a rejected patch must not change settings")
	}

	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get().GamesToWatch; !slices.Equal(got, s.GamesToWatch) {
		t.Fatalf("persisted games = %v", got)
	}
}

func TestLoadStoreFillsMissingBenefits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"mining_benefits":{"BADGE":false,"BOGUS":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b := st.Get().MiningBenefits
	if b["BADGE"] || !b["EMOTE"] || len(b) != len(BenefitTypes) {
		t.Fatalf("benefits = %v", b)
	}
}
