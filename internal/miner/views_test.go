package miner

import (
	"slices"
	"testing"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

func TestCampaignViewsHideLongExpired(t *testing.T) {
	s := config.Defaults()
	s.HideExpiredAfterHours = 6
	m := testMiner(s)
	ended := func(game string, ago time.Duration) {
		c := testCampaign(m, game, dropSpec{id: game + "-d", name: "Drop", minutes: 60})
		c.StartsAt, c.EndsAt = time.Now().Add(-ago-48*time.Hour), time.Now().Add(-ago)
		for _, d := range c.Drops {
			d.StartsAt, d.EndsAt = c.StartsAt, c.EndsAt
		}
	}
	testCampaign(m, "Live", dropSpec{id: "live-d", name: "Drop", minutes: 60})
	ended("Recent", time.Hour)
	ended("Old", 7*time.Hour)

	shown := func() []string {
		var names []string
		for _, v := range m.campaignViewsLocked() {
			names = append(names, v.GameName)
		}
		return names
	}
	if got := shown(); !slices.Equal(got, []string{"Live", "Recent"}) {
		t.Fatalf("after 6h: want Live and Recent shown, got %v", got)
	}

	s.HideExpiredAfterHours = 0
	m.policy = newPolicy(s)
	if got := shown(); !slices.Equal(got, []string{"Live"}) {
		t.Fatalf("after 0h: want only Live shown, got %v", got)
	}
}
