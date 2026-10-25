package miner

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// endIn moves a campaign and all its drops to end after d.
func endIn(c *Campaign, d time.Duration) {
	c.EndsAt = time.Now().Add(d)
	for _, dr := range c.Drops {
		dr.EndsAt = c.EndsAt
	}
}

func TestLateDropsWarnOrSkip(t *testing.T) {
	for _, skip := range []bool{false, true} {
		s := config.Defaults()
		s.SkipUnfinishableDrops = skip
		m := testMiner(s)
		c := testCampaign(m, "Rust",
			dropSpec{id: "fits", name: "Fits", minutes: 60},
			dropSpec{id: "long", name: "Long", minutes: 180},
			dropSpec{id: "early", name: "Early", minutes: 90},
			dropSpec{id: "after", name: "After", minutes: 20, pre: []string{"early"}},
		)
		endIn(c, 2*time.Hour)
		// early ends before it can be watched; after fits its own window but
		// waits on early.
		c.dropByID["early"].EndsAt = time.Now().Add(time.Hour)

		late := c.lateDrops(time.Now())
		for id, want := range map[string]bool{"fits": false, "long": true, "early": true, "after": true} {
			if late[id] != want {
				t.Errorf("skip=%v: late[%s] = %v, want %v", skip, id, late[id], want)
			}
		}
		if !c.dropByID["fits"].IsMineable() {
			t.Errorf("skip=%v: a drop that fits must stay mineable", skip)
		}
		long := c.dropByID["long"]
		if long.IsMineable() == skip {
			t.Errorf("skip=%v: long drop mineable = %v", skip, long.IsMineable())
		}
		if r := long.IgnoreReason(); skip != (r != nil && r.Kind == "too_late") {
			t.Errorf("skip=%v: long drop reason = %+v", skip, r)
		}
	}
}

func TestLateDropsCountProgress(t *testing.T) {
	m := testMiner(config.Defaults())
	c := testCampaign(m, "Rust", dropSpec{id: "d", name: "D", minutes: 180})
	endIn(c, 2*time.Hour)
	if !c.lateDrops(time.Now())["d"] {
		t.Fatal("180 minutes can't fit in 2 hours")
	}
	c.dropByID["d"].RealMinutes = 90
	if c.lateDrops(time.Now())["d"] {
		t.Fatal("90 minutes left fit in 2 hours")
	}
	// A drop that hasn't started only counts its own window.
	d := c.dropByID["d"]
	d.RealMinutes = 0
	d.StartsAt = time.Now().Add(30 * time.Minute)
	d.EndsAt = d.StartsAt.Add(4 * time.Hour)
	c.EndsAt = d.EndsAt
	if c.lateDrops(time.Now())["d"] {
		t.Fatal("an upcoming drop with a 4 hour window fits 180 minutes")
	}
}

func TestSkipLateChangesMiningPolicy(t *testing.T) {
	a := config.Defaults()
	b := a.Clone()
	b.SkipUnfinishableDrops = true
	if newPolicy(a).sameMining(newPolicy(b)) {
		t.Fatal("toggling the skip setting must recalculate what's mined")
	}
}

func TestWantedQueueTotalsAndFlags(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Rust"}
	m := testMiner(s)
	c := testCampaign(m, "Rust",
		dropSpec{id: "a", name: "A", minutes: 60},
		dropSpec{id: "b", name: "B", minutes: 600},
	)
	endIn(c, 3*time.Hour)
	c.dropByID["a"].RealMinutes = 15
	tree := m.wantedTreeLocked()
	if len(tree) != 1 || len(tree[0].Campaigns) != 1 {
		t.Fatalf("want one queued campaign, got %+v", tree)
	}
	if got := tree[0].RemainingMinutes; got != 600 {
		t.Fatalf("game remaining = %d, want the longest drop's 600", got)
	}
	flags := map[string]bool{}
	for _, d := range tree[0].Campaigns[0].Drops {
		flags[d.Name] = d.WontFinish
	}
	if flags["A"] || !flags["B"] {
		t.Fatalf("won't-finish flags = %v", flags)
	}
}

func TestBannedChannelIsNotWatched(t *testing.T) {
	s := config.Defaults()
	s.GamesToWatch = []string{"Rust"}
	m := testMiner(s)
	c := testCampaign(m, "Rust", dropSpec{id: "d", name: "D", minutes: 60})
	m.wanted = []*Game{c.Game}
	ch := newChannel("1", "streamer", "Streamer", false)
	ch.stream = &Stream{BroadcastID: "b", Game: c.Game, DropsEnabled: true}
	if !m.canWatchLocked(ch) {
		t.Fatal("setup: channel should be watchable")
	}
	ch.bannedUntil = banForever
	if m.canWatchLocked(ch) {
		t.Fatal("a channel that banned the viewer must not be watched")
	}
	ch.bannedUntil = time.Now().Add(-time.Minute)
	if !m.canWatchLocked(ch) {
		t.Fatal("a channel is watchable again once the timeout ends")
	}
}

func TestParseBanStatuses(t *testing.T) {
	var resp map[string]any
	body := `{"data":{"users":[
		{"id":"1","self":{"banStatus":null}},
		{"id":"2","self":{"banStatus":{"isPermanent":true,"expiresAt":null}}},
		{"id":"3","self":{"banStatus":{"isPermanent":false,"expiresAt":"2030-01-02T03:04:05Z"}}},
		null
	]}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	parseBanStatuses(resp, got)
	timeout := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	if len(got) != 3 || !got["1"].IsZero() || !got["2"].Equal(banForever) || !got["3"].Equal(timeout) {
		t.Fatalf("parsed %v", got)
	}
}
