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
		chs, err := m.discoveryChannels(ctx, &Game{Name: name, Slug: slugify(name)})
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
				ops[i] = config.ChannelCampaigns(ch.ID)
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

// discoveryChannels lists the live channels worth asking about campaigns:
// those Twitch tags as drops-enabled plus the most-watched ones. The tag
// alone isn't reliable; channels carrying a running campaign can be missing
// from the filtered directory.
func (m *Miner) discoveryChannels(ctx context.Context, g *Game) ([]*Channel, error) {
	tagged, taggedErr := m.liveStreams(ctx, g, 30, true)
	top, topErr := m.liveStreams(ctx, g, 30, false)
	if taggedErr != nil && topErr != nil {
		return nil, taggedErr
	}
	return mergeChannels(tagged, top), nil
}

// mergeChannels joins channel lists, keeping the first copy of each channel.
func mergeChannels(lists ...[]*Channel) []*Channel {
	seen := map[string]bool{}
	var out []*Channel
	for _, l := range lists {
		for _, ch := range l {
			if !seen[ch.ID] {
				seen[ch.ID] = true
				out = append(out, ch)
			}
		}
	}
	return out
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
		status, _ := c["status"].(string)
		if status == "" {
			status = "ACTIVE"
		}
		startAt, _ := c["startAt"].(string)
		if startAt == "" {
			// Without a campaign start, its earliest drop starts it.
			startAt = earliestStart(drops)
		}
		self, _ := c["self"].(map[string]any)
		if self == nil {
			// Twitch offers it to this viewer but didn't say whether the
			// game account is linked; assume it is rather than skip it.
			self = map[string]any{"isAccountConnected": true}
		}
		entry = map[string]any{
			"id":             id,
			"name":           c["name"],
			"status":         status,
			"startAt":        startAt,
			"endAt":          c["endAt"],
			"accountLinkURL": c["accountLinkURL"],
			"game": map[string]any{
				"id": game.ID, "name": game.Name, "displayName": game.Name,
				"slug": game.Slug, "boxArtURL": game.BoxArtURL,
			},
			"self":           self,
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

// GameResult is a Twitch category matching a search.
type GameResult struct {
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// SearchGames looks up Twitch categories by name, so games without a known
// campaign can be added under the exact name their directory uses.
func (m *Miner) SearchGames(ctx context.Context, query string) ([]GameResult, error) {
	resp, err := m.gql.Do1(ctx, config.SearchGames(query, 8))
	if err != nil {
		return nil, err
	}
	out := []GameResult{}
	for _, raw := range asList(twitch.Dig(resp, "data", "searchCategories", "edges")) {
		var node rawGame
		if twitch.Decode(twitch.Dig(raw, "node"), &node) != nil {
			continue
		}
		if g := newGame(&node); g != nil {
			out = append(out, GameResult{Name: g.Name, Icon: g.BoxArtURL})
		}
	}
	return out, nil
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
