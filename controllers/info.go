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
