package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"project/database"
	"project/middleware"
	"project/models"
	"project/utils"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type ForgotPasswordRequestOTPRequest struct {
	Number string `json:"number"`
}

type ForgotPasswordResendOTPRequest struct {
	Number string `json:"number"`
}

type ForgotPasswordVerifyOTPRequest struct {
	OTP       string `json:"otp"`
	RequestID string `json:"request_id"`
}

type ForgotPasswordResetPasswordRequest struct {
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
	Token           string `json:"token"`
}

// OTPRequest stores OTP request information
type OTPRequest struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"not null;index"`
	Phone     string    `gorm:"size:20;not null;index"`
	OTPID     string    `gorm:"type:varchar(255);not null"`
	Verified  bool      `gorm:"default:false"`
	ExpiresAt time.Time `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (OTPRequest) TableName() string {
	return "otp_requests"
}

// POST /v3/auth/forgot-password/request-otp
func ForgotPasswordRequestOTPHandler(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordRequestOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	// Validate phone number format (must start with 8)
	req.Number = strings.TrimSpace(req.Number)
	if req.Number == "" || !strings.HasPrefix(req.Number, "8") {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Nomor telepon harus dimulai dengan 8",
		})
		return
	}

	// Get IP address for rate limiting
	ip := middleware.GetClientIP(r)
	otpLimiter := middleware.GetOTPRateLimiter()

	// Check IP rate limit
	allowed, waitTime, msg := otpLimiter.CheckIPRateLimit(ip)
	if !allowed {
		utils.WriteJSON(w, http.StatusTooManyRequests, utils.APIResponse{
			Success: false,
			Message: msg,
			Data: map[string]interface{}{
				"retry_after_seconds": int(waitTime.Seconds()),
			},
		})
		return
	}

	// Check phone rate limit
	allowed, waitTime, msg = otpLimiter.CheckPhoneRateLimit(req.Number)
	if !allowed {
		utils.WriteJSON(w, http.StatusTooManyRequests, utils.APIResponse{
			Success: false,
			Message: msg,
			Data: map[string]interface{}{
				"retry_after_seconds": int(waitTime.Seconds()),
			},
		})
		return
	}

	db := database.DB

	// Check if phone number exists in database
	var user models.User
	if err := db.Where("number = ?", req.Number).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Nomor telepon tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Request OTP from Fazpass
	fazpassResp, err := utils.RequestOTP(req.Number)
	if err != nil {
		// Check if it's a FazpassError
		if fazpassErr, ok := err.(*utils.FazpassError); ok {
			userMessage := utils.GetUserFriendlyMessage(fazpassErr.Code)
			httpStatus := http.StatusBadRequest
			if fazpassErr.HTTPCode >= 400 && fazpassErr.HTTPCode < 600 {
				httpStatus = fazpassErr.HTTPCode
			}
			utils.WriteJSON(w, httpStatus, utils.APIResponse{
				Success: false,
				Message: userMessage,
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengirim Kode Verifikasi. Silakan coba lagi nanti.",
		})
		return
	}

	// Save OTP request to database (expires in 10 minutes)
	otpReq := OTPRequest{
		UserID:    user.ID,
		Phone:     req.Number,
		OTPID:     fazpassResp.Data.ID,
		Verified:  false,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	if err := db.Create(&otpReq).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Calculate retry_after_seconds based on current rate limit status
	retryAfter := otpLimiter.GetRetryAfterSeconds(req.Number)

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Kode Verifikasi berhasil dikirim",
		Data: map[string]interface{}{
			"request_id":          fazpassResp.Data.ID,
			"number":              req.Number,
			"retry_after_seconds": retryAfter,
		},
	})
}

// POST /v3/auth/forgot-password/resend-otp
func ForgotPasswordResendOTPHandler(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordResendOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	// Validate phone number format (must start with 8)
	req.Number = strings.TrimSpace(req.Number)
	if req.Number == "" || !strings.HasPrefix(req.Number, "8") {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Nomor telepon harus dimulai dengan 8",
		})
		return
	}

	// Get IP address for rate limiting
	ip := middleware.GetClientIP(r)
	otpLimiter := middleware.GetOTPRateLimiter()

	// Check IP rate limit
	allowed, waitTime, msg := otpLimiter.CheckIPRateLimit(ip)
	if !allowed {
		utils.WriteJSON(w, http.StatusTooManyRequests, utils.APIResponse{
			Success: false,
			Message: msg,
			Data: map[string]interface{}{
				"retry_after_seconds": int(waitTime.Seconds()),
			},
		})
		return
	}

	// Check phone rate limit
	allowed, waitTime, msg = otpLimiter.CheckPhoneRateLimit(req.Number)
	if !allowed {
		utils.WriteJSON(w, http.StatusTooManyRequests, utils.APIResponse{
			Success: false,
			Message: msg,
			Data: map[string]interface{}{
				"retry_after_seconds": int(waitTime.Seconds()),
			},
		})
		return
	}

	db := database.DB

	// Check if phone number exists in database
	var user models.User
	if err := db.Where("number = ?", req.Number).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Nomor telepon tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Request OTP from Fazpass
	fazpassResp, err := utils.RequestOTP(req.Number)
	if err != nil {
		// Check if it's a FazpassError
		if fazpassErr, ok := err.(*utils.FazpassError); ok {
			userMessage := utils.GetUserFriendlyMessage(fazpassErr.Code)
			httpStatus := http.StatusBadRequest
			if fazpassErr.HTTPCode >= 400 && fazpassErr.HTTPCode < 600 {
				httpStatus = fazpassErr.HTTPCode
			}
			utils.WriteJSON(w, httpStatus, utils.APIResponse{
				Success: false,
				Message: userMessage,
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengirim Kode Verifikasi. Silakan coba lagi nanti.",
		})
		return
	}

	// Update or create OTP request in database (expires in 10 minutes)
	otpReq := OTPRequest{
		UserID:    user.ID,
		Phone:     req.Number,
		OTPID:     fazpassResp.Data.ID,
		Verified:  false,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	// Delete old unverified OTP requests for this phone
	db.Where("phone = ? AND verified = ?", req.Number, false).Delete(&OTPRequest{})

	// Create new OTP request
	if err := db.Create(&otpReq).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Calculate retry_after_seconds based on current rate limit status
	retryAfter := otpLimiter.GetRetryAfterSeconds(req.Number)

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Kode Verifikasi berhasil dikirim ulang",
		Data: map[string]interface{}{
			"request_id":          fazpassResp.Data.ID,
			"number":              req.Number,
			"retry_after_seconds": retryAfter,
		},
	})
}

// POST /v3/auth/forgot-password/verify-otp
func ForgotPasswordVerifyOTPHandler(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordVerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	if req.OTP == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Kode Verifikasi harus diisi",
		})
		return
	}

	if req.RequestID == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	db := database.DB

	// Find OTP request
	var otpReq OTPRequest
	if err := db.Where("otp_id = ? AND verified = ?", req.RequestID, false).First(&otpReq).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Request Kode Verifikasi tidak ditemukan atau sudah digunakan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Check if OTP request has expired
	if time.Now().After(otpReq.ExpiresAt) {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Kode Verifikasi sudah kadaluarsa",
		})
		return
	}

	// Verify OTP with Fazpass
	_, err := utils.VerifyOTP(req.RequestID, req.OTP)
	if err != nil {
		// Check if it's a FazpassError
		if fazpassErr, ok := err.(*utils.FazpassError); ok {
			userMessage := utils.GetUserFriendlyMessage(fazpassErr.Code)
			httpStatus := http.StatusBadRequest
			if fazpassErr.HTTPCode >= 400 && fazpassErr.HTTPCode < 600 {
				httpStatus = fazpassErr.HTTPCode
			}
			utils.WriteJSON(w, httpStatus, utils.APIResponse{
				Success: false,
				Message: userMessage,
			})
			return
		}
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Kode Verifikasi salah",
		})
		return
	}

	// Mark OTP as verified
	otpReq.Verified = true
	if err := db.Save(&otpReq).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Generate JWT token for password reset (valid for 15 minutes)
	resetToken, err := utils.GenerateAccessToken(otpReq.UserID, "user")
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Reset phone rate limit after successful verification
	otpLimiter := middleware.GetOTPRateLimiter()
	otpLimiter.ResetPhoneLimit(otpReq.Phone)

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Kode Verifikasi benar, Silahkan ubah password Anda.",
		Data: map[string]interface{}{
			"token": resetToken,
		},
	})
}

// POST /v3/auth/forgot-password/reset-password
func ForgotPasswordResetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	if req.Password == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Password harus diisi",
		})
		return
	}

	if req.Password != req.ConfirmPassword {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Password dan konfirmasi password tidak sama",
		})
		return
	}

	if len(req.Password) < 6 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Password minimal 6 karakter",
		})
		return
	}

	if req.Token == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Token harus diisi",
		})
		return
	}

	// Validate JWT token
	token, claims, err := utils.ValidateAccessToken(req.Token)
	if err != nil || !token.Valid {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Token tidak valid atau sudah kadaluarsa",
		})
		return
	}

	// Get JTI from token to revoke it after use (one-time use)
	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Token tidak valid",
		})
		return
	}

	// Check if token is already revoked (already used)
	if RedisClient := utils.RedisClient; RedisClient != nil {
		ctx := context.Background()
		res, err := RedisClient.Get(ctx, "jwt:blacklist:"+jti).Result()
		if err == nil && res == "1" {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
				Success: false,
				Message: "Token sudah pernah digunakan",
			})
			return
		}
	} else if database.DB != nil {
		var rec struct {
			ID string `gorm:"primaryKey"`
		}
		err := database.DB.Table("revoked_tokens").Where("id = ?", jti).First(&rec).Error
		if err == nil {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
				Success: false,
				Message: "Token sudah pernah digunakan",
			})
			return
		}
	}

	// Get user ID from token
	userIDFloat, ok := claims["id"].(float64)
	if !ok {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Token tidak valid",
		})
		return
	}
	userID := uint(userIDFloat)

	db := database.DB

	// Get user
	var user models.User
	if err := db.First(&user, userID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Pengguna tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Hash new password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	// Update password and revoke token in a transaction
	err = db.Transaction(func(tx *gorm.DB) error {
		// Update password
		user.Password = string(hashedPassword)
		if err := tx.Save(&user).Error; err != nil {
			return err
		}

		// Revoke token (mark as used - one-time use)
		// Calculate TTL from token expiration
		var ttl time.Duration = 0
		if expRaw, ok := claims["exp"]; ok {
			switch v := expRaw.(type) {
			case float64:
				expTime := time.Unix(int64(v), 0)
				ttl = time.Until(expTime)
			case int64:
				expTime := time.Unix(v, 0)
				ttl = time.Until(expTime)
			case int:
				expTime := time.Unix(int64(v), 0)
				ttl = time.Until(expTime)
			}
		}
		if ttl < 0 {
			ttl = 0
		}

		// Revoke the token
		if err := utils.RevokeJTI(jti, ttl); err != nil {
			// Log error but don't fail the transaction
			// Token revocation is best-effort
		}

		return nil
	})

	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan. Silakan coba lagi nanti.",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Password berhasil diubah, Anda akan diarahkan ke halaman login dalam 5 detik",
		Data:    nil,
	})
}
