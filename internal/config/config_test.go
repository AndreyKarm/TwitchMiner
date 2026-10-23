package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOpWithMergesNestedVariables(t *testing.T) {
	op := OpGameDirectory.With(map[string]any{
		"slug":    "rust",
		"options": map[string]any{"systemFilters": []any{"DROPS_ENABLED"}},
	})
	opts := op.Variables["options"].(map[string]any)
	if op.Variables["slug"] != "rust" || opts["sort"] != "RELEVANCE" {
		t.Fatalf("defaults lost while merging: %v", op.Variables)
	}
	if f := opts["systemFilters"].([]any); len(f) != 1 {
		t.Fatalf("override not applied: %v", opts)
	}
	if base := OpGameDirectory.Variables["options"].(map[string]any)["systemFilters"].([]any); len(base) != 0 {
		t.Fatal("With must not modify the shared operation")
	}
}

func TestStorePatchValidatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Patch([]byte(`{"games_to_watch":[" Rust ","rust","","Apex Legends"],"connection_quality":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Rust", "Apex Legends"}; !slices.Equal(s.GamesToWatch, want) {
		t.Fatalf("games = %v, want %v", s.GamesToWatch, want)
	}
	if s.ConnectionQuality != 6 {
		t.Fatalf("quality should clamp to 6, got %d", s.ConnectionQuality)
	}

	if _, err := st.Patch([]byte(`{"proxy":"ftp://nope"}`)); err == nil {
		t.Fatal("unsupported proxy scheme should be rejected")
	}
	if st.Get().Proxy != "" {
		t.Fatal("a rejected patch must not change settings")
	}

	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get().GamesToWatch; !slices.Equal(got, s.GamesToWatch) {
		t.Fatalf("persisted games = %v", got)
	}
}

func TestSecretsStayServerSide(t *testing.T) {
	st, err := LoadStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abc"
	const hook = "https://discord.com/api/webhooks/123/abc-DEF_1"
	if _, err := st.Patch([]byte(`{"telegram_bot_token":"` + token + `","telegram_chat_id":"-100123","discord_webhook_url":"` + hook + `"}`)); err != nil {
		t.Fatal(err)
	}

	pub := st.Get().Public()
	if pub.TelegramBotToken != "" || pub.DiscordWebhookURL != "" {
		t.Fatal("Public must blank the secrets")
	}
	if !pub.TelegramConfigured || !pub.DiscordConfigured || pub.TelegramChatID != "-100123" {
		t.Fatalf("Public flags wrong: %+v", pub)
	}

	// A form that sends blank secrets keeps the saved ones.
	s, err := st.Patch([]byte(`{"telegram_bot_token":"","discord_webhook_url":"","telegram_chat_id":"@drops_chan"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.TelegramBotToken != token || s.DiscordWebhookURL != hook || s.TelegramChatID != "@drops_chan" {
		t.Fatalf("blank secrets should be kept: %+v", s)
	}

	s, err = st.Patch([]byte(`{"clear_discord":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.DiscordWebhookURL != "" || s.TelegramBotToken != token {
		t.Fatal("clear_discord should remove only the webhook")
	}

	_, err = st.Patch([]byte(`{"discord_webhook_url":"https://evil.example/api/webhooks/1/x"}`))
	if err == nil || strings.Contains(err.Error(), "evil") {
		t.Fatalf("bad webhook must be rejected without echoing it, got %v", err)
	}
}

func TestAlertEventsDefaultOnAndPatchIndividually(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// An older settings file has no alert_events at all.
	if err := os.WriteFile(path, []byte(`{"games_to_watch":["Rust"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range AlertEvents {
		if !st.Get().AlertOn(e) {
			t.Fatalf("%s should default to on", e)
		}
	}
	s, err := st.Patch([]byte(`{"alert_events":{"drop_claimed":false,"bogus":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.AlertOn(AlertDropClaimed) || !s.AlertOn(AlertCampaignStarted) || len(s.AlertEvents) != len(AlertEvents) {
		t.Fatalf("patch should switch off one event only and drop unknown keys: %v", s.AlertEvents)
	}
}

func TestLoadStoreFillsMissingBenefits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"mining_benefits":{"BADGE":false,"BOGUS":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b := st.Get().MiningBenefits
	if b["BADGE"] || !b["EMOTE"] || len(b) != len(BenefitTypes) {
		t.Fatalf("benefits = %v", b)
	}
}
