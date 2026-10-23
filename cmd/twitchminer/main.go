// Command twitchminer mines timed Twitch drops and serves a dashboard to
// control it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
	"github.com/AndreyKarm/TwitchMiner/internal/miner"
	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
	"github.com/AndreyKarm/TwitchMiner/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "twitchminer:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", envOr("TWITCHMINER_ADDR", "127.0.0.1:8080"), "dashboard listen address")
	dataDir := flag.String("data", envOr("TWITCHMINER_DATA", "data"), "directory for settings and login")
	open := flag.Bool("open", os.Getenv("TWITCHMINER_NO_BROWSER") == "", "open the dashboard in a browser on start")
	verbose := flag.Bool("v", false, "verbose logging")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *version {
		fmt.Println(config.Version)
		return nil
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	settings, err := config.LoadStore(filepath.Join(*dataDir, "settings.json"))
	if err != nil {
		return err
	}
	s := settings.Get()

	hub := web.NewHub()
	hub.Publish("version", config.Version)
	hub.Publish("settings", s)
	hub.Publish("status", "Starting...")

	client := config.SmartTV()
	httpClient, err := twitch.NewHTTP(client.UserAgent, s.ConnectionQuality, s.Proxy, hub.Print)
	if err != nil {
		return err
	}
	auth := twitch.NewAuth(filepath.Join(*dataDir, "auth.json"), httpClient, client)
	m := miner.New(settings, httpClient, auth, hub)

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("dashboard can't listen on %s: %w", *addr, err)
	}
	srv := &http.Server{Handler: web.NewServer(hub, m, settings), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("dashboard server stopped", "err", err)
		}
	}()
	url := dashboardURL(listener.Addr().(*net.TCPAddr))
	slog.Info("dashboard ready", "url", url, "version", config.Version)
	if *open {
		openBrowser(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m.Run(ctx)

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	return settings.Save()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func dashboardURL(a *net.TCPAddr) string {
	host := a.IP.String()
	if a.IP.IsUnspecified() {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(a.Port))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		slog.Info("open the dashboard manually", "url", url)
		return
	}
	go cmd.Wait()
}
