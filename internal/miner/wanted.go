package miner

import (
	"slices"
	"strings"
	"time"
)

// WantedDrop is a drop still worth mining.
type WantedDrop struct {
	Name     string   `json:"name"`
	Benefits []string `json:"benefits"`
}

// WantedCampaign groups wanted drops by campaign.
type WantedCampaign struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	URL   string       `json:"url"`
	Drops []WantedDrop `json:"drops"`
	ends  time.Time
}

// WantedGame is one entry of the mining queue, in priority order.
type WantedGame struct {
	GameID    string           `json:"game_id"`
	GameName  string           `json:"game_name"`
	GameIcon  string           `json:"game_icon"`
	Fallback  bool             `json:"fallback"`
	Campaigns []WantedCampaign `json:"campaigns"`
	game      *Game
}

func (m *Miner) wantedDropsLocked(c *Campaign, now time.Time) []WantedDrop {
	var out []WantedDrop
	for _, d := range c.Drops {
		if !d.IsWatchDrop() || d.IsClaimed || !d.EndsAt.After(now) || !d.IsMineable() {
			continue
		}
		if bens := d.wantedUnclaimedBenefits(m.policy.benefits); len(bens) > 0 {
			out = append(out, WantedDrop{Name: d.Name, Benefits: bens})
		}
	}
	return out
}

func newWantedCampaign(c *Campaign, drops []WantedDrop) WantedCampaign {
	return WantedCampaign{ID: c.ID, Name: c.Name, URL: c.URL(), Drops: drops, ends: c.EndsAt}
}

// wantedTreeLocked builds the mining queue: listed games in priority order,
// then (unless restricted to the list) other eligible games ordered by the
// earliest campaign end. Only campaigns earnable within the next hour count.
func (m *Miner) wantedTreeLocked() []WantedGame {
	now := time.Now()
	nextHour := now.Add(time.Hour)
	var out []WantedGame
	queued := map[string]bool{}

	for _, name := range m.policy.games {
		var game *Game
		var campaigns []WantedCampaign
		for _, c := range m.inventory {
			if !strings.EqualFold(c.Game.Name, name) {
				continue
			}
			if game == nil {
				game = c.Game
			}
			if !c.CanEarnWithin(nextHour) {
				continue
			}
			if drops := m.wantedDropsLocked(c, now); len(drops) > 0 {
				campaigns = append(campaigns, newWantedCampaign(c, drops))
			}
		}
		if len(campaigns) == 0 || queued[game.ID] {
			continue
		}
		queued[game.ID] = true
		out = append(out, WantedGame{GameID: game.ID, GameName: game.Name, GameIcon: game.BoxArtURL, Campaigns: campaigns, game: game})
	}

	if m.policy.priorityOnly {
		return out
	}
	fallback := map[string]*WantedGame{}
	var order []string
	for _, c := range m.inventory {
		if queued[c.Game.ID] || m.isListedGame(c.Game) || !c.CanEarnWithin(nextHour) {
			continue
		}
		drops := m.wantedDropsLocked(c, now)
		if len(drops) == 0 {
			continue
		}
		g := fallback[c.Game.ID]
		if g == nil {
			g = &WantedGame{GameID: c.Game.ID, GameName: c.Game.Name, GameIcon: c.Game.BoxArtURL, Fallback: true, game: c.Game}
			fallback[c.Game.ID] = g
			order = append(order, c.Game.ID)
		}
		g.Campaigns = append(g.Campaigns, newWantedCampaign(c, drops))
	}
	games := make([]*WantedGame, 0, len(order))
	for _, id := range order {
		g := fallback[id]
		slices.SortStableFunc(g.Campaigns, func(a, b WantedCampaign) int { return a.ends.Compare(b.ends) })
		games = append(games, g)
	}
	slices.SortStableFunc(games, func(a, b *WantedGame) int {
		if c := a.Campaigns[0].ends.Compare(b.Campaigns[0].ends); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.GameName), strings.ToLower(b.GameName))
	})
	for _, g := range games {
		out = append(out, *g)
	}
	return out
}
