package auth

import (
	"sync"
	"time"
)

type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	fails    int
	window   time.Time
	lockedTo time.Time
}

func NewLoginLimiter() *LoginLimiter {
	l := &LoginLimiter{entries: make(map[string]*limiterEntry)}
	go func() {
		for range time.Tick(time.Minute * 5) {
			l.mu.Lock()
			now := time.Now()
			for k, e := range l.entries {
				if now.After(e.lockedTo) && now.Sub(e.window) > time.Minute*10 && e.fails == 0 {
					delete(l.entries, k)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *LoginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		return true
	}
	now := time.Now()
	if now.Before(e.lockedTo) {
		return false
	}
	return true
}

func (l *LoginLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		e = &limiterEntry{window: time.Now()}
		l.entries[ip] = e
	}
	if time.Since(e.window) > time.Minute*5 {
		e.window = time.Now()
		e.fails = 0
	}
	e.fails++
	if e.fails >= 5 {
		e.lockedTo = time.Now().Add(time.Minute * 5)
	}
}

func (l *LoginLimiter) Success(ip string) {
	l.mu.Lock()
	delete(l.entries, ip)
	l.mu.Unlock()
}
