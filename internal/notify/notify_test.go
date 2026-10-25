package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSendsMessageAndHidesToken(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &got)
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	n := New("")
	n.telegramBase = srv.URL
	msg := Message{Title: "New campaign", Lines: []string{"Rust"}, URL: "https://www.twitch.tv/drops"}
	if err := n.Telegram(context.Background(), "123:secret", "42", msg); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:secret/sendMessage" || got["chat_id"] != "42" {
		t.Fatalf("unexpected request: %s %v", path, got)
	}
	if text := got["text"].(string); !strings.Contains(text, "Rust") || !strings.Contains(text, "twitch.tv") {
		t.Fatalf("text missing details: %q", text)
	}

	err := n.Telegram(context.Background(), "bad", "42", msg)
	if err == nil || strings.Contains(err.Error(), "bad") {
		t.Fatalf("want a rejection that doesn't echo the token, got %v", err)
	}
}

func TestTelegramUpdatesListChatsOnce(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &got)
		switch {
		case strings.Contains(r.URL.Path, "bad"):
			w.WriteHeader(http.StatusUnauthorized)
		case strings.Contains(r.URL.Path, "busy"):
			w.WriteHeader(http.StatusConflict)
		default:
			io.WriteString(w, `{"ok":true,"result":[
				{"update_id":10,"message":{"chat":{"id":42,"type":"private","first_name":"Andrii","username":"andrii"}}},
				{"update_id":11,"message":{"chat":{"id":42,"type":"private","first_name":"Andrii","username":"andrii"}}},
				{"update_id":12,"channel_post":{"chat":{"id":-1001234,"type":"channel","title":"Drops"}}},
				{"update_id":13,"edited_message":{"chat":{"id":7,"type":"private"}}}
			]}`)
		}
	}))
	defer srv.Close()

	n := New("")
	n.telegramBase = srv.URL
	chats, next, err := n.TelegramUpdates(context.Background(), "123:secret", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:secret/getUpdates" || got["offset"] != float64(5) {
		t.Fatalf("unexpected request: %s %v", path, got)
	}
	want := []TelegramChat{
		{ID: "42", Name: "Andrii (@andrii)", Type: "private"},
		{ID: "-1001234", Name: "Drops", Type: "channel"},
	}
	if len(chats) != len(want) || chats[0] != want[0] || chats[1] != want[1] {
		t.Fatalf("chats = %+v, want %+v", chats, want)
	}
	if next != 14 {
		t.Fatalf("next offset = %d, want 14 so every update is acknowledged", next)
	}

	for _, token := range []string{"bad", "busy"} {
		_, off, err := n.TelegramUpdates(context.Background(), token, 5, 0)
		if err == nil || strings.Contains(err.Error(), token) || off != 5 {
			t.Fatalf("%s: want an error without the token and the offset kept, got %v, %d", token, err, off)
		}
	}
}

func TestDiscordDisablesMentionsAndRefusesRedirects(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://evil.example/", http.StatusFound)
			return
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := New("")
	if err := n.Discord(context.Background(), srv.URL+"/hook", Message{Title: "Hi", Lines: []string{"@everyone"}}); err != nil {
		t.Fatal(err)
	}
	if mentions := got["allowed_mentions"].(map[string]any)["parse"].([]any); len(mentions) != 0 {
		t.Fatalf("mentions must be disabled: %v", mentions)
	}
	err := n.Discord(context.Background(), srv.URL+"/redirect", Message{Title: "Hi"})
	if err == nil || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("redirects must fail without echoing the URL, got %v", err)
	}
}
