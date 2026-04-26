package app

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	maxLoginFailures   = 5
	loginFailureWindow = 15 * time.Minute
	loginBlockDuration = 15 * time.Minute
)

type loginAttempt struct {
	failedCount  int
	firstFailure time.Time
	blockedUntil time.Time
}

type loginThrottle struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		attempts: make(map[string]loginAttempt),
	}
}

func (t *loginThrottle) isBlocked(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.attempts[key]
	if !ok {
		return false
	}
	if !entry.blockedUntil.IsZero() && now.Before(entry.blockedUntil) {
		return true
	}
	if !entry.blockedUntil.IsZero() && !now.Before(entry.blockedUntil) {
		delete(t.attempts, key)
	}
	return false
}

func (t *loginThrottle) recordFailure(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry := t.attempts[key]
	if entry.firstFailure.IsZero() || now.Sub(entry.firstFailure) > loginFailureWindow {
		entry = loginAttempt{
			failedCount:  1,
			firstFailure: now,
		}
		t.attempts[key] = entry
		return
	}

	entry.failedCount++
	if entry.failedCount >= maxLoginFailures {
		entry.blockedUntil = now.Add(loginBlockDuration)
	}
	t.attempts[key] = entry
}

func (t *loginThrottle) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, key)
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
