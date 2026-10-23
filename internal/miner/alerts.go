package miner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/notify"
)

// seenCampaigns remembers which active campaigns were already announced,
// so restarts and inventory reloads don't repeat alerts.
type seenCampaigns struct {
	path   string
	saveMu sync.Mutex
	// ids maps a campaign ID to its end time, used for pruning. Guarded by
	// Miner.mu.
	ids     map[string]time.Time
	existed bool
}

func loadSeenCampaigns(path string) *seenCampaigns {
	s := &seenCampaigns{path: path, ids: map[string]time.Time{}}
	data, err := os.ReadFile(path)
	if err == nil && json.Unmarshal(data, &s.ids) == nil {
		s.existed = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("reading seen campaigns", "err", err)
	}
	return s
}

func (s *seenCampaigns) save(snapshot map[string]time.Time) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := config.WriteJSONAtomic(s.path, snapshot); err != nil {
		slog.Error("saving seen campaigns", "err", err)
	}
}

// collectStartedLocked marks every active campaign as seen and returns an
// alert for each newly started one the user would mine. The first run only
// records what's already running.
func (m *Miner) collectStartedLocked() (alerts []notify.Message, snapshot map[string]time.Time) {
	seen := m.seen
	if seen == nil {
		return nil, nil
	}
	seeding := !seen.existed
	changed := false
	for _, c := range m.inventory {
		if !c.Active() {
			continue
		}
		if _, ok := seen.ids[c.ID]; ok {
			continue
		}
		seen.ids[c.ID] = c.EndsAt
		changed = true
		if !seeding && m.wouldMineLocked(c) {
			alerts = append(alerts, m.campaignAlertLocked(c))
		}
	}
	for id, ends := range seen.ids {
		if time.Since(ends) > 7*24*time.Hour {
			delete(seen.ids, id)
			changed = true
		}
	}
	if seeding {
		seen.existed = true
		changed = true
	}
	if !changed {
		return alerts, nil
	}
	snapshot = make(map[string]time.Time, len(seen.ids))
	for k, v := range seen.ids {
		snapshot[k] = v
	}
	return alerts, snapshot
}

// wouldMineLocked reports whether c belongs to a game the miner would mine
// and still has rewards of a wanted type.
func (m *Miner) wouldMineLocked(c *Campaign) bool {
	if !c.Eligible() || !c.hasWantedUnclaimedBenefits(m.policy.benefits) {
		return false
	}
	return !m.policy.priorityOnly || m.isListedGame(c.Game)
}

func (m *Miner) campaignAlertLocked(c *Campaign) notify.Message {
	lines := []string{"Game: " + c.Game.Name}
	ends := c.EndsAt.Local()
	lines = append(lines, fmt.Sprintf("Ends %s (in %s)", ends.Format("Mon 2 Jan 15:04"), roughDuration(time.Until(c.EndsAt))))
	var rewards []string
	seen := map[string]bool{}
	for _, d := range c.watchDrops() {
		for _, name := range d.wantedUnclaimedBenefits(m.policy.benefits) {
			if !seen[name] {
				seen[name] = true
				rewards = append(rewards, name)
			}
		}
	}
	if len(rewards) > 6 {
		rewards = append(rewards[:6], fmt.Sprintf("and %d more", len(rewards)-6))
	}
	if len(rewards) > 0 {
		lines = append(lines, "Rewards: "+strings.Join(rewards, ", "))
	}
	return notify.Message{Title: "Drops campaign started: " + c.Name, URL: c.URL(), Lines: lines, Image: c.Game.BoxArtURL}
}

func roughDuration(d time.Duration) string {
	hours := int(d.Hours())
	switch {
	case hours >= 48:
		return fmt.Sprintf("%dd %dh", hours/24, hours%24)
	case hours >= 1:
		return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
}

// deliverAlerts saves the seen list and sends alerts to every configured
// channel. Failures are shown in the console without secrets.
func (m *Miner) deliverAlerts(ctx context.Context, alerts []notify.Message, snapshot map[string]time.Time) {
	if snapshot != nil {
		m.seen.save(snapshot)
	}
	if len(alerts) == 0 {
		return
	}
	s := m.settings.Get()
	for _, a := range alerts {
		m.print(a.Title)
		for _, err := range m.send(ctx, s, a) {
			m.print("Alert failed: " + err.Error())
		}
	}
}

func (m *Miner) send(ctx context.Context, s config.Settings, msg notify.Message) []error {
	n := notify.New(s.Proxy)
	var errs []error
	if s.TelegramBotToken != "" && s.TelegramChatID != "" {
		if err := n.Telegram(ctx, s.TelegramBotToken, s.TelegramChatID, msg); err != nil {
			errs = append(errs, err)
		}
	}
	if s.DiscordWebhookURL != "" {
		if err := n.Discord(ctx, s.DiscordWebhookURL, msg); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// TestAlert sends a sample alert to one channel ("telegram" or "discord").
func (m *Miner) TestAlert(ctx context.Context, target string) error {
	s := m.settings.Get()
	msg := notify.Message{
		Title: "TwitchMiner test alert",
		Lines: []string{"Alerts work. You'll get a message like this when a drops campaign starts."},
	}
	n := notify.New(s.Proxy)
	switch target {
	case "telegram":
		if s.TelegramBotToken == "" || s.TelegramChatID == "" {
			return errors.New("save a Telegram bot token and chat ID first")
		}
		return n.Telegram(ctx, s.TelegramBotToken, s.TelegramChatID, msg)
	case "discord":
		if s.DiscordWebhookURL == "" {
			return errors.New("save a Discord webhook first")
		}
		return n.Discord(ctx, s.DiscordWebhookURL, msg)
	}
	return errors.New("unknown alert channel")
}
