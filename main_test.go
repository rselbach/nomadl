package main

import "testing"

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
