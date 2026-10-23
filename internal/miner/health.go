package miner

import (
	"fmt"
	"time"
)

// Health summarizes whether the miner is working, for the /healthz check.
type Health struct {
	OK            bool      `json:"ok"`
	State         string    `json:"state"`
	Detail        string    `json:"detail,omitempty"`
	LastInventory time.Time `json:"last_inventory,omitzero"`
	Watching      string    `json:"watching,omitempty"`
}

// firstLoadGrace is how long a new session may take to load the inventory.
const firstLoadGrace = 10 * time.Minute

// Health reports the miner's state. It's unhealthy only when something a
// restart might fix is wrong: the inventory stops loading while logged in.
// Waiting for a login or being paused is healthy.
func (m *Miner) Health() Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthLocked(time.Now())
}

func (m *Miner) healthLocked(now time.Time) Health {
	h := Health{OK: true, LastInventory: m.lastInventoryOK}
	if m.watching != nil {
		h.Watching = m.watching.Name()
	}
	maxAge := 3*time.Duration(m.policy.refreshMinutes)*time.Minute + 5*time.Minute
	switch {
	case m.endSession == nil:
		h.State = "waiting_for_login"
	case m.paused:
		h.State = "paused"
	case !m.inventoryLoaded && now.Sub(m.sessionStarted) > firstLoadGrace:
		h.OK, h.State = false, "stale"
		h.Detail = fmt.Sprintf("the inventory hasn't loaded since logging in %s ago", now.Sub(m.sessionStarted).Round(time.Minute))
	case !m.inventoryLoaded:
		h.State = "starting"
	case now.Sub(m.lastInventoryOK) > maxAge:
		h.OK, h.State = false, "stale"
		h.Detail = fmt.Sprintf("no successful inventory reload for %s", now.Sub(m.lastInventoryOK).Round(time.Minute))
	case m.watching != nil:
		h.State = "mining"
	default:
		h.State = "idle"
	}
	return h
}
