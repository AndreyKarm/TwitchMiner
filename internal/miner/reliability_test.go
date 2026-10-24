package miner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// Regression tests for the reliability fixes.

func TestOnlyMiningSettingsRestartMining(t *testing.T) {
	base := config.Defaults()
	base.GamesToWatch = []string{"Rust"}
	p := newPolicy(base)

	same := base.Clone()
	same.TelegramChatID = "123"
	same.AlertEvents[config.AlertDropClaimed] = false
	same.Proxy = "http://proxy:8080"
	same.RefreshIntervalMinutes = 60
	if !p.sameMining(newPolicy(same)) {
		t.Error("alert, proxy and reload-interval changes must not restart mining")
	}

	for name, change := range map[string]func(*config.Settings){
		"games":    func(s *config.Settings) { s.GamesToWatch = []string{"Rust", "Apex"} },
		"order":    func(s *config.Settings) { s.PriorityEndingSoonest = true },
		"list":     func(s *config.Settings) { s.PriorityListOnly = false },
		"benefits": func(s *config.Settings) { s.MiningBenefits["BADGE"] = false },
		"ignore":   func(s *config.Settings) { s.DropNameBlacklist = []string{"skin"} },
		"unlinked": func(s *config.Settings) { s.AllowUnlinkedCampaigns = true },
	} {
		s := base.Clone()
		change(&s)
		if p.sameMining(newPolicy(s)) {
			t.Errorf("changing %s must restart mining", name)
		}
	}
}

func TestInventoryRetryBacksOff(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		if got := inventoryRetryDelay(i + 1); got != w {
			t.Errorf("failure %d: retry after %v, want %v", i+1, got, w)
		}
	}
}

func TestFinishedDropsAreClaimedWithoutTwitchEvent(t *testing.T) {
	m := testMiner(config.Defaults())
	c := testCampaign(m, "War Thunder",
		dropSpec{id: "done", name: "Booster", minutes: 30},
		dropSpec{id: "half", name: "Plane", minutes: 60},
		dropSpec{id: "old", name: "Old", minutes: 30, claimed: true},
	)
	c.dropByID["done"].RealMinutes = 30
	c.dropByID["half"].RealMinutes = 30
	now := time.Now()

	ready := m.readyToClaimLocked(c, "42", now)
	if len(ready) != 1 || ready[0].ID != "done" {
		t.Fatalf("only the fully watched, unclaimed drop is ready, got %v", ready)
	}
	if got := ready[0].ClaimID; got != "42#"+c.ID+"#done" {
		t.Fatalf("built claim ID = %q", got)
	}
	if again := m.readyToClaimLocked(c, "42", now.Add(time.Minute)); len(again) != 0 {
		t.Fatal("a retry must wait for claimRetry")
	}
	if again := m.readyToClaimLocked(c, "42", now.Add(claimRetry+time.Second)); len(again) != 1 {
		t.Fatal("the drop should be retried after claimRetry")
	}

	// A claim ID Twitch sent is kept.
	c.dropByID["half"].RealMinutes = 60
	c.dropByID["half"].ClaimID = "from-twitch"
	m.readyToClaimLocked(c, "42", now)
	if c.dropByID["half"].ClaimID != "from-twitch" {
		t.Fatal("a real claim ID must not be replaced")
	}
}

func TestOldCampaignsAreNotAnnouncedAsStarted(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Old", "New"}
	m := testMiner(s)
	m.seen = &seenCampaigns{path: filepath.Join(t.TempDir(), "seen.json"), ids: map[string]time.Time{}, existed: true}
	old := testCampaign(m, "Old", dropSpec{id: "o", name: "O", minutes: 30})
	old.StartsAt = time.Now().Add(-3 * 24 * time.Hour)
	testCampaign(m, "New", dropSpec{id: "n", name: "N", minutes: 30}) // started an hour ago

	alerts, _ := m.collectStartedLocked()
	if len(alerts) != 1 || alerts[0].Title != "Drops campaign started: New campaign" {
		t.Fatalf("only the recently started campaign should be announced, got %+v", alerts)
	}
	if _, ok := m.seen.ids[old.ID]; !ok {
		t.Fatal("the old campaign should still be recorded as seen")
	}
}

func TestIdleDiscoveryChecksMoreOften(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Rust"}
	m := testMiner(s)
	full := time.Duration(s.RefreshIntervalMinutes) * time.Minute

	m.catalogHidden = true
	if got := m.reloadIntervalLocked(); got != idleDiscoveryInterval {
		t.Fatalf("idle with a hidden catalog: reload every %v, want %v", got, idleDiscoveryInterval)
	}
	testCampaign(m, "Rust", dropSpec{id: "r", name: "R", minutes: 30})
	if got := m.reloadIntervalLocked(); got != full {
		t.Fatalf("with something to mine: reload every %v, want %v", got, full)
	}
	m.policy.games = []string{"Rust", "Rocket League"}
	if got := m.reloadIntervalLocked(); got != idleDiscoveryInterval {
		t.Fatalf("with a listed game waiting for a campaign: reload every %v, want %v", got, idleDiscoveryInterval)
	}
	m.inventory = nil
	m.catalogHidden = false
	if got := m.reloadIntervalLocked(); got != full {
		t.Fatalf("with a visible catalog: reload every %v, want %v", got, full)
	}
}

type nopUI struct{}

func (nopUI) Publish(string, any) {}
func (nopUI) Print(string)        {}

func TestAddingGameSearchesHiddenCatalogNow(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Rust"}
	m := testMiner(s)
	m.ui = nopUI{}
	m.stateCh = make(chan struct{}, 1)
	m.netQuality, m.netProxy = s.ConnectionQuality, s.Proxy
	m.catalogHidden = true

	reordered := s.Clone()
	reordered.GamesToWatch = []string{"rust"}
	m.SettingsChanged(reordered)
	if m.inventoryPending {
		t.Fatal("renaming or reordering listed games must not reload the inventory")
	}

	added := s.Clone()
	added.GamesToWatch = []string{"Rust", "Rocket League"}
	m.SettingsChanged(added)
	if !m.inventoryPending {
		t.Fatal("adding a game with a hidden catalog must search its channels right away")
	}

	m.inventoryPending, m.catalogHidden = false, false
	more := added.Clone()
	more.GamesToWatch = []string{"Rust", "Rocket League", "Apex"}
	m.SettingsChanged(more)
	if m.inventoryPending {
		t.Fatal("with a visible catalog the new game's campaigns are already loaded")
	}
}

func TestProgressCardSurvivesReload(t *testing.T) {
	m := testMiner(config.Defaults())
	before := testCampaign(m, "Rust", dropSpec{id: "d", name: "D", minutes: 60})
	at := time.Now().Add(-30 * time.Second)
	m.progress = &progressState{drop: before.dropByID["d"], at: at}

	// A reload replaces every drop object.
	m.inventory = nil
	after := testCampaign(m, "Rust", dropSpec{id: "d", name: "D", minutes: 60})
	m.drops = map[string]*Drop{"d": after.dropByID["d"]}
	m.relinkProgressLocked()
	if m.progress == nil || m.progress.drop != after.dropByID["d"] || !m.progress.at.Equal(at) {
		t.Fatalf("progress should point at the reloaded drop and keep its timer: %+v", m.progress)
	}

	m.drops = map[string]*Drop{}
	m.relinkProgressLocked()
	if m.progress != nil {
		t.Fatal("progress for a drop that's gone should clear")
	}

	m.watching = newChannel("1", "x", "", false)
	m.progress = &progressState{drop: after.dropByID["d"], at: at}
	m.restartWatchingLocked()
	if m.progress == nil {
		t.Fatal("restarting the watch loop must not blank the progress card")
	}
}

func TestHealth(t *testing.T) {
	m := testMiner(config.Defaults())
	now := time.Now()
	check := func(wantOK bool, wantState string) {
		t.Helper()
		if h := m.healthLocked(now); h.OK != wantOK || h.State != wantState {
			t.Fatalf("health = %+v, want ok=%v state=%s", h, wantOK, wantState)
		}
	}

	check(true, "waiting_for_login")

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.endSession = cancel
	m.sessionStarted = now.Add(-time.Minute)
	check(true, "starting")
	m.sessionStarted = now.Add(-time.Hour)
	check(false, "stale")

	m.inventoryLoaded = true
	m.lastInventoryOK = now.Add(-10 * time.Minute)
	check(true, "idle")
	m.watching = newChannel("1", "x", "", false)
	check(true, "mining")
	m.lastInventoryOK = now.Add(-3 * time.Hour)
	check(false, "stale")
	m.paused = true
	check(true, "paused")
}
