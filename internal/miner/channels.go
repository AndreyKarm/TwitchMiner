package miner

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

type rawStreamInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Stream      *struct {
		ID           string `json:"id"`
		ViewersCount int    `json:"viewersCount"`
	} `json:"stream"`
	BroadcastSettings *struct {
		Title string   `json:"title"`
		Game  *rawGame `json:"game"`
	} `json:"broadcastSettings"`
}

func (r *rawStreamInfo) toStream() *Stream {
	if r.Stream == nil {
		return nil
	}
	s := &Stream{BroadcastID: r.Stream.ID, Viewers: r.Stream.ViewersCount, DropsEnabled: true}
	if r.BroadcastSettings != nil {
		s.Title = r.BroadcastSettings.Title
		s.Game = newGame(r.BroadcastSettings.Game)
	}
	return s
}

// liveStreams lists live, drops-enabled channels for a game.
func (m *Miner) liveStreams(ctx context.Context, g *Game, limit int) ([]*Channel, error) {
	op := config.OpGameDirectory.With(map[string]any{
		"limit": limit,
		"slug":  g.Slug,
		"options": map[string]any{
			"includeRestricted": []any{"SUB_ONLY_LIVE"},
			"systemFilters":     []any{"DROPS_ENABLED"},
		},
	})
	resp, err := m.gql.Do1(ctx, op)
	if err != nil {
		return nil, err
	}
	var out []*Channel
	for _, raw := range asList(twitch.Dig(resp, "data", "game", "streams", "edges")) {
		var node struct {
			ID           string   `json:"id"`
			Title        string   `json:"title"`
			ViewersCount int      `json:"viewersCount"`
			Game         *rawGame `json:"game"`
			Broadcaster  *struct {
				ID          string `json:"id"`
				Login       string `json:"login"`
				DisplayName string `json:"displayName"`
			} `json:"broadcaster"`
		}
		if twitch.Decode(twitch.Dig(raw, "node"), &node) != nil || node.Broadcaster == nil || node.Broadcaster.ID == "" {
			continue
		}
		ch := newChannel(node.Broadcaster.ID, node.Broadcaster.Login, node.Broadcaster.DisplayName, false)
		game := newGame(node.Game)
		if game == nil {
			game = g
		}
		ch.stream = &Stream{BroadcastID: node.ID, Viewers: node.ViewersCount, DropsEnabled: true, Game: game, Title: node.Title}
		out = append(out, ch)
	}
	return out, nil
}

// bulkCheckOnline fills in streams for channels not yet shared with other
// goroutines, 20 per batched request.
func (m *Miner) bulkCheckOnline(ctx context.Context, chs []*Channel) error {
	byID := make(map[string]*Channel, len(chs))
	for _, ch := range chs {
		byID[ch.ID] = ch
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for batch := range slices.Chunk(chs, 20) {
		wg.Go(func() {
			ops := make([]any, len(batch))
			for i, ch := range batch {
				ops[i] = config.OpGetStreamInfo.With(map[string]any{"channel": ch.Login})
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
			for _, r := range resps {
				var info rawStreamInfo
				if twitch.Decode(twitch.Dig(r, "data", "user"), &info) != nil || info.ID == "" {
					continue
				}
				if ch := byID[info.ID]; ch != nil {
					if ch.DisplayName == "" {
						ch.DisplayName = info.DisplayName
					}
					ch.stream = info.toStream()
				}
			}
		})
	}
	wg.Wait()
	return first
}

// updateStream re-reads a channel's stream and reacts to the change.
func (m *Miner) updateStream(ctx context.Context, ch *Channel) {
	m.mu.Lock()
	login := ch.Login
	m.mu.Unlock()
	resp, err := m.gql.Do1(ctx, config.OpGetStreamInfo.With(map[string]any{"channel": login}))
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("checking channel status failed", "channel", login, "err", err)
		}
		return
	}
	var stream *Stream
	var info rawStreamInfo
	hasUser := twitch.Decode(twitch.Dig(resp, "data", "user"), &info) == nil && info.ID != ""
	if hasUser {
		stream = info.toStream()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if hasUser && ch.DisplayName == "" {
		ch.DisplayName = info.DisplayName
	}
	old := ch.stream
	ch.stream = stream
	if m.channels[ch.ID] == ch {
		m.onChannelUpdateLocked(ch, old, stream)
	}
}

// checkOnlineLocked schedules a status check. "stream-up" arrives before
// stream info is available, so the check waits OnlineDelay.
func (m *Miner) checkOnlineLocked(ch *Channel) {
	if ch.pendingUp != nil {
		return
	}
	ctx := m.sctx
	ch.pendingUp = time.AfterFunc(config.OnlineDelay, func() {
		m.mu.Lock()
		ch.pendingUp = nil
		m.mu.Unlock()
		if ctx.Err() == nil {
			m.updateStream(ctx, ch)
		}
	})
	m.dirty |= dirtyChannels
}

func (m *Miner) setOfflineLocked(ch *Channel) {
	ch.cancelPending()
	if ch.Online() {
		old := ch.stream
		ch.stream = nil
		m.onChannelUpdateLocked(ch, old, nil)
	}
	m.dirty |= dirtyChannels
}

// onChannelUpdateLocked reacts to a channel going online, offline or
// changing its stream, switching channels when needed.
func (m *Miner) onChannelUpdateLocked(ch *Channel, before, after *Stream) {
	watchingThis := m.watching != nil && m.watching.ID == ch.ID
	switch {
	case before == nil && after != nil:
		if m.canWatchLocked(ch) && m.shouldSwitchLocked(ch) {
			m.print(ch.Name() + " goes ONLINE, switching...")
			m.watchLocked(ch, true)
		} else {
			slog.Info("channel goes online", "channel", ch.Name())
		}
	case before != nil && after == nil:
		if watchingThis {
			m.print(ch.Name() + " goes OFFLINE, switching...")
			m.changeStateLocked(StateChannelSwitch)
		} else {
			slog.Info("channel goes offline", "channel", ch.Name())
		}
	case before != nil && after != nil:
		if watchingThis && !m.canWatchLocked(ch) {
			slog.Info("watched channel can't progress drops anymore, switching", "channel", ch.Name())
			m.changeStateLocked(StateChannelSwitch)
		} else if !watchingThis && m.canWatchLocked(ch) && m.shouldSwitchLocked(ch) {
			m.watchLocked(ch, true)
		}
	}
	m.dirty |= dirtyChannels
}

// --- PubSub handlers ---

func (m *Miner) handleStreamState(channelID string, raw json.RawMessage) {
	var msg struct {
		Type    string `json:"type"`
		Viewers int    `json:"viewers"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := m.channels[channelID]
	if ch == nil {
		return
	}
	switch msg.Type {
	case "viewcount":
		if !ch.Online() {
			m.checkOnlineLocked(ch)
		} else {
			ch.stream.Viewers = msg.Viewers
			m.dirty |= dirtyChannels
		}
	case "stream-down":
		m.setOfflineLocked(ch)
	case "stream-up":
		m.checkOnlineLocked(ch)
	}
}

// handleStreamUpdate fires on title, game or tag changes. The delayed check
// coalesces bursts of edits.
func (m *Miner) handleStreamUpdate(channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch := m.channels[channelID]; ch != nil {
		m.checkOnlineLocked(ch)
	}
}

func (m *Miner) handleDrops(raw json.RawMessage) {
	var msg struct {
		Type string `json:"type"`
		Data struct {
			DropID         string `json:"drop_id"`
			DropInstanceID string `json:"drop_instance_id"`
			Current        int    `json:"current_progress_min"`
			Required       int    `json:"required_progress_min"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	switch msg.Type {
	case "drop-progress":
		m.mu.Lock()
		defer m.mu.Unlock()
		d := m.drops[msg.Data.DropID]
		slog.Debug("drop progress from pubsub", "drop", msg.Data.DropID, "minutes", msg.Data.Current, "required", msg.Data.Required)
		if d != nil && d.CanEarn(m.watching) {
			m.updateDropMinutesLocked(d, msg.Data.Current)
		}
	case "drop-claim":
		m.onDropClaimable(msg.Data.DropID, msg.Data.DropInstanceID)
	}
}

// onDropClaimable claims a drop Twitch says is ready, then waits for the
// next drop to start before polling again.
func (m *Miner) onDropClaimable(dropID, instanceID string) {
	m.mu.Lock()
	ctx := m.sctx
	d := m.drops[dropID]
	if d == nil {
		m.mu.Unlock()
		slog.Warn("claim notice for an unknown drop", "drop", dropID)
		return
	}
	d.ClaimID = instanceID
	watching := m.watching
	m.mu.Unlock()

	m.claimDrop(ctx, d)

	// The next drop starts 4-20s after a claim; wait until Twitch stops
	// reporting the old one.
	if twitch.Sleep(ctx, 4*time.Second) != nil {
		return
	}
	if watching != nil {
		for range 8 {
			resp, err := m.gql.Do1(ctx, config.OpCurrentDrop.With(map[string]any{"channelID": watching.ID}))
			if err != nil {
				break
			}
			if id, _ := twitch.Dig(resp, "data", "currentUser", "dropCurrentSession", "dropID").(string); id != d.ID {
				break
			}
			if twitch.Sleep(ctx, 2*time.Second) != nil {
				return
			}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if d.Campaign.CanEarn(m.watching) {
		m.restartWatchingLocked()
	} else {
		m.inventoryPending = true
		signal(m.stateCh)
	}
}

func (m *Miner) handleNotifications(raw json.RawMessage) {
	var msg struct {
		Type string `json:"type"`
		Data struct {
			Notification struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"notification"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &msg) != nil || msg.Type != "create-notification" {
		return
	}
	if msg.Data.Notification.Type != "user_drop_reward_reminder_notification" {
		return
	}
	m.RequestInventoryRefresh()
	m.mu.Lock()
	ctx := m.sctx
	m.mu.Unlock()
	op := config.OpNotificationsDelete.With(map[string]any{"input": map[string]any{"id": msg.Data.Notification.ID}})
	if _, err := m.gql.Do1(ctx, op); err != nil {
		slog.Debug("deleting notification failed", "err", err)
	}
}
