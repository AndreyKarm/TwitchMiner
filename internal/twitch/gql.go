package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// GQLError is an error Twitch's GraphQL API returned that the client
// couldn't recover from.
type GQLError struct{ Message string }

func (e *GQLError) Error() string { return "gql: " + e.Message }

// GQL sends rate-limited, retried requests to gql.twitch.tv.
type GQL struct {
	http    *HTTP
	auth    *Auth
	limiter *RateLimiter

	outdatedMu sync.Mutex
	// outdated holds persisted operations Twitch no longer recognizes.
	outdated   map[string]bool
	onOutdated func(ops []string)
}

// OnOutdated registers fn to receive the persisted operations Twitch no
// longer recognizes, whenever that set changes. Twitch retires a query's
// hash when its web client changes, and only an update of this app fixes
// that. An empty list means everything works again.
func (g *GQL) OnOutdated(fn func(ops []string)) {
	g.outdatedMu.Lock()
	g.onOutdated = fn
	g.outdatedMu.Unlock()
}

// setOutdated records whether op works and reports a change.
func (g *GQL) setOutdated(op string, outdated bool) {
	if op == "" {
		return
	}
	g.outdatedMu.Lock()
	if g.outdated[op] == outdated {
		g.outdatedMu.Unlock()
		return
	}
	if g.outdated == nil {
		g.outdated = map[string]bool{}
	}
	if outdated {
		g.outdated[op] = true
	} else {
		delete(g.outdated, op)
	}
	ops := slices.Sorted(maps.Keys(g.outdated))
	fn := g.onOutdated
	g.outdatedMu.Unlock()
	if fn != nil {
		fn(ops)
	}
}

// NewGQL builds a client using auth for credentials.
func NewGQL(h *HTTP, auth *Auth) *GQL {
	// GQL breaks everything when rate limited; keep these defaults.
	return &GQL{http: h, auth: auth, limiter: NewRateLimiter(5, time.Second)}
}

// Do1 runs a single operation.
func (g *GQL) Do1(ctx context.Context, op any) (map[string]any, error) {
	res, err := g.Do(ctx, op)
	if err != nil {
		return nil, err
	}
	return res[0], nil
}

// Do runs one or more operations in a single batched request and returns
// one response object per operation, in order.
func (g *GQL) Do(ctx context.Context, ops ...any) ([]map[string]any, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	var payload any = ops
	if len(ops) == 1 {
		payload = ops[0]
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// Error replies don't always name their operation, so take the names
	// from the request.
	names := make([]string, len(ops))
	for i, op := range ops {
		if o, ok := op.(config.Op); ok {
			names[i] = o.OperationName
		}
	}
	backoff := NewBackoff(time.Minute)
	singleRetry := true
	for {
		results, retry, err := g.attempt(ctx, body, names, &singleRetry)
		if err != nil || !retry {
			return results, err
		}
		delay := backoff.Next()
		if delay < 5*time.Second && !singleRetry {
			delay = 5 * time.Second
		}
		if err := Sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// attempt sends one request. names holds each operation's name, "" for
// raw queries.
func (g *GQL) attempt(ctx context.Context, body []byte, names []string, singleRetry *bool) ([]map[string]any, bool, error) {
	count := len(names)
	release, err := g.limiter.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	header := g.auth.Headers(true)
	header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(ctx, http.MethodPost, "https://gql.twitch.tv/gql", body, header)
	release()
	if err != nil {
		return nil, false, err
	}
	if resp.Status == http.StatusUnauthorized {
		g.auth.Invalidate()
		return nil, false, ErrUnauthorized
	}

	var results []map[string]any
	if count == 1 {
		var one map[string]any
		if err := json.Unmarshal(resp.Body, &one); err != nil {
			return nil, false, fmt.Errorf("gql: unreadable response (HTTP %d)", resp.Status)
		}
		results = []map[string]any{one}
	} else if err := json.Unmarshal(resp.Body, &results); err != nil {
		return nil, false, fmt.Errorf("gql: unreadable batch response (HTTP %d)", resp.Status)
	}
	if len(results) != count {
		return nil, false, fmt.Errorf("gql: expected %d responses, got %d", count, len(results))
	}

	for i, r := range results {
		name := names[i]
		if name == "" {
			name = operationName(r)
		}
		if msg, ok := r["error"].(string); ok {
			detail, _ := r["message"].(string)
			if strings.EqualFold(msg, "Unauthorized") {
				g.auth.Invalidate()
				return nil, false, ErrUnauthorized
			}
			return nil, false, &GQLError{Message: msg + ": " + detail}
		}
		errs, _ := r["errors"].([]any)
		if len(errs) == 0 {
			g.setOutdated(names[i], false)
			continue
		}
		var unhandled []string
		for _, raw := range errs {
			e, _ := raw.(map[string]any)
			msg, _ := e["message"].(string)
			switch msg {
			case "service error", "PersistedQueryNotFound":
				if *singleRetry {
					*singleRetry = false
					slog.Warn("retrying gql request once", "error", msg, "operation", name)
					return nil, true, nil
				}
				if msg == "PersistedQueryNotFound" {
					g.setOutdated(names[i], true)
				}
				unhandled = append(unhandled, msg)
			case "service timeout", "request cancelled", "service unavailable", "context deadline exceeded":
				return nil, true, nil
			case "server error":
				// Partial failure: drop the broken field and keep the rest.
				nullifyPath(r, e["path"])
			default:
				unhandled = append(unhandled, msg)
			}
		}
		if len(unhandled) > 0 {
			return nil, false, &GQLError{Message: strings.Join(unhandled, "; ") + " (" + name + ")"}
		}
		g.setOutdated(names[i], false)
	}
	return results, false, nil
}

// operationName reads the operation a reply names, for raw queries.
func operationName(r map[string]any) string {
	if ext, ok := r["extensions"].(map[string]any); ok {
		if name, ok := ext["operationName"].(string); ok {
			return name
		}
	}
	return "unknown operation"
}

func nullifyPath(r map[string]any, rawPath any) {
	path, _ := rawPath.([]any)
	if len(path) == 0 {
		return
	}
	cur, ok := r["data"].(map[string]any)
	if !ok {
		return
	}
	for _, p := range path[:len(path)-1] {
		key, ok := p.(string)
		if !ok {
			return
		}
		if cur, ok = cur[key].(map[string]any); !ok {
			return
		}
	}
	if key, ok := path[len(path)-1].(string); ok {
		cur[key] = nil
	}
}

// Dig walks nested JSON objects by key and returns the value found, or nil.
func Dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// Decode converts a generic JSON value into a typed struct.
func Decode(v any, out any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// MergeData recursively merges two JSON objects. Values from primary win;
// nested objects are merged key by key.
func MergeData(primary, secondary map[string]any) map[string]any {
	out := make(map[string]any, len(primary)+len(secondary))
	maps.Copy(out, secondary)
	for k, pv := range primary {
		if pm, ok := pv.(map[string]any); ok {
			if sm, ok := secondary[k].(map[string]any); ok {
				out[k] = MergeData(pm, sm)
				continue
			}
		}
		out[k] = pv
	}
	return out
}
