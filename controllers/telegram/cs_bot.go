package telegram

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"project/database"
	"project/models"
	"project/utils"
)

// ConversationHistory stores the last 10 messages per user
type ConversationHistory struct {
	Messages []utils.GroqMessage
	LastSeen time.Time
}

// RateLimiter tracks user rate limits
type RateLimiter struct {
	mu       sync.RWMutex
	lastCall map[int64]time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		lastCall: make(map[int64]time.Time),
	}
}

func (rl *RateLimiter) CanProceed(userID int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	lastCall, exists := rl.lastCall[userID]
	if !exists {
		rl.lastCall[userID] = time.Now()
		return true
	}

	if time.Since(lastCall) < 5*time.Second {
		return false
	}

	rl.lastCall[userID] = time.Now()
	return true
}

// TelegramUpdate represents a Telegram webhook update
type TelegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		From      *struct {
			ID        int64  `json:"id"`
			IsBot     bool   `json:"is_bot"`
			FirstName string `json:"first_name"`
			Username  string `json:"username"`
		} `json:"from"`
		Chat *struct {
			ID    int64  `json:"id"`
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"chat"`
		Text     string `json:"text"`
		Entities []struct {
			Type   string `json:"type"`
			Offset int    `json:"offset"`
			Length int    `json:"length"`
		} `json:"entities"`
		ReplyToMessage *struct {
			MessageID int64 `json:"message_id"`
			From      *struct {
				ID        int64  `json:"id"`
				IsBot     bool   `json:"is_bot"`
				FirstName string `json:"first_name"`
			} `json:"from"`
			Text string `json:"text"`
		} `json:"reply_to_message"`
	} `json:"message"`
}

// TelegramMessage represents a message to send
type TelegramMessage struct {
	ChatID      int64  `json:"chat_id"`
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	ReplyToID   int64  `json:"reply_to_message_id,omitempty"`
	ReplyMarkup *struct {
		ForceReply bool `json:"force_reply"`
	} `json:"reply_markup,omitempty"`
}

var (
	conversationHistory = make(map[int64]*ConversationHistory)
	historyMutex        sync.RWMutex
	rateLimiter         = NewRateLimiter()
	allowedGroupIDs     []int64
)

func init() {
	// Load allowed group IDs from environment
	groupIDsStr := os.Getenv("TELEGRAM_ALLOWED_GROUP_IDS")
	if groupIDsStr != "" {
		groupIDs := strings.Split(groupIDsStr, ",")
		for _, idStr := range groupIDs {
			var id int64
			if _, err := fmt.Sscanf(strings.TrimSpace(idStr), "%d", &id); err == nil {
				allowedGroupIDs = append(allowedGroupIDs, id)
			}
		}
	}
}

// SendTelegramMessage sends a message via Telegram Bot API
func SendTelegramMessage(chatID int64, text string, replyToID int64) error {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN not set")
	}

	msg := TelegramMessage{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "HTML",
	}
	if replyToID > 0 {
		msg.ReplyToID = replyToID
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}

	return nil
}

// ShouldRespond checks if bot should respond to the message
func ShouldRespond(update *TelegramUpdate) bool {
	if update.Message == nil {
		return false
	}

	// Ignore messages from bots
	if update.Message.From != nil && update.Message.From.IsBot {
		return false
	}

	// Respond in both groups and private chats
	chatType := update.Message.Chat.Type

	// If it's a group or supergroup, check if it's allowed
	if chatType == "group" || chatType == "supergroup" {
		// Check if group is allowed
		if len(allowedGroupIDs) > 0 {
			allowed := false
			for _, id := range allowedGroupIDs {
				if update.Message.Chat.ID == id {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}
	} else if chatType == "private" {
		// Allow all private chats - bot will respond to all private messages
		// No need to check allowedGroupIDs for private chats
	} else {
		// Ignore other chat types (channel, etc.)
		return false
	}

	// Bot will respond to all text messages in allowed groups
	// This makes it behave like a real CS that's always ready to help
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return false
	}

	// Ignore commands (messages starting with /)
	if strings.HasPrefix(text, "/") {
		return false
	}

	// Respond to all other messages
	return true
}

// GetConversationHistory returns the last 10 messages for a user
func GetConversationHistory(userID int64) []utils.GroqMessage {
	historyMutex.RLock()
	defer historyMutex.RUnlock()

	history, exists := conversationHistory[userID]
	if !exists {
		return []utils.GroqMessage{}
	}

	// Return last 10 messages
	if len(history.Messages) > 10 {
		return history.Messages[len(history.Messages)-10:]
	}
	return history.Messages
}

// AddToConversationHistory adds a message to conversation history
func AddToConversationHistory(userID int64, role string, content string) {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	if conversationHistory[userID] == nil {
		conversationHistory[userID] = &ConversationHistory{
			Messages: []utils.GroqMessage{},
			LastSeen: time.Now(),
		}
	}

	conversationHistory[userID].Messages = append(conversationHistory[userID].Messages, utils.GroqMessage{
		Role:    role,
		Content: content,
	})

	// Keep only last 10 messages
	if len(conversationHistory[userID].Messages) > 10 {
		conversationHistory[userID].Messages = conversationHistory[userID].Messages[len(conversationHistory[userID].Messages)-10:]
	}

	conversationHistory[userID].LastSeen = time.Now()
}

// GetFAQResponse tries to match question with FAQ
func GetFAQResponse(question string) (string, bool) {
	question = strings.ToLower(question)

	// FAQ patterns
	faqs := map[string]string{
		"harga":             getProductPrices(),
		"price":             getProductPrices(),
		"produk":            getProductDetails(),
		"product":           getProductDetails(),
		"minimal penarikan": getWithdrawalInfo(),
		"min penarikan":     getWithdrawalInfo(),
		"minimal withdraw":  getWithdrawalInfo(),
		"waktu penarikan":   getWithdrawalTime(),
		"jam penarikan":     getWithdrawalTime(),
		"withdrawal time":   getWithdrawalTime(),
		"cara daftar":       getRegistrationGuide(),
		"cara mendaftar":    getRegistrationGuide(),
		"register":          getRegistrationGuide(),
		"pendaftaran":       getRegistrationGuide(),
		"cara penarikan":    getWithdrawalGuide(),
		"cara withdraw":     getWithdrawalGuide(),
		"withdraw":          getWithdrawalGuide(),
		"cara beli":         getPurchaseGuide(),
		"cara pembelian":    getPurchaseGuide(),
		"beli produk":       getPurchaseGuide(),
		"pembelian":         getPurchaseGuide(),
	}

	for keyword, answer := range faqs {
		if strings.Contains(question, keyword) {
			return answer, true
		}
	}

	return "", false
}

// getProductPrices returns formatted product prices
func getProductPrices() string {
	db := database.DB
	var products []models.Product
	if err := db.Where("status = ?", "Active").Preload("Category").Find(&products).Error; err != nil {
		return "Maaf, saya tidak dapat mengakses informasi produk saat ini."
	}

	if len(products) == 0 {
		return "Belum ada produk yang tersedia."
	}

	var response strings.Builder
	response.WriteString("📦 <b>Daftar Produk & Harga:</b>\n\n")

	for _, product := range products {
		categoryName := "N/A"
		if product.Category != nil {
			categoryName = product.Category.Name
		}

		response.WriteString(fmt.Sprintf("• <b>%s</b> (%s)\n", product.Name, categoryName))
		response.WriteString(fmt.Sprintf("  💰 Harga: Rp%.0f\n", product.Amount))
		response.WriteString(fmt.Sprintf("  📈 Profit Harian: Rp%.0f\n", product.DailyProfit))
		response.WriteString(fmt.Sprintf("  ⏱ Durasi: %d hari\n", product.Duration))
		if product.RequiredVIP > 0 {
			response.WriteString(fmt.Sprintf("  ⭐ VIP Level: %d\n", product.RequiredVIP))
		}
		response.WriteString("\n")
	}

	return response.String()
}

// getProductDetails returns detailed product information
func getProductDetails() string {
	db := database.DB
	var products []models.Product
	if err := db.Where("status = ?", "Active").Preload("Category").Find(&products).Error; err != nil {
		return "Maaf, saya tidak dapat mengakses informasi produk saat ini."
	}

	if len(products) == 0 {
		return "Belum ada produk yang tersedia."
	}

	var response strings.Builder
	response.WriteString("📋 <b>Detail Produk:</b>\n\n")

	for _, product := range products {
		categoryName := "N/A"
		profitType := "Unlocked"
		if product.Category != nil {
			categoryName = product.Category.Name
			profitType = product.Category.ProfitType
		}

		response.WriteString(fmt.Sprintf("🔹 <b>%s</b>\n", product.Name))
		response.WriteString(fmt.Sprintf("   Kategori: %s\n", categoryName))
		response.WriteString(fmt.Sprintf("   Harga: Rp%.0f\n", product.Amount))
		response.WriteString(fmt.Sprintf("   Profit Harian: Rp%.0f\n", product.DailyProfit))
		response.WriteString(fmt.Sprintf("   Durasi: %d hari\n", product.Duration))
		response.WriteString(fmt.Sprintf("   Tipe Profit: %s\n", profitType))
		if product.RequiredVIP > 0 {
			response.WriteString(fmt.Sprintf("   VIP Level Diperlukan: %d\n", product.RequiredVIP))
		}
		if product.PurchaseLimit > 0 {
			response.WriteString(fmt.Sprintf("   Batas Pembelian: %d kali\n", product.PurchaseLimit))
		}
		response.WriteString("\n")
	}

	return response.String()
}

// getWithdrawalInfo returns minimum withdrawal information
func getWithdrawalInfo() string {
	sqlDB, err := database.DB.DB()
	if err != nil {
		return "Maaf, saya tidak dapat mengakses informasi penarikan saat ini."
	}

	setting, err := models.GetSetting(sqlDB)
	if err != nil {
		return "Maaf, saya tidak dapat mengakses informasi penarikan saat ini."
	}

	return fmt.Sprintf("💰 <b>Informasi Penarikan:</b>\n\n"+
		"• Minimal Penarikan: Rp%.0f\n"+
		"• Maksimal Penarikan: Rp%.0f\n"+
		"• Biaya Admin: Rp%.0f\n\n"+
		"💡 Penarikan hanya dapat dilakukan pada hari Senin-Sabtu, pukul 09:00-17:00 WIB.",
		setting.MinWithdraw, setting.MaxWithdraw, setting.WithdrawCharge)
}

// getWithdrawalTime returns withdrawal time information
func getWithdrawalTime() string {
	return "⏰ <b>Waktu Penarikan:</b>\n\n" +
		"• Hari: Senin - Sabtu\n" +
		"• Jam: 09:00 - 17:00 WIB\n\n" +
		"⚠️ Penarikan di luar jam tersebut tidak dapat diproses."
}

// getRegistrationGuide returns registration guide
func getRegistrationGuide() string {
	return "📝 <b>Cara Mendaftar:</b>\n\n" +
		"1. Akses https://xinxun.us/register pada browser Anda\n" +
		"2. Buka aplikasi dan pilih \"Daftar\"\n" +
		"3. Isi data diri:\n" +
		"   • Nama lengkap\n" +
		"   • Nomor telepon\n" +
		"   • Password (minimal 6 karakter)\n" +
		"   • Kode referral (gunakan kode referral teman Anda, atau gunakan XINXUN jika tidak ada kode referral teman Anda)\n" +
		"4. Klik \"Daftar\" dan selesai!\n\n" +
		"💡 Setelah mendaftar, Anda akan mendapat bonus pendaftaran sebesar Rp2.000"
}

// getWithdrawalGuide returns withdrawal guide
func getWithdrawalGuide() string {
	return "💸 <b>Cara Melakukan Penarikan:</b>\n\n" +
		"1. Pastikan saldo Anda mencukupi (minimal sesuai ketentuan)\n" +
		"2. Pastikan waktu penarikan (Senin-Sabtu, 09:00-17:00 WIB)\n" +
		"3. Buka menu \"Penarikan\" di aplikasi\n" +
		"4. Tambahkan rekening bank jika belum ada\n" +
		"5. Masukkan jumlah yang ingin ditarik\n" +
		"6. Pilih rekening tujuan\n" +
		"7. Konfirmasi penarikan\n\n" +
		"⚠️ Penarikan hanya dapat dilakukan 1 kali per hari"
}

// getPurchaseGuide returns purchase guide
func getPurchaseGuide() string {
	return "🛒 <b>Cara Membeli Produk:</b>\n\n" +
		"1. Buka aplikasi Xinxun\n" +
		"2. Pilih menu \"Produk\" atau \"Investasi\"\n" +
		"3. Pilih produk yang ingin dibeli\n" +
		"4. Baca detail produk (harga, profit, durasi)\n" +
		"5. Pilih metode pembayaran yang tersedia\n" +
		"6. Klik \"Konfirmasi\"\n" +
		"7. Lakukan pembayaran sesuai instruksi yang diberikan\n\n" +
		"💡 Setelah pembayaran berhasil, produk Anda akan otomatis berjalan sesuai durasi produk"
}

// CSBotWebhookHandler handles Telegram webhook updates
func CSBotWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var update TelegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Only process if should respond
	if !ShouldRespond(&update) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Check rate limit
	userID := update.Message.From.ID
	if !rateLimiter.CanProceed(userID) {
		// Silently ignore if rate limited
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get user message
	userMessage := update.Message.Text
	chatID := update.Message.Chat.ID
	messageID := update.Message.MessageID

	// Try FAQ first
	if faqResponse, found := GetFAQResponse(userMessage); found {
		if err := SendTelegramMessage(chatID, faqResponse, messageID); err != nil {
			log.Printf("Error sending FAQ response: %v", err)
		}
		AddToConversationHistory(userID, "user", userMessage)
		AddToConversationHistory(userID, "assistant", faqResponse)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get conversation history
	history := GetConversationHistory(userID)

	// Build system prompt
	systemPrompt := `Kamu adalah customer service bot untuk aplikasi Xinxun, sebuah platform investasi. 
Kamu adalah CS yang ramah, membantu, dan selalu siap membantu member di grup chat.

Gaya komunikasi:
- Gunakan bahasa Indonesia yang santai dan asik namun profesional
- Ramah dan hangat seperti teman yang membantu
- Bisa merespons berbagai jenis percakapan (pertanyaan, obrolan ringan, dll)
- Jika ada yang mengobrol atau bercanda, ikuti dengan ramah tapi tetap fokus pada topik Xinxun
- Jika ada pertanyaan serius tentang Xinxun, jawab dengan detail dan jelas

Informasi yang bisa kamu berikan:
- Harga produk dan detail produk
- Minimal dan maksimal penarikan
- Waktu penarikan (Senin-Sabtu, 09:00-17:00 WIB)
- Cara mendaftar
- Cara melakukan penarikan
- Cara melakukan pembelian produk
- Informasi umum tentang Xinxun

Jika tidak tahu jawabannya atau butuh informasi lebih detail, arahkan user untuk menghubungi admin melalui link CS yang tersedia.

Jawab dengan singkat, jelas, dan ramah. Maksimal 3-4 kalimat per respons agar tidak terlalu panjang.`

	// Add user message to history
	messages := append(history, utils.GroqMessage{
		Role:    "user",
		Content: userMessage,
	})

	// Call Groq API
	response, err := utils.CallGroqAPI(messages, systemPrompt)
	if err != nil {
		log.Printf("Error calling Groq API: %v", err)
		errorMsg := "Maaf, saya sedang mengalami gangguan. Silakan coba lagi nanti atau hubungi admin."
		if err := SendTelegramMessage(chatID, errorMsg, messageID); err != nil {
			log.Printf("Error sending error message: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Send response
	if err := SendTelegramMessage(chatID, response, messageID); err != nil {
		log.Printf("Error sending message: %v", err)
	}

	// Update conversation history
	AddToConversationHistory(userID, "user", userMessage)
	AddToConversationHistory(userID, "assistant", response)

	w.WriteHeader(http.StatusOK)
}
