package miner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/notify"
)

func TestChatFinderRunsOnlyWithoutAChatID(t *testing.T) {
	s := config.Defaults()
	if chatFinderToken(s) != "" {
		t.Fatal("no token: nothing to find")
	}
	s.TelegramBotToken = "123:abc"
	if chatFinderToken(s) != "123:abc" {
		t.Fatal("a token without a chat ID should be searched")
	}
	s.TelegramChatID = "42"
	if chatFinderToken(s) != "" {
		t.Fatal("once a chat ID is saved the search stops")
	}
}

func TestFoundChatsNewestFirstAndCapped(t *testing.T) {
	m := testMiner(config.Defaults())
	m.ui = nopUI{}
	m.chats.token = "tok"
	for i := range maxFoundChats + 2 {
		m.foundChat("tok", notify.TelegramChat{ID: fmt.Sprint(i)})
	}
	m.foundChat("tok", notify.TelegramChat{ID: "3"}) // writes again
	ids := []string{}
	for _, c := range m.chats.found {
		ids = append(ids, c.ID)
	}
	if got := strings.Join(ids, ","); got != "3,6,5,4,2" {
		t.Fatalf("found = %s", got)
	}

	m.foundChat("old-token", notify.TelegramChat{ID: "99"})
	if m.chats.found[0].ID == "99" {
		t.Fatal("a stopped search must not add chats")
	}
}

func TestChatIDReplyCarriesTheID(t *testing.T) {
	msg := chatIDReply(notify.TelegramChat{ID: "-1001234"})
	if !strings.Contains(msg.Title, "-1001234") || !strings.Contains(strings.Join(msg.Lines, " "), "Settings") {
		t.Fatalf("reply should give the ID and where to paste it: %+v", msg)
	}
}
