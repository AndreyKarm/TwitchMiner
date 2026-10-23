// Package notify sends alerts to Telegram and Discord.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusNotFound:
		return errors.New("telegram: the bot token was rejected")
	case status == http.StatusBadRequest || status == http.StatusForbidden:
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
	switch {
	case status == http.StatusNoContent || status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusNotFound:
		return errors.New("discord: the webhook no longer exists")
	}
	return fmt.Errorf("discord: HTTP %d", status)
}

func (n *Notifier) post(ctx context.Context, endpoint string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}
