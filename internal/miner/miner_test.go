package miner

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

var twitchDecode = twitch.Decode

// testMiner returns a miner with only the policy set, enough for model code.
func testMiner(s config.Settings) *Miner {
	return &Miner{policy: newPolicy(s), campaigns: map[string]*Campaign{}, drops: map[string]*Drop{}, channels: map[string]*Channel{}}
}

type dropSpec struct {
	id, name string
	minutes  int
	claimed  bool
	pre      []string
	noReward bool
}

func testCampaign(m *Miner, game string, specs ...dropSpec) *Campaign {
	now := time.Now()
	c := &Campaign{
		m: m, ID: "c-" + game, Name: game + " campaign",
		Game:     &Game{ID: "g-" + game, Name: game, Slug: slugify(game)},
		Linked:   true,
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
		valid: true, aclIDs: map[string]bool{}, dropByID: map[string]*Drop{},
	}
	for _, s := range specs {
		d := &Drop{
			Campaign: c, ID: s.id, Name: s.name, RequiredMinutes: s.minutes, IsClaimed: s.claimed,
			Preconditions: s.pre, StartsAt: c.StartsAt, EndsAt: c.EndsAt,
		}
		if !s.noReward {
			d.Benefits = []Benefit{{ID: "b-" + s.id, Name: s.name + " reward", Type: "DIRECT_ENTITLEMENT"}}
		}
		c.Drops = append(c.Drops, d)
		c.dropByID[d.ID] = d
	}
	m.inventory = append(m.inventory, c)
	return c
}

func TestIgnorePolicyBlocksDependents(t *testing.T) {
	s := config.Defaults()
	s.DropNameBlacklist = []string{"skin"}
	m := testMiner(s)
	c := testCampaign(m, "Game",
		dropSpec{id: "a", name: "Cool Skin", minutes: 30},
		dropSpec{id: "b", name: "Next", minutes: 60, pre: []string{"a"}},
		dropSpec{id: "c", name: "Other", minutes: 30},
	)
	if r := c.dropByID["a"].IgnoreReason(); r == nil || r.Kind != "keyword" {
		t.Fatalf("drop a: want keyword ignore, got %+v", r)
	}
	if r := c.dropByID["b"].IgnoreReason(); r == nil || r.Kind != "precondition" {
		t.Fatalf("drop b: want precondition ignore, got %+v", r)
	}
	if !c.dropByID["c"].IsMineable() {
		t.Fatal("drop c should stay mineable")
	}
}

func TestPrerequisiteWithoutRewardStaysMineable(t *testing.T) {
	m := testMiner(config.Defaults())
	c := testCampaign(m, "Game",
		dropSpec{id: "pre", name: "Step one", minutes: 30, noReward: true},
		dropSpec{id: "goal", name: "Prize", minutes: 30, pre: []string{"pre"}},
		dropSpec{id: "lonely", name: "Nothing", minutes: 30, noReward: true},
	)
	if !c.dropByID["pre"].IsMineable() {
		t.Error("a prerequisite of a rewarded drop must be mineable")
	}
	if c.dropByID["lonely"].IsMineable() {
		t.Error("a rewardless drop nothing depends on must not be mineable")
	}
	if got := c.dropByID["goal"].TotalRemainingMinutes(); got != 60 {
		t.Errorf("chain remaining minutes = %d, want 60", got)
	}
}

func TestPreconditionsGateEarning(t *testing.T) {
	m := testMiner(config.Defaults())
	c := testCampaign(m, "Game",
		dropSpec{id: "a", name: "First", minutes: 30},
		dropSpec{id: "b", name: "Second", minutes: 30, pre: []string{"a"}},
	)
	if c.dropByID["b"].CanEarn(nil) {
		t.Fatal("second drop can't be earned before the first is claimed")
	}
	c.dropByID["a"].IsClaimed = true
	if !c.dropByID["b"].CanEarn(nil) {
		t.Fatal("second drop should be earnable once the first is claimed")
	}
}

func TestWantedTreeFallbackOrder(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Listed"}
	s.PriorityListOnly = false
	m := testMiner(s)
	testCampaign(m, "Listed", dropSpec{id: "l", name: "L", minutes: 30})
	late := testCampaign(m, "Zeta", dropSpec{id: "z", name: "Z", minutes: 30})
	soon := testCampaign(m, "Alpha", dropSpec{id: "x", name: "X", minutes: 30})
	late.EndsAt = time.Now().Add(48 * time.Hour)
	soon.EndsAt = time.Now().Add(2 * time.Hour)
	for _, c := range []*Campaign{late, soon} {
		for _, d := range c.Drops {
			d.EndsAt = c.EndsAt
		}
	}

	var names []string
	for _, g := range m.wantedTreeLocked() {
		names = append(names, g.GameName)
	}
	if want := []string{"Listed", "Alpha", "Zeta"}; !slices.Equal(names, want) {
		t.Fatalf("queue = %v, want %v", names, want)
	}

	m.policy.priorityOnly = true
	if tree := m.wantedTreeLocked(); len(tree) != 1 {
		t.Fatalf("priority-only queue has %d games, want 1", len(tree))
	}
}

func TestEndingSoonestReordersListedGames(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Later", "Sooner", "Tie"}
	m := testMiner(s)
	base := time.Now()
	for name, hours := range map[string]time.Duration{"Later": 72, "Sooner": 5, "Tie": 72} {
		c := testCampaign(m, name, dropSpec{id: name, name: name, minutes: 30})
		c.EndsAt = base.Add(hours * time.Hour)
		c.Drops[0].EndsAt = c.EndsAt
	}
	names := func() []string {
		var out []string
		for _, g := range m.wantedTreeLocked() {
			out = append(out, g.GameName)
		}
		return out
	}
	if got, want := names(), []string{"Later", "Sooner", "Tie"}; !slices.Equal(got, want) {
		t.Fatalf("list order: got %v, want %v", got, want)
	}
	m.policy.endingSoonest = true
	if got, want := names(), []string{"Sooner", "Later", "Tie"}; !slices.Equal(got, want) {
		t.Fatalf("ending soonest: got %v, want %v", got, want)
	}
}

func TestStartedCampaignAlerts(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Listed"}
	m := testMiner(s)
	m.seen = &seenCampaigns{path: filepath.Join(t.TempDir(), "seen.json"), ids: map[string]time.Time{}}
	testCampaign(m, "Listed", dropSpec{id: "a", name: "A", minutes: 30})

	// The first run only records campaigns that are already running.
	if alerts, snap := m.collectStartedLocked(); len(alerts) != 0 || snap == nil {
		t.Fatalf("seeding run: %d alerts, snapshot %v", len(alerts), snap)
	}

	listed := testCampaign(m, "Listed", dropSpec{id: "b", name: "B", minutes: 30})
	listed.ID = "c-new"
	testCampaign(m, "Unlisted", dropSpec{id: "c", name: "C", minutes: 30})
	alerts, _ := m.collectStartedLocked()
	if len(alerts) != 1 || !strings.Contains(alerts[0].Title, "Listed campaign") {
		t.Fatalf("want one alert for the listed game, got %+v", alerts)
	}
	if again, _ := m.collectStartedLocked(); len(again) != 0 {
		t.Fatal("a campaign must only be announced once")
	}
}

func TestClaimAlerts(t *testing.T) {
	m := testMiner(config.Defaults())
	c := testCampaign(m, "Rust",
		dropSpec{id: "a", name: "First", minutes: 30, claimed: true},
		dropSpec{id: "b", name: "Second", minutes: 60},
	)
	b := c.dropByID["b"]

	claimed, completed := m.claimAlertsLocked(b)
	if completed != nil || !strings.Contains(claimed.Title, "Second reward") || !strings.Contains(strings.Join(claimed.Lines, " "), "1 of 2") {
		t.Fatalf("before the last claim: %+v, completed=%v", claimed, completed)
	}

	b.IsClaimed = true
	claimed, completed = m.claimAlertsLocked(b)
	if completed == nil || !strings.Contains(completed.Title, "Rust campaign") || !strings.Contains(strings.Join(claimed.Lines, " "), "2 of 2") {
		t.Fatalf("the last claim should complete the campaign: %+v, %+v", claimed, completed)
	}

	login := loginAlert("ABCD1234", "https://www.twitch.tv/activate")
	if !strings.Contains(strings.Join(login.Lines, " "), "ABCD1234") {
		t.Fatalf("login alert should carry the code: %+v", login)
	}
}

func TestDiscoveredCampaignsBecomeACLCampaigns(t *testing.T) {
	wt := &Game{ID: "66366", Name: "War Thunder", Slug: "war-thunder", BoxArtURL: "https://example.com/wt.jpg"}
	live := func(id, login string, g *Game) *Channel {
		ch := newChannel(id, login, login, false)
		ch.stream = &Stream{BroadcastID: "b" + id, DropsEnabled: true, Game: g}
		return ch
	}
	start := time.Now().Add(-2 * time.Hour).UTC()
	campaign := func(id, gameID string) map[string]any {
		drop := func(dropID string, minutes float64, offset time.Duration) any {
			return map[string]any{
				"id": dropID, "name": dropID, "requiredMinutesWatched": minutes,
				"startAt": start.Add(offset).Format(time.RFC3339), "endAt": start.Add(72 * time.Hour).Format(time.RFC3339),
				"benefitEdges": []any{map[string]any{"benefit": map[string]any{"id": "b-" + dropID, "name": dropID + " reward", "distributionType": "DIRECT_ENTITLEMENT"}}},
			}
		}
		return map[string]any{
			"id": id, "name": "WTCS", "endAt": start.Add(72 * time.Hour).Format(time.RFC3339),
			"game":           map[string]any{"id": gameID, "name": "x"},
			"timeBasedDrops": []any{drop("later", 120, time.Hour), drop("first", 60, 0)},
		}
	}

	found := map[string]map[string]any{}
	addDiscovered(found, campaign("wtcs", "66366"), live("1", "pl", wt))
	addDiscovered(found, campaign("wtcs", "66366"), live("2", "en", wt))
	addDiscovered(found, campaign("wtcs", "66366"), live("2", "en", wt)) // same channel twice
	// A cross-category promotion on a War Thunder stream is ignored.
	addDiscovered(found, campaign("apple", "509663"), live("1", "pl", wt))
	if len(found) != 1 {
		t.Fatalf("want only the War Thunder campaign, got %d", len(found))
	}

	var rc rawCampaign
	if err := twitchDecode(found["wtcs"], &rc); err != nil {
		t.Fatal(err)
	}
	m := testMiner(config.Defaults())
	c := newCampaign(m, rc, nil)
	if !c.Discovered || !c.Active() || !c.Eligible() || c.Game.BoxArtURL != wt.BoxArtURL {
		t.Fatalf("discovered campaign should be active and eligible: %+v", c)
	}
	if !c.StartsAt.Equal(start.Truncate(time.Second)) {
		t.Fatalf("campaign should start with its earliest drop: got %v, want %v", c.StartsAt, start)
	}
	if len(c.ACL) != 2 || !c.aclIDs["1"] || !c.aclIDs["2"] {
		t.Fatalf("allow list should be the channels carrying it: %+v", c.ACL)
	}
	if !c.allowsChannel(live("1", "pl", wt)) || c.allowsChannel(live("3", "fr", wt)) {
		t.Fatal("only channels found carrying the campaign should progress it")
	}
}

func TestCanWatchNeedsWantedGameAndACL(t *testing.T) {
	m := testMiner(config.Defaults())
	c := testCampaign(m, "Game", dropSpec{id: "a", name: "A", minutes: 30})
	c.ACL = []aclChannel{{ID: "1", Login: "allowed"}}
	c.aclIDs["1"] = true
	m.wanted = []*Game{c.Game}

	allowed := newChannel("1", "allowed", "", true)
	allowed.stream = &Stream{BroadcastID: "b1", DropsEnabled: true, Game: c.Game}
	other := newChannel("2", "other", "", false)
	other.stream = &Stream{BroadcastID: "b2", DropsEnabled: true, Game: c.Game}

	if !m.canWatchLocked(allowed) {
		t.Error("ACL channel playing the game should be watchable")
	}
	if m.canWatchLocked(other) {
		t.Error("channel outside the ACL must not be watchable")
	}
	allowed.stream.Game = &Game{ID: "elsewhere", Name: "Elsewhere"}
	if m.canWatchLocked(allowed) {
		t.Error("ACL channel streaming another category must not be watchable")
	}
}

func TestShouldSwitchPrefersHigherPriority(t *testing.T) {
	m := testMiner(config.Defaults())
	a := testCampaign(m, "A", dropSpec{id: "a", name: "A", minutes: 30})
	b := testCampaign(m, "B", dropSpec{id: "b", name: "B", minutes: 30})
	m.wanted = []*Game{a.Game, b.Game}

	low := newChannel("1", "low", "", false)
	low.stream = &Stream{BroadcastID: "1", DropsEnabled: true, Game: b.Game}
	high := newChannel("2", "high", "", false)
	high.stream = &Stream{BroadcastID: "2", DropsEnabled: true, Game: a.Game}

	m.watching = low
	if !m.shouldSwitchLocked(high) {
		t.Error("should switch to the higher priority game")
	}
	m.watching = high
	if m.shouldSwitchLocked(low) {
		t.Error("should not switch to a lower priority game")
	}
}

func TestPlaylistURLs(t *testing.T) {
	base, _ := url.Parse("https://video.example.net/v1/playlist/abc.m3u8?sig=1")
	media := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.000,live\nseg1.ts\n#EXTINF:2.000,live\nhttps://cdn.example.net/seg2.ts\n"
	got := playlistURLs(media, base, false)
	want := []string{"https://video.example.net/v1/playlist/seg1.ts", "https://cdn.example.net/seg2.ts"}
	if !slices.Equal(got, want) {
		t.Fatalf("media segments = %v, want %v", got, want)
	}

	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=6000000\nhttps://a.example.net/high.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=160000\nhttps://a.example.net/audio_only.m3u8\n"
	if got := playlistURLs(master, base, true); len(got) != 2 || got[1] != "https://a.example.net/audio_only.m3u8" {
		t.Fatalf("master variants = %v", got)
	}

	for name, bad := range map[string]string{
		"not a playlist":  "hello",
		"uri without tag": "#EXTM3U\nseg1.ts\n",
		"dangling tag":    "#EXTM3U\n#EXTINF:2.0,\n",
		"bad scheme":      "#EXTM3U\n#EXTINF:2.0,\nftp://x/seg.ts\n",
	} {
		if got := playlistURLs(bad, base, false); got != nil {
			t.Errorf("%s: want nil, got %v", name, got)
		}
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Tom Clancy's Rainbow Six Siege": "tom-clancys-rainbow-six-siege",
		"World of Tanks: HEAT":           "world-of-tanks-heat",
		"  --Rust--  ":                   "rust",
		"Pokémon UNITE":                  "pokémon-unite",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSegmentCacheEvictsOldest(t *testing.T) {
	c := newSegmentCache(2)
	c.add("a")
	c.add("b")
	c.add("c")
	if c.has("a") || !c.has("b") || !c.has("c") {
		t.Fatalf("cache should hold only b and c, got %v", c.order)
	}
}
