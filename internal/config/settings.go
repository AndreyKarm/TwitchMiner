package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Benefit distribution types Twitch reports for drop rewards.
var BenefitTypes = []string{"BADGE", "EMOTE", "DIRECT_ENTITLEMENT", "UNKNOWN"}

// Settings are the user's persisted preferences.
type Settings struct {
	// GamesToWatch is the ordered priority list of game names.
	GamesToWatch []string `json:"games_to_watch"`
	// PriorityListOnly restricts mining to GamesToWatch. When false, other
	// eligible games are mined as fallback after the listed ones.
	PriorityListOnly bool `json:"priority_list_only"`
	// MiningBenefits selects which reward types are worth mining.
	MiningBenefits map[string]bool `json:"mining_benefits"`
	// DropNameBlacklist holds case-insensitive substrings of drop names to skip.
	DropNameBlacklist []string `json:"drop_name_blacklist"`
	// AllowUnlinkedCampaigns mines campaigns whose game account isn't linked.
	AllowUnlinkedCampaigns bool `json:"allow_unlinked_campaigns"`
	// ConnectionQuality (1-6) scales HTTP timeouts for slow connections.
	ConnectionQuality int `json:"connection_quality"`
	// RefreshIntervalMinutes is how often the inventory is reloaded.
	RefreshIntervalMinutes int `json:"minimum_refresh_interval_minutes"`
	// Proxy is an optional http(s) or socks5 proxy URL.
	Proxy string `json:"proxy"`
}

// Defaults returns the settings used for a fresh install.
func Defaults() Settings {
	benefits := make(map[string]bool, len(BenefitTypes))
	for _, t := range BenefitTypes {
		benefits[t] = true
	}
	return Settings{
		GamesToWatch:           []string{},
		PriorityListOnly:       true,
		MiningBenefits:         benefits,
		DropNameBlacklist:      []string{},
		ConnectionQuality:      1,
		RefreshIntervalMinutes: 30,
	}
}

// Clone returns a deep copy so callers can't mutate shared slices or maps.
func (s Settings) Clone() Settings {
	out := s
	out.GamesToWatch = append([]string{}, s.GamesToWatch...)
	out.DropNameBlacklist = append([]string{}, s.DropNameBlacklist...)
	out.MiningBenefits = make(map[string]bool, len(s.MiningBenefits))
	for k, v := range s.MiningBenefits {
		out.MiningBenefits[k] = v
	}
	return out
}

// Normalize trims and deduplicates lists and clamps numbers into range. It
// returns an error only for values it can't repair, such as a bad proxy URL.
func (s *Settings) Normalize() error {
	s.GamesToWatch = UniqueFold(s.GamesToWatch)
	s.DropNameBlacklist = UniqueFold(s.DropNameBlacklist)
	if s.MiningBenefits == nil {
		s.MiningBenefits = map[string]bool{}
	}
	for k := range s.MiningBenefits {
		if !isBenefitType(k) {
			delete(s.MiningBenefits, k)
		}
	}
	for _, t := range BenefitTypes {
		if _, ok := s.MiningBenefits[t]; !ok {
			s.MiningBenefits[t] = true
		}
	}
	s.ConnectionQuality = min(max(s.ConnectionQuality, 1), 6)
	s.RefreshIntervalMinutes = min(max(s.RefreshIntervalMinutes, 5), 24*60)
	s.Proxy = strings.TrimSpace(s.Proxy)
	if s.Proxy != "" {
		u, err := url.Parse(s.Proxy)
		if err != nil || u.Host == "" {
			return errors.New("proxy must be a full URL, like http://host:port")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
		}
	}
	return nil
}

func isBenefitType(t string) bool {
	for _, b := range BenefitTypes {
		if b == t {
			return true
		}
	}
	return false
}

// UniqueFold trims entries, drops blanks and removes case-insensitive
// duplicates while keeping the first spelling and the original order.
func UniqueFold(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		key := strings.ToLower(v)
		if v == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

// Store persists Settings as JSON and guards concurrent access.
type Store struct {
	mu   sync.Mutex
	path string
	s    Settings
}

// LoadStore reads settings from path, falling back to defaults for missing
// files or fields.
func LoadStore(path string) (*Store, error) {
	s := Defaults()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if err := s.Normalize(); err != nil {
		// A broken proxy shouldn't stop the miner from starting.
		s.Proxy = ""
	}
	return &Store{path: path, s: s}, nil
}

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s.Clone()
}

// Patch applies a partial JSON object on top of the current settings,
// validates the result and saves it. The previous settings are kept if
// anything fails.
func (st *Store) Patch(patch []byte) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.s.Clone()
	if err := json.Unmarshal(patch, &next); err != nil {
		return st.s.Clone(), fmt.Errorf("invalid settings: %w", err)
	}
	if err := next.Normalize(); err != nil {
		return st.s.Clone(), err
	}
	if err := writeJSONAtomic(st.path, next); err != nil {
		return st.s.Clone(), err
	}
	st.s = next
	return next.Clone(), nil
}

// Save writes the current settings to disk.
func (st *Store) Save() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return writeJSONAtomic(st.path, st.s)
}

// writeJSONAtomic writes v as indented JSON through a temp file and rename,
// so a crash never leaves a half-written file behind.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteJSONAtomic is exported for other packages that persist small files.
func WriteJSONAtomic(path string, v any) error { return writeJSONAtomic(path, v) }
