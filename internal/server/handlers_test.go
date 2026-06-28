package server

import (
	"testing"
	"time"
)

func TestTimeRangeFromValue(t *testing.T) {
	now := time.Date(2026, 6, 27, 10, 40, 0, 0, time.UTC)

	tests := map[string]struct {
		value     string
		wantSince time.Time
		wantUntil time.Time
		wantErr   bool
	}{
		"default": {value: "", wantSince: time.Time{}},
		"all":     {value: "all", wantSince: time.Time{}},
		"live":    {value: "live", wantSince: now.Add(-15 * time.Minute)},
		"duration": {
			value:     "5m",
			wantSince: now.Add(-5 * time.Minute),
		},
		"seconds": {
			value:     "30s",
			wantSince: now.Add(-30 * time.Second),
		},
		"one minute": {
			value:     "1m",
			wantSince: now.Add(-time.Minute),
		},
		"compound duration": {
			value:     "1h30m",
			wantSince: now.Add(-90 * time.Minute),
		},
		"one hour": {
			value:     "1h",
			wantSince: now.Add(-time.Hour),
		},
		"days": {
			value:     "2d",
			wantSince: now.Add(-48 * time.Hour),
		},
		"weeks": {
			value:     "1w",
			wantSince: now.Add(-7 * 24 * time.Hour),
		},
		"months": {
			value:     "1mo",
			wantSince: now.Add(-30 * 24 * time.Hour),
		},
		"date range": {
			value:     "Jun 27, 10:25 am - Jun 27, 10:40 am",
			wantSince: time.Date(2026, 6, 27, 10, 25, 0, 0, time.UTC),
			wantUntil: time.Date(2026, 6, 27, 10, 40, 0, 0, time.UTC),
		},
		"en dash date range": {
			value:     "2026-06-27 10:25 – 2026-06-27 10:40",
			wantSince: time.Date(2026, 6, 27, 10, 25, 0, 0, time.UTC),
			wantUntil: time.Date(2026, 6, 27, 10, 40, 0, 0, time.UTC),
		},
		"invalid": {value: "forever-ish", wantErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotSince, gotUntil, err := timeRangeFromValue(tc.value, now)
			if tc.wantErr {
				if err == nil {
					t.Fatal("err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("time range: %v", err)
			}
			if !gotSince.Equal(tc.wantSince) {
				t.Fatalf("since = %s, want %s", gotSince, tc.wantSince)
			}
			if !gotUntil.Equal(tc.wantUntil) {
				t.Fatalf("until = %s, want %s", gotUntil, tc.wantUntil)
			}
		})
	}
}
