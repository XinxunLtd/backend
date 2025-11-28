package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OTPRequestRecord tracks OTP requests for a phone number
type OTPRequestRecord struct {
	Count       int
	FirstReqAt  time.Time
	LastReqAt   time.Time
	Locked      bool
	LockedUntil time.Time
}

// OTPRateLimiter manages rate limiting for OTP requests
type OTPRateLimiter struct {
	phoneRecords  map[string]*OTPRequestRecord
	ipRecords     map[string]*IPOTPRecord
	mu            sync.RWMutex
	cleanupTicker *time.Ticker
}

// IPOTPRecord tracks OTP requests per IP
type IPOTPRecord struct {
	Count      int
	FirstReqAt time.Time
	LastReqAt  time.Time
}

var globalOTPLimiter *OTPRateLimiter
var otpLimiterOnce sync.Once

// GetOTPRateLimiter returns the global OTP rate limiter instance
func GetOTPRateLimiter() *OTPRateLimiter {
	otpLimiterOnce.Do(func() {
		globalOTPLimiter = NewOTPRateLimiter()
	})
	return globalOTPLimiter
}

// NewOTPRateLimiter creates a new OTP rate limiter
func NewOTPRateLimiter() *OTPRateLimiter {
	limiter := &OTPRateLimiter{
		phoneRecords: make(map[string]*OTPRequestRecord),
		ipRecords:    make(map[string]*IPOTPRecord),
	}

	// Cleanup old records every 5 minutes
	limiter.cleanupTicker = time.NewTicker(5 * time.Minute)
	go limiter.cleanup()

	return limiter
}

// cleanup removes old records periodically
func (l *OTPRateLimiter) cleanup() {
	for range l.cleanupTicker.C {
		l.mu.Lock()
		now := time.Now()

		// Cleanup phone records older than 1 hour
		for phone, record := range l.phoneRecords {
			if !record.Locked && now.Sub(record.LastReqAt) > time.Hour {
				delete(l.phoneRecords, phone)
			} else if record.Locked && now.After(record.LockedUntil) {
				// Reset locked records after lock expires
				record.Locked = false
				record.Count = 0
				record.FirstReqAt = time.Time{}
				record.LastReqAt = time.Time{}
			}
		}

		// Cleanup IP records older than 30 minutes
		for ip, record := range l.ipRecords {
			if now.Sub(record.LastReqAt) > 30*time.Minute {
				delete(l.ipRecords, ip)
			}
		}

		l.mu.Unlock()
	}
}

// CheckPhoneRateLimit checks if a phone number can make an OTP request
// Returns (allowed, waitDuration, message)
func (l *OTPRateLimiter) CheckPhoneRateLimit(phone string) (bool, time.Duration, string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	record, exists := l.phoneRecords[phone]

	if !exists {
		// First request - allowed
		l.phoneRecords[phone] = &OTPRequestRecord{
			Count:      1,
			FirstReqAt: now,
			LastReqAt:  now,
			Locked:     false,
		}
		return true, 0, ""
	}

	// Check if locked
	if record.Locked {
		if now.Before(record.LockedUntil) {
			waitTime := record.LockedUntil.Sub(now)
			return false, waitTime, "Anda telah mencapai batas permintaan, silahkan ulangi dalam 1 jam"
		}
		// Lock expired, reset
		record.Locked = false
		record.Count = 0
		record.FirstReqAt = now
		record.LastReqAt = now
		return true, 0, ""
	}

	// Check count and apply rate limiting
	record.Count++
	record.LastReqAt = now

	switch record.Count {
	case 1:
		// First request - allowed
		return true, 0, ""
	case 2:
		// Second request - check if 1 minute has passed since first request
		elapsed := now.Sub(record.FirstReqAt)
		if elapsed < time.Minute {
			waitTime := time.Minute - elapsed
			record.Count-- // Revert count
			return false, waitTime, "Tunggu 1 menit sebelum meminta OTP lagi"
		}
		return true, 0, ""
	case 3:
		// Third request - check if 5 minutes have passed since first request
		elapsed := now.Sub(record.FirstReqAt)
		if elapsed < 5*time.Minute {
			waitTime := 5*time.Minute - elapsed
			record.Count-- // Revert count
			return false, waitTime, "Tunggu 5 menit sebelum meminta OTP lagi"
		}
		return true, 0, ""
	case 4:
		// Fourth request - check if 10 minutes have passed since first request
		elapsed := now.Sub(record.FirstReqAt)
		if elapsed < 10*time.Minute {
			waitTime := 10*time.Minute - elapsed
			record.Count-- // Revert count
			return false, waitTime, "Tunggu 10 menit sebelum meminta OTP lagi"
		}
		return true, 0, ""
	case 5:
		// Fifth request - lock for 1 hour
		record.Locked = true
		record.LockedUntil = now.Add(time.Hour)
		return false, time.Hour, "Anda telah mencapai batas permintaan, silahkan ulangi dalam 1 jam"
	default:
		// More than 5 requests - check lock
		if record.Locked && now.Before(record.LockedUntil) {
			waitTime := record.LockedUntil.Sub(now)
			return false, waitTime, "Anda telah mencapai batas permintaan, silahkan ulangi dalam 1 jam"
		}
		// Lock expired, reset
		record.Locked = false
		record.Count = 1
		record.FirstReqAt = now
		record.LastReqAt = now
		return true, 0, ""
	}
}

// CheckIPRateLimit checks if an IP can make an OTP request
// Returns (allowed, waitDuration, message)
func (l *OTPRateLimiter) CheckIPRateLimit(ip string) (bool, time.Duration, string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	record, exists := l.ipRecords[ip]

	if !exists {
		// First request - allowed
		l.ipRecords[ip] = &IPOTPRecord{
			Count:      1,
			FirstReqAt: now,
			LastReqAt:  now,
		}
		return true, 0, ""
	}

	// Check if 30 minutes have passed since first request
	elapsed := now.Sub(record.FirstReqAt)
	if elapsed >= 30*time.Minute {
		// Reset counter after 30 minutes
		record.Count = 1
		record.FirstReqAt = now
		record.LastReqAt = now
		return true, 0, ""
	}

	// Check count
	record.Count++
	record.LastReqAt = now

	if record.Count > 5 {
		// More than 5 requests in 30 minutes
		waitTime := 30*time.Minute - elapsed
		record.Count-- // Revert count
		return false, waitTime, "Terlalu banyak permintaan. Coba lagi nanti."
	}

	return true, 0, ""
}

// ResetPhoneLimit resets the rate limit for a phone number (used after successful OTP verification)
func (l *OTPRateLimiter) ResetPhoneLimit(phone string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.phoneRecords, phone)
}

// GetRetryAfterSeconds calculates retry_after_seconds for a phone number without modifying state
func (l *OTPRateLimiter) GetRetryAfterSeconds(phone string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := time.Now()
	record, exists := l.phoneRecords[phone]

	if !exists {
		return 0
	}

	// Check if locked
	if record.Locked {
		if now.Before(record.LockedUntil) {
			return int(record.LockedUntil.Sub(now).Seconds())
		}
		return 0
	}

	// Calculate retry_after based on count
	elapsed := now.Sub(record.FirstReqAt)
	switch record.Count {
	case 1:
		// After first request, must wait 1 minute before second request
		return int(time.Minute.Seconds()) // 60 seconds
	case 2:
		// After second request, check if 1 minute has passed since first request
		if elapsed < time.Minute {
			return int((time.Minute - elapsed).Seconds())
		}
		// If 1 minute has passed, can request again (but will need to wait 5 minutes from first request for third)
		if elapsed < 5*time.Minute {
			return int((5*time.Minute - elapsed).Seconds())
		}
		return 0
	case 3:
		// After third request, check if 5 minutes have passed since first request
		if elapsed < 5*time.Minute {
			return int((5*time.Minute - elapsed).Seconds())
		}
		// If 5 minutes have passed, can request again (but will need to wait 10 minutes from first request for fourth)
		if elapsed < 10*time.Minute {
			return int((10*time.Minute - elapsed).Seconds())
		}
		return 0
	case 4:
		// After fourth request, check if 10 minutes have passed since first request
		if elapsed < 10*time.Minute {
			return int((10*time.Minute - elapsed).Seconds())
		}
		// If 10 minutes have passed, can request again (but will need to wait 1 hour from first request for fifth)
		return int(time.Hour.Seconds())
	case 5:
		// After fifth request, must wait 1 hour
		return int(time.Hour.Seconds())
	default:
		if record.Locked && now.Before(record.LockedUntil) {
			return int(record.LockedUntil.Sub(now).Seconds())
		}
		return 0
	}
}

// GetClientIP extracts the client IP from the request
func GetClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	// Check X-Real-IP header
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}

	// Fallback to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
