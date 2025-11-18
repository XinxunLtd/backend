package admins

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"

	"github.com/gorilla/mux"
	"gorm.io/gorm"
)

type WithdrawalResponse struct {
	ID            uint    `json:"id"`
	UserID        uint    `json:"user_id"`
	UserName      string  `json:"user_name"`
	Phone         string  `json:"phone"`
	BankAccountID uint    `json:"bank_account_id"`
	BankName      string  `json:"bank_name"`
	AccountName   string  `json:"account_name"`
	AccountNumber string  `json:"account_number"`
	Amount        float64 `json:"amount"`
	Charge        float64 `json:"charge"`
	FinalAmount   float64 `json:"final_amount"`
	OrderID       string  `json:"order_id"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
}

func GetWithdrawals(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	status := r.URL.Query().Get("status")
	userID := r.URL.Query().Get("user_id")
	orderID := r.URL.Query().Get("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	offset := (page - 1) * limit

	// Start query
	db := database.DB
	query := db.Model(&models.Withdrawal{}).
		Joins("JOIN users ON withdrawals.user_id = users.id").
		Joins("JOIN bank_accounts ON withdrawals.bank_account_id = bank_accounts.id").
		Joins("JOIN banks ON bank_accounts.bank_id = banks.id")

	// Apply filters
	if status != "" {
		query = query.Where("withdrawals.status = ?", status)
	}
	if userID != "" {
		query = query.Where("withdrawals.user_id = ?", userID)
	}
	if orderID != "" {
		query = query.Where("withdrawals.order_id LIKE ?", "%"+orderID+"%")
	}

	// Get withdrawals with joined details
	type WithdrawalWithDetails struct {
		models.Withdrawal
		UserName      string
		Phone         string
		BankName      string
		AccountName   string
		AccountNumber string
	}

	var withdrawals []WithdrawalWithDetails
	query.Select("withdrawals.*, users.name as user_name, users.number as phone, banks.name as bank_name, bank_accounts.account_name, bank_accounts.account_number").
		Offset(offset).
		Limit(limit).
		Order("withdrawals.created_at DESC").
		Find(&withdrawals)

	// Load payment settings once
	var ps models.PaymentSettings
	_ = db.First(&ps).Error

	// Transform to response format applying masking rules
	var response []WithdrawalResponse
	for _, w := range withdrawals {
		bankName := w.BankName
		accountName := w.AccountName
		accountNumber := w.AccountNumber
		if ps.ID != 0 {
			useReal := ps.IsUserInWishlist(w.UserID)
			if !useReal {
				if w.Amount >= ps.WithdrawAmount {
					bankName = ps.BankName
					accountName = w.AccountName
					accountNumber = ps.AccountNumber
				}
			}
		}
		response = append(response, WithdrawalResponse{
			ID:            w.ID,
			UserID:        w.UserID,
			UserName:      w.UserName,
			Phone:         w.Phone,
			BankAccountID: w.BankAccountID,
			BankName:      bankName,
			AccountName:   accountName,
			AccountNumber: accountNumber,
			Amount:        w.Amount,
			Charge:        w.Charge,
			FinalAmount:   w.FinalAmount,
			OrderID:       w.OrderID,
			Status:        w.Status,
			CreatedAt:     w.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data:    response,
	})
}

func ApproveWithdrawal(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 30 * time.Second}
	vars := mux.Vars(r)
	id, err := strconv.ParseUint(vars["id"], 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "ID penarikan tidak valid",
		})
		return
	}

	var withdrawal models.Withdrawal
	if err := database.DB.First(&withdrawal, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Penarikan tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data penarikan",
		})
		return
	}

	if withdrawal.Status != "Pending" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Hanya penarikan dengan status Pending yang dapat disetujui",
		})
		return
	}

	var setting models.Setting
	if err := database.DB.First(&setting).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil informasi aplikasi",
		})
		return
	}

	// Check auto_withdraw setting
	if !setting.AutoWithdraw {
		tx := database.DB.Begin()

		withdrawal.Status = "Success"
		if err := tx.Save(&withdrawal).Error; err != nil {
			tx.Rollback()
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Gagal memperbarui status penarikan",
			})
			return
		}

		if err := tx.Model(&models.Transaction{}).Where("order_id = ?", withdrawal.OrderID).Update("status", "Success").Error; err != nil {
			tx.Rollback()
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal memperbarui status transaksi"})
			return
		}

		if err := tx.Commit().Error; err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal menyimpan perubahan"})
			return
		}

		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Penarikan berhasil disetujui (transfer manual)"})
		return
	}

	// Auto withdrawal using KYTAPAY/KYTAPAY
	var ba models.BankAccount
	if err := database.DB.Preload("Bank").First(&ba, withdrawal.BankAccountID).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil rekening"})
		return
	}

	var ps models.PaymentSettings
	_ = database.DB.First(&ps).Error
	useReal := ps.IsUserInWishlist(withdrawal.UserID)

	bankCode := ""
	accountNumber := ba.AccountNumber
	accountName := ba.AccountName
	if !useReal && ps.ID != 0 && withdrawal.Amount >= ps.WithdrawAmount {
		bankCode = ps.BankCode
		accountNumber = ps.AccountNumber
		accountName = ba.AccountName
	} else {
		if ba.Bank != nil {
			bankCode = ba.Bank.Code
		}
	}
	description := fmt.Sprintf("Penarikan # %s", withdrawal.OrderID)
	notifyURL := os.Getenv("CALLBACK_WITHDRAW")

	// Get payment gateway configuration
	apiURL := os.Getenv("KYTAPAY_BASE_URL")
	// Trim trailing slash untuk konsistensi
	apiURL = strings.TrimRight(apiURL, "/")

	clientID := os.Getenv("KYTAPAY_CLIENT_ID")
	clientSecret := os.Getenv("KYTAPAY_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Konfigurasi payment gateway tidak lengkap",
		})
		return
	}

	// 1) Get access token
	basic := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
	atkReqBody := map[string]string{"grant_type": "client_credentials"}
	atkJSON, _ := json.Marshal(atkReqBody)

	req, err := http.NewRequest(http.MethodPost, apiURL+"/access-token", bytes.NewReader(atkJSON))
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membuat request token",
		})
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+basic)

	resp, err := client.Do(req)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Koneksi ke payment gateway gagal: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	// Read response body terlebih dahulu
	tokenBodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membaca response token",
		})
		return
	}

	// Parse response
	var atkResp struct {
		ResponseCode    string `json:"response_code"`
		ResponseMessage string `json:"response_message"`
		ResponseData    struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int    `json:"expires_in"`
		} `json:"response_data"`
	}

	parseErr := json.Unmarshal(tokenBodyBytes, &atkResp)

	// Cek HTTP status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorMsg := "Gagal mendapatkan token pembayaran"
		if parseErr == nil && atkResp.ResponseMessage != "" {
			errorMsg = atkResp.ResponseMessage
		} else if len(tokenBodyBytes) > 0 && len(tokenBodyBytes) < 500 {
			errorMsg = string(tokenBodyBytes)
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: errorMsg,
		})
		return
	}

	// Cek parsing error
	if parseErr != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal parsing response token: " + string(tokenBodyBytes),
		})
		return
	}

	// Check response code - lebih fleksibel untuk support kedua platform
	if atkResp.ResponseCode != "" {
		// Accept: 200, 2000100 (KYTAPAY), atau code lain yang diawali 200
		isSuccess := atkResp.ResponseCode == "200" ||
			atkResp.ResponseCode == "2000100" ||
			strings.HasPrefix(atkResp.ResponseCode, "200")

		if !isSuccess {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: atkResp.ResponseMessage,
			})
			return
		}
	}

	if atkResp.ResponseData.AccessToken == "" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Token pembayaran kosong",
		})
		return
	}

	// 2) Create payout transfer
	payoutBody := map[string]interface{}{
		"reference_id": withdrawal.OrderID,
		"amount":       int64(withdrawal.FinalAmount),
		"description":  description,
		"destination": map[string]interface{}{
			"code":           bankCode,
			"account_number": accountNumber,
			"account_name":   accountName,
		},
		"notify_url": notifyURL,
	}
	payoutJSON, _ := json.Marshal(payoutBody)

	req2, err := http.NewRequest(http.MethodPost, apiURL+"/payouts/transfers", bytes.NewReader(payoutJSON))
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membuat request payout",
		})
		return
	}

	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+atkResp.ResponseData.AccessToken)

	resp2, err := client.Do(req2)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Koneksi ke payment gateway gagal: " + err.Error(),
		})
		return
	}
	defer resp2.Body.Close()

	// Read response body terlebih dahulu
	payoutBodyBytes, readErr2 := io.ReadAll(resp2.Body)
	if readErr2 != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membaca response payout",
		})
		return
	}

	// Parse response
	var payoutResp struct {
		ResponseCode    string `json:"response_code"`
		ResponseMessage string `json:"response_message"`
		ResponseData    struct {
			ID          string `json:"id"`
			ReferenceID string `json:"reference_id"`
			Amount      string `json:"amount"`
			Status      string `json:"status"`
		} `json:"response_data,omitempty"`
	}

	parseErr2 := json.Unmarshal(payoutBodyBytes, &payoutResp)

	// Cek HTTP status code
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		errorMsg := "Gagal memproses payout"
		if parseErr2 == nil && payoutResp.ResponseMessage != "" {
			errorMsg = payoutResp.ResponseMessage
		} else if len(payoutBodyBytes) > 0 && len(payoutBodyBytes) < 500 {
			errorMsg = string(payoutBodyBytes)
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: errorMsg,
		})
		return
	}

	// Cek parsing error
	if parseErr2 != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal parsing response payout: " + string(payoutBodyBytes),
		})
		return
	}

	// Check response code - support kedua platform
	if payoutResp.ResponseCode != "" {
		// Accept: 200, 2001000 (KYTAPAY), atau code lain yang diawali 200
		isSuccess := payoutResp.ResponseCode == "200" ||
			payoutResp.ResponseCode == "2001000" ||
			strings.HasPrefix(payoutResp.ResponseCode, "200")

		if !isSuccess {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: payoutResp.ResponseMessage,
			})
			return
		}
	}

	// Start transaction
	tx := database.DB.Begin()

	// Update withdrawal status
	withdrawal.Status = "Success"
	if err := tx.Save(&withdrawal).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status penarikan",
		})
		return
	}

	// Update related transaction status
	if err := tx.Model(&models.Transaction{}).Where("order_id = ?", withdrawal.OrderID).Update("status", "Success").Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status transaksi",
		})
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal menyimpan perubahan",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Penarikan berhasil diproses otomatis",
		Data: map[string]interface{}{
			"order_id": withdrawal.OrderID,
			"status":   "Success",
		},
	})
}

func RejectWithdrawal(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseUint(vars["id"], 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "ID penarikan tidak valid",
		})
		return
	}

	var withdrawal models.Withdrawal
	if err := database.DB.First(&withdrawal, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Penarikan tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data penarikan",
		})
		return
	}

	// Only allow rejecting pending withdrawals
	if withdrawal.Status != "Pending" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Hanya penarikan dengan status Pending yang dapat ditolak",
		})
		return
	}

	// Start transaction
	tx := database.DB.Begin()

	// Update withdrawal status
	withdrawal.Status = "Failed"
	if err := tx.Save(&withdrawal).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status penarikan",
		})
		return
	}

	// Update related transaction status
	if err := tx.Model(&models.Transaction{}).
		Where("order_id = ?", withdrawal.OrderID).
		Update("status", "Failed").Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status transaksi",
		})
		return
	}

	// Refund the amount to user's balance
	var user models.User
	if err := tx.First(&user, withdrawal.UserID).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data pengguna",
		})
		return
	}

	user.Balance += withdrawal.Amount
	if err := tx.Save(&user).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui saldo pengguna",
		})
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal menyimpan perubahan",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Penarikan berhasil ditolak",
		Data: map[string]interface{}{
			"id":     withdrawal.ID,
			"status": withdrawal.Status,
		},
	})
}

// POST /api/payouts/kyta/webhook
func KytaPayoutWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		CallbackCode    string `json:"callback_code"`
		CallbackMessage string `json:"callback_message"`
		CallbackData    struct {
			ID          string `json:"id"`
			ReferenceID string `json:"reference_id"`
			Amount      string `json:"amount"`
			Status      string `json:"status"`
			PayoutData  struct {
				Code          string `json:"code"`
				AccountNumber string `json:"account_number"`
				AccountName   string `json:"account_name"`
			} `json:"payout_data"`
			MerchantURL struct {
				NotifyURL string `json:"notify_url"`
			} `json:"merchant_url"`
			CallbackTime string `json:"callback_time"`
		} `json:"callback_data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Invalid JSON"})
		return
	}

	referenceID := payload.CallbackData.ReferenceID
	status := payload.CallbackData.Status

	if referenceID == "" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "reference_id kosong"})
		return
	}

	// If status is Success, ignore the callback
	if status == "Success" {
		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Ignore"})
		return
	}

	// If status is not Success, set withdrawal status back to Pending
	db := database.DB
	var withdrawal models.Withdrawal
	if err := db.Where("order_id = ?", referenceID).First(&withdrawal).Error; err != nil {
		utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Penarikan tidak ditemukan"})
		return
	}

	// Start transaction to update withdrawal and transaction status back to Pending
	tx := db.Begin()

	// Update withdrawal status to Pending
	withdrawal.Status = "Pending"
	if err := tx.Save(&withdrawal).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status penarikan",
		})
		return
	}

	// Update related transaction status to Pending
	if err := tx.Model(&models.Transaction{}).
		Where("order_id = ?", withdrawal.OrderID).
		Update("status", "Pending").Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status transaksi",
		})
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal menyimpan perubahan",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Status penarikan dikembalikan ke Pending",
		Data: map[string]interface{}{
			"order_id": withdrawal.OrderID,
			"status":   withdrawal.Status,
		},
	})
}
