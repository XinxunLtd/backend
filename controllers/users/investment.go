package users

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type KytaAccessTokenResponse struct {
	ResponseCode    string `json:"response_code"`
	ResponseMessage string `json:"response_message"`
	ResponseData    struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		RequestTime string `json:"request_time"`
	} `json:"response_data"`
}

type KytaPaymentResponse struct {
	ResponseCode    string `json:"response_code"`
	ResponseMessage string `json:"response_message"`
	ResponseData    struct {
		ID          string `json:"id"`
		ReferenceID string `json:"reference_id"`
		Amount      int64  `json:"amount"`
		PaymentData struct {
			QRString      string `json:"qr_string,omitempty"`
			BankCode      string `json:"bank_code,omitempty"`
			AccountNumber string `json:"account_number,omitempty"`
			AccountName   string `json:"account_name,omitempty"`
		} `json:"payment_data"`
		MerchantURL struct {
			NotifyURL  string `json:"notify_url"`
			SuccessURL string `json:"success_url"`
			FailedURL  string `json:"failed_url"`
		} `json:"merchant_url"`
		CheckoutURL string `json:"checkout_url"`
		ExpiresAt   string `json:"expires_at"`
		RequestTime string `json:"request_time"`
	} `json:"response_data"`
}

type CreateInvestmentRequest struct {
	ProductID      uint   `json:"product_id"`
	PaymentMethod  string `json:"payment_method"`
	PaymentChannel string `json:"payment_channel"`
}

// GET /api/users/investment/active
func GetActiveInvestmentsHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}
	db := database.DB

	// Get active categories (prioritize category ID 1)
	var categories []models.Category
	if err := db.Where("status = ?", "Active").Order("CASE WHEN id = 1 THEN 0 ELSE id END ASC").Find(&categories).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil kategori"})
		return
	}

	var investments []models.Investment
	if err := db.Preload("Category").Where("user_id = ? AND status IN ?", uid, []string{"Running", "Completed", "Suspended"}).Order("CASE WHEN category_id = 1 THEN 0 ELSE category_id END ASC, product_id ASC, id DESC").Find(&investments).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil investasi"})
		return
	}

	// Group investments by category name
	categoryMap := make(map[string][]map[string]interface{})
	for _, inv := range investments {
		var product models.Product
		if err := db.Preload("Category").Where("id = ?", inv.ProductID).First(&product).Error; err != nil {
			continue
		}

		catName := ""
		if inv.Category != nil {
			catName = inv.Category.Name
		}

		// Prepare product category info
		var productCategory map[string]interface{}
		if product.Category != nil {
			productCategory = map[string]interface{}{
				"id":          product.Category.ID,
				"name":        product.Category.Name,
				"status":      product.Category.Status,
				"profit_type": product.Category.ProfitType,
			}
		}

		m := map[string]interface{}{
			"id":               inv.ID,
			"user_id":          inv.UserID,
			"product_id":       inv.ProductID,
			"product_name":     product.Name,
			"product_category": productCategory,
			"category_id":      inv.CategoryID,
			"category_name":    catName,
			"amount":           int64(inv.Amount),
			"duration":         inv.Duration,
			"daily_profit":     int64(inv.DailyProfit),
			"total_paid":       inv.TotalPaid,
			"total_returned":   int64(inv.TotalReturned),
			"last_return_at":   inv.LastReturnAt,
			"next_return_at":   inv.NextReturnAt,
			"order_id":         inv.OrderID,
			"status":           inv.Status,
		}
		categoryMap[catName] = append(categoryMap[catName], m)
	}

	// Ensure all categories exist in response
	resp := make(map[string]interface{})
	for _, cat := range categories {
		if invs, ok := categoryMap[cat.Name]; ok {
			resp[cat.Name] = invs
		} else {
			resp[cat.Name] = []map[string]interface{}{}
		}
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: resp})
}

// POST /api/users/investments - FIXED VERSION
func CreateInvestmentHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateInvestmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Not valid JSON"})
		return
	}

	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	method := strings.ToUpper(strings.TrimSpace(req.PaymentMethod))
	channel := strings.ToUpper(strings.TrimSpace(req.PaymentChannel))
	if method != "QRIS" && method != "BANK" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Silahkan pilih metode pembayaran"})
		return
	}
	if method == "BANK" {
		allowed := map[string]struct{}{"BCA": {}, "BRI": {}, "BNI": {}, "MANDIRI": {}, "PERMATA": {}, "BNC": {}}
		if _, ok := allowed[channel]; !ok {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Bank tidak valid"})
			return
		}
	}

	db := database.DB
	var product models.Product
	if err := db.Preload("Category").Where("id = ? AND status = 'Active'", req.ProductID).First(&product).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Produk tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan, coba lagi"})
		return
	}

	if product.Category == nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Kategori produk tidak valid"})
		return
	}

	var user models.User
	if err := db.Select("level").Where("id = ?", uid).First(&user).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan, coba lagi"})
		return
	}

	userLevel := uint(0)
	if user.Level != nil {
		userLevel = *user.Level
	}

	if userLevel < uint(product.RequiredVIP) {
		msg := fmt.Sprintf("Produk %s memerlukan VIP level %d. Level VIP Anda saat ini: %d", product.Name, product.RequiredVIP, userLevel)
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: msg})
		return
	}

	if product.PurchaseLimit > 0 {
		var purchaseCount int64
		if err := db.Model(&models.Investment{}).
			Where("user_id = ? AND product_id = ? AND status IN ?", uid, product.ID, []string{"Running", "Completed", "Suspended"}).
			Count(&purchaseCount).Error; err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan, coba lagi"})
			return
		}
		if purchaseCount >= int64(product.PurchaseLimit) {
			msg := fmt.Sprintf("Anda telah mencapai batas pembelian untuk produk %s (maksimal %dx)", product.Name, product.PurchaseLimit)
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: msg})
			return
		}
	}

	kytapayBase := os.Getenv("KYTAPAY_BASE_URL")
	if kytapayBase == "" {
		kytapayBase = "https://api.kytapay.com/v2"
	}
	kytapayClientID := os.Getenv("KYTAPAY_CLIENT_ID")
	kytapayClientSecret := os.Getenv("KYTAPAY_CLIENT_SECRET")
	notifyURL := os.Getenv("NOTIFY_URL")
	successURL := os.Getenv("SUCCESS_URL")
	failedURL := os.Getenv("FAILED_URL")

	if kytapayClientID == "" || kytapayClientSecret == "" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Server error"})
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	orderID := utils.GenerateOrderID(uid)
	referenceID := orderID

	accessToken, _, err := getKytaAccessTokenSafe(r.Context(), httpClient, kytapayBase, kytapayClientID, kytapayClientSecret)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Terjadi kesalahan saat memanggil layanan pembayaran"})
		return
	}

	amount := product.Amount

	if method == "QRIS" && amount > 10000000 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Jumlah pembayaran maksimal menggunakan QRIS adalah Rp 10.000.000, Silahkan gunakan metode pembayaran lain"})
		return
	}

	if method == "BANK" && amount < 10000 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Jumlah pembayaran minimal menggunakan BANK adalah Rp 10.000, Silahkan gunakan metode pembayaran lain"})
		return
	}

	var payResp *KytaPaymentResponse
	if method == "QRIS" {
		payResp, _, err = createKytaQRISSafe(r.Context(), httpClient, kytapayBase, accessToken, referenceID, int64(amount), notifyURL, successURL, failedURL)
	} else {
		payResp, _, err = createKytaVASafe(r.Context(), httpClient, kytapayBase, accessToken, referenceID, int64(amount), channel, notifyURL, successURL, failedURL)
	}

	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Terjadi kesalahan saat memanggil layanan pembayaran"})
		return
	}
	if payResp == nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mendapatkan jawaban dari layanan pembayaran"})
		return
	}

	daily := product.DailyProfit

	inv := models.Investment{
		UserID:        uid,
		ProductID:     product.ID,
		CategoryID:    product.CategoryID,
		Amount:        amount,
		DailyProfit:   daily,
		Duration:      product.Duration,
		TotalPaid:     0,
		TotalReturned: 0,
		OrderID:       orderID,
		Status:        "Pending",
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}

		var paymentCode *string
		var expiredAt *time.Time

		methodToSave := strings.ToUpper(method)

		if method == "QRIS" {
			if qr := strings.TrimSpace(payResp.ResponseData.PaymentData.QRString); qr != "" {
				paymentCode = &qr
			}
		} else {
			if accNum := strings.TrimSpace(payResp.ResponseData.PaymentData.AccountNumber); accNum != "" {
				paymentCode = &accNum
			}
		}

		if expiredStr := strings.TrimSpace(payResp.ResponseData.ExpiresAt); expiredStr != "" {
			if t, err := parseTimeFlexible(expiredStr); err == nil {
				tt := t.UTC()
				expiredAt = &tt
			} else {
				t := time.Now().Add(15 * time.Minute)
				expiredAt = &t
			}
		} else {
			t := time.Now().Add(15 * time.Minute)
			expiredAt = &t
		}

		payment := models.Payment{
			InvestmentID: inv.ID,
			ReferenceID: func() *string {
				x := referenceID
				return &x
			}(),
			OrderID:       inv.OrderID,
			PaymentMethod: &methodToSave,
			PaymentChannel: func() *string {
				if methodToSave == "BANK" {
					return &channel
				}
				return nil
			}(),
			PaymentCode: paymentCode,
			PaymentLink: func() *string {
				if url := strings.TrimSpace(payResp.ResponseData.CheckoutURL); url != "" {
					return &url
				}
				return nil
			}(),
			Status:    "Pending",
			ExpiredAt: expiredAt,
		}

		if err := tx.Create(&payment).Error; err != nil {
			return err
		}

		if paymentCode != nil && *paymentCode != "" {
			if err := tx.Model(&models.Payment{}).Where("id = ?", payment.ID).Update("payment_code", *paymentCode).Error; err != nil {
				return err
			}
		}

		msg := fmt.Sprintf("Investasi %s", product.Name)
		trx := models.Transaction{
			UserID:          uid,
			Amount:          inv.Amount,
			Charge:          0,
			OrderID:         inv.OrderID,
			TransactionFlow: "credit",
			TransactionType: "investment",
			Message:         &msg,
			Status:          "Pending",
		}
		if err := tx.Create(&trx).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal membuat investasi"})
		return
	}

	resp := map[string]interface{}{
		"order_id":     inv.OrderID,
		"amount":       inv.Amount,
		"product":      product.Name,
		"category":     product.Category.Name,
		"category_id":  product.CategoryID,
		"duration":     product.Duration,
		"daily_profit": daily,
		"status":       inv.Status,
	}
	utils.WriteJSON(w, http.StatusCreated, utils.APIResponse{Success: true, Message: "Pembelian berhasil, silakan lakukan pembayaran", Data: resp})
}

// GET /api/users/investments
func ListInvestmentsHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	// Get query parameters
	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")
	searchQuery := strings.TrimSpace(r.URL.Query().Get("search"))

	// Parse pagination with defaults
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(limitStr)
	if limit < 1 {
		limit = 10
	}

	db := database.DB

	// Build base query for counting
	countQuery := db.Model(&models.Investment{}).Where("user_id = ?", uid)
	if searchQuery != "" {
		countQuery = countQuery.Where("order_id LIKE ?", "%"+searchQuery+"%")
	}

	// Count total rows
	var totalRows int64
	if err := countQuery.Count(&totalRows).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan"})
		return
	}

	// Calculate pagination
	totalPages := int(math.Ceil(float64(totalRows) / float64(limit)))
	offset := (page - 1) * limit

	// Build query for fetching data
	var rows []models.Investment
	query := db.Where("user_id = ?", uid)
	if searchQuery != "" {
		query = query.Where("order_id LIKE ?", "%"+searchQuery+"%")
	}
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan"})
		return
	}

	// Build response with pagination
	responseData := map[string]interface{}{
		"data": rows,
		"pagination": map[string]interface{}{
			"page":       page,
			"limit":      limit,
			"total_rows": totalRows,
			"total_pages": totalPages,
		},
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: responseData})
}

// GET /api/users/investments/{id}
func GetInvestmentHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var idStr string
	if len(parts) >= 4 {
		idStr = parts[3]
	}
	id64, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id64 == 0 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID tidak valid"})
		return
	}
	db := database.DB
	var row models.Investment
	if err := db.Where("id = ? AND user_id = ?", uint(id64), uid).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Data tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan"})
		return
	}
	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: row})
}

// GET /api/users/payment/{order_id}
func GetPaymentDetailsHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var orderID string
	if len(parts) >= 3 {
		orderID = parts[len(parts)-1]
	}

	db := database.DB
	var payment models.Payment
	if err := db.Where("order_id = ?", orderID).First(&payment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Data pembayaran tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan"})
		return
	}

	var inv models.Investment
	if err := db.Where("id = ?", payment.InvestmentID).First(&inv).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan mengambil data investasi"})
		return
	}
	var product models.Product
	if err := db.Select("name").Where("id = ?", inv.ProductID).First(&product).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan mengambil data produk"})
		return
	}
	resp := map[string]interface{}{
		"product":  product.Name,
		"order_id": payment.OrderID,
		"amount":   inv.Amount,
		"payment_code": func() interface{} {
			if payment.PaymentCode == nil {
				return nil
			}
			return *payment.PaymentCode
		}(),
		"payment_channel": func() interface{} {
			if payment.PaymentChannel == nil {
				return nil
			}
			return *payment.PaymentChannel
		}(),
		"payment_method": func() interface{} {
			if payment.PaymentMethod == nil {
				return nil
			}
			return *payment.PaymentMethod
		}(),
		"expired_at": func() interface{} {
			if payment.ExpiredAt == nil {
				return nil
			}
			return payment.ExpiredAt.UTC().Format(time.RFC3339)
		}(),
		"status": payment.Status,
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: resp})
}

// POST /api/payments/kyta/webhook
func KytaWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		CallbackCode    string `json:"callback_code"`
		CallbackMessage string `json:"callback_message"`
		CallbackData    struct {
			ID          string `json:"id"`
			ReferenceID string `json:"reference_id"`
			Amount      int64 `json:"amount"`
			Status      string `json:"status"`
			PaymentType string `json:"payment_type"`
			PaymentData struct {
				QRString      string `json:"qr_string,omitempty"`
				BankCode      string `json:"bank_code,omitempty"`
				AccountNumber string `json:"account_number,omitempty"`
				AccountName   string `json:"account_name,omitempty"`
			} `json:"payment_data"`
			MerchantURL struct {
				NotifyURL  string `json:"notify_url"`
				SuccessURL string `json:"success_url"`
				FailedURL  string `json:"failed_url"`
			} `json:"merchant_url"`
			CallbackTime string `json:"callback_time"`
		} `json:"callback_data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Invalid JSON"})
		return
	}

	referenceID := strings.TrimSpace(payload.CallbackData.ReferenceID)
	status := strings.ToUpper(strings.TrimSpace(payload.CallbackData.Status))
	paymentID := strings.TrimSpace(payload.CallbackData.ID)

	if referenceID == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "reference_id kosong"})
		return
	}

	success := status == "SUCCESS" || status == "PAID" || status == "COMPLETED"

	db := database.DB

	var payment models.Payment
	if err := db.Where("order_id = ?", referenceID).First(&payment).Error; err != nil {
		utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Pembayaran tidak ditemukan"})
		return
	}

	paymentUpdates := map[string]interface{}{}
	if paymentID != "" {
		paymentUpdates["reference_id"] = paymentID
	}
	if success {
		paymentUpdates["status"] = "Success"
	} else {
		paymentUpdates["status"] = "Failed"
	}
	if len(paymentUpdates) > 0 {
		_ = db.Model(&payment).Updates(paymentUpdates).Error
	}

	var inv models.Investment
	if err := db.Where("id = ?", payment.InvestmentID).First(&inv).Error; err != nil {
		utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Investasi tidak ditemukan"})
		return
	}

	if inv.Status != "Pending" {
		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Ignored"})
		return
	}

	if success {
		now := time.Now()
		next := now.Add(24 * time.Hour)
		_ = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.Transaction{}).Where("order_id = ?", inv.OrderID).Updates(map[string]interface{}{"status": "Success"}).Error; err != nil {
				return err
			}
			updates := map[string]interface{}{"status": "Running", "last_return_at": nil, "next_return_at": next}
			if err := tx.Model(&inv).Updates(updates).Error; err != nil {
				return err
			}

			// Get category info to determine if this is Monitor (locked profit)
			var category models.Category
			isMonitor := false
			if err := tx.Where("id = ?", inv.CategoryID).First(&category).Error; err == nil {
				if category.ProfitType == "locked" {
					isMonitor = true
				}
			}

			// Update user total_invest and total_invest_vip
			userUpdates := map[string]interface{}{
				"total_invest":      gorm.Expr("total_invest + ?", inv.Amount),
				"investment_status": "Active",
			}
			if isMonitor {
				userUpdates["total_invest_vip"] = gorm.Expr("total_invest_vip + ?", inv.Amount)
			}
			if err := tx.Model(&models.User{}).Where("id = ?", inv.UserID).Updates(userUpdates).Error; err != nil {
				return err
			}

			// Calculate VIP level based on total_invest_vip for locked categories
			if isMonitor {
				var user models.User
				if err := tx.Model(&models.User{}).Select("total_invest_vip").Where("id = ?", inv.UserID).First(&user).Error; err == nil {
					newLevel := calculateVIPLevel(user.TotalInvestVIP)
					if err := tx.Model(&models.User{}).Where("id = ?", inv.UserID).Update("level", newLevel).Error; err != nil {
						return err
					}
				}
			}

			// Bonus rekomendasi investor hanya untuk level 1: 30% dari amount
			var user models.User
			if err := tx.Select("id, reff_by").Where("id = ?", inv.UserID).First(&user).Error; err == nil && user.ReffBy != nil {
				var level1 models.User
				if err := tx.Select("id, spin_ticket").Where("id = ?", *user.ReffBy).First(&level1).Error; err == nil {
					// Give spin ticket if investment >= 100k
					if inv.Amount >= 100000 {
						if level1.SpinTicket == nil {
							one := uint(1)
							tx.Model(&models.User{}).Where("id = ?", level1.ID).Update("spin_ticket", one)
						} else {
							tx.Model(&models.User{}).Where("id = ?", level1.ID).UpdateColumn("spin_ticket", gorm.Expr("spin_ticket + 1"))
						}
					}

					// Give 30% bonus to direct referrer
					bonus := round3(inv.Amount * 0.30)
					tx.Model(&models.User{}).Where("id = ?", level1.ID).UpdateColumn("balance", gorm.Expr("balance + ?", bonus))
					msg := "Bonus rekomendasi investor"
					trx := models.Transaction{
						UserID:          level1.ID,
						Amount:          bonus,
						Charge:          0,
						OrderID:         utils.GenerateOrderID(level1.ID),
						TransactionFlow: "debit",
						TransactionType: "team",
						Message:         &msg,
						Status:          "Success",
					}
					tx.Create(&trx)
				}
			}
			return nil
		})
		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "OK"})
		return
	}

	_ = db.Transaction(func(tx *gorm.DB) error {
		_ = tx.Model(&models.Transaction{}).Where("order_id = ?", inv.OrderID).Update("status", "Failed").Error
		_ = tx.Model(&inv).Update("status", "Cancelled").Error
		return nil
	})
	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Failed updated"})
}

// POST /api/cron/daily-returns
func CronDailyReturnsHandler(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-CRON-KEY")
	if key == "" || key != os.Getenv("CRON_KEY") {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	db := database.DB
	now := time.Now()
	var due []models.Investment
	if err := db.Where("status = 'Running' AND next_return_at IS NOT NULL AND next_return_at <= ? AND total_paid < duration", now).Find(&due).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan"})
		return
	}
	processed := 0
	for i := range due {
		inv := due[i]
		_ = db.Transaction(func(tx *gorm.DB) error {
			var user models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, inv.UserID).Error; err != nil {
				return err
			}

			// Get category to check profit type
			var category models.Category
			if err := tx.Where("id = ?", inv.CategoryID).First(&category).Error; err != nil {
				return err
			}

			amount := inv.DailyProfit
			paid := inv.TotalPaid + 1
			returned := round3(inv.TotalReturned + amount)

			var product models.Product
			if err := tx.Where("id = ?", inv.ProductID).First(&product).Error; err != nil {
				return err
			}

			// For locked (Monitor) category: Don't pay to balance until completion, just accumulate
			// For unlocked (Insight/AutoPilot): Pay to balance immediately
			if category.ProfitType == "unlocked" {
				newBalance := round3(user.Balance + amount)
				if err := tx.Model(&user).Update("balance", newBalance).Error; err != nil {
					return err
				}

				orderID := utils.GenerateOrderID(inv.UserID)
				msg := fmt.Sprintf("Profit investasi produk %s", product.Name)
				trx := models.Transaction{
					UserID:          inv.UserID,
					Amount:          amount,
					Charge:          0,
					OrderID:         orderID,
					TransactionFlow: "debit",
					TransactionType: "return",
					Message:         &msg,
					Status:          "Success",
				}
				if err := tx.Create(&trx).Error; err != nil {
					return err
				}
			}

			// For locked (Monitor): If completing, pay total accumulated profit
			if category.ProfitType == "locked" && paid >= inv.Duration {
				totalProfit := round3(inv.DailyProfit * float64(inv.Duration))
				newBalance := round3(user.Balance + totalProfit)
				if err := tx.Model(&user).Update("balance", newBalance).Error; err != nil {
					return err
				}

				orderID := utils.GenerateOrderID(inv.UserID)
				msg := fmt.Sprintf("Total profit investasi produk %s selesai", product.Name)
				trx := models.Transaction{
					UserID:          inv.UserID,
					Amount:          totalProfit,
					Charge:          0,
					OrderID:         orderID,
					TransactionFlow: "debit",
					TransactionType: "return",
					Message:         &msg,
					Status:          "Success",
				}
				if err := tx.Create(&trx).Error; err != nil {
					return err
				}
			}

			// NO TEAM BONUSES - removed completely

			nowTime := time.Now()
			nextTime := nowTime.Add(24 * time.Hour)
			updates := map[string]interface{}{"total_paid": paid, "total_returned": returned, "last_return_at": nowTime, "next_return_at": nextTime}
			if paid >= inv.Duration {
				updates["status"] = "Completed"

				newBalance := round3(user.Balance + inv.Amount)
				if err := tx.Model(&user).Update("balance", newBalance).Error; err != nil {
					return err
				}

				orderID := utils.GenerateOrderID(inv.UserID)
				msg := fmt.Sprintf("Pengembalian modal investasi produk %s", product.Name)
				trx := models.Transaction{
					UserID:          inv.UserID,
					Amount:          inv.Amount,
					Charge:          0,
					OrderID:         orderID,
					TransactionFlow: "debit",
					TransactionType: "return",
					Message:         &msg,
					Status:          "Success",
				}
				if err := tx.Create(&trx).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&inv).Updates(updates).Error; err != nil {
				return err
			}
			processed++
			return nil
		})
	}
	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Cron executed", Data: map[string]interface{}{"processed": processed}})
}

func parseTimeFlexible(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %s", s)
}

// FIXED: getKytaAccessToken with proper error handling
func getKytaAccessTokenSafe(ctx context.Context, client *http.Client, baseURL, clientID, clientSecret string) (string, string, error) {
	url := strings.TrimRight(baseURL, "/") + "/access-token"

	credentials := clientID + ":" + clientSecret
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))

	payload := map[string]string{"grant_type": "client_credentials"}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "Gagal membuat request token", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+encodedCredentials)

	resp, err := client.Do(req)
	if err != nil {
		return "", "Koneksi ke layanan pembayaran gagal", err
	}
	defer resp.Body.Close()

	// Baca response body terlebih dahulu
	tokenBodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", "Gagal membaca response token", readErr
	}

	// Parse response
	var tokenResp KytaAccessTokenResponse
	parseErr := json.Unmarshal(tokenBodyBytes, &tokenResp)

	// Cek HTTP status
	if resp.StatusCode != http.StatusOK {
		errorMsg := "Gagal mendapatkan token pembayaran"
		if parseErr == nil && tokenResp.ResponseMessage != "" {
			errorMsg = tokenResp.ResponseMessage
		} else if len(tokenBodyBytes) > 0 && len(tokenBodyBytes) < 500 {
			errorMsg = string(tokenBodyBytes)
		}
		return "", errorMsg, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Cek parsing error setelah HTTP OK
	if parseErr != nil {
		return "", "Gagal parsing response token", parseErr
	}

	// Cek response code
	if tokenResp.ResponseCode != "" && tokenResp.ResponseCode != "2000100" && tokenResp.ResponseCode != "200" && !strings.HasPrefix(tokenResp.ResponseCode, "200") {
		return "", tokenResp.ResponseMessage, errors.New("kytapay error")
	}

	if tokenResp.ResponseData.AccessToken == "" {
		return "", "Token pembayaran kosong", errors.New("empty token")
	}

	return tokenResp.ResponseData.AccessToken, "", nil
}

// FIXED: createKytaQRIS with proper error handling
func createKytaQRISSafe(ctx context.Context, client *http.Client, baseURL, accessToken, referenceID string, amount int64, notifyURL, successURL, failedURL string) (*KytaPaymentResponse, string, error) {
	url := strings.TrimRight(baseURL, "/") + "/payments/create/qris"

	payload := map[string]interface{}{
		"reference_id": referenceID,
		"amount":       amount,
		"notify_url":   notifyURL,
		"success_url":  successURL,
		"failed_url":   failedURL,
		"expires_time": 900,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, "Gagal membuat request QRIS", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "Koneksi ke layanan pembayaran gagal", err
	}
	defer resp.Body.Close()

	// Baca response body terlebih dahulu
	paymentBodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "Gagal membaca response pembayaran", readErr
	}

	// Parse response
	var paymentResp KytaPaymentResponse
	parseErr := json.Unmarshal(paymentBodyBytes, &paymentResp)

	// Cek HTTP status
	if resp.StatusCode != http.StatusOK {
		errorMsg := "Gagal membuat pembayaran QRIS"
		if parseErr == nil && paymentResp.ResponseMessage != "" {
			errorMsg = paymentResp.ResponseMessage
		} else if len(paymentBodyBytes) > 0 && len(paymentBodyBytes) < 500 {
			errorMsg = string(paymentBodyBytes)
		}
		return nil, errorMsg, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Cek parsing error setelah HTTP OK
	if parseErr != nil {
		return nil, "Gagal parsing response pembayaran", parseErr
	}

	// Cek response code
	if paymentResp.ResponseCode != "" && paymentResp.ResponseCode != "2001100" && paymentResp.ResponseCode != "200" && !strings.HasPrefix(paymentResp.ResponseCode, "200") {
		return nil, paymentResp.ResponseMessage, errors.New("kytapay error")
	}

	return &paymentResp, "", nil
}

// FIXED: createKytaVA with proper error handling
func createKytaVASafe(ctx context.Context, client *http.Client, baseURL, accessToken, referenceID string, amount int64, bankCode, notifyURL, successURL, failedURL string) (*KytaPaymentResponse, string, error) {
	url := strings.TrimRight(baseURL, "/") + "/payments/create/va"

	payload := map[string]interface{}{
		"reference_id": referenceID,
		"amount":       amount,
		"bank_code":    bankCode,
		"notify_url":   notifyURL,
		"success_url":  successURL,
		"failed_url":   failedURL,
		"expires_time": 900,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, "Gagal membuat request VA", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "Koneksi ke layanan pembayaran gagal", err
	}
	defer resp.Body.Close()

	// Baca response body terlebih dahulu
	paymentBodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "Gagal membaca response pembayaran", readErr
	}

	// Parse response
	var paymentResp KytaPaymentResponse
	parseErr := json.Unmarshal(paymentBodyBytes, &paymentResp)

	// Cek HTTP status
	if resp.StatusCode != http.StatusOK {
		errorMsg := "Gagal membuat pembayaran Virtual Account"
		if parseErr == nil && paymentResp.ResponseMessage != "" {
			errorMsg = paymentResp.ResponseMessage
		} else if len(paymentBodyBytes) > 0 && len(paymentBodyBytes) < 500 {
			errorMsg = string(paymentBodyBytes)
		}
		return nil, errorMsg, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Cek parsing error setelah HTTP OK
	if parseErr != nil {
		return nil, "Gagal parsing response pembayaran", parseErr
	}

	// Cek response code
	if paymentResp.ResponseCode != "" && paymentResp.ResponseCode != "2001200" && paymentResp.ResponseCode != "200" && !strings.HasPrefix(paymentResp.ResponseCode, "200") {
		return nil, paymentResp.ResponseMessage, errors.New("kytapay error")
	}

	return &paymentResp, "", nil
}

func round3(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// calculateVIPLevel determines VIP level based on total locked category investments
// VIP1: 50k, VIP2: 1.2M, VIP3: 7M, VIP4: 30M, VIP5: 150M
func calculateVIPLevel(totalInvestVIP float64) uint {
	if totalInvestVIP >= 150000000 {
		return 5
	} else if totalInvestVIP >= 30000000 {
		return 4
	} else if totalInvestVIP >= 7000000 {
		return 3
	} else if totalInvestVIP >= 1200000 {
		return 2
	} else if totalInvestVIP >= 50000 {
		return 1
	}
	return 0
}
