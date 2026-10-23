package web

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/miner"
)

//go:embed static
var staticFiles embed.FS

// Server is the dashboard HTTP server.
type Server struct {
	hub      *Hub
	miner    *miner.Miner
	settings *config.Store
	mux      *http.ServeMux
}

// NewServer builds the dashboard routes.
func NewServer(hub *Hub, m *miner.Miner, settings *config.Store) *Server {
	s := &Server{hub: hub, miner: m, settings: settings, mux: http.NewServeMux()}
	static, _ := fs.Sub(staticFiles, "static")

	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", noCache(http.FileServerFS(static))))
	// 503 when the miner has stopped working, so container health reflects
	// the miner and not just this web server.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		h := s.miner.Health()
		status := http.StatusOK
		if !h.OK {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, h)
	})
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.settings.Get().Public())
	})
	s.mux.HandleFunc("POST /api/pause", s.guard(s.setPaused))
	s.mux.HandleFunc("POST /api/alerts/test", s.guard(s.testAlert))
	s.mux.HandleFunc("POST /api/settings", s.guard(s.patchSettings))
	s.mux.HandleFunc("POST /api/channels/select", s.guard(s.selectChannel))
	s.mux.HandleFunc("POST /api/manual/exit", s.guard(func(w http.ResponseWriter, r *http.Request) {
		s.miner.ExitManualMode()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	s.mux.HandleFunc("POST /api/reload", s.guard(func(w http.ResponseWriter, r *http.Request) {
		s.miner.RequestInventoryRefresh()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	s.mux.HandleFunc("POST /api/logout", s.guard(func(w http.ResponseWriter, r *http.Request) {
		s.miner.Logout()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// guard rejects cross-site writes. Browsers can't add the custom header to
// a cross-origin request without a CORS preflight, which we never approve.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Requested-With") != "TwitchMiner" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing request header"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request"})
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next(w, r)
	}
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unreadable request"})
		return
	}
	next, err := s.settings.Patch(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "settings": next.Public()})
		return
	}
	s.miner.SettingsChanged(next)
	writeJSON(w, http.StatusOK, next.Public())
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "paused must be true or false"})
		return
	}
	s.miner.SetPaused(req.Paused)
	writeJSON(w, http.StatusOK, map[string]bool{"paused": req.Paused})
}

func (s *Server) testAlert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.miner.TestAlert(ctx, req.Target); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) selectChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel id required"})
		return
	}
	if !s.miner.SelectChannel(req.ID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that channel is no longer tracked"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// events streams dashboard sections as Server-Sent Events.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	ch, snapshot := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	io.WriteString(w, "retry: 3000\n\n")
	for _, f := range snapshot {
		w.Write(f)
	}
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case f, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(f); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
