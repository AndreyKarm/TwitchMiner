package miner

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/notify"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// Finding the Telegram chat ID: once a bot token is saved without a chat
// ID, the miner reads the bot's messages and replies to each chat with its
// ID, so the user can message the bot and paste the answer into Settings.
// The chats are also shown in the dashboard to pick from. It stops once a
// chat ID is saved.

const (
	// chatPollWait is how long each getUpdates call waits for a message.
	chatPollWait = 8 * time.Second
	// chatPollRetry spaces out attempts after an error.
	chatPollRetry = 30 * time.Second
	// maxFoundChats caps the chats offered in the dashboard.
	maxFoundChats = 5
)

type chatFinder struct {
	mu     sync.Mutex
	token  string // the token being polled, "" when stopped
	cancel context.CancelFunc
	found  []notify.TelegramChat // newest first
}

// chatFinderToken is the bot token to find a chat for, or "" when there's
// nothing to find.
func chatFinderToken(s config.Settings) string {
	if s.TelegramChatID != "" {
		return ""
	}
	return s.TelegramBotToken
}

// syncChatFinder starts or stops looking for a Telegram chat to match s.
func (m *Miner) syncChatFinder(s config.Settings) {
	token := chatFinderToken(s)
	f := &m.chats
	f.mu.Lock()
	if token == f.token {
		f.mu.Unlock()
		return
	}
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.token, f.found = token, nil
	var ctx context.Context
	if token != "" {
		ctx, f.cancel = context.WithCancel(context.Background())
	}
	f.mu.Unlock()

	m.publishFoundChats()
	if token != "" {
		m.goSafe("finding the Telegram chat", func() { m.findTelegramChats(ctx, token, s.Proxy) })
	}
}

// findTelegramChats reads the bot's messages until ctx ends and answers
// every chat that writes with its chat ID.
func (m *Miner) findTelegramChats(ctx context.Context, token, proxy string) {
	n := notify.New(proxy)
	var offset int64
	lastErr := ""
	for ctx.Err() == nil {
		chats, next, err := n.TelegramUpdates(ctx, token, offset, chatPollWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Errors never contain the token; show each one once.
			if err.Error() != lastErr {
				lastErr = err.Error()
				m.print("Finding your Telegram chat ID: " + lastErr)
			}
			if twitch.Sleep(ctx, chatPollRetry) != nil {
				return
			}
			continue
		}
		lastErr, offset = "", next
		for _, c := range chats {
			m.foundChat(token, c)
			if err := n.Telegram(ctx, token, c.ID, chatIDReply(c)); err != nil && ctx.Err() == nil {
				m.print("Couldn't send the chat ID to " + chatLabel(c) + ": " + err.Error())
			}
		}
	}
}

func chatIDReply(c notify.TelegramChat) notify.Message {
	return notify.Message{
		Title: "Your chat ID is " + c.ID,
		Lines: []string{
			"Paste it into TwitchMiner: Settings → Alerts → Telegram → Chat ID, then press Save.",
			"Or press Use next to this chat in the same place.",
		},
	}
}

func chatLabel(c notify.TelegramChat) string {
	if c.Name != "" {
		return c.Name
	}
	return "chat " + c.ID
}

// foundChat puts c at the top of the chats offered in the dashboard,
// unless the search for token has since stopped.
func (m *Miner) foundChat(token string, c notify.TelegramChat) {
	f := &m.chats
	f.mu.Lock()
	if f.token != token {
		f.mu.Unlock()
		return
	}
	f.found = slices.DeleteFunc(f.found, func(o notify.TelegramChat) bool { return o.ID == c.ID })
	f.found = slices.Insert(f.found, 0, c)
	if len(f.found) > maxFoundChats {
		f.found = f.found[:maxFoundChats]
	}
	f.mu.Unlock()
	m.publishFoundChats()
}

func (m *Miner) publishFoundChats() {
	f := &m.chats
	f.mu.Lock()
	found := append([]notify.TelegramChat{}, f.found...)
	f.mu.Unlock()
	m.ui.Publish("telegram_chats", found)
}
