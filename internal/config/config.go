package config

import (
	"math/rand"
	"os"
	"strconv"
	"time"
)

const (
	jitterFactor     = 0.1 // ±10% noise applied to each retry delay
	maxExponentShift = 10  // caps 2^n before overflow; 2^10 × base already exceeds RetryMaxDelay
)

var (
	RetryBaseDelay = getDurationEnv("RETRY_BASE_DELAY", 5, time.Second)
	RetryMaxDelay  = getDurationEnv("RETRY_MAX_DELAY", 300, time.Second)
)

func getDurationEnv(key string, defaultValue int, unit time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return time.Duration(parsed) * unit
		}
	}
	return time.Duration(defaultValue) * unit
}

// GetRetryDelay returns an exponential backoff delay for the given attempt number,
// capped at RetryMaxDelay, with ±10% jitter to prevent thundering herd on recovery.
func GetRetryDelay(retryCount int32) time.Duration {
	if retryCount <= 0 {
		return jitter(RetryBaseDelay)
	}
	shift := retryCount - 1
	if shift > maxExponentShift {
		shift = maxExponentShift
	}
	delay := RetryBaseDelay * (1 << shift)
	if delay > RetryMaxDelay {
		delay = RetryMaxDelay
	}
	return jitter(delay)
}

// jitter adds ±10% random noise to d.
func jitter(d time.Duration) time.Duration {
	factor := 1.0 + jitterFactor*(rand.Float64()*2-1) //nolint:gosec
	return time.Duration(float64(d) * factor)
}
