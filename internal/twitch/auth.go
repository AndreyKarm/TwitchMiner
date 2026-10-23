package twitch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// ErrUnauthorized means Twitch rejected the saved token.
var ErrUnauthorized = errors.New("twitch rejected the access token")

// LoginState describes the login flow for the dashboard.
type LoginState struct {
	// State is one of: checking, code, logged_in, logged_out, error.
	State           string    `json:"state"`
	UserCode        string    `json:"user_code,omitempty"`
	VerificationURI string    `json:"verification_uri,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitzero"`
	Login           string    `json:"login,omitempty"`
	UserID          string    `json:"user_id,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type storedAuth struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	UserID       string `json:"user_id"`
	Login        string `json:"login"`
	DeviceID     string `json:"device_id"`
}

// Auth owns the OAuth token. It logs in with Twitch's device-code flow
// using the Android app client, which yields a long-lived token.
type Auth struct {
	path      string
	http      *HTTP
	client    config.ClientInfo
	sessionID string

	mu        sync.Mutex
	s         storedAuth
	onInvalid func()
}

// NewAuth loads any saved token from path.
func NewAuth(path string, h *HTTP, client config.ClientInfo) *Auth {
	a := &Auth{path: path, http: h, client: client, sessionID: randomHex(8)}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &a.s); err != nil {
			slog.Warn("ignoring unreadable auth file", "err", err)
			a.s = storedAuth{}
		}
	}
	if a.s.DeviceID == "" {
		a.s.DeviceID = randomHex(16)
	}
	return a
}

// OnInvalid registers a callback run when the token is rejected or the
// user logs out.
func (a *Auth) OnInvalid(fn func()) {
	a.mu.Lock()
	a.onInvalid = fn
	a.mu.Unlock()
}

// UserID returns the logged-in user's ID, or "" before login.
func (a *Auth) UserID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s.UserID
}

// Login returns the logged-in user's login name.
func (a *Auth) Login() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s.Login
}

// Token returns the access token, or "" when logged out.
func (a *Auth) Token() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s.AccessToken
}

// Headers returns the headers Twitch expects from this client. GQL requests
// also carry the OAuth token and the client origin.
func (a *Auth) Headers(gql bool) http.Header {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := http.Header{}
	h.Set("Accept", "*/*")
	h.Set("Accept-Language", "en-US")
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache")
	h.Set("Client-Id", a.client.ClientID)
	h.Set("Client-Session-Id", a.sessionID)
	h.Set("X-Device-Id", a.s.DeviceID)
	if gql {
		h.Set("Origin", a.client.URL)
		h.Set("Referer", a.client.URL)
		h.Set("Authorization", "OAuth "+a.s.AccessToken)
	}
	return h
}

// EnsureLogin returns once a valid token is available. It validates any
// saved token and otherwise runs the device-code flow, reporting progress
// through show.
func (a *Auth) EnsureLogin(ctx context.Context, show func(LoginState)) error {
	for {
		if a.Token() != "" || a.refreshToken() != "" {
			show(LoginState{State: "checking"})
			ok, err := false, error(nil)
			if a.Token() != "" {
				ok, err = a.validate(ctx)
			}
			if err == nil && !ok {
				ok, err = a.refresh(ctx)
			}
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				show(LoginState{State: "error", Error: "Could not validate the saved login: " + err.Error()})
				if err := Sleep(ctx, 15*time.Second); err != nil {
					return err
				}
				continue
			}
			if ok {
				show(LoginState{State: "logged_in", Login: a.Login(), UserID: a.UserID()})
				return nil
			}
			slog.Info("saved token is no longer valid")
			a.clear()
		}
		if err := a.deviceLogin(ctx, show); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			show(LoginState{State: "error", Error: err.Error()})
			if err := Sleep(ctx, 10*time.Second); err != nil {
				return err
			}
		}
	}
}

// validate checks the token with Twitch and fills in the user identity.
// It returns false when the token is rejected or belongs to another client.
func (a *Auth) validate(ctx context.Context) (bool, error) {
	h := http.Header{}
	h.Set("Authorization", "OAuth "+a.Token())
	resp, err := a.http.Do(ctx, http.MethodGet, "https://id.twitch.tv/oauth2/validate", nil, h)
	if err != nil {
		return false, err
	}
	if resp.Status == http.StatusUnauthorized {
		return false, nil
	}
	if resp.Status != http.StatusOK {
		return false, fmt.Errorf("validate returned HTTP %d", resp.Status)
	}
	var v struct {
		ClientID string `json:"client_id"`
		Login    string `json:"login"`
		UserID   string `json:"user_id"`
	}
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		return false, err
	}
	if v.ClientID != a.client.ClientID || v.UserID == "" {
		return false, nil
	}
	a.mu.Lock()
	a.s.UserID, a.s.Login = v.UserID, v.Login
	a.mu.Unlock()
	return true, a.save()
}

type deviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func (a *Auth) idHeaders() http.Header {
	h := a.Headers(false)
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Origin", a.client.URL)
	h.Set("Referer", a.client.URL)
	return h
}

// deviceLogin requests a code, shows it, and polls until the user approves
// it on twitch.tv/activate. Expired codes are replaced automatically.
func (a *Auth) deviceLogin(ctx context.Context, show func(LoginState)) error {
	for {
		form := url.Values{"client_id": {a.client.ClientID}, "scopes": {""}}
		resp, err := a.http.Do(ctx, http.MethodPost, "https://id.twitch.tv/oauth2/device", []byte(form.Encode()), a.idHeaders())
		if err != nil {
			return err
		}
		if resp.Status != http.StatusOK {
			return fmt.Errorf("twitch refused to start login (HTTP %d)", resp.Status)
		}
		var code deviceCode
		if err := json.Unmarshal(resp.Body, &code); err != nil {
			return err
		}
		expires := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
		show(LoginState{State: "code", UserCode: code.UserCode, VerificationURI: code.VerificationURI, ExpiresAt: expires})
		slog.Info("waiting for device login", "code", code.UserCode, "url", code.VerificationURI)

		interval := time.Duration(max(code.Interval, 1)) * time.Second
		token, err := a.pollToken(ctx, code, interval, expires)
		if err != nil {
			return err
		}
		if token == nil {
			continue // code expired, get a fresh one
		}
		a.mu.Lock()
		a.s.AccessToken, a.s.RefreshToken = token.AccessToken, token.RefreshToken
		a.mu.Unlock()
		ok, err := a.validate(ctx)
		if err != nil {
			return err
		}
		if !ok {
			a.clear()
			return errors.New("twitch issued a token that failed validation")
		}
		show(LoginState{State: "logged_in", Login: a.Login(), UserID: a.UserID()})
		return nil
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Message      string `json:"message"`
}

// pollToken returns nil, nil when the code expires before approval.
func (a *Auth) pollToken(ctx context.Context, code deviceCode, interval time.Duration, expires time.Time) (*tokenResponse, error) {
	form := url.Values{
		"client_id":   {a.client.ClientID},
		"device_code": {code.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	for time.Now().Before(expires) {
		if err := Sleep(ctx, interval); err != nil {
			return nil, err
		}
		resp, err := a.http.Do(ctx, http.MethodPost, "https://id.twitch.tv/oauth2/token", []byte(form.Encode()), a.idHeaders())
		if err != nil {
			return nil, err
		}
		var t tokenResponse
		_ = json.Unmarshal(resp.Body, &t)
		switch {
		case resp.Status == http.StatusOK && t.AccessToken != "":
			return &t, nil
		case strings.Contains(t.Message, "slow_down"):
			interval += 5 * time.Second
		case strings.Contains(t.Message, "authorization_pending"):
		case strings.Contains(t.Message, "invalid device code"), strings.Contains(t.Message, "expired"):
			return nil, nil
		case resp.Status != http.StatusBadRequest:
			return nil, fmt.Errorf("login polling failed (HTTP %d)", resp.Status)
		}
	}
	return nil, nil
}

func (a *Auth) refreshToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s.RefreshToken
}

// refresh trades the refresh token for a new access token. It returns
// false when Twitch no longer accepts the refresh token.
func (a *Auth) refresh(ctx context.Context) (bool, error) {
	rt := a.refreshToken()
	if rt == "" {
		return false, nil
	}
	form := url.Values{
		"client_id":     {a.client.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	}
	resp, err := a.http.Do(ctx, http.MethodPost, "https://id.twitch.tv/oauth2/token", []byte(form.Encode()), a.idHeaders())
	if err != nil {
		return false, err
	}
	var t tokenResponse
	if resp.Status != http.StatusOK || json.Unmarshal(resp.Body, &t) != nil || t.AccessToken == "" {
		slog.Info("refreshing the login failed", "status", resp.Status)
		return false, nil
	}
	a.mu.Lock()
	a.s.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		a.s.RefreshToken = t.RefreshToken
	}
	a.mu.Unlock()
	slog.Info("login refreshed")
	return a.validate(ctx)
}

// Invalidate drops an access token Twitch has rejected and notifies the
// miner. The refresh token is kept so the next login attempt can renew it.
func (a *Auth) Invalidate() {
	a.mu.Lock()
	had := a.s.AccessToken != ""
	a.s.AccessToken = ""
	a.mu.Unlock()
	if !had {
		return
	}
	slog.Warn("twitch rejected the access token, logging in again")
	if err := a.save(); err != nil {
		slog.Error("saving auth state", "err", err)
	}
	a.fireInvalid()
}

// Logout forgets the token and notifies the miner.
func (a *Auth) Logout() {
	a.clear()
	a.fireInvalid()
}

func (a *Auth) fireInvalid() {
	a.mu.Lock()
	fn := a.onInvalid
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (a *Auth) clear() {
	a.mu.Lock()
	a.s = storedAuth{DeviceID: a.s.DeviceID}
	a.mu.Unlock()
	if err := a.save(); err != nil {
		slog.Error("saving auth state", "err", err)
	}
}

func (a *Auth) save() error {
	a.mu.Lock()
	s := a.s
	a.mu.Unlock()
	return config.WriteJSONAtomic(a.path, s)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
