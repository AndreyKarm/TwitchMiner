package miner

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// Model methods read and mutate shared state and assume the caller holds
// Miner.mu.

// Game is a Twitch category.
type Game struct {
	ID        string
	Name      string
	Slug      string
	BoxArtURL string
}

type rawGame struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Slug        string `json:"slug"`
	BoxArtURL   string `json:"boxArtURL"`
}

func newGame(r *rawGame) *Game {
	if r == nil || r.ID == "" {
		return nil
	}
	g := &Game{ID: r.ID, Name: r.DisplayName, Slug: r.Slug, BoxArtURL: r.BoxArtURL}
	if g.Name == "" {
		g.Name = r.Name
	}
	if g.Slug == "" {
		g.Slug = slugify(g.Name)
	}
	return g
}

// IsSpecial reports whether campaigns for this category may progress on
// ACL channels streaming other categories.
func (g *Game) IsSpecial() bool { return g != nil && config.SpecialGameIDs[g.ID] }

func sameGame(a, b *Game) bool { return a != nil && b != nil && a.ID == b.ID }

var (
	slugNonWord = regexp.MustCompile(`[^\p{L}\p{N}_]+`)
	slugDashes  = regexp.MustCompile(`-{2,}`)
)

// slugify turns a game name into the slug the directory query expects.
func slugify(name string) string {
	s := strings.ReplaceAll(strings.ToLower(name), "'", "")
	s = slugNonWord.ReplaceAllString(s, "-")
	return slugDashes.ReplaceAllString(strings.Trim(s, "-"), "-")
}

// Benefit is a drop reward.
type Benefit struct {
	ID       string
	Name     string
	Type     string
	ImageURL string
}

func (b Benefit) isBadgeOrEmote() bool { return b.Type == "BADGE" || b.Type == "EMOTE" }

type rawBenefit struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DistributionType string `json:"distributionType"`
	ImageAssetURL    string `json:"imageAssetURL"`
}

func newBenefit(r rawBenefit) Benefit {
	t := r.DistributionType
	switch t {
	case "BADGE", "EMOTE", "DIRECT_ENTITLEMENT":
	default:
		t = "UNKNOWN"
	}
	return Benefit{ID: r.ID, Name: r.Name, Type: t, ImageURL: r.ImageAssetURL}
}

// Drop is one timed reward inside a campaign.
type Drop struct {
	Campaign        *Campaign
	ID              string
	Name            string
	Benefits        []Benefit
	StartsAt        time.Time
	EndsAt          time.Time
	ClaimID         string
	IsClaimed       bool
	Preconditions   []string
	RealMinutes     int
	RequiredMinutes int
	// ExtraMinutes are estimated locally while Twitch reports no progress.
	ExtraMinutes int
}

type rawDrop struct {
	ID                     string    `json:"id"`
	Name                   string    `json:"name"`
	StartAt                time.Time `json:"startAt"`
	EndAt                  time.Time `json:"endAt"`
	RequiredMinutesWatched int       `json:"requiredMinutesWatched"`
	BenefitEdges           []struct {
		Benefit rawBenefit `json:"benefit"`
	} `json:"benefitEdges"`
	PreconditionDrops []struct {
		ID string `json:"id"`
	} `json:"preconditionDrops"`
	Self *struct {
		CurrentMinutesWatched int     `json:"currentMinutesWatched"`
		DropInstanceID        *string `json:"dropInstanceID"`
		IsClaimed             bool    `json:"isClaimed"`
	} `json:"self"`
}

func newDrop(c *Campaign, r rawDrop, claimedBenefits map[string]time.Time) *Drop {
	d := &Drop{
		Campaign:        c,
		ID:              r.ID,
		Name:            r.Name,
		StartsAt:        r.StartAt,
		EndsAt:          r.EndAt,
		RequiredMinutes: r.RequiredMinutesWatched,
	}
	for _, e := range r.BenefitEdges {
		d.Benefits = append(d.Benefits, newBenefit(e.Benefit))
	}
	for _, p := range r.PreconditionDrops {
		d.Preconditions = append(d.Preconditions, p.ID)
	}
	if r.Self != nil {
		if r.Self.DropInstanceID != nil {
			d.ClaimID = *r.Self.DropInstanceID
		}
		d.IsClaimed = r.Self.IsClaimed
		d.RealMinutes = r.Self.CurrentMinutesWatched
	} else if len(d.Benefits) > 0 {
		// Without progress data, treat the drop as claimed when every reward
		// was awarded while the drop was running.
		all := true
		for _, b := range d.Benefits {
			at, ok := claimedBenefits[b.ID]
			if !ok || at.Before(d.StartsAt) || !at.Before(d.EndsAt) {
				all = false
				break
			}
		}
		d.IsClaimed = all
	}
	if d.IsClaimed {
		// Claimed drops can report inconsistent minutes.
		d.RealMinutes = d.RequiredMinutes
	}
	return d
}

// CurrentMinutes includes locally estimated minutes.
func (d *Drop) CurrentMinutes() int { return d.RealMinutes + d.ExtraMinutes }

// IsWatchDrop reports whether the drop is earned by watching.
func (d *Drop) IsWatchDrop() bool { return d.RequiredMinutes > 0 }

// RemainingMinutes for this drop alone.
func (d *Drop) RemainingMinutes() int { return d.RequiredMinutes - d.CurrentMinutes() }

// TotalRemainingMinutes includes the longest unclaimed prerequisite chain.
func (d *Drop) TotalRemainingMinutes() int { return d.chainMinutes(true, map[string]bool{}) }

// TotalRequiredMinutes includes the longest prerequisite chain.
func (d *Drop) TotalRequiredMinutes() int { return d.chainMinutes(false, map[string]bool{}) }

func (d *Drop) chainMinutes(remaining bool, visiting map[string]bool) int {
	if visiting[d.ID] || remaining && d.IsClaimed {
		return 0
	}
	visiting[d.ID] = true
	defer delete(visiting, d.ID)
	best := 0
	for _, id := range d.Preconditions {
		if pre := d.Campaign.dropByID[id]; pre != nil {
			best = max(best, pre.chainMinutes(remaining, visiting))
		}
	}
	if remaining {
		return d.RemainingMinutes() + best
	}
	return d.RequiredMinutes + best
}

// Progress is the watched fraction, 0..1.
func (d *Drop) Progress() float64 {
	cur := d.CurrentMinutes()
	switch {
	case cur <= 0 || !d.IsWatchDrop():
		return 0
	case cur >= d.RequiredMinutes:
		return 1
	}
	return float64(cur) / float64(d.RequiredMinutes)
}

func (d *Drop) preconditionsMet() bool {
	for _, id := range d.Preconditions {
		pre := d.Campaign.dropByID[id]
		if pre == nil || !pre.IsClaimed {
			return false
		}
	}
	return true
}

// IgnoreReason explains why an unclaimed drop is skipped, or nil.
func (d *Drop) IgnoreReason() *IgnoreReason {
	if d.IsClaimed {
		return nil
	}
	return d.Campaign.evaluation().Reasons[d.ID]
}

// IsMineable reports whether the drop is unclaimed and still leads to a
// wanted reward.
func (d *Drop) IsMineable() bool {
	return !d.IsClaimed && d.Campaign.evaluation().Mineable[d.ID]
}

func (d *Drop) baseEarnConditions() bool {
	return d.IsMineable() && d.preconditionsMet() && d.IsWatchDrop() &&
		d.ExtraMinutes < config.MaxExtraMinutes
}

func (d *Drop) baseCanEarn(now time.Time) bool {
	return d.baseEarnConditions() && !now.Before(d.StartsAt) && now.Before(d.EndsAt)
}

func (d *Drop) canEarnWithin(now, stamp time.Time) bool {
	return d.baseEarnConditions() && d.EndsAt.After(now) && d.StartsAt.Before(stamp)
}

// CanEarn reports whether the drop progresses on ch right now (any channel
// if ch is nil).
func (d *Drop) CanEarn(ch *Channel) bool {
	now := time.Now()
	return d.baseCanEarn(now) && d.Campaign.baseCanEarn(ch, false, now)
}

// CanClaim reports whether a claim request may succeed. Twitch allows
// claims up to 24 hours after the campaign ends.
func (d *Drop) CanClaim() bool {
	return d.ClaimID != "" && !d.IsClaimed && time.Now().Before(d.Campaign.EndsAt.Add(24*time.Hour))
}

func (d *Drop) rewardsText() string {
	names := make([]string, len(d.Benefits))
	for i, b := range d.Benefits {
		names[i] = b.Name
	}
	return strings.Join(names, ", ")
}

func (d *Drop) wantedUnclaimedBenefits(allowed map[string]bool) []string {
	if d.IsClaimed || !d.IsMineable() {
		return nil
	}
	var out []string
	for _, b := range d.Benefits {
		if allowed[b.Type] {
			out = append(out, b.Name)
		}
	}
	return out
}

// updateRealMinutes applies an authoritative progress delta.
func (d *Drop) updateRealMinutes(delta int) bool {
	if delta == 0 || d.RealMinutes+delta < 0 || !d.CanEarn(nil) {
		return false
	}
	d.RealMinutes = min(d.RealMinutes+delta, d.RequiredMinutes)
	d.ExtraMinutes = 0
	return true
}

// bumpMinutes adds one estimated minute; it reports whether the estimate
// hit the cap.
func (d *Drop) bumpMinutes(ch *Channel) (bumped, capped bool) {
	if !d.CanEarn(ch) {
		return false, false
	}
	d.ExtraMinutes++
	return true, d.ExtraMinutes >= config.MaxExtraMinutes
}

// Campaign is a drops campaign for one game.
type Campaign struct {
	m        *Miner
	ID       string
	Name     string
	Game     *Game
	Linked   bool
	LinkURL  string
	StartsAt time.Time
	EndsAt   time.Time
	valid    bool
	// ACL lists the only channels this campaign progresses on, if any.
	ACL      []aclChannel
	aclIDs   map[string]bool
	Drops    []*Drop
	dropByID map[string]*Drop
	// Discovered campaigns were found on live channels because Twitch hides
	// the campaign list; their account-link status is unknown.
	Discovered bool

	evalKey string
	eval    *Evaluation
}

type aclChannel struct {
	ID          string `json:"id"`
	Login       string `json:"name"`
	DisplayName string `json:"displayName"`
}

type rawCampaign struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	StartAt        time.Time `json:"startAt"`
	EndAt          time.Time `json:"endAt"`
	AccountLinkURL string    `json:"accountLinkURL"`
	Game           *rawGame  `json:"game"`
	Self           *struct {
		IsAccountConnected bool `json:"isAccountConnected"`
	} `json:"self"`
	Allow *struct {
		Channels  []aclChannel `json:"channels"`
		IsEnabled *bool        `json:"isEnabled"`
	} `json:"allow"`
	TimeBasedDrops []rawDrop `json:"timeBasedDrops"`
	Discovered     bool      `json:"discovered"`
}

func newCampaign(m *Miner, r rawCampaign, claimedBenefits map[string]time.Time) *Campaign {
	c := &Campaign{
		m:          m,
		ID:         r.ID,
		Name:       r.Name,
		Game:       newGame(r.Game),
		LinkURL:    r.AccountLinkURL,
		StartsAt:   r.StartAt,
		EndsAt:     r.EndAt,
		valid:      r.Status != "EXPIRED",
		aclIDs:     map[string]bool{},
		dropByID:   map[string]*Drop{},
		Discovered: r.Discovered,
	}
	if r.Self != nil {
		c.Linked = r.Self.IsAccountConnected
	}
	if r.Allow != nil && len(r.Allow.Channels) > 0 && (r.Allow.IsEnabled == nil || *r.Allow.IsEnabled) {
		for _, ch := range r.Allow.Channels {
			if ch.ID != "" && !c.aclIDs[ch.ID] {
				c.ACL = append(c.ACL, ch)
				c.aclIDs[ch.ID] = true
			}
		}
	}
	for _, rd := range r.TimeBasedDrops {
		d := newDrop(c, rd, claimedBenefits)
		c.Drops = append(c.Drops, d)
		c.dropByID[d.ID] = d
	}
	return c
}

// URL is the campaign's page on Twitch.
func (c *Campaign) URL() string { return "https://www.twitch.tv/drops/campaigns?dropID=" + c.ID }

func (c *Campaign) watchDrops() []*Drop {
	var out []*Drop
	for _, d := range c.Drops {
		if d.IsWatchDrop() {
			out = append(out, d)
		}
	}
	return out
}

// evaluation returns the ignore-policy result, cached until the keyword
// list or any claim state changes.
func (c *Campaign) evaluation() *Evaluation {
	var b strings.Builder
	b.WriteString(c.m.policy.keywordKey)
	for _, d := range c.Drops {
		if d.IsClaimed {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	if key := b.String(); c.eval == nil || key != c.evalKey {
		c.eval = evaluateIgnorePolicy(c.m.policy.keywords, c.Drops)
		c.evalKey = key
	}
	return c.eval
}

func (c *Campaign) Active() bool {
	now := time.Now()
	return c.valid && !now.Before(c.StartsAt) && now.Before(c.EndsAt)
}

func (c *Campaign) Upcoming() bool { return c.valid && time.Now().Before(c.StartsAt) }

func (c *Campaign) Expired() bool { return !c.valid || !time.Now().Before(c.EndsAt) }

// Eligible reports whether the account can earn this campaign at all.
func (c *Campaign) Eligible() bool {
	return c.Linked || c.hasBadgeOrEmote() || c.m.policy.allowUnlinked
}

func (c *Campaign) hasBadgeOrEmote() bool {
	for _, d := range c.Drops {
		for _, b := range d.Benefits {
			if b.isBadgeOrEmote() {
				return true
			}
		}
	}
	return false
}

func (c *Campaign) mineableWatchDrops() []*Drop {
	var out []*Drop
	for _, d := range c.watchDrops() {
		if d.IsMineable() {
			out = append(out, d)
		}
	}
	return out
}

// RemainingMinutes is the longest remaining chain across mineable drops.
func (c *Campaign) RemainingMinutes() int {
	best := 0
	for _, d := range c.mineableWatchDrops() {
		best = max(best, d.TotalRemainingMinutes())
	}
	return best
}

// Availability is minutes left in the campaign per minute still needed;
// lower values are more urgent.
func (c *Campaign) Availability() float64 {
	best := math.Inf(1)
	now := time.Now()
	for _, d := range c.mineableWatchDrops() {
		if rem := d.TotalRemainingMinutes(); rem > 0 && now.Before(d.EndsAt) {
			best = math.Min(best, d.EndsAt.Sub(now).Minutes()/float64(rem))
		}
	}
	return best
}

// FirstDrop is the earnable drop closest to completion.
func (c *Campaign) FirstDrop() *Drop {
	var best *Drop
	for _, d := range c.watchDrops() {
		if d.CanEarn(nil) && (best == nil || d.RemainingMinutes() < best.RemainingMinutes()) {
			best = d
		}
	}
	return best
}

func (c *Campaign) allowsChannel(ch *Channel) bool {
	return len(c.ACL) == 0 || c.aclIDs[ch.ID]
}

func (c *Campaign) baseCanEarn(ch *Channel, ignoreChannelStatus bool, now time.Time) bool {
	if !c.Eligible() || !c.valid || now.Before(c.StartsAt) || !now.Before(c.EndsAt) {
		return false
	}
	if ch == nil {
		return true
	}
	if !c.allowsChannel(ch) {
		return false
	}
	return ignoreChannelStatus ||
		sameGame(ch.Game(), c.Game) ||
		ch.Online() && c.Game.IsSpecial() && len(c.ACL) > 0
}

// CanEarn reports whether any drop of this campaign progresses on ch now.
func (c *Campaign) CanEarn(ch *Channel) bool {
	now := time.Now()
	if !c.baseCanEarn(ch, false, now) {
		return false
	}
	for _, d := range c.Drops {
		if d.baseCanEarn(now) {
			return true
		}
	}
	return false
}

// CanEarnWithin reports whether the campaign can be earned before stamp,
// ignoring channels.
func (c *Campaign) CanEarnWithin(stamp time.Time) bool {
	now := time.Now()
	if !c.Eligible() || !c.valid || !c.EndsAt.After(now) || !c.StartsAt.Before(stamp) {
		return false
	}
	for _, d := range c.Drops {
		if d.canEarnWithin(now, stamp) {
			return true
		}
	}
	return false
}

func (c *Campaign) hasWantedUnclaimedBenefits(allowed map[string]bool) bool {
	for _, d := range c.Drops {
		if len(d.wantedUnclaimedBenefits(allowed)) > 0 {
			return true
		}
	}
	return false
}

func (c *Campaign) timeTriggers() []time.Time {
	out := []time.Time{c.StartsAt, c.EndsAt}
	for _, d := range c.Drops {
		out = append(out, d.StartsAt, d.EndsAt)
	}
	return out
}

func (c *Campaign) claimedCount() (claimed, total int) {
	for _, d := range c.watchDrops() {
		total++
		if d.IsClaimed {
			claimed++
		}
	}
	return claimed, total
}

// Stream is a live broadcast on a channel.
type Stream struct {
	BroadcastID  string
	Viewers      int
	DropsEnabled bool
	Game         *Game
	Title        string
}

// Channel is a Twitch channel the miner tracks.
type Channel struct {
	ID          string
	Login       string
	DisplayName string
	// ACLBased channels come from a campaign's allow list. They are preferred
	// over directory channels and survive partial cleanups.
	ACLBased bool

	stream    *Stream
	pendingUp *time.Timer

	// Watch state, used by the watch loop.
	watchBroadcast    string
	segments          *segmentCache
	lastSpade         time.Time
	spadeURL          string
	playlistURL       string
	playlistBroadcast string
}

func newChannel(id, login, displayName string, acl bool) *Channel {
	return &Channel{ID: id, Login: login, DisplayName: displayName, ACLBased: acl, segments: newSegmentCache(256)}
}

// Name is the display name, falling back to the login.
func (ch *Channel) Name() string {
	if ch.DisplayName != "" {
		return ch.DisplayName
	}
	return ch.Login
}

// URL is the public channel page.
func (ch *Channel) URL() string { return config.WebURL + "/" + ch.Login }

func (ch *Channel) Online() bool { return ch.stream != nil }

// Offline means not live and no pending online check.
func (ch *Channel) Offline() bool { return ch.stream == nil && ch.pendingUp == nil }

func (ch *Channel) PendingOnline() bool { return ch.stream == nil && ch.pendingUp != nil }

func (ch *Channel) Game() *Game {
	if ch.stream == nil {
		return nil
	}
	return ch.stream.Game
}

func (ch *Channel) Viewers() int {
	if ch.stream == nil {
		return -1
	}
	return ch.stream.Viewers
}

func (ch *Channel) DropsEnabled() bool { return ch.stream != nil && ch.stream.DropsEnabled }

func (ch *Channel) cancelPending() {
	if ch.pendingUp != nil {
		ch.pendingUp.Stop()
		ch.pendingUp = nil
	}
}

// segmentCache remembers recently fetched HLS segments in insertion order.
type segmentCache struct {
	limit int
	order []string
	set   map[string]bool
}

func newSegmentCache(limit int) *segmentCache {
	return &segmentCache{limit: limit, set: map[string]bool{}}
}

func (s *segmentCache) has(k string) bool { return s.set[k] }

func (s *segmentCache) add(k string) {
	if s.set[k] {
		return
	}
	s.set[k] = true
	s.order = append(s.order, k)
	if len(s.order) > s.limit {
		delete(s.set, s.order[0])
		s.order = s.order[1:]
	}
}

func (s *segmentCache) reset() {
	s.order = nil
	s.set = map[string]bool{}
}
