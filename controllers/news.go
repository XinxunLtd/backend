package controllers

import (
	"encoding/json"
	"net/http"

	"project/database"
	"project/models"
	"project/utils"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// POST /v3/news/login
func NewsLoginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Number   string `json:"number"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	if req.Number == "" || req.Password == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Number dan password harus diisi",
		})
		return
	}

	db := database.DB

	var user models.User
	if err := db.Where("number = ?", req.Number).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
				Success: false,
				Message: "Nomor telpon atau password salah",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Server error",
		})
		return
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Nomor telpon atau password salah",
		})
		return
	}

	// Validate status_publisher
	if user.StatusPublisher == "Inactive" {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{
			Success: false,
			Message: "Fitur publisher pada akun anda tidak aktif, Silahkan hubungi Admin untuk mengaktifkan fitur tersebut",
		})
		return
	}

	if user.StatusPublisher == "Suspend" {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{
			Success: false,
			Message: "Status publisher Anda saat ini ditangguhkan, Silahkan untuk menghubungi Admin",
		})
		return
	}

	// Build response data
	responseData := map[string]interface{}{
		"id":        user.ID,
		"name":      user.Name,
		"number":    user.Number,
		"balance":   user.Balance,
		"status":    user.Status,
		"reff_code": user.ReffCode,
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Login berhasil",
		Data:    responseData,
	})
}

// POST /v3/news/reward
func NewsRewardHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID uint    `json:"user_id"`
		Amount float64 `json:"amount"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	if req.UserID == 0 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "user_id harus diisi",
		})
		return
	}

	if req.Amount <= 0 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "amount harus lebih dari 0",
		})
		return
	}

	db := database.DB

	// Check if user exists
	var user models.User
	if err := db.First(&user, req.UserID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "User tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Server error",
		})
		return
	}

	// Update user balance and create transaction in a single transaction
	err := db.Transaction(func(tx *gorm.DB) error {
		// Update user balance
		newBalance := user.Balance + req.Amount
		if err := tx.Model(&user).Update("balance", newBalance).Error; err != nil {
			return err
		}

		// Create transaction record
		msg := "Bonus publish berita terbaru"
		trx := models.Transaction{
			UserID:          user.ID,
			Amount:          req.Amount,
			Charge:          0,
			OrderID:         utils.GenerateOrderID(user.ID),
			TransactionFlow: "debit",
			TransactionType: "bonus",
			Message:         &msg,
			Status:          "Success",
		}

		if err := tx.Create(&trx).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal menambahkan saldo",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Reward berhasil ditambahkan",
		Data:    nil,
	})
}
