// Command twitchminer mines timed Twitch drops and serves a dashboard to
// control it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
		slog.Error("fatal", "err", err)
		// The Windows tray build has no console, so say it in a dialog too.
		showFatal("TwitchMiner couldn't start:\n\n" + err.Error())
		os.Exit(1)
	}
}

const defaultAddr = "127.0.0.1:8080"

// trayOptions is what the tray needs to open the dashboard and register
// autostart with the same settings.
type trayOptions struct {
	url     string
	dataDir string
	addr    string
}

func run() error {
	addr := flag.String("addr", envOr("TWITCHMINER_ADDR", defaultAddr), "dashboard listen address")
	dataDir := flag.String("data", envOr("TWITCHMINER_DATA", "data"), "directory for settings, login and the log file")
	open := flag.Bool("open", os.Getenv("TWITCHMINER_NO_BROWSER") == "", "open the dashboard in a browser on start")
	tray := flag.Bool("tray", trayAvailable && os.Getenv("TWITCHMINER_NO_TRAY") == "", "show a system tray icon (Windows only)")
	verbose := flag.Bool("v", false, "verbose logging")
	version := flag.Bool("version", false, "print the version and exit")
	healthcheck := flag.Bool("healthcheck", false, "check that a running dashboard on -addr responds, then exit")
	flag.Parse()

	if *version {
		fmt.Println(config.Version)
		return nil
	}
	if *healthcheck {
		return checkHealth(*addr)
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logFile := openLogFile(filepath.Join(*dataDir, "twitchminer.log"))
	if logFile != nil {
		defer logFile.Close()
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(teeWriter{os.Stderr, logFile}, &slog.HandlerOptions{Level: level})))

	settings, err := config.LoadStore(filepath.Join(*dataDir, "settings.json"))
	if err != nil {
		return err
	}
	s := settings.Get()

	hub := web.NewHub()
	hub.Publish("version", config.Version)
	hub.Publish("settings", s.Public())
	hub.Publish("status", "Starting...")

	client := config.SmartTV()
	httpClient, err := twitch.NewHTTP(client.UserAgent, s.ConnectionQuality, s.Proxy, hub.Print)
	if err != nil {
		return err
	}
	auth := twitch.NewAuth(filepath.Join(*dataDir, "auth.json"), httpClient, client)
	m := miner.New(settings, httpClient, auth, hub, *dataDir)

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
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	if *tray {
		// Blocks until Quit is chosen or the miner stops.
		runTray(trayOptions{url: url, dataDir: *dataDir, addr: *addr}, m, stop, done)
	}
	<-done

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	return settings.Save()
}

// checkHealth is used by container health checks, since the runtime image
// has no curl or shell.
func checkHealth(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// openLogFile opens the log for appending, starting fresh once it passes
// 5 MB. It returns nil if the file can't be opened.
func openLogFile(path string) *os.File {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// teeWriter writes to the console and the log file. Write errors are
// ignored, because a GUI build on Windows has no valid stderr.
type teeWriter struct {
	console io.Writer
	file    *os.File
}

func (t teeWriter) Write(p []byte) (int, error) {
	t.console.Write(p)
	if t.file != nil {
		t.file.Write(p)
	}
	return len(p), nil
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
