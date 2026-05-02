package config

import (
	"testing"
	"time"
)

func TestGetDurationEnv(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  time.Duration
	}{
		{
			name:  "uses default when not set",
			key:   "TEST_CONFIG_KEY",
			value: "",
			want:  10 * time.Second,
		},
		{
			name:  "uses env value",
			key:   "TEST_CONFIG_KEY",
			value: "20",
			want:  20 * time.Second,
		},
		{
			name:  "invalid env uses default",
			key:   "TEST_CONFIG_KEY",
			value: "invalid",
			want:  10 * time.Second,
		},
		{
			name:  "empty env uses default",
			key:   "TEST_CONFIG_KEY",
			value: "",
			want:  10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			got := getDurationEnv(tt.key, 10, time.Second)
			if got != tt.want {
				t.Errorf("getDurationEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

// assertInRange fails if got falls outside [wantMin, wantMax].
func assertInRange(t *testing.T, name string, got, wantMin, wantMax time.Duration) {
	t.Helper()
	if got < wantMin || got > wantMax {
		t.Errorf("%s: got %v, want in [%v, %v]", name, got, wantMin, wantMax)
	}
}

func TestGetRetryDelay(t *testing.T) {
	// GetRetryDelay adds ±10% jitter, so we check ranges rather than exact values.
	const lo, hi = 0.9, 1.1

	tests := []struct {
		name       string
		retryCount int32
		base       time.Duration // expected center value before jitter
	}{
		{name: "base delay on first call", retryCount: 0, base: RetryBaseDelay},
		{name: "first retry same as base", retryCount: 1, base: RetryBaseDelay},
		{name: "second retry doubles", retryCount: 2, base: RetryBaseDelay * 2},
		{name: "third retry quadruples", retryCount: 3, base: RetryBaseDelay * 4},
		{name: "capped at max delay", retryCount: 100, base: RetryMaxDelay},
		{name: "negative treated as base", retryCount: -1, base: RetryBaseDelay},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetRetryDelay(tt.retryCount)
			assertInRange(t, tt.name, got,
				time.Duration(float64(tt.base)*lo),
				time.Duration(float64(tt.base)*hi),
			)
		})
	}
}
