package miner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// fetchInventory loads in-progress and available campaigns, merges in
// their details and replaces the miner's inventory.
func (m *Miner) fetchInventory(ctx context.Context) error {
	m.setStatus("Fetching inventory...")
	resp, err := m.gql.Do1(ctx, config.OpInventory)
	if err != nil {
		return err
	}
	inv, ok := twitch.Dig(resp, "data", "currentUser", "inventory").(map[string]any)
	if !ok {
		return errors.New("twitch returned no inventory")
	}
	data := map[string]any{}
	for _, raw := range asList(inv["dropCampaignsInProgress"]) {
		if c, ok := raw.(map[string]any); ok {
			if id, _ := c["id"].(string); id != "" {
				data[id] = c
			}
		}
	}
	// gameEventDrops lists claimed benefit IDs, not drop IDs.
	claimed := map[string]time.Time{}
	for _, raw := range asList(inv["gameEventDrops"]) {
		var b struct {
			ID            string    `json:"id"`
			LastAwardedAt time.Time `json:"lastAwardedAt"`
		}
		if twitch.Decode(raw, &b) == nil && b.ID != "" {
			claimed[b.ID] = b.LastAwardedAt
		}
	}

	resp, err = m.gql.Do1(ctx, config.OpCampaigns)
	if err != nil {
		return err
	}
	catalog := twitch.Dig(resp, "data", "currentUser", "dropCampaigns")
	m.mu.Lock()
	warn := catalog == nil && !m.catalogWarned
	m.catalogWarned = m.catalogWarned || warn
	m.mu.Unlock()
	if warn {
		m.print("Twitch hides its campaign list from this login type, so campaigns for your listed games are found through their live channels.")
	}
	if catalog == nil {
		m.setStatus("Looking for campaigns on live channels...")
		discovered, err := m.discoverCampaigns(ctx, data)
		if err != nil {
			return err
		}
		for id, c := range discovered {
			data[id] = c
		}
		if len(discovered) > 0 {
			slog.Info("found campaigns on live channels", "campaigns", len(discovered))
		}
	}
	var available []map[string]any
	for _, raw := range asList(catalog) {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if status, _ := c["status"].(string); status == "ACTIVE" || status == "UPCOMING" {
			available = append(available, c)
		}
	}

	m.setStatus("Fetching campaigns...")
	details, err := m.fetchCampaignDetails(ctx, available)
	if err != nil {
		return err
	}
	for _, chunk := range details {
		data = twitch.MergeData(data, chunk)
	}

	var campaigns []*Campaign
	for _, raw := range data {
		var rc rawCampaign
		if err := twitch.Decode(raw, &rc); err != nil {
			slog.Warn("skipping unreadable campaign", "err", err)
			continue
		}
		if rc.Game == nil || rc.Game.ID == "" {
			continue
		}
		campaigns = append(campaigns, newCampaign(m, rc, claimed))
	}

	m.mu.Lock()
	// Eligible first, then by the next relevant date, then active first.
	slices.SortStableFunc(campaigns, func(a, b *Campaign) int {
		if c := trueFirst(a.Eligible(), b.Eligible()); c != 0 {
			return c
		}
		if c := campaignTimeKey(a).Compare(campaignTimeKey(b)); c != 0 {
			return c
		}
		return trueFirst(a.Active(), b.Active())
	})
	m.inventory = campaigns
	m.campaigns = make(map[string]*Campaign, len(campaigns))
	m.drops = map[string]*Drop{}
	nextHour := time.Now().Add(time.Hour)
	var triggers []time.Time
	for _, c := range campaigns {
		m.campaigns[c.ID] = c
		for _, d := range c.Drops {
			m.drops[d.ID] = d
		}
		if c.CanEarnWithin(nextHour) {
			triggers = append(triggers, c.timeTriggers()...)
		}
	}
	slices.SortFunc(triggers, time.Time.Compare)
	triggers = slices.CompactFunc(triggers, time.Time.Equal)
	now := time.Now()
	for len(triggers) > 0 && !triggers[0].After(now) {
		triggers = triggers[1:]
	}
	m.triggers = triggers
	m.dirty |= dirtyInventory | dirtyWanted | dirtyGames
	m.mu.Unlock()

	slog.Info("inventory loaded", "campaigns", len(campaigns))
	m.restartMaintenance(ctx)
	return nil
}

// fetchCampaignDetails requests details in batches of 20 and merges each
// campaign's summary over its details.
func (m *Miner) fetchCampaignDetails(ctx context.Context, available []map[string]any) ([]map[string]any, error) {
	uid := m.auth.UserID()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []map[string]any
		first   error
	)
	for batch := range slices.Chunk(available, 20) {
		wg.Go(func() {
			ops := make([]any, len(batch))
			for i, c := range batch {
				ops[i] = config.OpCampaignDetails.With(map[string]any{"channelLogin": uid, "dropID": c["id"]})
			}
			resps, err := m.gql.Do(ctx, ops...)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			summaries := map[string]any{}
			fetched := map[string]any{}
			for _, c := range batch {
				if id, _ := c["id"].(string); id != "" {
					summaries[id] = c
				}
			}
			for _, r := range resps {
				if dc, ok := twitch.Dig(r, "data", "user", "dropCampaign").(map[string]any); ok {
					if id, _ := dc["id"].(string); id != "" {
						fetched[id] = dc
					}
				}
			}
			results = append(results, twitch.MergeData(summaries, fetched))
		})
	}
	wg.Wait()
	return results, first
}

func campaignTimeKey(c *Campaign) time.Time {
	if c.Upcoming() {
		return c.StartsAt
	}
	return c.EndsAt
}

// trueFirst is a comparator that sorts true before false.
func trueFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// claimDrop claims a finished drop. It reports whether the drop is claimed.
func (m *Miner) claimDrop(ctx context.Context, d *Drop) bool {
	m.mu.Lock()
	if d.IsClaimed {
		m.mu.Unlock()
		return true
	}
	if !d.CanClaim() {
		m.mu.Unlock()
		return false
	}
	claimID := d.ClaimID
	m.mu.Unlock()

	ok := false
	resp, err := m.gql.Do1(ctx, config.OpClaimDrop.With(map[string]any{"input": map[string]any{"dropInstanceID": claimID}}))
	if err == nil {
		status, _ := twitch.Dig(resp, "data", "claimDropRewards", "status").(string)
		ok = status == "ELIGIBLE_FOR_ALL" || status == "DROP_INSTANCE_ALREADY_CLAIMED"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !ok {
		slog.Error("drop claim may have failed", "drop", d.ID, "err", err)
		return false
	}
	if !d.IsClaimed {
		d.IsClaimed = true
		d.RealMinutes, d.ExtraMinutes = d.RequiredMinutes, 0
		c := d.Campaign
		claimedN, total := c.claimedCount()
		m.print(fmt.Sprintf("Claimed drop: %s %s (%d/%d)", c.Game.Name, d.rewardsText(), claimedN, total))
		claimed, completed := m.claimAlertsLocked(d)
		go m.sendAlerts(config.AlertDropClaimed, claimed)
		if completed != nil {
			go m.sendAlerts(config.AlertCampaignCompleted, *completed)
		}
		// A claim can leave only unwanted rewards behind; let fallback games
		// take over promptly.
		m.requestGamesUpdateLocked()
		m.dirty |= dirtyInventory | dirtyWanted
	}
	return true
}

// updateDropMinutesLocked applies authoritative progress for d to its
// whole campaign.
func (m *Miner) updateDropMinutesLocked(d *Drop, minutes int) {
	delta := minutes - d.RealMinutes
	switch {
	case d.RealMinutes+delta < 0:
		delta = -d.RealMinutes
	case d.RealMinutes+delta > d.RequiredMinutes:
		delta = d.RequiredMinutes - d.RealMinutes
	}
	if delta != 0 {
		for _, cd := range d.Campaign.Drops {
			if cd.updateRealMinutes(delta) {
				m.dirty |= dirtyInventory
			}
		}
	}
	// Progress can arrive while idle, for example from a browser watching on
	// the same account; only show it while the miner itself is watching.
	if fd := d.Campaign.FirstDrop(); fd != nil && m.watching != nil {
		m.displayDropLocked(fd)
	}
}

// bumpCampaignLocked adds an estimated minute when Twitch reports nothing.
func (m *Miner) bumpCampaignLocked(c *Campaign, ch *Channel) {
	capped := false
	for _, d := range c.Drops {
		bumped, hit := d.bumpMinutes(ch)
		if bumped {
			m.dirty |= dirtyInventory
		}
		capped = capped || hit
	}
	if capped {
		slog.Warn("a drop reached the estimated minutes limit", "campaign", c.Name, "game", c.Game.Name)
		m.changeStateLocked(StateChannelSwitch)
	}
	if fd := c.FirstDrop(); fd != nil {
		m.displayDropLocked(fd)
	}
}

// restartMaintenance schedules cleanups at campaign boundaries and the
// next periodic inventory reload.
func (m *Miner) restartMaintenance(ctx context.Context) {
	m.mu.Lock()
	if m.mntCancel != nil {
		m.mntCancel()
	}
	mctx, cancel := context.WithCancel(ctx)
	m.mntCancel = cancel
	interval := time.Duration(m.policy.refreshMinutes) * time.Minute
	m.mu.Unlock()
	go m.maintenance(mctx, interval)
}

func (m *Miner) maintenance(ctx context.Context, interval time.Duration) {
	nextPeriod := time.Now().Add(interval)
	for time.Now().Before(nextPeriod) {
		m.mu.Lock()
		next := nextPeriod
		if len(m.triggers) > 0 && !m.triggers[0].After(nextPeriod) {
			next = m.triggers[0]
			m.triggers = m.triggers[1:]
		}
		m.mu.Unlock()
		slog.Debug("maintenance waiting", "until", next.Format(time.TimeOnly), "reload", next.Equal(nextPeriod))
		if twitch.Sleep(ctx, time.Until(next)) != nil {
			return
		}
		if !time.Now().Before(nextPeriod) {
			break
		}
		if !next.Equal(nextPeriod) {
			// A campaign or drop started or ended.
			m.RequestGamesUpdate()
		}
	}
	m.RequestInventoryRefresh()
}
