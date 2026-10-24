package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestListenDashboardFreePort(t *testing.T) {
	l, err := listenDashboard("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestListenDashboardRefusesRunningMiner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"state":"watching"}`))
	}))
	defer srv.Close()
	_, err := listenDashboard(srv.Listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "another TwitchMiner") {
		t.Fatalf("want a running-miner error, got %v", err)
	}
}

func TestListenDashboardRefusesOtherProgram(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := listenDashboard(srv.Listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "another program") {
		t.Fatalf("want a port-in-use error, got %v", err)
	}
}

// A miner on all addresses must block one started later on loopback, which
// Windows would otherwise allow.
func TestListenDashboardSeesWildcardListener(t *testing.T) {
	wild, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wild.Close()
	port := wild.Addr().(*net.TCPAddr).Port
	go http.Serve(wild, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"state":"login"}`))
	}))
	_, err = listenDashboard(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil || !strings.Contains(err.Error(), "another TwitchMiner") {
		t.Fatalf("want a running-miner error, got %v", err)
	}
}
