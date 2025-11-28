package utils

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"project/database"
	"project/models"

	"github.com/golang-jwt/jwt/v5"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func init() {
	if os.Getenv("JWT_SECRET") == "supersecretjwtkey" {
		panic("JWT_SECRET environment variable is not set")
	}
}

// RedisClient is an optional shared Redis client used for token revocation and other
// cross-process coordination (lockout, blacklists). It will be nil when REDIS_ADDR
// is not configured.
var RedisClient *redis.Client

func init() {
	// Initialize Redis client if configured (optional revocation store)
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		return
	}
	// If someone accidentally put a trailing colon or space, sanitize common mistakes
	addr = strings.ReplaceAll(addr, " ", "")
	opts := &redis.Options{Addr: addr}
	if p := os.Getenv("REDIS_PASS"); p != "" {
		opts.Password = p
	}
	if dbStr := os.Getenv("REDIS_DB"); dbStr != "" {
		var dbn int
		_, _ = fmt.Sscanf(dbStr, "%d", &dbn)
		opts.DB = dbn
	}
	rc := redis.NewClient(opts)
	ctx := context.Background()
	if err := rc.Ping(ctx).Err(); err != nil {
		fmt.Printf("warning: redis ping failed: %v\n", err)
		// don't fail startup for redis issues; revocation will fall back to DB if available
		return
	}
	RedisClient = rc
}

type contextKey string

const UserIDKey = contextKey("userID")
const UserRoleKey = contextKey("userRole")
const RequestIDKey = contextKey("requestID")

// ValidateToken validates a JWT token and returns the parsed token if valid
func ValidateToken(tokenString string) (*jwt.Token, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return nil, errors.New("JWT_SECRET is not set")
	}

	// Parse without validating claims yet so we can inspect them.
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid token")
	}

	// Basic registered claim checks are done by higher-level helpers.
	return token, nil
}

// GenerateJWT generates a new JWT token for the given user ID, username and role
func GenerateJWT(id int64, username, role string) (string, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return "", errors.New("JWT_SECRET is not set")
	}

	// Access token lifetime (short-lived). Consider making configurable.
	var expTime time.Duration
	if role == "admin" {
		expTime = time.Hour * 6
	} else {
		expTime = time.Hour * 24
	}

	now := time.Now()
	jti, err := generateJTI(32)
	if err != nil {
		return "", err
	}

	claims := jwt.MapClaims{
		"id":       id,
		"username": username,
		"role":     role,
		"exp":      now.Add(expTime).Unix(),
		"iat":      now.Unix(),
		"nbf":      now.Unix(),
		"jti":      jti,
		"aud":      os.Getenv("JWT_AUD"),
		"iss":      os.Getenv("JWT_ISS"),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

// GenerateAccessToken issues a short-lived access token (default 15 minutes).
func GenerateAccessToken(userID uint, role string) (string, error) {
	return GenerateAccessTokenWithExpiry(userID, role, 15*time.Minute)
}

// GenerateAccessTokenWithExpiry issues an access token with custom expiry duration
func GenerateAccessTokenWithExpiry(userID uint, role string, expiry time.Duration) (string, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return "", errors.New("JWT_SECRET is not set")
	}
	now := time.Now()
	exp := now.Add(expiry)
	jti, err := generateJTI(32)
	if err != nil {
		return "", err
	}

	rc := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ID:        jti,
	}

	// Custom claims wrapper
	claims := jwt.MapClaims{
		"id":   userID,
		"role": role,
		"exp":  rc.ExpiresAt.Unix(),
		"iat":  rc.IssuedAt.Unix(),
		"nbf":  rc.NotBefore.Unix(),
		"jti":  rc.ID,
		"aud":  os.Getenv("JWT_AUD"),
		"iss":  os.Getenv("JWT_ISS"),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GenerateRefreshToken creates a refresh token, stores it in DB and returns the token string (contains jti)
func GenerateRefreshToken(userID uint) (string, string, error) {
	// We store the refresh token ID in DB and return opaque token = jti
	jti, err := generateJTI(48)
	if err != nil {
		return "", "", err
	}
	// Create DB entry
	rt, err := models.NewRefreshToken(userID, 7) // 7 days
	if err != nil {
		return "", "", err
	}
	// override generated ID with jti for consistency
	rt.ID = jti
	if database.DB == nil {
		return "", "", errors.New("database not initialized")
	}
	if err := database.DB.Create(rt).Error; err != nil {
		return "", "", err
	}
	return jti, rt.ID, nil
}

// ValidateAccessToken parses and validates the access token and optionally checks jti revocation store in DB (not implemented here)
func ValidateAccessToken(tokenStr string) (*jwt.Token, jwt.MapClaims, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return nil, nil, errors.New("JWT_SECRET is not set")
	}
	// Parse token with claims as MapClaims so we can do explicit checks.
	token, err := jwt.ParseWithClaims(tokenStr, jwt.MapClaims{}, func(t *jwt.Token) (interface{}, error) {
		// Require exact HS256 algorithm to avoid algorithm confusion.
		if t.Method == nil || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, nil, errors.New("invalid token")
	}

	// Extract and validate claims
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return token, nil, errors.New("invalid claims")
	}

	// Validate registered claims: exp, nbf, aud, iss, jti
	now := time.Now()
	// exp
	if expRaw, ok := claims["exp"]; ok {
		switch v := expRaw.(type) {
		case float64:
			if now.Unix() > int64(v) {
				return token, nil, errors.New("token expired")
			}
		case int64:
			if now.Unix() > v {
				return token, nil, errors.New("token expired")
			}
		case int:
			if now.Unix() > int64(v) {
				return token, nil, errors.New("token expired")
			}
		}
	}

	// nbf
	if nbfRaw, ok := claims["nbf"]; ok {
		switch v := nbfRaw.(type) {
		case float64:
			if now.Unix() < int64(v) {
				return token, nil, errors.New("token not yet valid")
			}
		}
	}

	// aud
	audEnv := os.Getenv("JWT_AUD")
	if audEnv != "" {
		audRaw, ok := claims["aud"]
		if !ok {
			return token, nil, errors.New("aud claim missing")
		}
		switch v := audRaw.(type) {
		case string:
			if v != audEnv {
				return token, nil, errors.New("invalid audience")
			}
		case []interface{}:
			found := false
			for _, a := range v {
				if s, ok := a.(string); ok && s == audEnv {
					found = true
					break
				}
			}
			if !found {
				return token, nil, errors.New("invalid audience")
			}
		default:
			return token, nil, errors.New("invalid audience claim format")
		}
	}

	// iss
	issEnv := os.Getenv("JWT_ISS")
	if issEnv != "" {
		if issRaw, ok := claims["iss"].(string); !ok || issRaw != issEnv {
			return token, nil, errors.New("invalid issuer")
		}
	}

	// jti revocation: check Redis blacklist first (if configured), otherwise check DB table 'revoked_tokens'
	if jtiRaw, ok := claims["jti"].(string); ok && jtiRaw != "" {
		if RedisClient != nil {
			ctx := context.Background()
			res, err := RedisClient.Get(ctx, "jwt:blacklist:"+jtiRaw).Result()
			if err == nil && res == "1" {
				return token, nil, errors.New("token revoked")
			}
			// ignore redis errors (do not fail auth due to redis outage)
		} else if database.DB != nil {
			var rec struct {
				ID string `gorm:"primaryKey"`
			}
			err := database.DB.Table("revoked_tokens").Where("id = ?", jtiRaw).First(&rec).Error
			if err == nil {
				return token, nil, errors.New("token revoked")
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				// Ignore DB errors (do not fail authentication because of DB outage)
			}
		}
	}

	return token, claims, nil
}

// ValidateRefreshToken checks whether a refresh token jti exists in DB and is not expired/revoked
func ValidateRefreshToken(jti string) (*models.RefreshToken, error) {
	if database.DB == nil {
		return nil, errors.New("database not initialized")
	}
	var rt models.RefreshToken
	if err := database.DB.Where("id = ?", jti).First(&rt).Error; err != nil {
		return nil, err
	}
	if rt.Revoked {
		return nil, errors.New("refresh token revoked")
	}
	if time.Now().After(rt.ExpiresAt) {
		return nil, errors.New("refresh token expired")
	}
	return &rt, nil
}

// ExtractUserIDFromRequest parses JWT from Authorization header and returns userID (uint) or error.
func ExtractUserIDFromRequest(r *http.Request) (uint, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" || !strings.HasPrefix(authz, "Bearer ") {
		return 0, errors.New("missing or invalid Authorization header")
	}
	tokenStr := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return 0, errors.New("server misconfiguration")
	}
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return 0, errors.New("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("invalid token claims")
	}
	rawID, ok := claims["id"]
	if !ok {
		return 0, errors.New("invalid token payload")
	}
	switch v := rawID.(type) {
	case float64:
		return uint(v), nil
	case int:
		return uint(v), nil
	case string:
		// try parse string to uint
		return parseUintString(v)
	default:
		return 0, errors.New("invalid token payload")
	}
}

func parseUintString(s string) (uint, error) {
	var n uint64
	_, err := fmt.Sscanf(s, "%d", &n)
	return uint(n), err
}

// RevokeJTI inserts a jti into the revocation store. If Redis is configured, set a key with TTL.
// Otherwise fall back to inserting into `revoked_tokens` table.
func RevokeJTI(jti string, ttl time.Duration) error {
	if jti == "" {
		return errors.New("empty jti")
	}
	ctx := context.Background()
	if RedisClient != nil {
		return RedisClient.Set(ctx, "jwt:blacklist:"+jti, "1", ttl).Err()
	}
	if database.DB != nil {
		// Upsert into revoked_tokens (MySQL ON DUPLICATE KEY). If DB is unavailable, return error.
		res := database.DB.Exec("INSERT INTO revoked_tokens (id, revoked_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE revoked_at = VALUES(revoked_at)", jti, time.Now())
		return res.Error
	}
	return errors.New("no revocation store configured")
}

// generateJTI creates a URL-safe random identifier used as JWT ID
func generateJTI(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// simple hex-like encoding
	const hex = "0123456789abcdef"
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = hex[int(b[i])%len(hex)]
	}
	return string(out), nil
}

// Middleware: inject userID to context
func AuthUserIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := ExtractUserIDFromRequest(r)
		if err != nil {
			WriteJSON(w, http.StatusUnauthorized, APIResponse{Success: false, Message: "Unauthorized"})
			return
		}
		ctx := context.WithValue(r.Context(), UserIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Get userID from context
func GetUserID(r *http.Request) (uint, bool) {
	v := r.Context().Value(UserIDKey)
	id, ok := v.(uint)
	return id, ok
}
