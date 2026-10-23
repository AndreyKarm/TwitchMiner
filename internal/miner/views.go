package miner

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Views are the JSON shapes the dashboard renders.

type ChannelView struct {
	ID           string `json:"id"`
	Login        string `json:"login"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	Game         string `json:"game,omitempty"`
	GameID       string `json:"game_id,omitempty"`
	GameIcon     string `json:"game_icon,omitempty"`
	Viewers      int    `json:"viewers"`
	Online       bool   `json:"online"`
	Pending      bool   `json:"pending"`
	DropsEnabled bool   `json:"drops_enabled"`
	ACLBased     bool   `json:"acl_based"`
	Watching     bool   `json:"watching"`
	Watchable    bool   `json:"watchable"`
}

type BenefitView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

type DropView struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	CurrentMinutes  int           `json:"current_minutes"`
	RequiredMinutes int           `json:"required_minutes"`
	Progress        float64       `json:"progress"`
	IsClaimed       bool          `json:"is_claimed"`
	CanClaim        bool          `json:"can_claim"`
	IsMineable      bool          `json:"is_mineable"`
	IsIgnored       bool          `json:"is_ignored"`
	IsSkipped       bool          `json:"is_skipped"`
	IgnoredReason   string        `json:"ignored_reason,omitempty"`
	IgnoredDetail   string        `json:"ignored_detail,omitempty"`
	Benefits        []BenefitView `json:"benefits"`
	StartsAt        time.Time     `json:"starts_at"`
	EndsAt          time.Time     `json:"ends_at"`
}

type CampaignView struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	GameID         string     `json:"game_id"`
	GameName       string     `json:"game_name"`
	GameIcon       string     `json:"game_icon"`
	URL            string     `json:"url"`
	LinkURL        string     `json:"link_url"`
	Linked         bool       `json:"linked"`
	Discovered     bool       `json:"discovered"`
	Eligible       bool       `json:"eligible"`
	StartsAt       time.Time  `json:"starts_at"`
	EndsAt         time.Time  `json:"ends_at"`
	Active         bool       `json:"active"`
	Upcoming       bool       `json:"upcoming"`
	Expired        bool       `json:"expired"`
	ClaimedDrops   int        `json:"claimed_drops"`
	TotalDrops     int        `json:"total_drops"`
	Finished       bool       `json:"finished"`
	MiningFinished bool       `json:"mining_finished"`
	Drops          []DropView `json:"drops"`
}

type ProgressView struct {
	DropID           string  `json:"drop_id"`
	DropName         string  `json:"drop_name"`
	Rewards          string  `json:"rewards"`
	BenefitImage     string  `json:"benefit_image,omitempty"`
	CampaignID       string  `json:"campaign_id"`
	CampaignName     string  `json:"campaign_name"`
	CampaignURL      string  `json:"campaign_url"`
	GameName         string  `json:"game_name"`
	GameIcon         string  `json:"game_icon"`
	CurrentMinutes   int     `json:"current_minutes"`
	RequiredMinutes  int     `json:"required_minutes"`
	Progress         float64 `json:"progress"`
	RemainingMinutes int     `json:"remaining_minutes"`
	CampaignProgress float64 `json:"campaign_progress"`
	UpdatedAt        int64   `json:"updated_at"`
	Channel          string  `json:"channel,omitempty"`
	ChannelLogin     string  `json:"channel_login,omitempty"`
}

type ManualView struct {
	Active  bool   `json:"active"`
	Game    string `json:"game,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// uiLoop publishes dirty sections at most once per interval, coalescing
// bursts such as viewer count updates.
func (m *Miner) uiLoop(ctx context.Context) {
	tick := time.NewTicker(750 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		m.mu.Lock()
		dirty := m.dirty
		m.dirty = 0
		if dirty == 0 {
			m.mu.Unlock()
			continue
		}
		sections := map[string]any{}
		if dirty&dirtyChannels != 0 {
			sections["channels"] = m.channelViewsLocked()
		}
		if dirty&dirtyInventory != 0 {
			sections["inventory"] = m.campaignViewsLocked()
		}
		if dirty&(dirtyProgress|dirtyInventory) != 0 {
			sections["progress"] = m.progressViewLocked()
		}
		if dirty&dirtyWanted != 0 {
			tree := m.wantedTreeLocked()
			if tree == nil {
				tree = []WantedGame{}
			}
			sections["wanted"] = tree
		}
		if dirty&dirtyManual != 0 {
			v := ManualView{Active: m.manualGame != nil}
			if m.manualGame != nil {
				v.Game = m.manualGame.Name
			}
			if m.manualChannel != nil {
				v.Channel = m.manualChannel.Name()
			}
			sections["manual"] = v
		}
		if dirty&dirtyGames != 0 {
			sections["games"] = m.gameNamesLocked()
		}
		m.mu.Unlock()
		for name, v := range sections {
			m.ui.Publish(name, v)
		}
	}
}

func (m *Miner) channelViewsLocked() []ChannelView {
	out := []ChannelView{}
	for _, ch := range m.orderedChannelsLocked() {
		v := ChannelView{
			ID:           ch.ID,
			Login:        ch.Login,
			Name:         ch.Name(),
			URL:          ch.URL(),
			Viewers:      ch.Viewers(),
			Online:       ch.Online(),
			Pending:      ch.PendingOnline(),
			DropsEnabled: ch.DropsEnabled(),
			ACLBased:     ch.ACLBased,
			Watching:     m.watching != nil && m.watching.ID == ch.ID,
			Watchable:    m.canWatchLocked(ch),
		}
		if g := ch.Game(); g != nil {
			v.Game, v.GameID, v.GameIcon = g.Name, g.ID, g.BoxArtURL
		}
		out = append(out, v)
	}
	return out
}

func (m *Miner) campaignViewsLocked() []CampaignView {
	out := []CampaignView{}
	for _, c := range m.inventory {
		v := CampaignView{
			ID:         c.ID,
			Name:       c.Name,
			GameID:     c.Game.ID,
			GameName:   c.Game.Name,
			GameIcon:   c.Game.BoxArtURL,
			URL:        c.URL(),
			LinkURL:    c.LinkURL,
			Linked:     c.Linked,
			Discovered: c.Discovered,
			Eligible:   c.Eligible(),
			StartsAt:   c.StartsAt,
			EndsAt:     c.EndsAt,
			Active:     c.Active(),
			Upcoming:   c.Upcoming(),
			Expired:    c.Expired(),
			Drops:      []DropView{},
		}
		mineable := false
		for _, d := range c.watchDrops() {
			dv := dropView(d)
			v.Drops = append(v.Drops, dv)
			if dv.IsMineable {
				mineable = true
			}
		}
		if len(v.Drops) == 0 {
			continue // nothing to watch for, like subscription rewards
		}
		v.ClaimedDrops, v.TotalDrops = c.claimedCount()
		v.Finished = v.ClaimedDrops == v.TotalDrops
		v.MiningFinished = !mineable
		out = append(out, v)
	}
	return out
}

func dropView(d *Drop) DropView {
	v := DropView{
		ID:              d.ID,
		Name:            d.Name,
		CurrentMinutes:  min(d.CurrentMinutes(), d.RequiredMinutes),
		RequiredMinutes: d.RequiredMinutes,
		Progress:        d.Progress(),
		IsClaimed:       d.IsClaimed,
		CanClaim:        d.CanClaim(),
		IsMineable:      d.IsMineable(),
		Benefits:        []BenefitView{},
		StartsAt:        d.StartsAt,
		EndsAt:          d.EndsAt,
	}
	if r := d.IgnoreReason(); r != nil {
		v.IsIgnored, v.IgnoredReason, v.IgnoredDetail = true, r.Kind, r.Detail
	}
	v.IsSkipped = !d.IsClaimed && !v.IsIgnored && !v.IsMineable
	for _, b := range d.Benefits {
		v.Benefits = append(v.Benefits, BenefitView{Name: b.Name, Type: b.Type, ImageURL: b.ImageURL})
	}
	return v
}

// progressViewLocked describes the drop being mined, or nil.
func (m *Miner) progressViewLocked() *ProgressView {
	p := m.progress
	if p == nil {
		return nil
	}
	d, c := p.drop, p.drop.Campaign
	v := &ProgressView{
		DropID:           d.ID,
		DropName:         d.Name,
		Rewards:          d.rewardsText(),
		CampaignID:       c.ID,
		CampaignName:     c.Name,
		CampaignURL:      c.URL(),
		GameName:         c.Game.Name,
		GameIcon:         c.Game.BoxArtURL,
		CurrentMinutes:   min(d.CurrentMinutes(), d.RequiredMinutes),
		RequiredMinutes:  d.RequiredMinutes,
		Progress:         d.Progress(),
		RemainingMinutes: max(d.RemainingMinutes(), 0),
		UpdatedAt:        p.at.UnixMilli(),
	}
	if len(d.Benefits) > 0 {
		v.BenefitImage = d.Benefits[0].ImageURL
	}
	if claimed, total := c.claimedCount(); total > 0 {
		v.CampaignProgress = float64(claimed) / float64(total)
	}
	if m.watching != nil {
		v.Channel, v.ChannelLogin = m.watching.Name(), m.watching.Login
	}
	return v
}

// gameNamesLocked lists games with campaigns, for the settings game picker.
func (m *Miner) gameNamesLocked() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range m.inventory {
		if c.Expired() || seen[c.Game.ID] {
			continue
		}
		seen[c.Game.ID] = true
		out = append(out, c.Game.Name)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}
