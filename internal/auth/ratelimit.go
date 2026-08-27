package auth

import (
	"sync"
	"time"
)

// LoginLimiter rate-limits login attempts per IP: 5 attempts per 5 minutes,
// then locked until the window resets.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempts
}

type loginAttempts struct {
	windowStart time.Time
	count       int
}

const (
	loginMaxAttempts = 5
	loginWindow      = 5 * time.Minute
)

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{attempts: make(map[string]*loginAttempts)}
}

// Allow reports whether a login attempt from ip is permitted right now.
func (l *LoginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if a == nil {
		return true
	}
	if time.Since(a.windowStart) > loginWindow {
		delete(l.attempts, ip)
		return true
	}
	return a.count < loginMaxAttempts
}

// Fail records a failed attempt from ip.
func (l *LoginLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if a == nil || time.Since(a.windowStart) > loginWindow {
		a = &loginAttempts{windowStart: time.Now()}
		l.attempts[ip] = a
	}
	a.count++
}

// Succeed clears the failure record for ip.
func (l *LoginLimiter) Succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}
