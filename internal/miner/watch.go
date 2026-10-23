package miner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// watchLoop "watches" the selected channel: it polls the stream's HLS
// playlist and HEADs new segments, which is what Twitch credits, without
// downloading any video. When Twitch goes quiet about progress it asks
// GQL for the current drop, or estimates a minute as a last resort.
func (m *Miner) watchLoop(ctx context.Context) {
	nextProgress := time.Now().Add(20 * time.Second)
	for {
		ch := m.waitForWatching(ctx)
		if ch == nil {
			return
		}
		m.mu.Lock()
		if !ch.Online() {
			if m.watching == ch {
				m.stopWatchingLocked()
			}
			m.mu.Unlock()
			continue
		}
		stream := ch.stream
		m.mu.Unlock()

		started := time.Now()
		ok := m.sendWatch(ctx, ch, stream)
		if !ok {
			slog.Debug("watch request failed", "channel", ch.Login)
		}
		m.watchSleep(ctx, config.WatchPoll-time.Since(started))
		if ctx.Err() != nil {
			return
		}
		if !m.watchCurrent(ch, stream) {
			continue
		}

		m.mu.Lock()
		stale := m.progress == nil || time.Since(m.progress.at) >= time.Minute
		m.mu.Unlock()
		if !ok || !stale || time.Now().Before(nextProgress) {
			continue
		}
		nextProgress = time.Now().Add(config.WatchInterval)

		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := m.gql.Do1(qctx, config.OpCurrentDrop.With(map[string]any{"channelID": ch.ID}))
		cancel()
		if !m.watchCurrent(ch, stream) {
			continue
		}
		m.mu.Lock()
		handled := false
		if err == nil {
			var cur struct {
				DropID  string `json:"dropID"`
				Minutes int    `json:"currentMinutesWatched"`
			}
			if twitch.Decode(twitch.Dig(resp, "data", "currentUser", "dropCurrentSession"), &cur) == nil {
				if d := m.drops[cur.DropID]; d != nil && d.CanEarn(ch) {
					m.updateDropMinutesLocked(d, cur.Minutes)
					handled = true
				}
			}
		}
		if !handled {
			if c := m.activeCampaignLocked(ch); c != nil {
				m.bumpCampaignLocked(c, ch)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Miner) waitForWatching(ctx context.Context) *Channel {
	for {
		m.mu.Lock()
		ch := m.watching
		m.mu.Unlock()
		if ch != nil {
			return ch
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.watchCh:
		}
	}
}

// watchSleep waits for d, cut short by restartWatchingLocked.
func (m *Miner) watchSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-m.watchRestart:
	}
}

// watchCurrent reports whether ch is still the watched channel and still
// on the same broadcast.
func (m *Miner) watchCurrent(ch *Channel, s *Stream) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watching == ch && ch.stream != nil && ch.stream.BroadcastID == s.BroadcastID
}

func (m *Miner) sendWatch(ctx context.Context, ch *Channel, s *Stream) bool {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ok := m.watchPlaylist(pctx, ch, s)
	cancel()
	if !ok || !m.watchCurrent(ch, s) {
		return false
	}
	// Minute-watched telemetry is auxiliary; a 204 doesn't prove credit.
	m.mu.Lock()
	due := ch.lastSpade.IsZero() || time.Since(ch.lastSpade) >= config.WatchInterval
	if due {
		ch.lastSpade = time.Now()
	}
	m.mu.Unlock()
	if due {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := m.postSpade(sctx, ch, s); err != nil {
			slog.Debug("spade event failed", "channel", ch.Login, "err", err)
		}
		cancel()
	}
	return m.watchCurrent(ch, s)
}

// watchPlaylist fetches the media playlist and HEADs each segment not yet
// seen. Signed URLs are never logged.
func (m *Miner) watchPlaylist(ctx context.Context, ch *Channel, s *Stream) bool {
	m.mu.Lock()
	if m.watching != ch || ch.stream == nil || ch.stream.BroadcastID != s.BroadcastID {
		m.mu.Unlock()
		return false
	}
	if ch.watchBroadcast != s.BroadcastID {
		ch.watchBroadcast = s.BroadcastID
		ch.segments.reset()
		ch.lastSpade = time.Time{}
	}
	playlist := ""
	if ch.playlistBroadcast == s.BroadcastID {
		playlist = ch.playlistURL
	}
	login := ch.Login
	m.mu.Unlock()

	forget := func() {
		m.mu.Lock()
		ch.playlistURL, ch.playlistBroadcast = "", ""
		m.mu.Unlock()
	}

	if playlist == "" {
		var err error
		if playlist, err = m.mediaPlaylistURL(ctx, login); err != nil {
			slog.Debug("no playlist for stream", "channel", login, "err", err)
			return false
		}
		m.mu.Lock()
		ch.playlistURL, ch.playlistBroadcast = playlist, s.BroadcastID
		m.mu.Unlock()
	}
	if !m.watchCurrent(ch, s) {
		return false
	}
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := m.http.Do(gctx, http.MethodGet, playlist, nil, nil)
	cancel()
	if err != nil {
		return false
	}
	if resp.Status != http.StatusOK {
		if expiredStatus(resp.Status) {
			forget()
		}
		return false
	}
	base, _ := url.Parse(playlist)
	segments := playlistURLs(string(resp.Body), base, false)
	if len(segments) == 0 {
		return false
	}
	succeeded := true
	for _, seg := range segments {
		if !m.watchCurrent(ch, s) {
			return false
		}
		m.mu.Lock()
		seen := ch.segments.has(seg)
		m.mu.Unlock()
		if seen {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		resp, err := m.http.Do(hctx, http.MethodHead, seg, nil, nil)
		cancel()
		if !m.watchCurrent(ch, s) {
			return false
		}
		switch {
		case err != nil:
			succeeded = false
		case resp.Status == http.StatusOK:
			m.mu.Lock()
			ch.segments.add(seg)
			m.mu.Unlock()
		default:
			succeeded = false
			if expiredStatus(resp.Status) {
				forget()
			}
		}
	}
	return succeeded
}

func expiredStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound
}

// mediaPlaylistURL resolves the lowest-bandwidth variant playlist.
func (m *Miner) mediaPlaylistURL(ctx context.Context, login string) (string, error) {
	resp, err := m.gql.Do1(ctx, config.OpPlaybackAccessToken.With(map[string]any{"login": login}))
	if err != nil {
		return "", err
	}
	value, _ := twitch.Dig(resp, "data", "streamPlaybackAccessToken", "value").(string)
	sig, _ := twitch.Dig(resp, "data", "streamPlaybackAccessToken", "signature").(string)
	if value == "" || sig == "" {
		return "", errors.New("no playback token")
	}
	master := &url.URL{
		Scheme:   "https",
		Host:     "usher.ttvnw.net",
		Path:     "/api/channel/hls/" + login + ".m3u8",
		RawQuery: url.Values{"sig": {sig}, "token": {value}}.Encode(),
	}
	res, err := m.http.Do(ctx, http.MethodGet, master.String(), nil, nil)
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		return "", errors.New("master playlist HTTP " + strconv.Itoa(res.Status))
	}
	variants := playlistURLs(string(res.Body), master, true)
	if len(variants) == 0 {
		return "", errors.New("master playlist has no variants")
	}
	// The last variant is normally audio-only or the lowest bandwidth.
	return variants[len(variants)-1], nil
}

// playlistURLs resolves the URI lines following each #EXT-X-STREAM-INF
// (master) or #EXTINF (media) tag. Malformed playlists yield nothing.
func playlistURLs(playlist string, base *url.URL, master bool) []string {
	if base == nil || !strings.HasPrefix(strings.TrimSpace(playlist), "#EXTM3U") {
		return nil
	}
	tag := "#EXTINF:"
	if master {
		tag = "#EXT-X-STREAM-INF:"
	}
	var out []string
	pending := false
	for _, raw := range strings.Split(playlist, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			if strings.HasPrefix(line, tag) {
				pending = true
			}
			continue
		case !pending:
			return nil
		}
		ref, err := url.Parse(line)
		if err != nil {
			return nil
		}
		u := base.ResolveReference(ref)
		if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil ||
			master && !strings.HasSuffix(u.Path, ".m3u8") {
			return nil
		}
		out = append(out, u.String())
		pending = false
	}
	if pending {
		return nil
	}
	return out
}

var (
	settingsPattern = regexp.MustCompile(`(?i)src="(https://[\w.]+/config/settings\.[0-9a-f]{32}\.js)"`)
	spadePattern    = regexp.MustCompile(`(?i)"beacon_?url": ?"(https://[^"]+)"`)
)

// spadeURL scrapes the telemetry endpoint from the channel page, or from
// the settings script it references.
func (m *Miner) spadeURL(ctx context.Context, pageURL string) (string, error) {
	page, err := m.http.Do(ctx, http.MethodGet, pageURL, nil, nil)
	if err != nil {
		return "", err
	}
	if match := spadePattern.FindSubmatch(page.Body); match != nil {
		return string(match[1]), nil
	}
	match := settingsPattern.FindSubmatch(page.Body)
	if match == nil {
		return "", errors.New("spade URL not found on channel page")
	}
	js, err := m.http.Do(ctx, http.MethodGet, string(match[1]), nil, nil)
	if err != nil {
		return "", err
	}
	if match := spadePattern.FindSubmatch(js.Body); match != nil {
		return string(match[1]), nil
	}
	return "", errors.New("spade URL not found in settings script")
}

func (m *Miner) postSpade(ctx context.Context, ch *Channel, s *Stream) error {
	m.mu.Lock()
	endpoint, pageURL, login, channelID := ch.spadeURL, ch.URL(), ch.Login, ch.ID
	m.mu.Unlock()
	if endpoint == "" {
		var err error
		if endpoint, err = m.spadeURL(ctx, pageURL); err != nil {
			return err
		}
		m.mu.Lock()
		ch.spadeURL = endpoint
		m.mu.Unlock()
	}
	if !m.watchCurrent(ch, s) {
		return nil
	}
	userID, _ := strconv.ParseInt(m.auth.UserID(), 10, 64)
	gameName, gameID := "", ""
	if s.Game != nil {
		gameName, gameID = s.Game.Name, s.Game.ID
	}
	payload, err := json.Marshal([]map[string]any{{
		"event": "minute-watched",
		"properties": map[string]any{
			"broadcast_id":   s.BroadcastID,
			"channel_id":     channelID,
			"channel":        login,
			"client_time":    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			"game":           gameName,
			"game_id":        gameID,
			"hidden":         false,
			"is_live":        true,
			"live":           true,
			"location":       "channel",
			"logged_in":      true,
			"minutes_logged": 1,
			"muted":          false,
			"player":         "site",
			"user_id":        userID,
		},
	}})
	if err != nil {
		return err
	}
	form := url.Values{"data": {base64.StdEncoding.EncodeToString(payload)}}
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.http.Do(ctx, http.MethodPost, endpoint, []byte(form.Encode()), h)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusNoContent {
		return errors.New("spade HTTP " + strconv.Itoa(resp.Status))
	}
	return nil
}
