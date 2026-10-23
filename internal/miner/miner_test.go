package miner

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

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
