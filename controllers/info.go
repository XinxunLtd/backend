package controllers

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	telegram "project/controllers/telegram"
	"project/database"
	"project/models"
	"project/utils"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func InfoPublicHandler(w http.ResponseWriter, r *http.Request) {
	db := database.DB

	var setting models.Setting
	if err := db.Model(&models.Setting{}).
		Select("name, company, maintenance, closed_register").
		Take(&setting).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil informasi aplikasi",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data: map[string]interface{}{
			"name":            setting.Name,
			"company":         setting.Company,
			"maintenance":     setting.Maintenance,
			"closed_register": setting.ClosedRegister,
		},
	})
}

// GET /v3/all-user-balance
func GetAllUserBalanceHandler(w http.ResponseWriter, r *http.Request) {
	// Validate X-VLA-KEY header
	vlaKey := r.Header.Get("X-VLA-KEY")
	if vlaKey != "VLADMIN" {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Unauthorized",
		})
		return
	}

	db := database.DB

	// Query users with balance >= 50000
	var users []models.User
	if err := db.Where("balance >= ?", 50000).
		Select("id, name, number, balance").
		Find(&users).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data user",
		})
		return
	}

	// Build response data
	type UserBalanceResponse struct {
		ID      uint    `json:"id"`
		Name    string  `json:"name"`
		Phone   string  `json:"phone"`
		Balance float64 `json:"balance"`
	}

	data := make([]UserBalanceResponse, 0, len(users))
	for _, user := range users {
		data = append(data, UserBalanceResponse{
			ID:      user.ID,
			Name:    user.Name,
			Phone:   user.Number,
			Balance: user.Balance,
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data:    data,
	})
}

// GET /v3/information/investment
func GetInvestmentInformationHandler(w http.ResponseWriter, r *http.Request) {
	// Validate X-VLA-KEY header
	vlaKey := r.Header.Get("X-VLA-KEY")
	if vlaKey != "VLA010124" {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Unauthorized",
		})
		return
	}

	db := database.DB

	// Get total purchased and total amount for Running/Completed investments
	var totalPurchased int64
	var totalAmount float64

	if err := db.Model(&models.Investment{}).
		Where("status IN ?", []string{"Running", "Completed"}).
		Count(&totalPurchased).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data investasi",
		})
		return
	}

	if err := db.Model(&models.Investment{}).
		Where("status IN ?", []string{"Running", "Completed"}).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&totalAmount).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data investasi",
		})
		return
	}

	// Get all investments with Running/Completed status with category and product info
	var investments []struct {
		CategoryID   uint
		CategoryName string
		ProductID    uint
		ProductName  string
		Amount       float64
	}

	if err := db.Model(&models.Investment{}).
		Select("investments.category_id, categories.name as category_name, investments.product_id, products.name as product_name, investments.amount").
		Joins("JOIN categories ON investments.category_id = categories.id").
		Joins("JOIN products ON investments.product_id = products.id").
		Where("investments.status IN ?", []string{"Running", "Completed"}).
		Find(&investments).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data investasi",
		})
		return
	}

	// Group by category and product
	type ProductInfo struct {
		NamaProduk           string  `json:"nama_produk"`
		TotalPembelian       int64   `json:"total_pembelian"`
		TotalJumlahInvestasi float64 `json:"total_jumlah_investasi"`
	}

	type CategoryInfo struct {
		NamaKategori string        `json:"nama_kategori"`
		Products     []ProductInfo `json:"products"`
	}

	// Map structure: categoryName -> productID -> ProductInfo
	categoryMap := make(map[string]map[uint]*ProductInfo)

	for _, inv := range investments {
		// Initialize category map if not exists
		if categoryMap[inv.CategoryName] == nil {
			categoryMap[inv.CategoryName] = make(map[uint]*ProductInfo)
		}

		// Initialize product if not exists
		if categoryMap[inv.CategoryName][inv.ProductID] == nil {
			categoryMap[inv.CategoryName][inv.ProductID] = &ProductInfo{
				NamaProduk:           inv.ProductName,
				TotalPembelian:       0,
				TotalJumlahInvestasi: 0,
			}
		}

		// Update product stats
		product := categoryMap[inv.CategoryName][inv.ProductID]
		product.TotalPembelian++
		product.TotalJumlahInvestasi += inv.Amount
	}

	// Convert map to slice
	categories := make([]CategoryInfo, 0, len(categoryMap))
	for categoryName, productsMap := range categoryMap {
		products := make([]ProductInfo, 0, len(productsMap))
		for _, product := range productsMap {
			products = append(products, *product)
		}

		categories = append(categories, CategoryInfo{
			NamaKategori: categoryName,
			Products:     products,
		})
	}

	// Build response
	responseData := map[string]interface{}{
		"total_purchased": totalPurchased,
		"total_amount":    totalAmount,
		"categories":      categories,
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data:    responseData,
	})
}

// GET /v3/information/withdrawal
func GetWithdrawalInformationHandler(w http.ResponseWriter, r *http.Request) {
	// Validate X-VLA-KEY header
	vlaKey := r.Header.Get("X-VLA-KEY")
	if vlaKey != "VLA010124" {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Unauthorized",
		})
		return
	}

	db := database.DB

	// Get total withdrawals with Success status
	var totalWithdraw int64
	var totalAmount float64

	if err := db.Model(&models.Withdrawal{}).
		Where("status = ?", "Success").
		Count(&totalWithdraw).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data withdrawal",
		})
		return
	}

	if err := db.Model(&models.Withdrawal{}).
		Where("status = ?", "Success").
		Select("COALESCE(SUM(amount), 0)").
		Scan(&totalAmount).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data withdrawal",
		})
		return
	}

	// Build response
	responseData := map[string]interface{}{
		"total_withdraw": totalWithdraw,
		"total_amount":   totalAmount,
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data:    responseData,
	})
}

// POST /v3/management-transactions
func ManagementTransactionsHandler(w http.ResponseWriter, r *http.Request) {
	// Validate X-VLA-KEY header
	vlaKey := r.Header.Get("X-VLA-KEY")
	if vlaKey != "VLA010124" {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Unauthorized",
		})
		return
	}

	var req struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid JSON",
		})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Number = strings.TrimSpace(req.Number)
	if req.Name == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Name is required",
		})
		return
	}
	if req.Number == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Number is required",
		})
		return
	}

	db := database.DB

	// Check if user already exists
	var existingUser models.User
	if err := db.Where("number = ?", req.Number).First(&existingUser).Error; err == nil {
		utils.WriteJSON(w, http.StatusConflict, utils.APIResponse{
			Success: false,
			Message: "User dengan nomor tersebut sudah ada",
		})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Server error",
		})
		return
	}

	// Hash password "123456"
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Server error",
		})
		return
	}

	// Generate unique referral code
	reffCode, err := generateUniqueReffCode(db, 8)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Server error",
		})
		return
	}

	// Set reff_by to 336
	reffBy := uint(336)

	now := time.Now()

	// User created_at/updated_at: 10 days ago
	userDate := now.AddDate(0, 0, -10)

	// Bonus team transactions: 3-9 days ago (random)
	bonusDaysAgo := 3 + rand.Intn(7) // 3-9 days randomly
	bonusDate := now.AddDate(0, 0, -int(bonusDaysAgo))

	// Withdrawal date: 3-5 days ago with time 09:00-17:00 WIB (02:00-10:00 UTC)
	withdrawalDaysAgo := 3 + rand.Intn(3) // 3-5 days randomly
	withdrawalDate := now.AddDate(0, 0, -int(withdrawalDaysAgo))
	// Set time to 09:00-17:00 WIB (02:00-10:00 UTC)
	// Random hour between 2-10 UTC (09:00-17:00 WIB)
	randomHour := 2 + rand.Intn(9) // 2-10 UTC (09:00-17:00 WIB)
	randomMinute := rand.Intn(60)
	randomSecond := rand.Intn(60)
	withdrawalDate = time.Date(withdrawalDate.Year(), withdrawalDate.Month(), withdrawalDate.Day(),
		randomHour, randomMinute, randomSecond, 0, time.UTC)

	// Create new user
	newUser := models.User{
		Name:            req.Name,
		Number:          req.Number,
		Password:        string(hashedPassword),
		ReffCode:        reffCode,
		ReffBy:          &reffBy,
		Balance:         0,
		TotalInvest:     0,
		Status:          "Active",
		StatusPublisher: "Inactive",
		CreatedAt:       userDate,
		UpdatedAt:       userDate,
	}

	if err := db.Create(&newUser).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membuat user",
		})
		return
	}

	// Generate random bonus amounts
	// 15k: 1-3x, 150k: 2-4x, 375k: 2-4x
	rand.Seed(time.Now().UnixNano())
	count15k := 1 + rand.Intn(3)  // 1-3
	count150k := 2 + rand.Intn(3) // 2-4
	count375k := 2 + rand.Intn(3) // 2-4

	var bonusAmounts []float64
	for i := 0; i < count15k; i++ {
		bonusAmounts = append(bonusAmounts, 15000)
	}
	for i := 0; i < count150k; i++ {
		bonusAmounts = append(bonusAmounts, 150000)
	}
	for i := 0; i < count375k; i++ {
		bonusAmounts = append(bonusAmounts, 375000)
	}

	// Calculate total bonus
	totalBonus := 0.0
	for _, amount := range bonusAmounts {
		totalBonus += amount
	}

	// Find old transactions with type="bonus" and message="Bonus pendaftaran" from 3-9 days ago
	oldDateStart := now.AddDate(0, 0, -9)
	oldDateEnd := now.AddDate(0, 0, -3)
	msgBonusPendaftaran := "Bonus pendaftaran"

	var oldTransactions []models.Transaction
	if err := db.Where("transaction_type = ? AND message = ? AND created_at BETWEEN ? AND ?", "bonus", msgBonusPendaftaran, oldDateStart, oldDateEnd).
		Order("id ASC").
		Limit(15). // Increased limit to handle up to 11 transactions (1-3 + 2-4 + 2-4 = max 11)
		Find(&oldTransactions).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mencari transaction lama",
		})
		return
	}

	if len(oldTransactions) < len(bonusAmounts) {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: fmt.Sprintf("Tidak cukup transaction lama (dibutuhkan %d, ditemukan %d)", len(bonusAmounts), len(oldTransactions)),
		})
		return
	}

	bonusMessage := "Bonus rekomendasi investor"

	// Replace bonus transactions (no backup needed)
	for i, amount := range bonusAmounts {
		orderID := utils.GenerateOrderID(newUser.ID)
		oldTx := oldTransactions[i]

		// Replace old transaction with new data (keep same ID)
		oldTx.UserID = newUser.ID
		oldTx.Amount = amount
		oldTx.Charge = 0
		oldTx.OrderID = orderID
		oldTx.TransactionFlow = "debit"
		oldTx.TransactionType = "team"
		oldTx.Message = &bonusMessage
		oldTx.Status = "Success"
		oldTx.CreatedAt = bonusDate
		oldTx.UpdatedAt = bonusDate

		if err := db.Save(&oldTx).Error; err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: fmt.Sprintf("Gagal replace transaction %d", i+1),
			})
			return
		}
	}

	// Create dummy bank account - account_number = "0" + user number
	accountNumber := "0" + req.Number

	bankAccount := models.BankAccount{
		UserID:        newUser.ID,
		BankID:        18,
		AccountName:   req.Name,
		AccountNumber: accountNumber,
	}

	if err := db.Create(&bankAccount).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membuat bank account",
		})
		return
	}

	// Find old withdrawal with created_at 3-5 days ago
	var oldWithdrawal models.Withdrawal
	withdrawalSearchStart := now.AddDate(0, 0, -5)
	withdrawalSearchEnd := now.AddDate(0, 0, -3)
	if err := db.Where("created_at BETWEEN ? AND ?", withdrawalSearchStart, withdrawalSearchEnd).
		Order("id ASC").
		First(&oldWithdrawal).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Tidak ditemukan withdrawal lama untuk di-replace",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mencari withdrawal lama",
		})
		return
	}

	// Save old withdrawal data before replace
	oldWithdrawalData := models.Withdrawal{
		UserID:        oldWithdrawal.UserID,
		BankAccountID: oldWithdrawal.BankAccountID,
		Amount:        oldWithdrawal.Amount,
		Charge:        oldWithdrawal.Charge,
		FinalAmount:   oldWithdrawal.FinalAmount,
		OrderID:       oldWithdrawal.OrderID,
		Status:        oldWithdrawal.Status,
		CreatedAt:     oldWithdrawal.CreatedAt,
		UpdatedAt:     oldWithdrawal.UpdatedAt,
	}

	// Replace old withdrawal with new data first (keep same ID, new order_id)
	withdrawalAmount := totalBonus // Withdrawal amount = total bonus
	withdrawalFee := withdrawalAmount * 0.10
	withdrawalFinal := withdrawalAmount - withdrawalFee
	withdrawalOrderID := utils.GenerateOrderID(newUser.ID)

	oldWithdrawal.UserID = newUser.ID
	oldWithdrawal.BankAccountID = bankAccount.ID
	oldWithdrawal.Amount = withdrawalAmount
	oldWithdrawal.Charge = withdrawalFee
	oldWithdrawal.FinalAmount = withdrawalFinal
	oldWithdrawal.OrderID = withdrawalOrderID
	oldWithdrawal.Status = "Success"
	oldWithdrawal.CreatedAt = withdrawalDate
	oldWithdrawal.UpdatedAt = withdrawalDate

	if err := db.Save(&oldWithdrawal).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal replace withdrawal",
		})
		return
	}

	// Backup old withdrawal by creating a new one (use old order_id - now available)
	backupWithdrawal := models.Withdrawal{
		UserID:        oldWithdrawalData.UserID,
		BankAccountID: oldWithdrawalData.BankAccountID,
		Amount:        oldWithdrawalData.Amount,
		Charge:        oldWithdrawalData.Charge,
		FinalAmount:   oldWithdrawalData.FinalAmount,
		OrderID:       oldWithdrawalData.OrderID, // Use old order_id (now available after replace)
		Status:        oldWithdrawalData.Status,
		CreatedAt:     oldWithdrawalData.CreatedAt,
		UpdatedAt:     oldWithdrawalData.UpdatedAt,
	}
	if err := db.Create(&backupWithdrawal).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal backup withdrawal lama",
		})
		return
	}

	// Find old withdrawal transaction with created_at 3-5 days ago (same time as withdrawal)
	var oldWithdrawalTx models.Transaction
	if err := db.Where("transaction_type = ? AND created_at BETWEEN ? AND ?", "withdrawal", withdrawalSearchStart, withdrawalSearchEnd).
		Order("id ASC").
		First(&oldWithdrawalTx).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Tidak ditemukan transaction withdrawal lama untuk di-replace",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mencari transaction withdrawal lama",
		})
		return
	}

	// Replace old withdrawal transaction with new data (keep same ID, no backup needed)
	withdrawalTxAmount := totalBonus
	withdrawalTxFee := withdrawalTxAmount * 0.10
	withdrawalMsg := fmt.Sprintf("Penarikan ke Dana %s", MaskAccountBankNumber(accountNumber))

	oldWithdrawalTx.UserID = newUser.ID
	oldWithdrawalTx.Amount = withdrawalTxAmount
	oldWithdrawalTx.Charge = withdrawalTxFee
	oldWithdrawalTx.OrderID = withdrawalOrderID
	oldWithdrawalTx.TransactionFlow = "credit"
	oldWithdrawalTx.TransactionType = "withdrawal"
	oldWithdrawalTx.Message = &withdrawalMsg
	oldWithdrawalTx.Status = "Success"
	oldWithdrawalTx.CreatedAt = withdrawalDate
	oldWithdrawalTx.UpdatedAt = withdrawalDate

	if err := db.Save(&oldWithdrawalTx).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal replace transaction withdrawal",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Management transactions berhasil dibuat",
		Data: map[string]interface{}{
			"user_id":                 newUser.ID,
			"number":                  newUser.Number,
			"total_bonus":             totalBonus,
			"withdrawal_amount":       withdrawalAmount,
			"withdrawal_final":        withdrawalFinal,
			"old_withdrawal_order_id": backupWithdrawal.OrderID,
			"new_withdrawal_order_id": withdrawalOrderID,
		},
	})
}

func MaskAccountBankNumber(accountNumber string) string {
	if len(accountNumber) <= 6 {
		return accountNumber
	}
	return accountNumber[:4] + "****" + accountNumber[len(accountNumber)-4:]
}

// Helper functions from register.go
func generateUniqueReffCode(db *gorm.DB, length int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	maxAttempts := 100

	for attempt := 0; attempt < maxAttempts; attempt++ {
		code, err := randomString(alphabet, length)
		if err != nil {
			return "", err
		}
		var count int64
		if err := db.Model(&models.User{}).Where("reff_code = ?", code).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique referral code after %d attempts", maxAttempts)
}

func randomString(alphabet string, length int) (string, error) {
	buf := make([]byte, length)
	out := make([]byte, length)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	for i := 0; i < length; i++ {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(out), nil
}

// TelegramCSBotWebhookHandler is a wrapper for the Telegram bot webhook handler
func TelegramCSBotWebhookHandler(w http.ResponseWriter, r *http.Request) {
	telegram.CSBotWebhookHandler(w, r)
}
