package controllers

import (
	"net/http"

	"project/database"
	"project/models"
	"project/utils"
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
		ID     uint    `json:"id"`
		Name   string  `json:"name"`
		Phone  string  `json:"phone"`
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
		NamaProduk            string  `json:"nama_produk"`
		TotalPembelian        int64   `json:"total_pembelian"`
		TotalJumlahInvestasi  float64 `json:"total_jumlah_investasi"`
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
