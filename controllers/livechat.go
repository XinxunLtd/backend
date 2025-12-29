package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"project/controllers/telegram"
	"project/database"
	"project/models"
	"project/utils"

	"github.com/gorilla/mux"
	"gorm.io/gorm"
)

// StartChatRequest untuk memulai chat baru
type StartChatRequest struct {
	Name string `json:"name,omitempty"` // Optional: untuk non-auth users
}

// StartChatResponse response saat memulai chat
type StartChatResponse struct {
	SessionID uint   `json:"session_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

// SendMessageRequest untuk mengirim pesan
type SendMessageRequest struct {
	Message string `json:"message" validate:"required"`
}

// SendMessageResponse response dari AI
type SendMessageResponse struct {
	Message   string `json:"message"`
	SessionID uint   `json:"session_id"`
	Status    string `json:"status"` // 'active' or 'ended'
	Ended     bool   `json:"ended"`
}

// ChatHistoryResponse untuk melihat riwayat chat
type ChatHistoryResponse struct {
	SessionID uint       `json:"session_id"`
	Status    string     `json:"status"`
	Messages  []Message  `json:"messages"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// StartChatHandler memulai chat session baru
func StartChatHandler(w http.ResponseWriter, r *http.Request) {
	var req StartChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	db := database.DB
	var session models.ChatSession
	var userID *uint
	var userName string
	var isAuth bool

	// Check if user is authenticated
	authUserID, hasAuth := utils.GetUserID(r)
	if hasAuth {
		// Authenticated user
		isAuth = true
		userID = &authUserID

		// Get user name
		var user models.User
		if err := db.First(&user, authUserID).Error; err == nil {
			userName = user.Name
		} else {
			userName = "User"
		}
	} else {
		// Non-authenticated user
		isAuth = false
		userID = nil
		if req.Name != "" {
			userName = strings.TrimSpace(req.Name)
		} else {
			userName = "Guest"
		}
	}

	// Create new session
	now := time.Now()
	session = models.ChatSession{
		UserID:        userID,
		UserName:      userName,
		IsAuth:        isAuth,
		Status:        "active",
		LastMessageAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := db.Create(&session).Error; err != nil {
		log.Printf("[LiveChat] Error creating session: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to create chat session",
		})
		return
	}

	// Send welcome message
	var welcomeMsg string
	if isAuth {
		welcomeMsg = fmt.Sprintf("Halo Kak %s! 👋 Saya CS Xinxun, AI Agent bantu menjawab pertanyaan Kakak. Ada yang bisa dibantu? 😊", userName)
	} else {
		welcomeMsg = "Halo Kak! 👋 Saya CS Xinxun, AI Agent bantu menjawab pertanyaan Kakak. Ada yang bisa dibantu? 😊"
	}

	// Save welcome message
	message := models.ChatMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   welcomeMsg,
		CreatedAt: now,
	}
	if err := db.Create(&message).Error; err != nil {
		log.Printf("[LiveChat] Error saving welcome message: %v", err)
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Chat session started",
		Data: StartChatResponse{
			SessionID: session.ID,
			Status:    session.Status,
			Message:   welcomeMsg,
		},
	})
}

// SendMessageHandler mengirim pesan ke AI
func SendMessageHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionIDStr, ok := vars["session_id"]
	if !ok {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Session ID is required",
		})
		return
	}

	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid session ID",
		})
		return
	}

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	if strings.TrimSpace(req.Message) == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Message cannot be empty",
		})
		return
	}

	db := database.DB

	// Get session
	var session models.ChatSession
	if err := db.Preload("User").First(&session, uint(sessionID)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Chat session not found",
			})
			return
		}
		log.Printf("[LiveChat] Error getting session: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to get chat session",
		})
		return
	}

	// Check if session is ended
	if session.Status == "ended" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Chat session has ended. Please start a new chat.",
		})
		return
	}

	// Check authentication (if session is auth, user must be authenticated)
	if session.IsAuth {
		authUserID, hasAuth := utils.GetUserID(r)
		if !hasAuth || authUserID != *session.UserID {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
				Success: false,
				Message: "Unauthorized",
			})
			return
		}
	}

	// Check timeout (15 minutes)
	if time.Since(session.LastMessageAt) > 15*time.Minute {
		// Auto end session
		now := time.Now()
		session.Status = "ended"
		session.EndedAt = &now
		session.EndReason = "timeout"
		db.Save(&session)

		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Chat session expired due to inactivity. Please start a new chat.",
		})
		return
	}

	// Save user message
	userMessage := models.ChatMessage{
		SessionID: session.ID,
		Role:      "user",
		Content:   req.Message,
		CreatedAt: time.Now(),
	}
	if err := db.Create(&userMessage).Error; err != nil {
		log.Printf("[LiveChat] Error saving user message: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to save message",
		})
		return
	}

	// Get conversation history
	var messages []models.ChatMessage
	if err := db.Where("session_id = ?", session.ID).Order("created_at ASC").Find(&messages).Error; err != nil {
		log.Printf("[LiveChat] Error getting messages: %v", err)
	}

	// Convert to GroqMessage format
	history := make([]utils.GroqMessage, 0, len(messages))
	for _, msg := range messages {
		history = append(history, utils.GroqMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Get user name for greeting
	userName := session.UserName
	if session.IsAuth && session.User != nil {
		userName = session.User.Name
	}
	userGreeting := "Kak"
	if userName != "" && userName != "Guest" {
		userGreeting = userName
	}

	// Detect message type and FAQ
	messageType := detectMessageType(req.Message)
	detectedGreeting := detectUserGreeting(req.Message)

	var contextData string
	if faqType := telegram.DetectFAQType(req.Message); faqType != "" {
		contextData = telegram.GetContextData(faqType)
	}

	// Build system prompt (similar to telegram bot)
	systemPrompt := buildSystemPrompt(userName, req.Message, messageType, detectedGreeting, contextData, userGreeting)

	// Call Groq API
	response, err := utils.CallGroqAPI(history, systemPrompt)
	if err != nil {
		log.Printf("[LiveChat] Groq API error: %v", err)
		response = "Maaf, saya sedang mengalami gangguan. Silakan coba lagi nanti atau hubungi CS https://t.me/xinxun_forindo"
	}

	// Sanitize response
	response = strings.TrimSpace(response)
	response = sanitizeResponse(response)

	// Check if AI decided to end chat (AI will include [END_CHAT] marker if user wants to end)
	shouldEnd := false
	if strings.Contains(response, "[END_CHAT]") {
		shouldEnd = true
		// Remove the marker from response
		response = strings.ReplaceAll(response, "[END_CHAT]", "")
		response = strings.TrimSpace(response)
	} else {
		// AI can also end chat naturally with closing phrases - let AI decide based on context
		// We trust AI's judgment to provide appropriate closing message
		responseLower := strings.ToLower(response)
		closingIndicators := []string{
			"semoga membantu",
			"terima kasih sudah",
			"ada pertanyaan lagi",
			"jangan ragu untuk chat lagi",
			"sama-sama",
			"sampai jumpa",
			"terima kasih",
		}
		// Check if response contains closing message patterns and user message indicates ending
		userMsgLower := strings.ToLower(req.Message)
		userEndIndicators := []string{
			"terima kasih", "makasih", "thanks", "sudah jelas", "sudah", "oke sudah",
			"selesai", "sudah selesai", "cukup", "oke cukup",
		}
		userWantsToEnd := false
		for _, indicator := range userEndIndicators {
			if strings.Contains(userMsgLower, indicator) {
				userWantsToEnd = true
				break
			}
		}

		// If user indicates they want to end AND AI responds with closing message, end the chat
		if userWantsToEnd {
			closingCount := 0
			for _, indicator := range closingIndicators {
				if strings.Contains(responseLower, indicator) {
					closingCount++
				}
			}
			// If AI response has multiple closing indicators, it's likely a closing message
			if closingCount >= 2 {
				shouldEnd = true
			}
		}
	}

	// Save AI response
	aiMessage := models.ChatMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   response,
		CreatedAt: time.Now(),
	}
	if err := db.Create(&aiMessage).Error; err != nil {
		log.Printf("[LiveChat] Error saving AI message: %v", err)
	}

	// Update session
	now := time.Now()
	session.LastMessageAt = now
	session.UpdatedAt = now

	ended := false
	if shouldEnd {
		session.Status = "ended"
		session.EndedAt = &now
		session.EndReason = "user"
		ended = true
	}

	db.Save(&session)

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Message sent",
		Data: SendMessageResponse{
			Message:   response,
			SessionID: session.ID,
			Status:    session.Status,
			Ended:     ended,
		},
	})
}

// EndChatHandler mengakhiri chat session
func EndChatHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionIDStr, ok := vars["session_id"]
	if !ok {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Session ID is required",
		})
		return
	}

	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid session ID",
		})
		return
	}

	db := database.DB

	var session models.ChatSession
	if err := db.First(&session, uint(sessionID)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Chat session not found",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to get chat session",
		})
		return
	}

	// Check authentication
	if session.IsAuth {
		authUserID, hasAuth := utils.GetUserID(r)
		if !hasAuth || authUserID != *session.UserID {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
				Success: false,
				Message: "Unauthorized",
			})
			return
		}
	}

	if session.Status == "ended" {
		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
			Success: true,
			Message: "Chat session already ended",
		})
		return
	}

	// End session
	now := time.Now()
	session.Status = "ended"
	session.EndedAt = &now
	session.EndReason = "user"
	db.Save(&session)

	// Add closing message
	closingMsg := "Terima kasih sudah chat dengan kami! 😊 Semoga membantu ya. Kalau ada pertanyaan lagi, jangan ragu untuk chat lagi! 👋"
	closingMessage := models.ChatMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   closingMsg,
		CreatedAt: now,
	}
	db.Create(&closingMessage)

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Chat session ended",
		Data: map[string]interface{}{
			"session_id": session.ID,
			"status":     session.Status,
			"message":    closingMsg,
		},
	})
}

// GetChatHistoryHandler mendapatkan riwayat chat
func GetChatHistoryHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionIDStr, ok := vars["session_id"]
	if !ok {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Session ID is required",
		})
		return
	}

	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Invalid session ID",
		})
		return
	}

	db := database.DB

	var session models.ChatSession
	if err := db.Preload("User").First(&session, uint(sessionID)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Chat session not found",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to get chat session",
		})
		return
	}

	// Check authentication
	if session.IsAuth {
		authUserID, hasAuth := utils.GetUserID(r)
		if !hasAuth || authUserID != *session.UserID {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
				Success: false,
				Message: "Unauthorized",
			})
			return
		}
	}

	// Get messages
	var messages []models.ChatMessage
	if err := db.Where("session_id = ?", uint(sessionID)).Order("created_at ASC").Find(&messages).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to get messages",
		})
		return
	}

	// Convert to response format
	responseMessages := make([]Message, len(messages))
	for i, msg := range messages {
		responseMessages[i] = Message{
			Role:      msg.Role,
			Content:   msg.Content,
			CreatedAt: msg.CreatedAt,
		}
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Chat history retrieved",
		Data: ChatHistoryResponse{
			SessionID: session.ID,
			Status:    session.Status,
			Messages:  responseMessages,
			CreatedAt: session.CreatedAt,
			EndedAt:   session.EndedAt,
		},
	})
}

// GetChatSessionsHandler mendapatkan daftar chat sessions user
func GetChatSessionsHandler(w http.ResponseWriter, r *http.Request) {
	// Only for authenticated users
	userID, hasAuth := utils.GetUserID(r)
	if !hasAuth {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{
			Success: false,
			Message: "Unauthorized",
		})
		return
	}

	db := database.DB

	var sessions []models.ChatSession
	if err := db.Where("user_id = ?", userID).Order("created_at DESC").Find(&sessions).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Failed to get chat sessions",
		})
		return
	}

	// Format response
	type SessionSummary struct {
		ID          uint       `json:"id"`
		Status      string     `json:"status"`
		CreatedAt   time.Time  `json:"created_at"`
		EndedAt     *time.Time `json:"ended_at,omitempty"`
		LastMessage string     `json:"last_message,omitempty"`
	}

	summaries := make([]SessionSummary, len(sessions))
	for i, session := range sessions {
		// Get last message
		var lastMsg models.ChatMessage
		db.Where("session_id = ?", session.ID).Order("created_at DESC").First(&lastMsg)

		summaries[i] = SessionSummary{
			ID:          session.ID,
			Status:      session.Status,
			CreatedAt:   session.CreatedAt,
			EndedAt:     session.EndedAt,
			LastMessage: lastMsg.Content,
		}
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Chat sessions retrieved",
		Data:    summaries,
	})
}

// Helper functions (similar to telegram bot)

// detectMessageType detects what kind of message this is
func detectMessageType(text string) string {
	textLower := strings.ToLower(text)
	words := strings.Fields(textLower)

	// Urgent/Emergency
	urgentWords := []string{"urgent", "darurat", "penting banget", "tolong cepat", "emergency", "segera"}
	for _, word := range urgentWords {
		if strings.Contains(textLower, word) {
			return "urgent"
		}
	}

	// Scam/Fraud related
	scamWords := []string{"scam", "tipu", "penipuan", "penipu", "hack", "di hack", "dihack", "dicuri", "hilang semua", "minta password", "minta otp"}
	for _, word := range scamWords {
		if strings.Contains(textLower, word) {
			return "scam_alert"
		}
	}

	// Complaint/Problem
	complaintWords := []string{
		"error", "gagal", "pending", "gak bisa", "tidak bisa", "gabisa", "ga bisa",
		"kenapa", "masalah", "problem", "issue", "bug", "stuck",
		"hilang", "kehilangan", "lama", "lambat", "belum masuk", "gak masuk", "tidak masuk",
		"kecewa", "marah", "kesel", "bete",
	}
	for _, word := range complaintWords {
		if strings.Contains(textLower, word) {
			return "complaint"
		}
	}

	// Simple greeting (1-3 words)
	greetings := []string{"halo", "hai", "hi", "hello", "hey", "pagi", "siang", "sore", "malam", "selamat", "assalamualaikum"}
	if len(words) <= 3 {
		for _, word := range words {
			for _, greet := range greetings {
				if word == greet || strings.HasPrefix(word, greet) {
					return "greeting"
				}
			}
		}
	}

	// Question
	questionWords := []string{
		"gimana", "bagaimana", "cara", "apa", "berapa", "kapan", "dimana",
		"siapa", "mengapa", "kenapa", "bisa", "boleh", "apakah", "gmn", "gmna",
	}
	if strings.Contains(text, "?") {
		return "question"
	}
	for _, word := range questionWords {
		if strings.Contains(textLower, word) {
			return "question"
		}
	}

	// Thanks
	thanksWords := []string{"makasih", "terima kasih", "thanks", "thank you", "thx", "tq", "tengkyu", "mksh"}
	for _, word := range thanksWords {
		if strings.Contains(textLower, word) {
			return "thanks"
		}
	}

	// Confirmation/Answer
	confirmWords := []string{"iya", "ya", "yoi", "yup", "yes", "ok", "oke", "sip", "siap", "sudah", "udah", "belum", "tidak", "gak", "engga"}
	if len(words) <= 3 {
		for _, word := range words {
			for _, confirm := range confirmWords {
				if word == confirm {
					return "confirmation"
				}
			}
		}
	}

	return "general"
}

// detectUserGreeting detects what greeting the user used
func detectUserGreeting(text string) string {
	textLower := strings.ToLower(text)

	greetingMap := map[string]string{
		"pagi":             "pagi",
		"selamat pagi":     "pagi",
		"siang":            "siang",
		"selamat siang":    "siang",
		"sore":             "sore",
		"selamat sore":     "sore",
		"malam":            "malam",
		"selamat malam":    "malam",
		"halo":             "halo",
		"hai":              "hai",
		"hi":               "hi",
		"hello":            "hello",
		"hey":              "hey",
		"assalamualaikum":  "waalaikumsalam",
		"assalamu'alaikum": "waalaikumsalam",
	}

	for keyword, greeting := range greetingMap {
		if strings.Contains(textLower, keyword) {
			return greeting
		}
	}
	return ""
}

// buildSystemPrompt creates the AI system prompt for live chat
func buildSystemPrompt(userName string, userMessage string, messageType string, userGreeting string, contextData string, greeting string) string {
	now := time.Now()
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now = now.In(loc)

	hour := now.Hour()
	var timeGreeting string
	switch {
	case hour >= 5 && hour < 11:
		timeGreeting = "pagi"
	case hour >= 11 && hour < 15:
		timeGreeting = "siang"
	case hour >= 15 && hour < 18:
		timeGreeting = "sore"
	default:
		timeGreeting = "malam"
	}

	dayNames := []string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	monthNames := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}

	prompt := fmt.Sprintf(`Kamu CS Xinxun yang santai, friendly, dan helpful. Chat kayak temen biasa, bukan robot.

===== INFO =====
Waktu: %s WIB (%s, %d %s %d)
Sapaan waktu: %s
User: %s
Tipe pesan: %s
`, now.Format("15:04"), dayNames[now.Weekday()], now.Day(), monthNames[int(now.Month())], now.Year(), timeGreeting, func() string {
		if userName != "" && userName != "Guest" {
			return userName
		}
		return "(nama tidak diketahui)"
	}(), messageType)

	// Add user greeting context if detected
	if userGreeting != "" {
		prompt += fmt.Sprintf("User menyapa dengan: %s\n", userGreeting)
	}

	// Add context data if available
	if contextData != "" {
		prompt += fmt.Sprintf("\n===== DATA RELEVAN =====\n%s\n", contextData)
	}

	prompt += fmt.Sprintf(`
===== CARA JAWAB =====

GAYA BAHASA:
• Santai, gaul, kayak chat sama temen
• Pakai: "nih", "sih", "dong", "deh", "ya", "gitu", "aja", "banget", "kali", "coba"
• Emoji secukupnya (1-3 aja), jangan lebay
• JANGAN: "gue/lo", terlalu formal, kaku, template robot

SAPAAN:
• Panggil user dengan "%s"
• JANGAN "Bro" kalau udah tau nama asli

GREETING (PENTING!):
• User bilang "pagi" → bales "Pagi!" atau "Pagi juga!"
• User bilang "malam" → bales "Malam!" atau "Malam juga!"
• User bilang "halo" → bales "Halo!" atau "Hai!"
• User bilang "assalamualaikum" → bales "Waalaikumsalam!"
• IKUTIN sapaan user, JANGAN ganti berdasarkan waktu sekarang!

PANJANG JAWABAN:
• Greeting/simple → 1-2 kalimat, santai aja
• Pertanyaan biasa → 2-4 kalimat, to the point
• Keluhan/masalah → empati + solusi, max 5-6 kalimat
• Pertanyaan kompleks → max 8 kalimat, pakai bullet kalau perlu

HANDLE BERDASARKAN TIPE:
• greeting → bales santai, bisa tanya kabar/ada apa
• question → jawab langsung, kasih info yang diminta
• complaint → empati dulu ("wah sorry ya.."), baru kasih solusi
• urgent → prioritas tinggi, arahkan ke CS https://t.me/xinxun_forindo kalau perlu
• scam_alert → warning keras, kasih info CS resmi
• thanks → "Sama-sama!", "Siap!", "Yoi!"
• confirmation → lanjutin konteks pembicaraan sebelumnya

KONTEKS PERCAKAPAN:
• Baca history chat, pahami alurnya
• Kalau user jawab pertanyaan kamu sebelumnya → LANJUTKAN, jangan ulang
• Kalau user konfirmasi/jawab singkat → respond sesuai konteks

MENGAKHIRI CHAT (PENTING!):
• Kamu HARUS menentukan sendiri apakah user ingin mengakhiri chat berdasarkan konteks
• Jika user bilang "terima kasih", "makasih", "sudah jelas", "sudah", "selesai", "cukup", "oke sudah", dll → user mungkin ingin mengakhiri chat
• Jika kamu memberikan closing message (misalnya: "Sama-sama! Semoga membantu ya. Kalau ada pertanyaan lagi, jangan ragu untuk chat lagi! 👋"), TAMBAHKAN [END_CHAT] di akhir response
• Contoh closing message yang baik:
  - "Sama-sama! 😊 Semoga membantu ya. Kalau ada pertanyaan lagi, jangan ragu untuk chat lagi! 👋 [END_CHAT]"
  - "Oke, semoga membantu! Terima kasih sudah chat dengan kami. Ada pertanyaan lagi? Chat aja ya! 😊 [END_CHAT]"
• JANGAN akhiri chat jika user masih bertanya atau ada pertanyaan yang belum jelas
• Hanya akhiri chat jika jelas user sudah puas dengan jawaban dan ingin mengakhiri percakapan
• Jika user cuma bilang "makasih" atau "terima kasih" tanpa indikasi jelas ingin mengakhiri, cukup balas "Sama-sama!" tanpa [END_CHAT]
• Gunakan [END_CHAT] HANYA jika user jelas-jelas ingin mengakhiri percakapan (contoh: "terima kasih sudah jelas", "makasih sudah", "oke sudah jelas", dll)

ANTI NGACO:
• Gak tau jawabannya → bilang "Wah kurang tau nih, coba langsung tanya CS https://t.me/xinxun_forindo ya"
• JANGAN ngarang info yang gak ada di data
• JANGAN ngarang harga/produk/fitur

SCAM WARNING (sampaikan kalau relevan):
• CS resmi CUMA https://t.me/xinxun_forindo
• Xinxun GAK PERNAH minta password/OTP
• Pembayaran CUMA via QRIS/VA resmi, bukan transfer ke rekening pribadi
• Ada yang minta transfer ke rekening pribadi = PENIPUAN

FORMAT (PENTING!):
• Pakai HTML untuk formatting
• Bold: <b>teks</b>
• Italic: <i>teks</i>
• Code: <code>teks</code>
• JANGAN pakai ** atau * untuk formatting!

===== LINK PENTING =====
• Register: https://xinxun.us/register
• Login: https://xinxun.us/login
• Dashboard: https://xinxun.us/dashboard
• Withdraw: https://xinxun.us/withdraw
• Referral: https://xinxun.us/referral
• Spin: https://xinxun.us/spin-wheel
• Forum: https://xinxun.us/forum
• News: https://news.xinxun.us
• CS Resmi: https://t.me/xinxun_forindo
• Grup: https://t.me/+R4rZNjqcQ9FhMDRl

===== INGAT =====
Kamu temen yang kebetulan kerja di Xinxun. Bantuin dengan santai, jangan kayak robot customer service yang kaku dan template. Tiap jawaban harus natural dan sesuai konteks chat.`, greeting)

	return prompt
}

// sanitizeResponse cleans and converts AI response for HTML
func sanitizeResponse(text string) string {
	// Convert Markdown bold **text** to HTML <b>text</b>
	result := strings.ReplaceAll(text, "**", "")

	// Basic HTML sanitization - ensure proper spacing
	result = strings.ReplaceAll(result, "-<b>", "- <b>")
	result = strings.ReplaceAll(result, "•<b>", "• <b>")
	result = strings.ReplaceAll(result, "</b>-", "</b> -")

	return result
}
