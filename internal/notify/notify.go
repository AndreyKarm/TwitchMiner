// Package notify sends alerts to Telegram and Discord.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Message is a channel-neutral alert.
type Message struct {
	Title string
	URL   string
	Lines []string
	Image string
}

// Notifier posts messages. Errors never contain the bot token or webhook
// URL, which are secrets.
type Notifier struct {
	client       *http.Client
	telegramBase string
}

// New builds a notifier that uses proxy (if set) for its requests.
func New(proxy string) *Notifier {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if u, err := url.Parse(proxy); proxy != "" && err == nil {
		transport.Proxy = http.ProxyURL(u)
	}
	return &Notifier{
		client: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
			// Never follow a redirect away from the configured endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		telegramBase: "https://api.telegram.org",
	}
}

// Telegram sends m to a chat through a bot.
func (n *Notifier) Telegram(ctx context.Context, token, chatID string, m Message) error {
	text := m.Title
	if len(m.Lines) > 0 {
		text += "\n" + strings.Join(m.Lines, "\n")
	}
	if m.URL != "" {
		text += "\n" + m.URL
	}
	body, _ := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	status, err := n.post(ctx, n.telegramBase+"/bot"+token+"/sendMessage", body)
	if err != nil {
		return errors.New("telegram: could not reach the Telegram API")
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusNotFound:
		return errors.New("telegram: the bot token was rejected")
	case http.StatusBadRequest, http.StatusForbidden:
		return errors.New("telegram: the chat ID was rejected; message the bot first, or add it to the channel")
	}
	return fmt.Errorf("telegram: HTTP %d", status)
}

// Discord sends m to a webhook as an embed. Mentions are disabled.
func (n *Notifier) Discord(ctx context.Context, webhook string, m Message) error {
	embed := map[string]any{
		"title":       m.Title,
		"description": strings.Join(m.Lines, "\n"),
		"color":       0xd88cde,
	}
	if m.URL != "" {
		embed["url"] = m.URL
	}
	if strings.HasPrefix(m.Image, "https://") {
		embed["thumbnail"] = map[string]string{"url": m.Image}
	}
	body, _ := json.Marshal(map[string]any{
		"username":         "TwitchMiner",
		"embeds":           []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	status, err := n.post(ctx, webhook, body)
	if err != nil {
		return errors.New("discord: could not reach Discord")
	}
	switch status {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusNotFound:
		return errors.New("discord: the webhook no longer exists")
	}
	return fmt.Errorf("discord: HTTP %d", status)
}

// TelegramChat is a chat that sent the bot a message.
type TelegramChat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// TelegramUpdates waits up to wait for messages to the bot and returns the
// chats they came from, each once, with the offset that acknowledges them.
// The wait must stay under the client's 10 second timeout.
func (n *Notifier) TelegramUpdates(ctx context.Context, token string, offset int64, wait time.Duration) ([]TelegramChat, int64, error) {
	body, _ := json.Marshal(map[string]any{
		"offset":          offset,
		"timeout":         int(wait.Seconds()),
		"allowed_updates": []string{"message", "channel_post"},
	})
	status, data, err := n.postRead(ctx, n.telegramBase+"/bot"+token+"/getUpdates", body)
	if err != nil {
		return nil, offset, errors.New("telegram: could not reach the Telegram API")
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusNotFound:
		return nil, offset, errors.New("telegram: the bot token was rejected")
	case http.StatusConflict:
		return nil, offset, errors.New("telegram: another program is reading this bot's messages, so its chat ID can't be found here")
	default:
		return nil, offset, fmt.Errorf("telegram: HTTP %d", status)
	}
	var resp struct {
		Result []struct {
			UpdateID    int64        `json:"update_id"`
			Message     *rawTGUpdate `json:"message"`
			ChannelPost *rawTGUpdate `json:"channel_post"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, offset, errors.New("telegram: unreadable reply")
	}
	var chats []TelegramChat
	seen := map[string]bool{}
	for _, u := range resp.Result {
		offset = max(offset, u.UpdateID+1)
		msg := u.Message
		if msg == nil {
			msg = u.ChannelPost
		}
		if msg == nil {
			continue
		}
		c := msg.Chat.chat()
		if !seen[c.ID] {
			seen[c.ID] = true
			chats = append(chats, c)
		}
	}
	return chats, offset, nil
}

type rawTGUpdate struct {
	Chat rawTGChat `json:"chat"`
}

type rawTGChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (c rawTGChat) chat() TelegramChat {
	name := c.Title
	if name == "" {
		name = strings.TrimSpace(c.FirstName + " " + c.LastName)
	}
	if c.Username != "" {
		if name == "" {
			name = "@" + c.Username
		} else {
			name += " (@" + c.Username + ")"
		}
	}
	return TelegramChat{ID: strconv.FormatInt(c.ID, 10), Name: name, Type: c.Type}
}

func (n *Notifier) post(ctx context.Context, endpoint string, body []byte) (int, error) {
	status, _, err := n.postRead(ctx, endpoint, body)
	return status, err
}

// postRead posts JSON and returns the status and up to 1 MB of the reply.
func (n *Notifier) postRead(ctx context.Context, endpoint string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}
