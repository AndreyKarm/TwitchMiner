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

// startedAlertWindow is how recently a campaign must have started to be
// announced as started.
const startedAlertWindow = 24 * time.Hour

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
		// A campaign seen for the first time can be old, for example after
		// adding its game to the watch list; that isn't news.
		if !seeding && m.wouldMineLocked(c) && time.Since(c.StartsAt) < startedAlertWindow {
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

// deliverStarted saves the seen list and announces newly started campaigns.
func (m *Miner) deliverStarted(alerts []notify.Message, snapshot map[string]time.Time) {
	if snapshot != nil {
		m.seen.save(snapshot)
	}
	for _, a := range alerts {
		m.print(a.Title)
	}
	m.sendAlerts(config.AlertCampaignStarted, alerts...)
}

// sendAlerts delivers messages for one event to every configured channel,
// if the user turned that event on. It runs on its own context so an alert
// about a session ending isn't cancelled with it. Failures are shown in the
// console without secrets.
func (m *Miner) sendAlerts(event string, msgs ...notify.Message) {
	if len(msgs) == 0 {
		return
	}
	s := m.settings.Get()
	if !s.AlertOn(event) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, msg := range msgs {
		for _, err := range m.send(ctx, s, msg) {
			m.print("Alert failed: " + err.Error())
		}
	}
}

// claimAlertsLocked describes a drop that was just claimed, plus its
// campaign when that claim completed it.
func (m *Miner) claimAlertsLocked(d *Drop) (claimed notify.Message, completed *notify.Message) {
	c := d.Campaign
	n, total := c.claimedCount()
	rewards := d.rewardsText()
	if rewards == "" {
		rewards = d.Name
	}
	image := c.Game.BoxArtURL
	if len(d.Benefits) > 0 && d.Benefits[0].ImageURL != "" {
		image = d.Benefits[0].ImageURL
	}
	claimed = notify.Message{
		Title: "Drop received: " + rewards,
		URL:   "https://www.twitch.tv/drops/inventory",
		Lines: []string{"Game: " + c.Game.Name, fmt.Sprintf("Campaign: %s (%d of %d claimed)", c.Name, n, total)},
		Image: image,
	}
	if total > 0 && n == total {
		completed = &notify.Message{
			Title: "Campaign completed: " + c.Name,
			URL:   c.URL(),
			Lines: []string{"Game: " + c.Game.Name, fmt.Sprintf("All %d drops claimed.", total)},
			Image: c.Game.BoxArtURL,
		}
	}
	return claimed, completed
}

// loginAlert asks the user to log in again, with the code so it can be done
// from a phone.
func loginAlert(code, link string) notify.Message {
	return notify.Message{
		Title: "Twitch login needed",
		URL:   link,
		Lines: []string{
			"Twitch logged TwitchMiner out, so mining has stopped.",
			"Enter code " + code + " at twitch.tv/activate, or open the dashboard.",
		},
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
