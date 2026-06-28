package main

import "testing"

func TestWantsVersion(t *testing.T) {
	tests := map[string]struct {
		args []string
		want bool
	}{
		"double dash":      {args: []string{"--version"}, want: true},
		"single dash":      {args: []string{"-version"}, want: true},
		"with other flags": {args: []string{"--addr", "127.0.0.1:7788", "--version"}, want: true},
		"after terminator": {args: []string{"--", "--version"}, want: false},
		"absent":           {args: []string{"--addr", "127.0.0.1:7788"}, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := wantsVersion(tc.args); got != tc.want {
				t.Fatalf("wantsVersion() = %v, want %v", got, tc.want)
			}
		})
	}
}
