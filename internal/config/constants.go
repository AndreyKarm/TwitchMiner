// Package config holds static Twitch client data, GQL operations and the
// persisted user settings.
package config

import "time"

// Version is the application version, overridable at build time with
// -ldflags "-X github.com/AndreyKarm/TwitchMiner/internal/config.Version=...".
var Version = "0.1.0-dev"

// PubSub limits. Twitch allows 50 topics per connection; two topics are
// reserved for the user and each tracked channel uses two more.
const (
	MaxWebsockets    = 8
	WSTopicsLimit    = 50
	BaseTopics       = 2
	TopicsPerChannel = 2
	MaxTopics        = MaxWebsockets*WSTopicsLimit - BaseTopics
	MaxChannels      = MaxTopics / TopicsPerChannel
)

// MaxExtraMinutes caps how many locally estimated minutes a drop may gain
// before the miner stops trusting its estimate and switches channel.
const MaxExtraMinutes = 15

const (
	PingInterval  = 3 * time.Minute
	PingTimeout   = 10 * time.Second
	OnlineDelay   = 120 * time.Second
	WatchInterval = 59 * time.Second
	// WatchPoll is how often the HLS playlist is polled; it must stay inside
	// Twitch's rolling segment window.
	WatchPoll = 10 * time.Second
)

// SpecialGameIDs are categories (Special Events, IRL) whose campaigns can
// progress on ACL channels streaming any category.
var SpecialGameIDs = map[string]bool{"509663": true, "509672": true}

// ClientInfo identifies the Twitch client the miner presents itself as.
type ClientInfo struct {
	URL       string
	ClientID  string
	UserAgent string
}

// SmartTV is the Android TV client. As of September 2026 it is one of the
// few client IDs Twitch still accepts for device-code login (the Android
// app and web IDs return "invalid client"). Twitch may withhold the full
// campaign catalog from it; the miner reports that when it happens.
func SmartTV() ClientInfo {
	return ClientInfo{
		URL:      "https://android.tv.twitch.tv",
		ClientID: "ue6666qo983tsx6so1t0vnawi233wa",
		UserAgent: "Mozilla/5.0 (Linux; Android 7.1; Smart Box C1) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
	}
}

// WebURL is the public site, used for channel pages and spade discovery.
const WebURL = "https://www.twitch.tv"

// PubSub topic prefixes.
const (
	TopicUserDrops          = "user-drop-events"
	TopicUserNotifications  = "onsite-notifications"
	TopicChannelStreamState = "video-playback-by-id"
	TopicChannelUpdate      = "broadcast-settings-update"
)
