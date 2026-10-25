package miner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

func TestGameIconsLearnAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game-icons.json")
	icons := loadGameIcons(path)
	if !icons.learn("War Thunder", "https://img/wt-30x40.jpg") {
		t.Fatal("a new picture should count as a change")
	}
	if icons.learn("war thunder", "https://img/wt-30x40.jpg") {
		t.Fatal("the same picture under another case is not a change")
	}
	if icons.learn("Rust", "") {
		t.Fatal("an empty URL must be ignored")
	}

	got := icons.missing([]string{"War Thunder", "Rust", "World of Tanks"})
	if !slices.Equal(got, []string{"Rust", "World of Tanks"}) {
		t.Fatalf("missing = %v", got)
	}
	icons.markTried("Rust")
	if got := icons.missing([]string{"Rust"}); len(got) != 0 {
		t.Fatalf("a name with no search match shouldn't be searched again: %v", got)
	}

	if err := config.WriteJSONAtomic(path, icons.snapshot()); err != nil {
		t.Fatal(err)
	}
	reloaded := loadGameIcons(path)
	if reloaded.snapshot()["war thunder"] != "https://img/wt-30x40.jpg" {
		t.Fatalf("pictures should survive a restart: %v", reloaded.snapshot())
	}
}

func TestUnreadableGameIconsStartEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game-icons.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadGameIcons(path).snapshot(); len(got) != 0 {
		t.Fatalf("an unreadable file should be ignored, got %v", got)
	}
}

func TestCampaignIconsAreLearned(t *testing.T) {
	m := testMiner(config.Defaults())
	m.icons = loadGameIcons(filepath.Join(t.TempDir(), "game-icons.json"))
	c := testCampaign(m, "Rust", dropSpec{id: "d", name: "D", minutes: 60})
	c.Game.BoxArtURL = "https://img/rust-{width}x{height}.jpg"
	if !m.learnCampaignIconsLocked() || m.icons.snapshot()["rust"] != c.Game.BoxArtURL {
		t.Fatalf("campaign art should be learned: %v", m.icons.snapshot())
	}
	if m.learnCampaignIconsLocked() {
		t.Fatal("learning the same art again is not a change")
	}
}
