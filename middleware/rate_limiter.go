package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"project/utils"
)

// Enhanced in-memory rate limiter with per-endpoint rules, trusted-proxy support,
// progressive penalties, and cleanup. This is intentionally memory-efficient and
// designed to be replaced by Redis later.

type timestamps []int64 // unix nanos

func nowUnix() int64 { return time.Now().UnixNano() }

// Configuration defaults (override via env)
func getEnvInt(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return time.Duration(v) * time.Second
		}
	}
	return def
}

// IPRateLimiter implements per-IP fixed-window counters with optional trusted-proxy parsing
type IPRateLimiter struct {
	window      time.Duration
	mu          sync.Mutex
	state       map[string]timestamps
	cleanupTick time.Duration
	trustedCIDR []string
	// optional per-instance max override (used by compatibility wrapper)
	instanceMax int
}

// NewIPRateLimiter creates an IPRateLimiter with an instance-level max requests and window.
// This preserves the original signature used in routes: NewIPRateLimiter(maxReq, window)
func NewIPRateLimiter(maxReq int, window time.Duration) *IPRateLimiter {
	l := &IPRateLimiter{
		window:      window,
		state:       make(map[string]timestamps),
		cleanupTick: getEnvDuration("RATE_CLEANUP_SECONDS", 60*time.Second),
		instanceMax: maxReq,
	}
	if v := os.Getenv("TRUSTED_PROXIES"); v != "" {
		l.trustedCIDR = strings.Split(v, ",")
	}
	go l.cleanupLoop()
	return l
}

// clientIP returns the client IP, using X-Forwarded-For only when the remote
// address is in the configured trusted proxies.
// (removed wrapper) use clientIPGeneric directly where needed

// clientIPGeneric returns the client IP string. If trustedCIDR is provided,
// X-Forwarded-For / X-Real-IP headers are honored when remote addr is inside
// one of the trusted CIDRs or IPs.
func clientIPGeneric(r *http.Request, trustedCIDR []string) string {
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	remoteIP := net.ParseIP(remoteHost)
	trusted := false
	for _, cidr := range trustedCIDR {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if strings.Contains(cidr, "/") {
			if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
				if remoteIP != nil && ipnet.Contains(remoteIP) {
					trusted = true
					break
				}
			}
			continue
		}
		if ip := net.ParseIP(cidr); ip != nil && remoteIP != nil && ip.Equal(remoteIP) {
			trusted = true
			break
		}
	}
	if trusted {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Middleware applies per-IP limits and sets rate-limit headers.
func (l *IPRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIPGeneric(r, l.trustedCIDR)
		now := nowUnix()
		windowNs := int64(l.window)

		l.mu.Lock()
		arr := l.state[ip]
		// filter within window
		var filtered timestamps
		cutoff := now - windowNs
		for _, ts := range arr {
			if ts >= cutoff {
				filtered = append(filtered, ts)
			}
		}
		filtered = append(filtered, now)
		l.state[ip] = filtered
		count := len(filtered)
		l.mu.Unlock()

		// Determine limit based on endpoint category. Prefer constructor-provided instanceMax
		// and fall back to env var defaults.
		limit := l.instanceMax
		if limit <= 0 {
			limit = getEnvInt("RATE_IP_DEFAULT", 200)
		}
		if strings.HasPrefix(r.URL.Path, "/auth") {
			// For auth endpoints prefer env override if set, otherwise use instanceMax or default
			envLimit := getEnvInt("RATE_IP_AUTH", -1)
			if envLimit > 0 {
				limit = envLimit
			} else if l.instanceMax <= 0 {
				limit = getEnvInt("RATE_IP_AUTH", 5)
			}
		}

		remaining := limit - count
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		if count > limit {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(l.window.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("Terlalu banyak permintaan, Coba lagi dalam %d detik", int(l.window.Seconds())),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *IPRateLimiter) cleanupLoop() {
	tick := time.NewTicker(l.cleanupTick)
	defer tick.Stop()
	for range tick.C {
		l.mu.Lock()
		now := nowUnix()
		for k, arr := range l.state {
			// drop entries that have no timestamps within window
			cutoff := now - int64(l.window)
			var filtered timestamps
			for _, ts := range arr {
				if ts >= cutoff {
					filtered = append(filtered, ts)
				}
			}
			if len(filtered) == 0 {
				delete(l.state, k)
			} else {
				l.state[k] = filtered
			}
		}
		l.mu.Unlock()
	}
}

// UserRateLimiter implements sliding window per user with per-endpoint rules and penalties
type UserRateLimiter struct {
	mu            sync.Mutex
	state         map[string]timestamps // key = userID:routeCategory
	penalty       map[string]penaltyInfo
	windowDefault time.Duration
	cleanupTick   time.Duration
	instanceRead  int
	instanceWrite int
}

type penaltyInfo struct {
	Level int
	Until int64 // unix nanos
}

// NewUserRateLimiter preserves the original constructor signature used by routes:
// NewUserRateLimiter(maxReqRead, maxReqWrite, windowSec)
func NewUserRateLimiter(maxReqRead, maxReqWrite int, windowSec int) *UserRateLimiter {
	window := time.Duration(windowSec) * time.Second
	l := &UserRateLimiter{
		state:         make(map[string]timestamps),
		penalty:       make(map[string]penaltyInfo),
		windowDefault: window,
		cleanupTick:   getEnvDuration("RATE_CLEANUP_SECONDS", 60*time.Second),
		// set instance overrides
		instanceRead:  maxReqRead,
		instanceWrite: maxReqWrite,
	}
	go l.cleanupLoop()
	return l
}

func routeCategory(path string) string {
	if strings.HasPrefix(path, "/auth") {
		return "auth"
	}
	if strings.HasPrefix(path, "/admin") {
		return "admin"
	}
	if strings.Contains(path, "/upload") || strings.Contains(path, "/forum") {
		return "upload"
	}
	return "api"
}

func (l *UserRateLimiter) getLimitsForCategory(cat string, role string) (int, time.Duration) {
	// defaults
	switch cat {
	case "auth":
		return getEnvInt("RATE_USER_AUTH", 5), time.Minute
	case "upload":
		return getEnvInt("RATE_USER_UPLOAD", 10), time.Minute
	case "admin":
		if role == "admin" {
			return getEnvInt("RATE_USER_ADMIN", 500), time.Minute
		}
		return getEnvInt("RATE_USER_ADMIN", 10), time.Minute
	default:
		return getEnvInt("RATE_USER_API", 100), time.Minute
	}
}

func (l *UserRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := utils.GetUserID(r)
		roleVal := r.Context().Value(utils.UserRoleKey)
		var role string
		if s, ok2 := roleVal.(string); ok2 {
			role = s
		}
		// admin bypass
		if role == "admin" {
			next.ServeHTTP(w, r)
			return
		}
		if !ok {
			// for unauthenticated endpoints, fallback to IP-based limiter
			next.ServeHTTP(w, r)
			return
		}
		userKey := fmt.Sprintf("u:%d", uid)
		cat := routeCategory(r.URL.Path)
		limit, window := l.getLimitsForCategory(cat, role)

		key := userKey + ":" + cat
		now := nowUnix()
		cutoff := now - int64(window)

		l.mu.Lock()
		arr := l.state[key]
		var filtered timestamps
		for _, ts := range arr {
			if ts >= cutoff {
				filtered = append(filtered, ts)
			}
		}
		filtered = append(filtered, now)
		l.state[key] = filtered
		count := len(filtered)

		// check penalties
		pi := l.penalty[key]
		if pi.Until > now {
			retry := time.Duration(pi.Until-now) * time.Nanosecond
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Too many requests (user), temporary penalty in effect."})
			l.mu.Unlock()
			return
		}

		remaining := limit - count
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		if count > limit {
			// apply penalty: exponential backoff based on previous level
			newLevel := pi.Level + 1
			// penalty durations in minutes: 1,5,15,30 -> convert to seconds
			var durationSec int
			switch newLevel {
			case 1:
				durationSec = 60
			case 2:
				durationSec = 5 * 60
			case 3:
				durationSec = 15 * 60
			default:
				durationSec = 30 * 60
			}
			l.penalty[key] = penaltyInfo{Level: newLevel, Until: now + int64(time.Duration(durationSec)*time.Second)}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", durationSec))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Too many requests (user). Temporary penalty applied."})
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (l *UserRateLimiter) cleanupLoop() {
	tick := time.NewTicker(l.cleanupTick)
	defer tick.Stop()
	for range tick.C {
		l.mu.Lock()
		now := nowUnix()
		// cleanup state
		for k, arr := range l.state {
			cutoff := now - int64(l.windowDefault)
			var filtered timestamps
			for _, ts := range arr {
				if ts >= cutoff {
					filtered = append(filtered, ts)
				}
			}
			if len(filtered) == 0 {
				delete(l.state, k)
			} else {
				l.state[k] = filtered
			}
		}
		// cleanup penalties
		for k, p := range l.penalty {
			if p.Until < now {
				delete(l.penalty, k)
			}
		}
		l.mu.Unlock()
	}
}

// Account lockout tracker for failed logins
var (
	loginMu   sync.Mutex
	failedMap = make(map[string]int)   // key = user:<id> -> failures
	lockMap   = make(map[string]int64) // key -> lockUntil unix nanos
)

func IsAccountLocked(userID uint) (bool, time.Duration) {
	// Prefer Redis-backed lock if available for cross-instance consistency.
	if utils.RedisClient != nil {
		ctx := context.Background()
		lockKey := fmt.Sprintf("login:lock:u:%d", userID)
		ttl, err := utils.RedisClient.TTL(ctx, lockKey).Result()
		if err == nil && ttl > 0 {
			return true, ttl
		}
		return false, 0
	}
	// Fallback to in-memory
	loginMu.Lock()
	defer loginMu.Unlock()
	key := fmt.Sprintf("u:%d", userID)
	until := lockMap[key]
	if until == 0 {
		return false, 0
	}
	now := nowUnix()
	if until > now {
		return true, time.Duration(until-now) * time.Nanosecond
	}
	delete(lockMap, key)
	failedMap[key] = 0
	return false, 0
}

func RecordFailedLogin(userID uint) {
	// If Redis is available, store fail counter and set lock key with TTL when thresholds reached.
	if utils.RedisClient != nil {
		ctx := context.Background()
		failKey := fmt.Sprintf("login:fail:u:%d", userID)
		lockKey := fmt.Sprintf("login:lock:u:%d", userID)
		// increment failures
		failures, err := utils.RedisClient.Incr(ctx, failKey).Result()
		if err != nil {
			// On Redis error fallback to in-memory below
			goto mem
		}
		// set a reasonable expiry for the failure counter (e.g., 30 minutes)
		_, _ = utils.RedisClient.Expire(ctx, failKey, 30*time.Minute).Result()

		// progressive lockout based on failures
		var duration time.Duration
		switch failures {
		case 1:
			duration = 1 * time.Minute
		case 2:
			duration = 5 * time.Minute
		case 3:
			duration = 15 * time.Minute
		default:
			duration = 30 * time.Minute
		}
		if failures >= 1 {
			_ = utils.RedisClient.Set(ctx, lockKey, "1", duration).Err()
		}
		return
	}

mem:
	loginMu.Lock()
	defer loginMu.Unlock()
	key := fmt.Sprintf("u:%d", userID)
	failedMap[key] = failedMap[key] + 1
	failures := failedMap[key]
	// progressive lockout: 1 -> 1min, 2 ->5min, 3 ->15min, >=4 ->30min
	var durationSec int
	switch failures {
	case 1:
		durationSec = 60
	case 2:
		durationSec = 5 * 60
	case 3:
		durationSec = 15 * 60
	default:
		durationSec = 30 * 60
	}
	lockMap[key] = nowUnix() + int64(time.Duration(durationSec)*time.Second)
}

func ResetFailedLogin(userID uint) {
	if utils.RedisClient != nil {
		ctx := context.Background()
		failKey := fmt.Sprintf("login:fail:u:%d", userID)
		lockKey := fmt.Sprintf("login:lock:u:%d", userID)
		_, _ = utils.RedisClient.Del(ctx, failKey, lockKey).Result()
		return
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	key := fmt.Sprintf("u:%d", userID)
	delete(lockMap, key)
	failedMap[key] = 0
}

// WebhookLimiter: sliding window + whitelist IP
type WebhookLimiter struct {
	maxReq    int
	window    time.Duration
	whitelist map[string]bool
	mu        sync.Mutex
	state     map[string]timestamps // ip -> timestamps
}

func NewWebhookLimiter(maxReq int, window time.Duration, whitelist []string) *WebhookLimiter {
	wl := make(map[string]bool)
	for _, ip := range whitelist {
		wl[ip] = true
	}
	return &WebhookLimiter{
		maxReq:    maxReq,
		window:    window,
		whitelist: wl,
		state:     make(map[string]timestamps),
	}
}

func (l *WebhookLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIPGeneric(r, nil)
		if l.whitelist[ip] {
			next.ServeHTTP(w, r)
			return
		}
		now := nowUnix()
		l.mu.Lock()
		arr := l.state[ip]
		cutoff := now - int64(l.window)
		var filtered timestamps
		for _, ts := range arr {
			if ts >= cutoff {
				filtered = append(filtered, ts)
			}
		}
		filtered = append(filtered, now)
		l.state[ip] = filtered
		count := len(filtered)
		l.mu.Unlock()
		if count > l.maxReq {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Too many webhook requests. Please try again later."})
			return
		}
		next.ServeHTTP(w, r)
	})
}
