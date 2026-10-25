package pubsub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// pubsubURL is Twitch's PubSub endpoint; tests point it at a local server.
var pubsubURL = "wss://pubsub-edge.twitch.tv/v1"

// conn is one PubSub websocket. Its topics map is guarded by the pool mutex.
type conn struct {
	pool *Pool
	idx  int

	topics    map[string]Topic
	changed   chan struct{}
	reconnect chan struct{}
	connected atomic.Bool

	cancel context.CancelFunc
	done   chan struct{}
}

func newConn(p *Pool, idx int) *conn {
	return &conn{
		pool:      p,
		idx:       idx,
		topics:    map[string]Topic{},
		changed:   make(chan struct{}, 1),
		reconnect: make(chan struct{}, 1),
	}
}

// add takes as many topics as fit and returns the rest. Pool lock held.
func (c *conn) add(topics []Topic) []Topic {
	n := 0
	for n < len(topics) && len(c.topics) < config.WSTopicsLimit {
		c.topics[topics[n].ID] = topics[n]
		n++
	}
	if n > 0 {
		signal(c.changed)
	}
	return topics[n:]
}

// remove drops any of ids this connection owns. Pool lock held.
func (c *conn) remove(ids []string) {
	changed := false
	for _, id := range ids {
		if _, ok := c.topics[id]; ok {
			delete(c.topics, id)
			changed = true
		}
	}
	if changed {
		signal(c.changed)
	}
}

// drain removes and returns all topics. Pool lock held.
func (c *conn) drain() []Topic {
	out := make([]Topic, 0, len(c.topics))
	for _, t := range c.topics {
		out = append(out, t)
	}
	c.topics = map[string]Topic{}
	return out
}

func (c *conn) start(ctx context.Context) {
	if c.cancel != nil {
		return
	}
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		c.run(ctx)
	}()
}

func (c *conn) stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
}

func (c *conn) run(ctx context.Context) {
	backoff := twitch.NewBackoff(3 * time.Minute)
	for ctx.Err() == nil {
		ws, _, err := websocket.Dial(ctx, pubsubURL, &websocket.DialOptions{HTTPClient: c.pool.httpClient()})
		if err != nil {
			delay := backoff.Next()
			slog.Info("pubsub connect failed", "ws", c.idx, "retry_in", delay.Round(time.Second), "err", err)
			if twitch.Sleep(ctx, delay) != nil {
				return
			}
			continue
		}
		ws.SetReadLimit(4 << 20)
		c.connected.Store(true)
		c.pool.report()
		slog.Debug("pubsub connected", "ws", c.idx)

		err = c.safeSession(ctx, ws)

		c.connected.Store(false)
		c.pool.report()
		if ctx.Err() != nil {
			ws.Close(websocket.StatusNormalClosure, "")
			return
		}
		ws.CloseNow()
		if err != nil {
			slog.Info("pubsub disconnected, reconnecting", "ws", c.idx, "err", err)
		}
		backoff.Reset()
		if twitch.Sleep(ctx, time.Second) != nil {
			return
		}
	}
}

type inbound struct {
	Type  string `json:"type"`
	Error string `json:"error"`
	Nonce string `json:"nonce"`
	Data  struct {
		Topic   string `json:"topic"`
		Message string `json:"message"`
	} `json:"data"`
}

// safeSession turns a panic in session into an error, so the connection
// reconnects instead of taking the process down.
func (c *conn) safeSession(ctx context.Context, ws *websocket.Conn) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pubsub connection panicked", "ws", c.idx, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return c.session(ctx, ws)
}

// session serves one live connection until it fails or must reconnect.
func (c *conn) session(ctx context.Context, ws *websocket.Conn) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	msgs := make(chan []byte, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, data, err := ws.Read(sctx)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case msgs <- data:
			case <-sctx.Done():
				return
			}
		}
	}()

	submitted := map[string]bool{}
	// listens maps the nonce of each LISTEN in flight to its topics, so a
	// rejected one can be retried.
	listens := map[string][]string{}
	var retryAt time.Time
	signal(c.changed) // subscribe to everything we own on this new connection
	nextPing := time.Now()
	var pongDeadline time.Time
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.reconnect:
			slog.Info("pubsub reconnecting on request", "ws", c.idx)
			return nil
		case err := <-readErr:
			return err
		case data := <-msgs:
			var m inbound
			if err := json.Unmarshal(data, &m); err != nil {
				slog.Warn("pubsub sent unreadable message", "ws", c.idx)
				continue
			}
			switch m.Type {
			case "MESSAGE":
				c.pool.dispatch(m.Data.Topic, json.RawMessage(m.Data.Message))
			case "PONG":
				pongDeadline = time.Time{}
			case "RECONNECT":
				slog.Info("pubsub requested reconnect", "ws", c.idx)
				return nil
			case "RESPONSE":
				topics := listens[m.Nonce]
				delete(listens, m.Nonce)
				if m.Error == "" {
					continue
				}
				slog.Warn("pubsub rejected a request", "ws", c.idx, "error", m.Error, "topics", len(topics))
				if m.Error == "ERR_BADAUTH" {
					c.pool.authFailed()
				}
				// Forget the rejected topics so the next sync sends them again.
				for _, id := range topics {
					delete(submitted, id)
				}
				if len(topics) > 0 && retryAt.IsZero() {
					retryAt = time.Now().Add(listenRetry)
				}
			}
		case <-c.changed:
			if err := c.syncTopics(sctx, ws, submitted, listens); err != nil {
				return err
			}
		case now := <-tick.C:
			if !retryAt.IsZero() && now.After(retryAt) {
				retryAt = time.Time{}
				signal(c.changed)
			}
			if !pongDeadline.IsZero() && now.After(pongDeadline) {
				slog.Info("pubsub missed a PONG", "ws", c.idx)
				return nil
			}
			if now.After(nextPing) {
				nextPing = now.Add(config.PingInterval)
				pongDeadline = now.Add(config.PingTimeout)
				if err := c.send(sctx, ws, map[string]any{"type": "PING"}); err != nil {
					return err
				}
			}
		}
	}
}

// listenRetry is how long to wait before sending rejected LISTENs again.
var listenRetry = 30 * time.Second

// syncTopics sends LISTEN/UNLISTEN so the server matches c.topics. Each
// LISTEN's nonce is recorded in listens with the topics it covers.
func (c *conn) syncTopics(ctx context.Context, ws *websocket.Conn, submitted map[string]bool, listens map[string][]string) error {
	c.pool.mu.Lock()
	var added, removed []string
	for id := range c.topics {
		if !submitted[id] {
			added = append(added, id)
		}
	}
	for id := range submitted {
		if _, ok := c.topics[id]; !ok {
			removed = append(removed, id)
		}
	}
	c.pool.mu.Unlock()

	token := c.pool.token()
	for _, batch := range chunk(removed, 10) {
		if err := c.send(ctx, ws, listenMsg("UNLISTEN", nonce(30), batch, token)); err != nil {
			return err
		}
		for _, id := range batch {
			delete(submitted, id)
		}
	}
	for _, batch := range chunk(added, 10) {
		n := nonce(30)
		if err := c.send(ctx, ws, listenMsg("LISTEN", n, batch, token)); err != nil {
			return err
		}
		listens[n] = batch
		for _, id := range batch {
			submitted[id] = true
		}
	}
	return nil
}

func listenMsg(kind, nonce string, topics []string, token string) map[string]any {
	return map[string]any{
		"type":  kind,
		"nonce": nonce,
		"data":  map[string]any{"topics": topics, "auth_token": token},
	}
}

func (c *conn) send(ctx context.Context, ws *websocket.Conn, msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// LISTEN payloads carry the OAuth token; never log them.
	return ws.Write(wctx, websocket.MessageText, data)
}

func chunk(ids []string, size int) [][]string {
	var out [][]string
	for len(ids) > 0 {
		n := min(size, len(ids))
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	return out
}

const nonceChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func nonce(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = nonceChars[int(b[i])%len(nonceChars)]
	}
	return string(b)
}
