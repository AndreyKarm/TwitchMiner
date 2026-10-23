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
