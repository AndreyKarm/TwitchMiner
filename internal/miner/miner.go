// Package miner implements the drops mining state machine: it loads the
// drops inventory, picks wanted games and live channels, "watches" the best
// channel and claims finished drops.
package miner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/pubsub"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// UI receives state for the dashboard. Implementations must not block.
type UI interface {
	// Publish replaces the named section of dashboard state.
	Publish(section string, v any)
	// Print appends a line to the dashboard console.
	Print(line string)
}

// State is a step of the mining state machine.
type State int

const (
	StateIdle State = iota
	StateInventoryFetch
	StateGamesUpdate
	StateChannelsCleanup
	StateChannelsFetch
	StateChannelSwitch
)

// policy is the slice of settings the models consult, cached so model code
// never touches the settings store.
type policy struct {
	games          []string
	priorityOnly   bool
	endingSoonest  bool
	benefits       map[string]bool
	keywords       []string
	keywordKey     string
	allowUnlinked  bool
	refreshMinutes int
	checkMinutes   int
}

func newPolicy(s config.Settings) policy {
	return policy{
		games:          s.GamesToWatch,
		priorityOnly:   s.PriorityListOnly,
		endingSoonest:  s.PriorityEndingSoonest,
		benefits:       s.MiningBenefits,
		keywords:       s.DropNameBlacklist,
		keywordKey:     strings.ToLower(strings.Join(s.DropNameBlacklist, "\x00")) + "\x01",
		allowUnlinked:  s.AllowUnlinkedCampaigns,
		refreshMinutes: s.RefreshIntervalMinutes,
		checkMinutes:   s.CampaignCheckMinutes,
	}
}

// sameMining reports whether p and o pick the same games and drops, so a
// settings save that only touches alerts or connection details doesn't
// restart mining.
func (p policy) sameMining(o policy) bool {
	return slices.Equal(p.games, o.games) &&
		p.priorityOnly == o.priorityOnly &&
		p.endingSoonest == o.endingSoonest &&
		maps.Equal(p.benefits, o.benefits) &&
		p.keywordKey == o.keywordKey &&
		p.allowUnlinked == o.allowUnlinked
}

// gamesAdded reports whether next lists a game that prev doesn't.
func gamesAdded(prev, next []string) bool {
	return slices.ContainsFunc(next, func(name string) bool {
		return !slices.ContainsFunc(prev, func(p string) bool { return strings.EqualFold(p, name) })
	})
}

type dirtyFlags uint8

const (
	dirtyChannels dirtyFlags = 1 << iota
	dirtyInventory
	dirtyProgress
	dirtyWanted
	dirtyManual
	dirtyGames
	dirtyAll = 0xff
)

type progressState struct {
	drop *Drop
	at   time.Time
}

// Miner owns all mining state. Fields below mu are guarded by it; network
// I/O is never performed while holding it.
type Miner struct {
	settings *config.Store
	http     *twitch.HTTP
	auth     *twitch.Auth
	gql      *twitch.GQL
	ui       UI

	stateCh      chan struct{}
	watchCh      chan struct{}
	watchRestart chan struct{}

	printMu     sync.Mutex
	lastPrinted string

	seen  *seenCampaigns
	icons *gameIcons
	chats chatFinder

	// crashed is set when a panic ended the session.
	crashed atomic.Bool
	// outdatedAlerted holds GQL operations already reported as outdated.
	outdatedMu      sync.Mutex
	outdatedAlerted map[string]bool

	mu                 sync.Mutex
	policy             policy
	netQuality         int
	netProxy           string
	state              State
	paused             bool
	hadSession         bool // logged in at least once since starting
	userLogout         bool // the current logout was requested by the user
	loginAlertSent     bool
	inventoryPending   bool
	gamesUpdatePending bool
	inventoryLoaded    bool
	inventoryFailures  int
	lastInventoryOK    time.Time
	sessionStarted     time.Time
	catalogHidden      bool
	claimAttempts      map[string]time.Time
	catalogWarned      bool
	inventory          []*Campaign
	campaigns          map[string]*Campaign
	drops              map[string]*Drop
	wanted             []*Game
	channels           map[string]*Channel
	channelOrder       []string
	watching           *Channel
	selectedID         string
	manualChannel      *Channel
	manualGame         *Game
	triggers           []time.Time
	progress           *progressState
	dirty              dirtyFlags
	pool               *pubsub.Pool
	sctx               context.Context
	endSession         context.CancelFunc
	mntCancel          context.CancelFunc
}

// New wires a miner that keeps its own state files in dataDir. Call Run to
// start it.
func New(settings *config.Store, h *twitch.HTTP, auth *twitch.Auth, ui UI, dataDir string) *Miner {
	initial := settings.Get()
	m := &Miner{
		netQuality:   initial.ConnectionQuality,
		netProxy:     initial.Proxy,
		settings:     settings,
		http:         h,
		auth:         auth,
		gql:          twitch.NewGQL(h, auth),
		ui:           ui,
		seen:         loadSeenCampaigns(filepath.Join(dataDir, "seen-campaigns.json")),
		icons:        loadGameIcons(filepath.Join(dataDir, "game-icons.json")),
		stateCh:      make(chan struct{}, 1),
		watchCh:      make(chan struct{}, 1),
		watchRestart: make(chan struct{}, 1),
		policy:       newPolicy(initial),
		campaigns:    map[string]*Campaign{},
		drops:        map[string]*Drop{},
		channels:     map[string]*Channel{},
		dirty:        dirtyAll,
		sctx:         context.Background(),

		outdatedAlerted: map[string]bool{},
	}
	m.gql.OnOutdated(m.outdatedChanged)
	auth.OnInvalid(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.endSession != nil {
			m.endSession()
		}
	})
	return m
}

// Run mines until ctx ends, logging in again whenever the token is lost.
func (m *Miner) Run(ctx context.Context) {
	m.ui.Publish("paused", m.Paused())
	m.publishGameIcons(false)
	m.syncChatFinder(m.settings.Get())
	go m.keepRunning(ctx, "dashboard updates", m.uiLoop)
	for ctx.Err() == nil {
		err := m.auth.EnsureLogin(ctx, m.showLogin)
		if err != nil {
			return
		}
		m.runSession(ctx)
		if m.crashed.Swap(false) {
			m.print(fmt.Sprintf("Restarting mining in %d seconds after an internal error", int(crashPause.Seconds())))
			if twitch.Sleep(ctx, crashPause) != nil {
				return
			}
		}
	}
}

func (m *Miner) showLogin(s twitch.LoginState) {
	m.ui.Publish("login", s)
	switch s.State {
	case "checking":
		m.setStatus("Checking the saved Twitch login...")
	case "code":
		m.setStatus("Waiting for Twitch login")
		m.print(fmt.Sprintf("Log in by entering code %s at %s", s.UserCode, s.VerificationURI))
		// Alert once when Twitch, not the user, ended a working session.
		m.mu.Lock()
		alert := m.hadSession && !m.userLogout && !m.loginAlertSent
		m.loginAlertSent = m.loginAlertSent || alert
		m.mu.Unlock()
		if alert {
			m.goSafe("login alert", func() {
				m.sendAlerts(config.AlertLoginRequired, loginAlert(s.UserCode, s.VerificationURI))
			})
		}
	case "logged_in":
		m.mu.Lock()
		m.userLogout, m.loginAlertSent = false, false
		m.mu.Unlock()
		m.print(fmt.Sprintf("Logged in as %s", s.Login))
	case "error":
		m.print(s.Error)
	}
}

// runSession mines with the current login until the session ends.
func (m *Miner) runSession(ctx context.Context) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pool := pubsub.New(m.http.WebsocketClient, m.auth.Token, func(s pubsub.Status) {
		m.ui.Publish("pubsub", s)
	})
	// PubSub refusing the token usually means it was revoked; ask Twitch, so
	// a dead token leads to a new login instead of silent missing events.
	pool.OnAuthError(func() {
		m.goSafe("login check", func() { m.auth.Recheck(sctx) })
	})

	m.mu.Lock()
	m.sctx, m.endSession, m.pool = sctx, cancel, pool
	m.state = StateIdle
	m.hadSession = true
	m.sessionStarted = time.Now()
	m.mu.Unlock()

	pool.Start(sctx)
	uid := m.auth.UserID()
	if err := pool.Add([]pubsub.Topic{
		{ID: pubsub.TopicID(config.TopicUserDrops, uid), Handle: m.safeHandler("drop event", m.handleDrops)},
		{ID: pubsub.TopicID(config.TopicUserNotifications, uid), Handle: m.safeHandler("notification", m.handleNotifications)},
	}); err != nil {
		slog.Error("subscribing user topics", "err", err)
	}

	var wg sync.WaitGroup
	wg.Go(func() { m.sessionTask("watch loop", cancel, func() { m.watchLoop(sctx) }) })
	wg.Go(func() {
		defer m.recoverPanic("game picture lookup")
		m.lookupGameIcons(sctx)
	})
	wg.Go(func() {
		defer m.recoverPanic("sleep detection")
		m.watchForResume(sctx)
	})
	m.RequestInventoryRefresh()
	m.sessionTask("state loop", cancel, func() { m.stateLoop(sctx) })

	cancel()
	pool.Stop()
	wg.Wait()
	m.resetSession()
}

// resetSession drops all account-derived state after a session ends.
func (m *Miner) resetSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mntCancel != nil {
		m.mntCancel()
		m.mntCancel = nil
	}
	for _, ch := range m.channels {
		ch.cancelPending()
	}
	m.channels = map[string]*Channel{}
	m.channelOrder = nil
	m.inventory = nil
	m.campaigns = map[string]*Campaign{}
	m.drops = map[string]*Drop{}
	m.wanted = nil
	m.watching = nil
	m.selectedID = ""
	m.manualChannel, m.manualGame = nil, nil
	m.triggers = nil
	m.progress = nil
	m.inventoryLoaded = false
	m.inventoryFailures = 0
	m.catalogHidden = false
	m.claimAttempts = nil
	m.catalogWarned = false
	m.inventoryPending = false
	m.gamesUpdatePending = false
	m.pool = nil
	m.endSession = nil
	m.sctx = context.Background()
	m.dirty = dirtyAll
	m.ui.Publish("pubsub", pubsub.Status{})
	m.ui.Publish("checks", CheckSchedule{})
}

// --- public API used by the dashboard ---

// RequestInventoryRefresh queues an inventory reload.
func (m *Miner) RequestInventoryRefresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inventoryPending = true
	signal(m.stateCh)
}

// RequestGamesUpdate queues a recalculation of the wanted games.
func (m *Miner) RequestGamesUpdate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestGamesUpdateLocked()
}

func (m *Miner) requestGamesUpdateLocked() {
	m.gamesUpdatePending = true
	signal(m.stateCh)
}

// SettingsChanged applies new settings to the running miner. Only changes
// that affect what's mined trigger a recalculation, and only connection
// changes rebuild the HTTP client, so saving unrelated settings never
// interrupts watching.
func (m *Miner) SettingsChanged(s config.Settings) {
	m.mu.Lock()
	netChanged := s.ConnectionQuality != m.netQuality || s.Proxy != m.netProxy
	m.netQuality, m.netProxy = s.ConnectionQuality, s.Proxy
	old, next := m.policy, newPolicy(s)
	miningChanged := !old.sameMining(next)
	refreshChanged := next.refreshMinutes != old.refreshMinutes || next.checkMinutes != old.checkMinutes
	m.policy = next
	if miningChanged {
		m.dirty |= dirtyInventory | dirtyWanted
		m.requestGamesUpdateLocked()
	}
	// Without Twitch's campaign list, a newly listed game's campaigns are
	// only found by searching its channels, so search now rather than at the
	// next periodic reload.
	added := gamesAdded(old.games, next.games)
	if m.catalogHidden && added {
		m.inventoryPending = true
		signal(m.stateCh)
	}
	sctx, loaded, inSession := m.sctx, m.inventoryLoaded, m.endSession != nil
	m.mu.Unlock()

	if added && inSession {
		m.goSafe("game picture lookup", func() { m.lookupGameIcons(sctx) })
	}

	if netChanged {
		if err := m.http.Configure(s.ConnectionQuality, s.Proxy); err != nil {
			m.print("Could not apply connection settings: " + err.Error())
		}
	}
	if refreshChanged && loaded {
		m.restartMaintenance(sctx)
	}
	m.syncChatFinder(s)
	m.ui.Publish("settings", s.Public())
}

// SelectChannel switches to the channel the user clicked, if it's watchable.
func (m *Miner) SelectChannel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.channels[id] == nil {
		return false
	}
	m.selectedID = id
	m.changeStateLocked(StateChannelSwitch)
	return true
}

// ExitManualMode returns to automatic channel selection.
func (m *Miner) ExitManualMode() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clearManualLocked("left manual mode") {
		m.changeStateLocked(StateChannelSwitch)
	}
}

// SetPaused stops or resumes mining. While paused nothing is watched and
// queued reloads wait, but drops Twitch reports as ready are still claimed.
func (m *Miner) SetPaused(paused bool) {
	m.mu.Lock()
	if m.paused == paused {
		m.mu.Unlock()
		return
	}
	m.paused = paused
	if paused {
		m.stopWatchingLocked()
		m.changeStateLocked(StateIdle)
	} else if m.inventoryLoaded {
		m.requestGamesUpdateLocked()
	} else {
		m.inventoryPending = true
		signal(m.stateCh)
	}
	m.mu.Unlock()
	m.ui.Publish("paused", paused)
	if paused {
		m.print("Mining paused")
	} else {
		m.print("Mining resumed")
	}
}

// Paused reports whether mining is paused.
func (m *Miner) Paused() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused
}

// Logout forgets the token; the miner falls back to the login screen.
func (m *Miner) Logout() {
	m.print("Logging out")
	m.mu.Lock()
	m.userLogout = true
	m.mu.Unlock()
	m.auth.Logout()
}

// --- state machine ---

func (m *Miner) changeStateLocked(s State) {
	m.state = s
	signal(m.stateCh)
}

// activatePendingLocked lets queued refreshes take over before the next wait.
func (m *Miner) activatePendingLocked() {
	if m.paused {
		return // keep requests queued until mining resumes
	}
	if m.inventoryPending {
		m.state = StateInventoryFetch
		signal(m.stateCh)
	}
	if m.gamesUpdatePending && m.inventoryLoaded && m.state != StateInventoryFetch {
		m.state = StateGamesUpdate
		signal(m.stateCh)
	}
}

func (m *Miner) stateLoop(ctx context.Context) {
	fullCleanup := false
	for ctx.Err() == nil {
		m.mu.Lock()
		m.activatePendingLocked()
		state := m.state
		m.mu.Unlock()

		switch state {
		case StateIdle:
			m.mu.Lock()
			if m.paused {
				m.setStatus("Paused")
			} else {
				m.setStatus("Idle")
			}
			m.stopWatchingLocked()
			drain(m.stateCh)
			m.mu.Unlock()

		case StateInventoryFetch:
			m.mu.Lock()
			m.inventoryPending = false
			m.mu.Unlock()
			if err := m.fetchInventory(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				m.inventoryFailed(ctx, err)
				break
			}
			m.mu.Lock()
			m.inventoryLoaded = true
			m.inventoryFailures = 0
			m.changeStateLocked(StateGamesUpdate)
			m.mu.Unlock()

		case StateGamesUpdate:
			m.claimAvailable(ctx)
			m.mu.Lock()
			m.gamesUpdatePending = false
			m.updateWantedLocked()
			alerts, seen := m.collectStartedLocked()
			fullCleanup = true
			m.restartWatchingLocked()
			m.changeStateLocked(StateChannelsCleanup)
			m.mu.Unlock()
			m.goSafe("campaign alerts", func() { m.deliverStarted(alerts, seen) })

		case StateChannelsCleanup:
			m.setStatus("Cleaning up channels...")
			m.mu.Lock()
			m.cleanupChannelsLocked(fullCleanup)
			fullCleanup = false
			if len(m.wanted) > 0 {
				m.changeStateLocked(StateChannelsFetch)
			} else {
				m.changeStateLocked(StateIdle)
			}
			noWanted := len(m.wanted) == 0
			m.mu.Unlock()
			if noWanted {
				m.printOnce("No active campaigns to mine drops for. Waiting for an active campaign...")
			}

		case StateChannelsFetch:
			m.fetchChannels(ctx)

		case StateChannelSwitch:
			m.switchChannel()
		}

		m.mu.Lock()
		m.activatePendingLocked()
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-m.stateCh:
		}
	}
}

// inventoryRetryDelay backs off 1, 2, 4, 8 and then 15 minutes.
func inventoryRetryDelay(failures int) time.Duration {
	return min(time.Minute<<min(max(failures-1, 0), 4), 15*time.Minute)
}

// inventoryFailed schedules a retry. With an inventory already loaded the
// miner keeps watching on what it knows, so a network blip doesn't stop
// mining.
func (m *Miner) inventoryFailed(ctx context.Context, err error) {
	m.mu.Lock()
	m.inventoryFailures++
	delay := inventoryRetryDelay(m.inventoryFailures)
	m.publishScheduleLocked(time.Now().Add(delay))
	if m.inventoryLoaded {
		m.changeStateLocked(StateChannelSwitch)
	} else {
		m.changeStateLocked(StateIdle)
	}
	m.mu.Unlock()
	m.print(fmt.Sprintf("Loading the drops inventory failed, retrying in %d min: %v", int(delay.Minutes()), err))
	m.goSafe("inventory retry", func() {
		if twitch.Sleep(ctx, delay) == nil {
			m.RequestInventoryRefresh()
		}
	})
}

// claimAvailable claims every finished drop of started campaigns.
func (m *Miner) claimAvailable(ctx context.Context) {
	m.mu.Lock()
	var claimable []*Drop
	uid := m.auth.UserID()
	now := time.Now()
	for _, c := range m.inventory {
		if c.Upcoming() {
			continue
		}
		// Fully watched drops Twitch hasn't given a claim ID get one built.
		m.readyToClaimLocked(c, uid, now)
		for _, d := range c.Drops {
			if d.CanClaim() {
				claimable = append(claimable, d)
			}
		}
	}
	m.mu.Unlock()
	for _, d := range claimable {
		m.claimDrop(ctx, d)
	}
}

// updateWantedLocked rebuilds the wanted games list and adjusts manual mode.
func (m *Miner) updateWantedLocked() {
	tree := m.wantedTreeLocked()
	m.wanted = m.wanted[:0]
	for _, g := range tree {
		m.wanted = append(m.wanted, g.game)
	}
	if len(m.wanted) > 0 {
		names := make([]string, len(m.wanted))
		for i, g := range m.wanted {
			names[i] = g.Name
		}
		slog.Info("wanted games", "games", strings.Join(names, ", "))
	}
	if m.manualGame != nil {
		nextHour := time.Now().Add(time.Hour)
		hasDrops := false
		for _, c := range m.inventory {
			if sameGame(c.Game, m.manualGame) && c.CanEarnWithin(nextHour) {
				hasDrops = true
				break
			}
		}
		if !hasDrops {
			m.clearManualLocked("all drops completed for the manual game")
		} else if i := m.wantedIndexLocked(m.manualGame); i > 0 {
			g := m.wanted[i]
			m.wanted = slices.Delete(m.wanted, i, i+1)
			m.wanted = slices.Insert(m.wanted, 0, g)
		}
	}
	m.dirty |= dirtyInventory | dirtyWanted
}

func (m *Miner) cleanupChannelsLocked(full bool) {
	var remove []*Channel
	for _, id := range m.channelOrder {
		ch := m.channels[id]
		if ch == nil {
			continue
		}
		if full || len(m.wanted) == 0 ||
			!ch.ACLBased && (ch.Offline() || m.wantedIndexLocked(ch.Game()) < 0) {
			remove = append(remove, ch)
		}
	}
	m.removeChannelsLocked(remove)
}

func (m *Miner) removeChannelsLocked(chs []*Channel) {
	if len(chs) == 0 {
		return
	}
	for _, ch := range chs {
		delete(m.channels, ch.ID)
	}
	m.channelOrder = slices.DeleteFunc(m.channelOrder, func(id string) bool { return m.channels[id] == nil })
	m.unsubscribeLocked(chs)
	m.dirty |= dirtyChannels
}

// unsubscribeLocked stops tracking updates for channels.
func (m *Miner) unsubscribeLocked(chs []*Channel) {
	topics := make([]string, 0, len(chs)*2)
	for _, ch := range chs {
		ch.cancelPending()
		topics = append(topics,
			pubsub.TopicID(config.TopicChannelStreamState, ch.ID),
			pubsub.TopicID(config.TopicChannelUpdate, ch.ID))
	}
	if m.pool != nil {
		m.pool.Remove(topics)
	}
}

// fetchChannels gathers ACL channels of wanted campaigns plus live
// directory channels for wanted games without an ACL.
func (m *Miner) fetchChannels(ctx context.Context) {
	m.setStatus("Gathering channels...")
	nextHour := time.Now().Add(time.Hour)

	m.mu.Lock()
	known := m.channels
	m.channels = map[string]*Channel{}
	m.channelOrder = nil
	var aclNew []*Channel
	aclSeen := map[string]bool{}
	var noACL []*Game
	for _, c := range m.inventory {
		if m.wantedIndexLocked(c.Game) < 0 || !c.CanEarnWithin(nextHour) {
			continue
		}
		if len(c.ACL) == 0 {
			if !slices.ContainsFunc(noACL, func(g *Game) bool { return sameGame(g, c.Game) }) {
				noACL = append(noACL, c.Game)
			}
			continue
		}
		for _, a := range c.ACL {
			if known[a.ID] == nil && !aclSeen[a.ID] {
				aclSeen[a.ID] = true
				aclNew = append(aclNew, newChannel(a.ID, a.Login, a.DisplayName, true))
			}
		}
	}
	m.mu.Unlock()

	// The new channel objects aren't shared yet, so they're filled without the lock.
	if err := m.bulkCheckOnline(ctx, aclNew); err != nil && ctx.Err() == nil {
		m.print("Checking campaign channels failed: " + err.Error())
	}
	var directory []*Channel
	for _, g := range noACL {
		chs, err := m.liveStreams(ctx, g, 20, true)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.print(fmt.Sprintf("Loading live channels for %s failed: %v", g.Name, err))
			continue
		}
		directory = append(directory, chs...)
	}
	if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	merged := known
	for _, ch := range append(aclNew, directory...) {
		if merged[ch.ID] == nil {
			merged[ch.ID] = ch
		}
	}
	ordered := make([]*Channel, 0, len(merged))
	for _, ch := range merged {
		ordered = append(ordered, ch)
	}
	slices.SortStableFunc(ordered, func(a, b *Channel) int {
		if c := m.comparePriorityLocked(a, b); c != 0 {
			return c
		}
		if a.ACLBased != b.ACLBased {
			if a.ACLBased {
				return -1
			}
			return 1
		}
		return b.Viewers() - a.Viewers()
	})
	if len(ordered) > config.MaxChannels {
		// Drop the low-priority tail, which holds offline and unwanted channels.
		m.unsubscribeLocked(ordered[config.MaxChannels:])
		ordered = ordered[:config.MaxChannels]
	}
	m.channels = make(map[string]*Channel, len(ordered))
	m.channelOrder = make([]string, 0, len(ordered))
	topics := make([]pubsub.Topic, 0, len(ordered)*2)
	for _, ch := range ordered {
		m.channels[ch.ID] = ch
		m.channelOrder = append(m.channelOrder, ch.ID)
		id := ch.ID
		topics = append(topics,
			pubsub.Topic{ID: pubsub.TopicID(config.TopicChannelStreamState, id), Handle: m.safeHandler("stream event", func(msg json.RawMessage) { m.handleStreamState(id, msg) })},
			pubsub.Topic{ID: pubsub.TopicID(config.TopicChannelUpdate, id), Handle: m.safeHandler("channel update", func(json.RawMessage) { m.handleStreamUpdate(id) })},
		)
	}
	if m.pool != nil {
		if err := m.pool.Add(topics); err != nil {
			slog.Error("subscribing channel topics", "err", err)
		}
	}

	// Keep watching the same channel through the refresh when we still can.
	// Otherwise stop: a channel left out of the list gets no more status
	// updates, so it would be watched long after its stream ends.
	if m.watching != nil {
		if ch := m.channels[m.watching.ID]; ch != nil && m.canWatchLocked(ch) {
			m.watchLocked(ch, false)
		} else {
			m.stopWatchingLocked()
		}
	}
	for _, ch := range ordered {
		if m.canWatchLocked(ch) {
			if c := m.activeCampaignLocked(ch); c != nil {
				if d := c.FirstDrop(); d != nil {
					m.displayDropLocked(d)
				}
			}
			break
		}
	}
	m.dirty |= dirtyChannels
	m.changeStateLocked(StateChannelSwitch)
}

func (m *Miner) switchChannel() {
	m.setStatus("Switching channel...")
	m.mu.Lock()
	var next *Channel
	selected := m.channels[m.selectedID]
	m.selectedID = ""
	watching := m.watching

	switch {
	case selected != nil && m.canWatchLocked(selected):
		if watching != nil && !sameGame(selected.Game(), watching.Game()) {
			m.enterManualLocked(selected)
		}
		next = selected
	case m.manualGame != nil:
		// Channel objects are replaced on refresh, so look the target up by ID.
		var target *Channel
		if m.manualChannel != nil {
			target = m.channels[m.manualChannel.ID]
		}
		if target != nil && m.canWatchLocked(target) {
			next = target
			m.manualChannel = target
		} else {
			for _, ch := range m.orderedChannelsLocked() {
				if sameGame(ch.Game(), m.manualGame) && m.canWatchLocked(ch) {
					next = ch
					m.manualChannel = ch
					m.dirty |= dirtyManual
					break
				}
			}
			if next == nil {
				m.clearManualLocked("no channels left for the manual game")
			}
		}
	}
	if next == nil && m.manualGame == nil {
		candidates := m.orderedChannelsLocked()
		slices.SortStableFunc(candidates, m.comparePriorityLocked)
		for _, ch := range candidates {
			if m.canWatchLocked(ch) && m.shouldSwitchLocked(ch) {
				next = ch
				break
			}
		}
	}

	var status string
	switch {
	case next != nil:
		m.watchLocked(next, true)
		if c := m.activeCampaignLocked(next); c != nil {
			if d := c.FirstDrop(); d != nil {
				m.displayDropLocked(d)
			}
		}
		drain(m.stateCh)
	case watching != nil && m.canWatchLocked(watching):
		status = m.watchingTextLocked(watching)
		drain(m.stateCh)
	default:
		m.changeStateLocked(StateIdle)
	}
	m.mu.Unlock()

	if status != "" {
		m.setStatus(status)
	} else if next == nil {
		m.print("No available channels to watch. Waiting for an ONLINE channel...")
	}
}

// --- channel selection rules ---

func (m *Miner) orderedChannelsLocked() []*Channel {
	out := make([]*Channel, 0, len(m.channelOrder))
	for _, id := range m.channelOrder {
		if ch := m.channels[id]; ch != nil {
			out = append(out, ch)
		}
	}
	return out
}

func (m *Miner) wantedIndexLocked(g *Game) int {
	if g == nil {
		return -1
	}
	return slices.IndexFunc(m.wanted, func(w *Game) bool { return w.ID == g.ID })
}

// canWatchLocked reports whether ch is online and progresses a campaign of
// a wanted game.
func (m *Miner) canWatchLocked(ch *Channel) bool {
	if m.paused || len(m.wanted) == 0 || !ch.Online() {
		return false
	}
	for _, c := range m.inventory {
		if m.wantedIndexLocked(c.Game) >= 0 && c.CanEarn(ch) && (c.Game.IsSpecial() || ch.DropsEnabled()) {
			return true
		}
	}
	return false
}

// priority ranks a channel by its game's position in the wanted list.
func (m *Miner) priorityLocked(ch *Channel) int {
	if i := m.wantedIndexLocked(ch.Game()); i >= 0 {
		return i
	}
	return math.MaxInt
}

// miningPriorityLocked keeps listed games ahead of fallback games when
// fallback mining is on.
func (m *Miner) miningPriorityLocked(ch *Channel) (group, rank int) {
	rank = m.priorityLocked(ch)
	if m.policy.priorityOnly {
		return 0, rank
	}
	if ch.Online() {
		for _, c := range m.inventory {
			if m.wantedIndexLocked(c.Game) >= 0 && m.isListedGame(c.Game) && c.CanEarn(ch) &&
				(c.Game.IsSpecial() || ch.DropsEnabled()) {
				return 0, rank
			}
		}
	}
	return 1, rank
}

func (m *Miner) isListedGame(g *Game) bool {
	return slices.ContainsFunc(m.policy.games, func(name string) bool { return strings.EqualFold(name, g.Name) })
}

func (m *Miner) comparePriorityLocked(a, b *Channel) int {
	ag, ar := m.miningPriorityLocked(a)
	bg, br := m.miningPriorityLocked(b)
	if ag != bg {
		return ag - bg
	}
	switch {
	case ar < br:
		return -1
	case ar > br:
		return 1
	}
	return 0
}

// shouldSwitchLocked reports whether ch beats the channel being watched.
func (m *Miner) shouldSwitchLocked(ch *Channel) bool {
	w := m.watching
	if w == nil || !m.canWatchLocked(w) {
		return true
	}
	c := m.comparePriorityLocked(ch, w)
	return c < 0 || c == 0 && ch.ACLBased && !w.ACLBased
}

// activeCampaignLocked is the campaign closest to completion on the watched
// channel (or ch when nothing is watched).
func (m *Miner) activeCampaignLocked(ch *Channel) *Campaign {
	if len(m.wanted) == 0 {
		return nil
	}
	if m.watching != nil {
		ch = m.watching
	}
	if ch == nil {
		return nil
	}
	var best *Campaign
	for _, c := range m.inventory {
		if c.CanEarn(ch) && (best == nil || c.RemainingMinutes() < best.RemainingMinutes()) {
			best = c
		}
	}
	return best
}

// --- watching ---

func (m *Miner) watchingTextLocked(ch *Channel) string {
	if m.manualGame != nil {
		return fmt.Sprintf("Manual mode: watching %s for %s", ch.Name(), m.manualGame.Name)
	}
	return "Watching: " + ch.Name()
}

func (m *Miner) watchLocked(ch *Channel, announce bool) {
	m.watching = ch
	signal(m.watchCh)
	m.dirty |= dirtyChannels
	if announce {
		text := m.watchingTextLocked(ch)
		m.print(text)
		m.setStatus(text)
	}
}

func (m *Miner) stopWatchingLocked() {
	if m.watching != nil || m.progress != nil {
		m.dirty |= dirtyChannels | dirtyProgress
	}
	m.watching = nil
	m.progress = nil
}

// restartWatchingLocked makes the watch loop poll again right away. The
// progress card stays; stopWatchingLocked clears it when watching ends.
func (m *Miner) restartWatchingLocked() {
	signal(m.watchRestart)
}

func (m *Miner) displayDropLocked(d *Drop) {
	m.progress = &progressState{drop: d, at: time.Now()}
	m.dirty |= dirtyProgress
}

// --- manual mode ---

func (m *Miner) enterManualLocked(ch *Channel) {
	g := ch.Game()
	if g == nil {
		return
	}
	m.manualChannel, m.manualGame = ch, g
	m.dirty |= dirtyManual
	slog.Info("entered manual mode", "game", g.Name, "channel", ch.Name())
}

func (m *Miner) clearManualLocked(reason string) bool {
	if m.manualGame == nil && m.manualChannel == nil {
		return false
	}
	slog.Info("leaving manual mode", "reason", reason)
	m.manualChannel, m.manualGame = nil, nil
	m.dirty |= dirtyManual
	return true
}

// --- output ---

func (m *Miner) setStatus(text string) { m.ui.Publish("status", text) }

func (m *Miner) print(msg string) {
	m.printMu.Lock()
	m.lastPrinted = msg
	m.printMu.Unlock()
	slog.Info(msg)
	m.ui.Print(msg)
}

// printOnce suppresses a message identical to the previous console line.
func (m *Miner) printOnce(msg string) {
	m.printMu.Lock()
	dup := m.lastPrinted == msg
	m.printMu.Unlock()
	if !dup {
		m.print(msg)
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func drain(ch chan struct{}) {
	select {
	case <-ch:
	default:
	}
}
