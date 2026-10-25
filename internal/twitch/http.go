package twitch

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const maxBodySize = 32 << 20

// Response is a fully read HTTP response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// HTTP is a retrying HTTP client. Network failures and 5xx responses are
// retried with exponential backoff until the context ends.
type HTTP struct {
	userAgent string
	notify    func(string)

	mu       sync.RWMutex
	client   *http.Client
	wsClient *http.Client
}

// NewHTTP creates a client. notify receives user-facing retry messages.
func NewHTTP(userAgent string, quality int, proxy string, notify func(string)) (*HTTP, error) {
	h := &HTTP{userAgent: userAgent, notify: notify}
	if err := h.Configure(quality, proxy); err != nil {
		return nil, err
	}
	return h, nil
}

// Configure applies the connection quality (timeout multiplier, 1-6) and
// proxy. It is safe to call while requests are in flight.
func (h *HTTP) Configure(quality int, proxy string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return fmt.Errorf("invalid proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(u)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	quality = min(max(quality, 1), 6)
	dialTimeout := time.Duration(5*quality) * time.Second
	transport.TLSHandshakeTimeout = dialTimeout
	transport.ResponseHeaderTimeout = time.Duration(10*quality) * time.Second

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != nil {
		if old, ok := h.client.Transport.(*http.Transport); ok {
			defer old.CloseIdleConnections()
		}
	}
	h.client = &http.Client{Transport: transport, Timeout: time.Duration(10*quality) * time.Second}
	// Websocket dials must not carry a client timeout; they use contexts.
	h.wsClient = &http.Client{Transport: transport}
	return nil
}

// UserAgent is the User-Agent sent with every request.
func (h *HTTP) UserAgent() string { return h.userAgent }

// WebsocketClient returns an HTTP client suitable for websocket dials.
func (h *HTTP) WebsocketClient() *http.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.wsClient
}

func (h *HTTP) current() *http.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.client
}

// Do sends a request, retrying transport failures and 5xx responses. Any
// response below 500 is returned as-is for the caller to interpret.
func (h *HTTP) Do(ctx context.Context, method, rawURL string, body []byte, header http.Header) (*Response, error) {
	backoff := NewBackoff(3 * time.Minute)
	for {
		resp, err := h.once(ctx, method, rawURL, body, header)
		if err == nil && resp.Status < 500 {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return nil, err
		}
		delay := backoff.Next()
		switch {
		case err == nil:
			slog.Debug("http server error", "method", method, "status", resp.Status)
			h.notify(fmt.Sprintf("Twitch is having trouble (HTTP %d), retrying in %d seconds...", resp.Status, int(delay.Seconds()+0.5)))
		case backoff.Steps() > 1:
			// Quick first retries aren't worth bothering the user with.
			slog.Debug("http request failed", "method", method, "err", err)
			h.notify(fmt.Sprintf("Cannot reach Twitch, retrying in %d seconds...", int(delay.Seconds()+0.5)))
		}
		if err := Sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (h *HTTP) once(ctx context.Context, method, rawURL string, body []byte, header http.Header) (*Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	maps.Copy(req.Header, header)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", h.userAgent)
	}
	res, err := h.current().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBodySize))
	if err != nil {
		return nil, err
	}
	return &Response{Status: res.StatusCode, Header: res.Header, Body: data}, nil
}
