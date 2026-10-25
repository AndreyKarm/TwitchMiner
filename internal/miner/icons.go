package miner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// gameIcons remembers box art by game name, so the watch list can show a
// picture for games with no campaign running. It's saved in the data
// directory so pictures appear right away on the next start.
type gameIcons struct {
	path string

	mu   sync.Mutex
	urls map[string]string // lower-case game name -> box art URL
	// tried holds names a search found no exact match for this run.
	tried map[string]bool
}

func loadGameIcons(path string) *gameIcons {
	g := &gameIcons{path: path, urls: map[string]string{}, tried: map[string]bool{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &g.urls); err != nil {
			slog.Warn("ignoring unreadable game icons", "err", err)
			g.urls = map[string]string{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.Warn("reading game icons", "err", err)
	}
	return g
}

// learn records url for name and reports whether anything changed.
func (g *gameIcons) learn(name, url string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || url == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.urls[key] == url {
		return false
	}
	g.urls[key] = url
	return true
}

// missing lists the names with no known picture that haven't been searched
// for without success already.
func (g *gameIcons) missing(names []string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, name := range names {
		key := strings.ToLower(name)
		if g.urls[key] == "" && !g.tried[key] {
			out = append(out, name)
		}
	}
	return out
}

func (g *gameIcons) markTried(name string) {
	g.mu.Lock()
	g.tried[strings.ToLower(name)] = true
	g.mu.Unlock()
}

func (g *gameIcons) snapshot() map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return maps.Clone(g.urls)
}

// publishGameIcons sends the known pictures to the dashboard and saves them.
func (m *Miner) publishGameIcons(save bool) {
	urls := m.icons.snapshot()
	m.ui.Publish("game_icons", urls)
	if save {
		if err := config.WriteJSONAtomic(m.icons.path, urls); err != nil {
			slog.Error("saving game icons", "err", err)
		}
	}
}

// learnCampaignIconsLocked records the box art of every campaign's game.
func (m *Miner) learnCampaignIconsLocked() bool {
	changed := false
	for _, c := range m.inventory {
		if c.Game != nil && m.icons.learn(c.Game.Name, c.Game.BoxArtURL) {
			changed = true
		}
	}
	return changed
}

// lookupGameIcons searches Twitch for listed games with no known picture.
// Only an exact name match is used, so a game never gets the wrong art.
func (m *Miner) lookupGameIcons(ctx context.Context) {
	m.mu.Lock()
	names := slices.Clone(m.policy.games)
	m.mu.Unlock()
	changed := false
	for _, name := range m.icons.missing(names) {
		results, err := m.SearchGames(ctx, name)
		if err != nil {
			if ctx.Err() == nil {
				slog.Debug("looking up game picture failed", "game", name, "err", err)
			}
			continue // try again on the next lookup
		}
		found := false
		for _, r := range results {
			if strings.EqualFold(r.Name, name) && r.Icon != "" {
				changed = m.icons.learn(name, r.Icon) || changed
				found = true
				break
			}
		}
		if !found {
			m.icons.markTried(name)
		}
	}
	if changed {
		m.publishGameIcons(true)
	}
}
