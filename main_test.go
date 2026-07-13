package main

import (
	"net"
	"testing"
)

func TestListenWithFallback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		if err := occupied.Close(); err != nil {
			t.Errorf("close occupied listener: %v", err)
		}
	})
	addr := occupied.Addr().String()

	if _, err := listenWithFallback(addr, false); err == nil {
		t.Fatal("explicit address on a taken port must fail, got nil error")
	}

	ln, err := listenWithFallback(addr, true)
	if err != nil {
		t.Fatalf("fallback listen: %v", err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close fallback listener: %v", err)
		}
	})

	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	boundPort := ln.Addr().(*net.TCPAddr).Port
	if boundPort <= occupiedPort {
		t.Fatalf("fallback port = %d, want above %d", boundPort, occupiedPort)
	}

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	freeAddr := free.Addr().String()
	if err := free.Close(); err != nil {
		t.Fatalf("close free listener: %v", err)
	}
	direct, err := listenWithFallback(freeAddr, true)
	if err != nil {
		t.Fatalf("free port listen: %v", err)
	}
	if got := direct.Addr().String(); got != freeAddr {
		t.Fatalf("free port moved: got %s, want %s", got, freeAddr)
	}
	if err := direct.Close(); err != nil {
		t.Fatalf("close direct listener: %v", err)
	}
}

func TestUIAddress(t *testing.T) {
	tests := map[string]struct {
		listenAddr string
		want       string
	}{
		"loopback unchanged": {listenAddr: "127.0.0.1:7788", want: "127.0.0.1:7788"},
		"hostname unchanged": {listenAddr: "localhost:7788", want: "localhost:7788"},
		"ipv4 wildcard":      {listenAddr: "0.0.0.0:7788", want: "127.0.0.1:7788"},
		"ipv6 wildcard":      {listenAddr: "[::]:7788", want: "127.0.0.1:7788"},
		"empty host":         {listenAddr: ":7788", want: "127.0.0.1:7788"},
		"unparseable":        {listenAddr: "garbage", want: "garbage"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := uiAddress(tc.listenAddr); got != tc.want {
				t.Fatalf("uiAddress(%q) = %q, want %q", tc.listenAddr, got, tc.want)
			}
		})
	}
}
