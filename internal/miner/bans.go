package miner

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// banForever is the end of a permanent ban.
var banForever = time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)

// banRecheck spaces out ban checks of the watched channel. They only run
// while Twitch reports no progress there, which is what a ban looks like.
const banRecheck = 5 * time.Minute

// banStatuses asks Twitch whether the viewer is banned or timed out in each
// channel, 100 channels per request. It maps every channel Twitch answered
// for to the end of its ban, or the zero time when there's none.
func (m *Miner) banStatuses(ctx context.Context, ids []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(ids))
	for batch := range slices.Chunk(ids, 100) {
		resp, err := m.gql.Do1(ctx, config.BanStatuses(batch))
		if err != nil {
			return out, err
		}
		parseBanStatuses(resp, out)
	}
	return out, nil
}

// parseBanStatuses adds each user in a BanStatuses reply to out.
func parseBanStatuses(resp map[string]any, out map[string]time.Time) {
	for _, raw := range asList(twitch.Dig(resp, "data", "users")) {
		var u struct {
			ID   string `json:"id"`
			Self *struct {
				BanStatus *struct {
					IsPermanent bool       `json:"isPermanent"`
					ExpiresAt   *time.Time `json:"expiresAt"`
				} `json:"banStatus"`
			} `json:"self"`
		}
		if twitch.Decode(raw, &u) != nil || u.ID == "" {
			continue
		}
		var until time.Time
		if u.Self != nil && u.Self.BanStatus != nil {
			until = banForever
			if b := u.Self.BanStatus; !b.IsPermanent && b.ExpiresAt != nil {
				until = *b.ExpiresAt
			}
		}
		out[u.ID] = until
	}
}

// fillBans looks up the ban status of channels not yet shared with other
// goroutines. A failed lookup leaves them unbanned: the watched channel is
// checked again if Twitch doesn't credit it.
func (m *Miner) fillBans(ctx context.Context, chs []*Channel) {
	if len(chs) == 0 {
		return
	}
	ids := make([]string, len(chs))
	for i, ch := range chs {
		ids[i] = ch.ID
	}
	bans, err := m.banStatuses(ctx, ids)
	if err != nil && ctx.Err() == nil {
		slog.Warn("checking channel bans failed", "err", err)
	}
	now := time.Now()
	for _, ch := range chs {
		if until, ok := bans[ch.ID]; ok {
			ch.bannedUntil, ch.banChecked = until, now
		}
	}
}

// recheckBan asks again whether the viewer is banned from ch, at most once
// per banRecheck, and switches channel if they now are.
func (m *Miner) recheckBan(ctx context.Context, ch *Channel) {
	m.mu.Lock()
	due := time.Since(ch.banChecked) >= banRecheck
	if due {
		ch.banChecked = time.Now()
	}
	id := ch.ID
	m.mu.Unlock()
	if !due {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	bans, err := m.banStatuses(qctx, []string{id})
	cancel()
	if err != nil {
		slog.Debug("checking channel ban failed", "channel", ch.Login, "err", err)
		return
	}
	until, ok := bans[id]
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	was := ch.Banned(now)
	ch.bannedUntil = until
	if ch.Banned(now) == was {
		return
	}
	m.dirty |= dirtyChannels
	if !was {
		m.banFoundLocked(ch)
		if m.watching == ch {
			m.changeStateLocked(StateChannelSwitch)
		}
	}
}

// banFoundLocked reports a ban once per channel and ban, and for a timeout
// reconsiders the channel when it ends.
func (m *Miner) banFoundLocked(ch *Channel) {
	if m.bansNoted == nil {
		m.bansNoted = map[string]time.Time{}
	}
	if m.bansNoted[ch.ID].Equal(ch.bannedUntil) {
		return
	}
	m.bansNoted[ch.ID] = ch.bannedUntil
	m.print(banText(ch.Name(), ch.bannedUntil, time.Now()))
	if ch.bannedUntil.Equal(banForever) {
		return
	}
	ctx := m.sctx
	time.AfterFunc(time.Until(ch.bannedUntil), func() {
		defer m.recoverPanic("timeout end")
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.channels[ch.ID] != ch || ch.Banned(time.Now()) {
			return
		}
		m.dirty |= dirtyChannels
		if m.canWatchLocked(ch) && m.shouldSwitchLocked(ch) {
			m.changeStateLocked(StateChannelSwitch)
		}
	})
}

func banText(channel string, until, now time.Time) string {
	if until.Equal(banForever) {
		return fmt.Sprintf("You're banned from %s's chat, so it can't earn drops. Skipping that channel.", channel)
	}
	layout := "15:04"
	if until.Sub(now) >= 24*time.Hour {
		layout = "Jan 2 15:04"
	}
	return fmt.Sprintf("You're timed out in %s's chat until %s, so it can't earn drops until then. Skipping that channel.",
		channel, until.Local().Format(layout))
}
