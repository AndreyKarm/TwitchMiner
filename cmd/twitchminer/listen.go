package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// listenDashboard refuses to start when something already answers on the
// dashboard port. Windows lets a loopback listener share a port with one on
// all addresses (Docker's published ports, for example), and localhost would
// then reach only one of the two miners without saying so.
func listenDashboard(addr string) (net.Listener, error) {
	if err := checkPortFree(addr); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dashboard can't listen on %s: %w", addr, err)
	}
	return l, nil
}

// watchForRival calls stop with an error once another program takes the
// dashboard port after this miner started. It returns when ctx ends.
func watchForRival(ctx context.Context, addr string, stop func(error)) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := rivalOnPort(addr); err != nil {
				stop(err)
				return
			}
		}
	}
}

// checkPortFree connects to addr and fails if anything accepts, naming
// another TwitchMiner when its health check answers.
func checkPortFree(addr string) error {
	target, err := localTarget(addr)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		return nil // nothing listening
	}
	conn.Close()
	if isMiner(target) {
		return fmt.Errorf("another TwitchMiner is already running at http://%s (maybe the Docker container or the Windows app). "+
			"Stop it first, or start this one with another -addr port", target)
	}
	return fmt.Errorf("port %s is already in use by another program; start with another -addr port", target)
}

// isMiner reports whether the server at target answers /healthz like this
// program does.
func isMiner(target string) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + target + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		OK    *bool  `json:"ok"`
		State string `json:"state"`
	}
	return json.NewDecoder(resp.Body).Decode(&h) == nil && h.OK != nil && h.State != ""
}

// localTarget turns a listen address into one to connect to, using loopback
// for an unspecified host.
func localTarget(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}
