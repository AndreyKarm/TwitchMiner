// Package pubsub maintains Twitch PubSub websocket connections, sharding
// topics across up to config.MaxWebsockets connections.
package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// Topic is a subscription and the handler for its messages. A topic's
// messages are handled one at a time, in the order they arrived, off the
// connection's read loop, so a handler may block; it then delays only its
// own topic.
type Topic struct {
	ID     string
	Handle func(msg json.RawMessage)
}

// TopicID builds a topic ID such as "video-playback-by-id.12345".
func TopicID(prefix, targetID string) string { return prefix + "." + targetID }

// Status reports how many connections are up.
type Status struct {
	Connected int `json:"connected"`
	Total     int `json:"total"`
	Topics    int `json:"topics"`
}

// Pool distributes topics over several connections.
type Pool struct {
	httpClient func() *http.Client
	token      func() string
	onStatus   func(Status)

	mu      sync.Mutex
	ctx     context.Context
	conns   []*conn
	stopped bool

	queue serial

	authMu      sync.Mutex
	onAuthError func()
	lastAuthErr time.Time
}

// authErrorEvery limits how often onAuthError runs; every LISTEN batch on
// every connection can report the same bad token.
const authErrorEvery = time.Minute

// OnAuthError registers fn to run when Twitch rejects the token for a
// subscription. It runs at most once per authErrorEvery, on its own goroutine.
func (p *Pool) OnAuthError(fn func()) {
	p.authMu.Lock()
	p.onAuthError = fn
	p.authMu.Unlock()
}

func (p *Pool) authFailed() {
	p.authMu.Lock()
	fn := p.onAuthError
	due := fn != nil && time.Since(p.lastAuthErr) >= authErrorEvery
	if due {
		p.lastAuthErr = time.Now()
	}
	p.authMu.Unlock()
	if due {
		go fn()
	}
}

// Reconnect drops and reopens every connection, for example after the
// computer slept and the sockets may be dead without knowing it.
func (p *Pool) Reconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		signal(c.reconnect)
	}
}

// dispatch hands a message to its topic's handler, in arrival order.
func (p *Pool) dispatch(topic string, msg json.RawMessage) {
	if h := p.handler(topic); h != nil {
		p.queue.run(topic, func() { h(msg) })
	}
}

// New creates a pool. token supplies the OAuth token for LISTEN requests.
func New(httpClient func() *http.Client, token func() string, onStatus func(Status)) *Pool {
	return &Pool{httpClient: httpClient, token: token, onStatus: onStatus}
}

// Start connects all existing connections and any created later. The pool
// shuts down when ctx ends.
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctx = ctx
	for _, c := range p.conns {
		c.start(ctx)
	}
}

// Stop closes every connection, drops all topics and waits for the
// connection goroutines to exit.
func (p *Pool) Stop() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.stopped = true
	p.mu.Unlock()
	for _, c := range conns {
		c.stop()
	}
	p.report()
}

// Add subscribes to topics, opening new connections as needed.
func (p *Pool) Add(topics []Topic) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return nil
	}
	pending := make([]Topic, 0, len(topics))
	for _, t := range topics {
		if p.owner(t.ID) == nil {
			pending = append(pending, t)
		}
	}
	defer p.reportLocked()
	for i := 0; len(pending) > 0 && i < config.MaxWebsockets; i++ {
		if i == len(p.conns) {
			c := newConn(p, i)
			p.conns = append(p.conns, c)
			if p.ctx != nil {
				c.start(p.ctx)
			}
		}
		pending = p.conns[i].add(pending)
	}
	if len(pending) > 0 {
		return errors.New("maximum PubSub topic limit reached")
	}
	return nil
}

// Remove unsubscribes topics and closes connections that are no longer needed.
func (p *Pool) Remove(ids []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(ids) == 0 || p.stopped {
		return
	}
	for _, c := range p.conns {
		c.remove(ids)
	}
	var recycled []Topic
	for len(p.conns) > 0 && p.countLocked() <= (len(p.conns)-1)*config.WSTopicsLimit {
		last := p.conns[len(p.conns)-1]
		p.conns = p.conns[:len(p.conns)-1]
		recycled = append(recycled, last.drain()...)
		go last.stop()
	}
	for i := 0; len(recycled) > 0 && i < len(p.conns); i++ {
		recycled = p.conns[i].add(recycled)
	}
	p.reportLocked()
}

func (p *Pool) owner(id string) *conn {
	for _, c := range p.conns {
		if _, ok := c.topics[id]; ok {
			return c
		}
	}
	return nil
}

func (p *Pool) countLocked() int {
	n := 0
	for _, c := range p.conns {
		n += len(c.topics)
	}
	return n
}

func (p *Pool) handler(id string) func(json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.owner(id); c != nil {
		return c.topics[id].Handle
	}
	return nil
}

func (p *Pool) report() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reportLocked()
}

func (p *Pool) reportLocked() {
	if p.onStatus == nil {
		return
	}
	s := Status{Total: len(p.conns), Topics: p.countLocked()}
	for _, c := range p.conns {
		if c.connected.Load() {
			s.Connected++
		}
	}
	// onStatus must not call back into the pool; it runs under the pool lock.
	p.onStatus(s)
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
