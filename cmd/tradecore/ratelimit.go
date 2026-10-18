package main

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// tokenBucket refills continuously at rate tokens per second up to burst.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*tokenBucket
}

func newLimiter(perSecond, burst float64) *limiter {
	return &limiter{rate: perSecond, burst: burst, buckets: make(map[string]*tokenBucket)}
}

// allow takes one token for key, reporting false when none are left. A
// non-positive rate disables the limiter.
func (l *limiter) allow(key string, now time.Time) bool {
	if l.rate <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[key]
	if bucket == nil {
		bucket = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = bucket
	}
	bucket.tokens = min(l.burst, bucket.tokens+now.Sub(bucket.last).Seconds()*l.rate)
	bucket.last = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// prune forgets buckets that have refilled completely.
func (l *limiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, bucket := range l.buckets {
		if bucket.tokens+now.Sub(bucket.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, key)
		}
	}
}

type limiters struct {
	ordersPerUser *limiter
	ordersPerIP   *limiter
	authPerIP     *limiter
	wsMessages    *limiter
}

// newLimitersFromEnv applies the PRD limits (10 orders/s per user) plus
// per-IP limits. Each can be overridden, mainly for load testing; 0 disables.
func newLimitersFromEnv() *limiters {
	return &limiters{
		ordersPerUser: newLimiter(envFloat("ORDER_RATE_PER_USER", 10), envFloat("ORDER_RATE_PER_USER", 10)),
		ordersPerIP:   newLimiter(envFloat("ORDER_RATE_PER_IP", 30), envFloat("ORDER_RATE_PER_IP", 30)),
		authPerIP:     newLimiter(envFloat("AUTH_RATE_PER_MINUTE", 10)/60, envFloat("AUTH_RATE_PER_MINUTE", 10)),
		wsMessages:    newLimiter(20, 40),
	}
}

func (l *limiters) prune(now time.Time) {
	for _, each := range []*limiter{l.ordersPerUser, l.ordersPerIP, l.authPerIP, l.wsMessages} {
		each.prune(now)
	}
}

func envFloat(name string, fallback float64) float64 {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value >= 0 {
			return value
		}
	}
	return fallback
}
