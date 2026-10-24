package main

import (
	"fmt"
	"net"
)

// rivalOnPort reports another program that has taken the dashboard port on
// all addresses after this miner started, as Docker does when it publishes
// a container's port. Windows allows that next to this miner's loopback
// listener, so a bind attempt on all addresses is how to notice it.
func rivalOnPort(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		return nil // this miner already holds every address
	}
	l, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", port))
	if err != nil {
		return fmt.Errorf("another program, such as a TwitchMiner Docker container, started on port %s, "+
			"so this miner stopped to avoid mining twice. Run only one, or give one of them another port", port)
	}
	l.Close()
	return nil
}
