package pubsub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSerialKeepsOrderPerKey(t *testing.T) {
	var s serial
	var mu sync.Mutex
	got := map[string][]int{}
	var wg sync.WaitGroup
	for i := range 50 {
		for _, key := range []string{"a", "b"} {
			wg.Add(1)
			s.run(key, func() {
				defer wg.Done()
				if i%7 == 0 {
					time.Sleep(time.Millisecond) // let other work pile up
				}
				mu.Lock()
				got[key] = append(got[key], i)
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	for _, key := range []string{"a", "b"} {
		if !slices.IsSorted(got[key]) || len(got[key]) != 50 {
			t.Fatalf("%s ran out of order or lost work: %v", key, got[key])
		}
	}
}

func TestSerialSurvivesPanic(t *testing.T) {
	var s serial
	done := make(chan struct{})
	s.run("k", func() { panic("boom") })
	s.run("k", func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("work queued after a panicking handler never ran")
	}
}

func TestAuthErrorsAreRateLimited(t *testing.T) {
	p := New(nil, func() string { return "" }, nil)
	calls := make(chan struct{}, 10)
	p.OnAuthError(func() { calls <- struct{}{} })
	for range 5 {
		p.authFailed()
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(calls); n != 1 {
		t.Fatalf("auth error callback ran %d times, want once per %v", n, authErrorEvery)
	}
}

// fakeTwitch is a PubSub server that rejects the first LISTEN with
// ERR_BADAUTH, accepts later ones, then sends three messages on the topic.
func fakeTwitch(t *testing.T, topic string) (url string, listens *int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := r.Context()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type  string `json:"type"`
				Nonce string `json:"nonce"`
			}
			json.Unmarshal(data, &msg)
			reply := func(v any) {
				b, _ := json.Marshal(v)
				ws.Write(ctx, websocket.MessageText, b)
			}
			switch msg.Type {
			case "PING":
				reply(map[string]any{"type": "PONG"})
			case "LISTEN":
				mu.Lock()
				count++
				first := count == 1
				mu.Unlock()
				if first {
					reply(map[string]any{"type": "RESPONSE", "nonce": msg.Nonce, "error": "ERR_BADAUTH"})
					continue
				}
				reply(map[string]any{"type": "RESPONSE", "nonce": msg.Nonce, "error": ""})
				for _, n := range []string{"1", "2", "3"} {
					reply(map[string]any{"type": "MESSAGE", "data": map[string]any{"topic": topic, "message": n}})
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), &count
}

func TestRejectedListenIsRetriedAndMessagesStayInOrder(t *testing.T) {
	const topic = "video-playback-by-id.1"
	url, _ := fakeTwitch(t, topic)
	oldURL, oldRetry := pubsubURL, listenRetry
	pubsubURL, listenRetry = url, 100*time.Millisecond
	t.Cleanup(func() { pubsubURL, listenRetry = oldURL, oldRetry })

	p := New(func() *http.Client { return http.DefaultClient }, func() string { return "token" }, nil)
	authErrors := make(chan struct{}, 5)
	p.OnAuthError(func() { authErrors <- struct{}{} })
	got := make(chan string, 3)
	if err := p.Add([]Topic{{ID: topic, Handle: func(msg json.RawMessage) { got <- string(msg) }}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); p.Stop() }()
	p.Start(ctx)

	select {
	case <-authErrors:
	case <-time.After(5 * time.Second):
		t.Fatal("ERR_BADAUTH was not reported")
	}
	var order []string
	for range 3 {
		select {
		case m := <-got:
			order = append(order, m)
		case <-time.After(5 * time.Second):
			t.Fatalf("the rejected LISTEN was not retried; got messages %v", order)
		}
	}
	if !slices.Equal(order, []string{"1", "2", "3"}) {
		t.Fatalf("messages handled out of order: %v", order)
	}
}
