package scim

import (
	"context"
	"sync"
	"time"
)

// limiter is a minimal leaky-bucket throttle. It exists so this package needs
// no third-party dependency: a partner should be able to vendor this client and
// build it offline.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(perSecond float64) *limiter {
	if perSecond <= 0 {
		return &limiter{}
	}
	return &limiter{interval: time.Duration(float64(time.Second) / perSecond)}
}

// wait blocks until the next slot is free, using the injected sleep so tests
// stay instant.
func (l *limiter) wait(ctx context.Context, sleep func(context.Context, time.Duration) error) error {
	if l == nil || l.interval == 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	delay := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if delay <= 0 {
		return ctx.Err()
	}
	return sleep(ctx, delay)
}
