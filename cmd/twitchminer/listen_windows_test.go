package main

import (
	"net"
	"strconv"
	"testing"
)

// A program that takes the port on all addresses after this miner started
// on loopback is noticed.
func TestRivalOnPort(t *testing.T) {
	l, err := listenDashboard("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	addr := l.Addr().String()
	if err := rivalOnPort(addr); err != nil {
		t.Fatalf("no rival yet, got %v", err)
	}
	rival, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	defer rival.Close()
	if err := rivalOnPort(addr); err == nil {
		t.Fatal("want the rival reported")
	}
}
