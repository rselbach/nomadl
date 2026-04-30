package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitialTargetFromArgs(t *testing.T) {
	tests := map[string]struct {
		args    []string
		want    string
		wantErr bool
	}{
		"none": {
			args: nil,
			want: "",
		},
		"one": {
			args: []string{"api"},
			want: "api",
		},
		"trim": {
			args: []string{" api "},
			want: "api",
		},
		"empty": {
			args:    []string{" "},
			wantErr: true,
		},
		"too many": {
			args:    []string{"api", "worker"},
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			got, err := initialTargetFromArgs(tc.args)
			if tc.wantErr {
				r.Error(err)
				return
			}
			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}
