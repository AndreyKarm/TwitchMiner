package miner

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// discoverCampaigns finds campaigns for the listed games through their live
// channels. It's used when Twitch hides the campaign dashboard from this
// login type, which otherwise leaves only campaigns already in progress.
//
// Each live drops-enabled channel is asked which campaigns it offers. A
// campaign's allow list becomes the channels found carrying it, so the miner
// watches a channel that actually progresses it. Campaigns already in skip
// (the inventory) are left alone, since the inventory has their full data.
func (m *Miner) discoverCampaigns(ctx context.Context, skip map[string]any) (map[string]any, error) {
	m.mu.Lock()
	games := slices.Clone(m.policy.games)
	m.mu.Unlock()

	found := map[string]map[string]any{}
	for _, name := range games {
		chs, err := m.liveStreams(ctx, &Game{Name: name, Slug: slugify(name)}, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slog.Warn("looking up live channels failed", "game", name, "err", err)
			continue
		}
		for batch := range slices.Chunk(chs, 20) {
			ops := make([]any, len(batch))
			for i, ch := range batch {
				ops[i] = config.OpAvailableDrops.With(map[string]any{"channelID": ch.ID})
			}
			resps, err := m.gql.Do(ctx, ops...)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				slog.Warn("asking channels for their campaigns failed", "game", name, "err", err)
				continue
			}
			for i, r := range resps {
				for _, raw := range asList(twitch.Dig(r, "data", "channel", "viewerDropCampaigns")) {
					c, _ := raw.(map[string]any)
					if id, _ := c["id"].(string); id != "" && skip[id] == nil {
						addDiscovered(found, c, batch[i])
					}
				}
			}
		}
	}

	out := make(map[string]any, len(found))
	for id, c := range found {
		out[id] = c
	}
	return out, nil
}

// addDiscovered records that ch offers campaign c, building the campaign the
// first time it's seen. Only campaigns for the game ch is streaming count, so
// cross-category promotions aren't mistaken for this game's drops.
func addDiscovered(found map[string]map[string]any, c map[string]any, ch *Channel) {
	game := ch.Game()
	gameID, _ := twitch.Dig(c, "game", "id").(string)
	if game == nil || gameID != game.ID {
		return
	}
	id, _ := c["id"].(string)
	entry := found[id]
	if entry == nil {
		drops := asList(c["timeBasedDrops"])
		entry = map[string]any{
			"id":     id,
			"name":   c["name"],
			"status": "ACTIVE",
			// The campaign itself carries no start; its earliest drop starts it.
			"startAt":        earliestStart(drops),
			"endAt":          c["endAt"],
			"accountLinkURL": "",
			"game": map[string]any{
				"id": game.ID, "name": game.Name, "displayName": game.Name,
				"slug": game.Slug, "boxArtURL": game.BoxArtURL,
			},
			// Twitch offers it to this viewer; link status isn't reported.
			"self":           map[string]any{"isAccountConnected": true},
			"allow":          map[string]any{"isEnabled": true, "channels": []any{}},
			"timeBasedDrops": drops,
			"discovered":     true,
		}
		found[id] = entry
	}
	allow := entry["allow"].(map[string]any)
	channels := allow["channels"].([]any)
	for _, existing := range channels {
		if existing.(map[string]any)["id"] == ch.ID {
			return
		}
	}
	allow["channels"] = append(channels, map[string]any{"id": ch.ID, "name": ch.Login, "displayName": ch.DisplayName})
}

func earliestStart(drops []any) string {
	var best time.Time
	var bestRaw string
	for _, d := range drops {
		raw, _ := twitch.Dig(d, "startAt").(string)
		t, err := time.Parse(time.RFC3339, raw)
		if err == nil && (best.IsZero() || t.Before(best)) {
			best, bestRaw = t, raw
		}
	}
	return bestRaw
}
