package admins

import (
	"net/http"
	"strconv"
	"time"

	"project/database"
	"project/models"
	"project/utils"
)

type TransactionResponse struct {
	ID              uint    `json:"id"`
	UserID          uint    `json:"user_id"`
	UserName        string  `json:"username"`
	Phone           string  `json:"phone"`
	Amount          float64 `json:"amount"`
	Charge          float64 `json:"charge"`
	OrderID         string  `json:"order_id"`
	TransactionFlow string  `json:"transaction_flow"`
	TransactionType string  `json:"transaction_type"`
	Message         string  `json:"message"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
}

func GetTransactions(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	userId := r.URL.Query().Get("userId")
	transactionType := r.URL.Query().Get("type")
	status := r.URL.Query().Get("status")
	orderID := r.URL.Query().Get("search")
	startDate := r.URL.Query().Get("start_date")
	endDate := r.URL.Query().Get("end_date")

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	offset := (page - 1) * limit

	// Start query
	db := database.DB
	query := db.Model(&models.Transaction{}).
		Joins("JOIN users ON transactions.user_id = users.id").
		Where("users.user_mode != ? OR users.user_mode IS NULL", "promotor")

	// Apply filters
	if userId != "" {
		query = query.Where("transactions.user_id = ?", userId)
	}
	if transactionType != "" {
		query = query.Where("transactions.transaction_type = ?", transactionType)
	}
	if status != "" {
		query = query.Where("transactions.status = ?", status)
	}

	if orderID != "" {
		query = query.Where("transactions.order_id LIKE ?", "%"+orderID+"%")
	}

	// Apply date filters if provided
	jakartaLoc, _ := time.LoadLocation("Asia/Jakarta")
	if startDate != "" {
		startTime, err := time.ParseInLocation("2006-01-02", startDate, jakartaLoc)
		if err == nil {
			query = query.Where("created_at >= ?", startTime)
		}
	}
	if endDate != "" {
		endTime, err := time.ParseInLocation("2006-01-02", endDate, jakartaLoc)
		if err == nil {
			// Add one day to get to the start of the next day in Jakarta time
			endTime = endTime.AddDate(0, 0, 1)
			query = query.Where("created_at < ?", endTime)
		}
	}

	var transactions []models.Transaction
	query.Select("transactions.*").
		Offset(offset).
		Limit(limit).
		Order("transactions.created_at DESC").
		Find(&transactions)

	// Prepare user IDs to fetch names and phones in batch
	userIDsSet := make(map[uint]struct{})
	for _, t := range transactions {
		userIDsSet[t.UserID] = struct{}{}
	}
	var userIDs []uint
	for id := range userIDsSet {
		userIDs = append(userIDs, id)
	}

	// Fetch users and build a map[id]user
	usersByID := make(map[uint]models.User, len(userIDs))
	if len(userIDs) > 0 {
		var users []models.User
		db.Select("id, name, number").Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			usersByID[u.ID] = u
		}
	}

	// Transform to response format
	var response []TransactionResponse
	for _, t := range transactions {
		response = append(response, TransactionResponse{
			ID:              t.ID,
			UserID:          t.UserID,
			UserName:        usersByID[t.UserID].Name,
			Phone:           usersByID[t.UserID].Number,
			Amount:          t.Amount,
			Charge:          t.Charge,
			OrderID:         t.OrderID,
			TransactionFlow: t.TransactionFlow,
			TransactionType: t.TransactionType,
			Message:         utils.GetStringValue(t.Message),
			Status:          t.Status,
			CreatedAt:       t.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data:    response,
	})
}
