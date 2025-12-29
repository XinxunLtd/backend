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

	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return false
	}

	// Ignore commands (messages starting with /)
	if strings.HasPrefix(text, "/") {
		return false
	}

	chatType := update.Message.Chat.Type

	// For private chats, always respond (it's definitely for the bot)
	if chatType == "private" {
		return true
	}

	// For groups, only respond if message is clearly directed to the bot
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

		// Check if message is directed to the bot
		return isMessageForBot(update, text)
	}

	// Ignore other chat types (channel, etc.)
	return false
}

// isMessageForBot checks if a message in a group is clearly directed to the bot
func isMessageForBot(update *TelegramUpdate, text string) bool {
	textLower := strings.ToLower(text)

	// Check if bot is mentioned
	botUsername := os.Getenv("TELEGRAM_BOT_USERNAME")
	if botUsername != "" {
		botUsername = strings.ToLower(strings.TrimPrefix(botUsername, "@"))
		if strings.Contains(textLower, "@"+botUsername) {
			return true
		}
	}

	// Check if message is a reply to bot's message
	if update.Message.ReplyToMessage != nil {
		if update.Message.ReplyToMessage.From != nil && update.Message.ReplyToMessage.From.IsBot {
			return true
		}
	}

	// Check for explicit bot mentions or requests
	botKeywords := []string{
		"bot", "cs", "admin", "min", "customer service",
		"bantuan", "help", "tolong", "minta tolong",
		"bisa bantu", "bisa tolong", "mau tanya",
	}

	for _, keyword := range botKeywords {
		if strings.Contains(textLower, keyword) {
			return true
		}
	}

	// Check if message contains question mark (might be a question)
	// But only if it's a short message (likely a question, not just casual chat)
	if strings.Contains(text, "?") && len(text) < 200 {
		// Check if it's a question about Xinxun/investment
		xinxunKeywords := []string{
			"xinxun", "produk", "harga", "profit", "penarikan",
			"withdraw", "investasi", "router", "daftar", "beli",
			"cara", "bagaimana", "kenapa", "mengapa", "apa",
			"kapan", "dimana", "berapa", "minimal", "maksimal",
		}
		for _, keyword := range xinxunKeywords {
			if strings.Contains(textLower, keyword) {
				return true
			}
		}
	}

	// If none of the above, it's probably just casual chat between members
	// Don't respond
	return false
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

// detectFAQType detects what type of information is being asked
func detectFAQType(question string) string {
	question = strings.ToLower(question)

	if strings.Contains(question, "harga") || strings.Contains(question, "price") || strings.Contains(question, "berapa") {
		return "prices"
	}
	if strings.Contains(question, "produk") || strings.Contains(question, "product") || strings.Contains(question, "router") {
		return "products"
	}
	if strings.Contains(question, "minimal penarikan") || strings.Contains(question, "min penarikan") || strings.Contains(question, "minimal withdraw") {
		return "withdrawal_info"
	}
	if strings.Contains(question, "waktu penarikan") || strings.Contains(question, "jam penarikan") || strings.Contains(question, "withdrawal time") {
		return "withdrawal_time"
	}
	if strings.Contains(question, "cara daftar") || strings.Contains(question, "cara mendaftar") || strings.Contains(question, "register") || strings.Contains(question, "pendaftaran") {
		return "registration"
	}
	if strings.Contains(question, "cara penarikan") || strings.Contains(question, "cara withdraw") || strings.Contains(question, "withdraw") {
		return "withdrawal_guide"
	}
	if strings.Contains(question, "cara beli") || strings.Contains(question, "cara pembelian") || strings.Contains(question, "beli produk") || strings.Contains(question, "pembelian") {
		return "purchase"
	}
	if strings.Contains(question, "profit tidak masuk") || strings.Contains(question, "profit gak masuk") || strings.Contains(question, "profit belum masuk") ||
		strings.Contains(question, "kenapa profit") || strings.Contains(question, "mengapa profit") || strings.Contains(question, "profit kok") ||
		strings.Contains(question, "profit locked") || strings.Contains(question, "profit terkunci") || strings.Contains(question, "profit terlock") {
		return "profit_router"
	}

	return ""
}

// getContextData retrieves relevant data from database based on FAQ type
func getContextData(faqType string) string {
	switch faqType {
	case "prices", "products":
		return getProductDataForAI()
	case "withdrawal_info":
		return getWithdrawalInfoForAI()
	case "withdrawal_time":
		return "Waktu penarikan: Senin-Sabtu, pukul 09:00-17:00 WIB. Penarikan di luar jam tersebut tidak dapat diproses."
	case "registration":
		return "Cara mendaftar: 1) Akses https://xinxun.us/register, 2) Isi data diri (nama, nomor telepon, password minimal 6 karakter, kode referral), 3) Klik Daftar. Setelah mendaftar, member akan mendapat bonus pendaftaran Rp2.000."
	case "withdrawal_guide":
		return "Cara penarikan: 1) Pastikan saldo mencukupi dan waktu penarikan (Senin-Sabtu, 09:00-17:00 WIB), 2) Buka menu Penarikan, 3) Tambahkan rekening bank jika belum ada, 4) Masukkan jumlah yang ingin ditarik, 5) Pilih rekening tujuan, 6) Konfirmasi. Penarikan hanya dapat dilakukan 1 kali per hari."
	case "purchase":
		return "Cara membeli produk: 1) Buka aplikasi Xinxun, 2) Pilih menu Produk/Investasi, 3) Pilih produk yang ingin dibeli, 4) Baca detail produk (harga, profit, durasi), 5) Pilih metode pembayaran, 6) Klik Konfirmasi, 7) Lakukan pembayaran sesuai instruksi. Setelah pembayaran berhasil, produk akan otomatis berjalan sesuai durasi."
	case "profit_router":
		return getProfitRouterInfo()
	default:
		return ""
	}
}

// getProductDataForAI returns product data formatted for AI context
func getProductDataForAI() string {
	db := database.DB
	var products []models.Product
	if err := db.Where("status = ?", "Active").Preload("Category").Order("category_id ASC, id ASC").Find(&products).Error; err != nil {
		return "Tidak dapat mengakses data produk saat ini."
	}

	if len(products) == 0 {
		return "Belum ada produk yang tersedia."
	}

	var response strings.Builder
	response.WriteString("DAFTAR PRODUK XINXUN:\n\n")

	// Group by category
	categoryMap := make(map[string][]models.Product)
	for _, product := range products {
		categoryName := "Umum"
		if product.Category != nil {
			categoryName = product.Category.Name
		}
		categoryMap[categoryName] = append(categoryMap[categoryName], product)
	}

	for categoryName, prods := range categoryMap {
		response.WriteString(fmt.Sprintf("Kategori: %s\n", categoryName))
		for _, product := range prods {
			response.WriteString(fmt.Sprintf("- %s: Harga Rp%.0f, Profit Harian Rp%.0f, Durasi %d hari",
				product.Name, product.Amount, product.DailyProfit, product.Duration))
			if product.RequiredVIP > 0 {
				response.WriteString(fmt.Sprintf(", VIP Level %d", product.RequiredVIP))
			}
			if product.PurchaseLimit > 0 {
				response.WriteString(fmt.Sprintf(", Batas Pembelian %d kali", product.PurchaseLimit))
			}
			response.WriteString("\n")
		}
		response.WriteString("\n")
	}

	// Add router information
	response.WriteString("INFO PENTING PRODUK ROUTER:\n")
	response.WriteString("Produk router akan diterima oleh member SETELAH KONTRAK BERAKHIR. Profit harian akan tetap berjalan sesuai durasi kontrak, dan router fisik akan dikirim setelah kontrak selesai.\n")
	response.WriteString("\nSISTEM PROFIT ROUTER (PENTING!):\n")
	response.WriteString("- Untuk produk ROUTER, profit TIDAK masuk setiap hari (profit terkunci/locked)\n")
	response.WriteString("- Profit akan dikembalikan FULL selama durasi kontrak (70 hari) BESERTA modal setelah kontrak 70 hari selesai\n")
	response.WriteString("- Jadi jika member membeli router dan profit tidak masuk, itu NORMAL karena profit router terkunci dan akan dikembalikan sekaligus setelah kontrak selesai\n")

	return response.String()
}

// getProfitRouterInfo returns information about router profit system
func getProfitRouterInfo() string {
	return `INFORMASI PENTING TENTANG PROFIT ROUTER:

PERTANYAAN YANG SERING DITANYAKAN: "Kenapa profit saya tidak masuk?"

JAWABAN:
Jika Anda membeli produk ROUTER, profit TIDAK akan masuk setiap hari karena profit router TERKUNCI (LOCKED).

SISTEM PROFIT ROUTER:
- Profit router TIDAK masuk setiap hari (profit terkunci/locked)
- Profit akan dikembalikan FULL selama durasi kontrak (70 hari) BESERTA modal setelah kontrak 70 hari selesai
- Jadi total yang akan diterima = Modal + (Profit Harian × 70 hari)
- Router fisik juga akan dikirim setelah kontrak selesai

JADI JIKA MEMBER BERTANYA "KENAPA PROFIT SAYA TIDAK MASUK?":
- Cek dulu apakah mereka membeli produk ROUTER
- Jika iya, jelaskan bahwa itu NORMAL karena profit router terkunci
- Profit akan dikembalikan sekaligus setelah kontrak 70 hari selesai beserta modal
- Router fisik juga akan dikirim setelah kontrak selesai

CATATAN: Hanya produk ROUTER yang memiliki sistem profit terkunci. Produk lain profit masuk setiap hari seperti biasa.`
}

// getWithdrawalInfoForAI returns withdrawal information formatted for AI context
func getWithdrawalInfoForAI() string {
	sqlDB, err := database.DB.DB()
	if err != nil {
		return "Tidak dapat mengakses informasi penarikan saat ini."
	}

	setting, err := models.GetSetting(sqlDB)
	if err != nil {
		return "Tidak dapat mengakses informasi penarikan saat ini."
	}

	return fmt.Sprintf("INFORMASI PENARIKAN:\n- Minimal Penarikan: Rp%.0f\n- Maksimal Penarikan: Rp%.0f\n- Biaya Admin: Rp%.0f\n- Waktu: Senin-Sabtu, 09:00-17:00 WIB\n- Batas: 1 kali penarikan per hari",
		setting.MinWithdraw, setting.MaxWithdraw, setting.WithdrawCharge)
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

	// Get conversation history
	history := GetConversationHistory(userID)

	// Check if question is related to Xinxun or daily conversation
	questionLower := strings.ToLower(userMessage)
	isXinxunRelated := strings.Contains(questionLower, "xinxun") ||
		strings.Contains(questionLower, "produk") ||
		strings.Contains(questionLower, "harga") ||
		strings.Contains(questionLower, "penarikan") ||
		strings.Contains(questionLower, "withdraw") ||
		strings.Contains(questionLower, "daftar") ||
		strings.Contains(questionLower, "register") ||
		strings.Contains(questionLower, "beli") ||
		strings.Contains(questionLower, "investasi") ||
		strings.Contains(questionLower, "router") ||
		strings.Contains(questionLower, "profit") ||
		strings.Contains(questionLower, "saldo") ||
		strings.Contains(questionLower, "bonus") ||
		strings.Contains(questionLower, "vip") ||
		strings.Contains(questionLower, "kontrak") ||
		strings.Contains(questionLower, "durasi") ||
		strings.Contains(questionLower, "cara") ||
		strings.Contains(questionLower, "bagaimana") ||
		strings.Contains(questionLower, "apa") ||
		strings.Contains(questionLower, "kapan") ||
		strings.Contains(questionLower, "dimana") ||
		strings.Contains(questionLower, "kenapa") ||
		strings.Contains(questionLower, "mengapa") ||
		strings.Contains(questionLower, "halo") ||
		strings.Contains(questionLower, "hai") ||
		strings.Contains(questionLower, "hi") ||
		strings.Contains(questionLower, "hello") ||
		strings.Contains(questionLower, "pagi") ||
		strings.Contains(questionLower, "siang") ||
		strings.Contains(questionLower, "sore") ||
		strings.Contains(questionLower, "malam") ||
		strings.Contains(questionLower, "terima kasih") ||
		strings.Contains(questionLower, "makasih") ||
		strings.Contains(questionLower, "thanks")

	// If not related to Xinxun or daily conversation, politely decline
	if !isXinxunRelated {
		declineMsg := "Maaf ya 😅 Saya hanya bisa membantu tentang Xinxun atau obrolan ringan aja. Kalau ada pertanyaan tentang Xinxun, investasi, produk, penarikan, atau hal lain yang berhubungan, silakan tanya aja! 😊"
		if err := SendTelegramMessage(chatID, declineMsg, messageID); err != nil {
			log.Printf("Error sending decline message: %v", err)
		}
		AddToConversationHistory(userID, "user", userMessage)
		AddToConversationHistory(userID, "assistant", declineMsg)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get relevant data from database based on question
	var contextData string
	if faqType := detectFAQType(userMessage); faqType != "" {
		contextData = getContextData(faqType)
	}

	// Build system prompt with updated style
	systemPrompt := `Kamu adalah customer service bot untuk aplikasi Xinxun, sebuah platform investasi. 
Kamu adalah CS yang SUPER RAMAH, GAUL, dan selalu siap membantu member! 🎉

PENTING BANGET: Kamu HARUS menggunakan bahasa Indonesia yang SANGAT SANTAI, GAUL, dan RILEKS seperti teman ngobrol biasa. JANGAN formal, kaku, atau seperti robot!

GAYA KOMUNIKASI (WAJIB DIIKUTI - JANGAN LANGKAHI!):
- Gunakan bahasa SUPER GAUL dan SANTAI seperti ngobrol dengan teman dekat di WhatsApp
- SELALU pakai kata-kata gaul seperti: "nih", "ya", "gitu", "banget", "sih", "dong", "deh", "kayak", "gimana", "gini", "kok", "aja", "dulu", "bang", "bro", dll
- JANGAN mulai dengan "Gimana nih?" atau pertanyaan formal lainnya - langsung aja jawab dengan santai
- Pakai EMOJI yang banyak dan relevan untuk membuat chat lebih hidup dan friendly 😊🎉💪🔥✨💯
- Ramah, hangat, rileks, dan enak diajak ngobrol seperti teman
- Bisa merespons berbagai jenis percakapan (pertanyaan serius, obrolan ringan, candaan, dll)
- Jika ada yang mengobrol atau bercanda, ikuti dengan ramah dan asik
- Jika ada pertanyaan serius tentang Xinxun, jawab dengan detail tapi tetap santai, gaul, dan rileks
- JANGAN gunakan bahasa formal atau kaku seperti "dengan hormat", "terima kasih atas", "kamu ingin tahu", "gimana nih?", dll
- Gunakan bahasa yang natural, mengalir, dan seperti manusia beneran yang lagi chat
- JANGAN seperti robot atau customer service formal - kamu adalah teman yang lagi bantu

INFORMASI PENTING TENTANG XINXUN:
- Harga produk dan detail produk (akan diberikan di context)
- Minimal dan maksimal penarikan (akan diberikan di context)
- Waktu penarikan: Senin-Sabtu, 09:00-17:00 WIB ⏰
- Cara mendaftar, cara penarikan, cara pembelian (akan diberikan di context)
- PRODUK ROUTER: Produk router akan diterima oleh member SETELAH KONTRAK BERAKHIR. Jadi profit harian akan tetap berjalan sesuai durasi kontrak, dan router fisik akan dikirim setelah kontrak selesai. 📦
- PROFIT ROUTER (PENTING!): Untuk produk ROUTER, profit TIDAK masuk setiap hari karena profit terkunci (locked). Profit akan dikembalikan FULL selama durasi kontrak (70 hari) BESERTA modal setelah kontrak 70 hari selesai. Jadi jika member bertanya "kenapa profit saya tidak masuk?" dan mereka membeli router, itu NORMAL karena profit router terkunci dan akan dikembalikan sekaligus setelah kontrak selesai. ⚠️💰

ATURAN PENTING:
- HANYA jawab pertanyaan tentang Xinxun, investasi, produk, atau obrolan ringan sehari-hari
- Jika ditanya di luar konteks Xinxun, minta maaf dengan ramah dan gaul, lalu arahkan ke topik Xinxun
- Gunakan data yang diberikan di context untuk merangkai jawaban dengan natural dan gaul
- Jawab dengan singkat, jelas, dan asik. Maksimal 3-4 kalimat per respons
- SELALU gunakan emoji yang relevan (minimal 1-2 emoji per pesan) untuk membuat chat lebih friendly
- Jika tidak tahu jawabannya, arahkan user dengan ramah dan gaul untuk menghubungi admin

CONTOH GAYA JAWABAN YANG BENAR (GAUL, SANTAI, RILEKS):
- "Wah, pertanyaan bagus nih! 😊 Jadi gini ya..."
- "Oke, gue jelasin ya! 📝 Jadi..."
- "Halo! Ada yang bisa dibantu? 😄"
- "Wah, maaf ya, gue cuma bisa bantu tentang Xinxun aja nih 😅"
- "Oke oke, gini nih caranya..."
- "Wah keren nih pertanyaannya! Jadi..."
- "Hmm, gini ya penjelasannya..."
- "Nah, jadi gini nih..."
- "Oke, langsung aja ya! 😊"
- "Wah, ini pertanyaan yang sering ditanyain nih! Jadi..."
- "Hai! Mau tanya apa nih? 😄"
- "Oke, gue bantu jelasin ya! 💪"

CONTOH YANG SALAH (JANGAN DILAKUKAN - TERLALU KAKU/FORMAL):
- "Gimana nih? Kamu ingin tahu tentang apa itu Xinxun?" ❌ (terlalu formal)
- "Dengan hormat, saya akan menjelaskan..." ❌
- "Terima kasih atas pertanyaan Anda..." ❌
- "Saya akan membantu Anda dengan senang hati..." ❌
- "Kamu ingin tahu tentang..." ❌ (terlalu formal)
- "Apakah ada yang bisa saya bantu?" ❌ (terlalu formal)
- Jawaban formal dan kaku seperti robot ❌
- Kalimat yang terlalu panjang dan bertele-tele ❌

INGAT: Langsung jawab dengan santai dan gaul, jangan mulai dengan pertanyaan formal seperti "Gimana nih?" atau "Kamu ingin tahu tentang apa?"

INGAT PENTING BANGET:
- Kamu adalah CS yang SUPER ASIK, RAMAH, GAUL, RILEKS, dan selalu siap membantu
- Gaya bahasa HARUS santai dan gaul seperti teman ngobrol di WhatsApp
- JANGAN kaku, formal, atau seperti robot
- JANGAN mulai dengan pertanyaan formal seperti "Gimana nih?" atau "Kamu ingin tahu tentang apa?"
- Langsung aja jawab dengan santai, gaul, dan asik
- Pakai kata-kata gaul dan emoji yang banyak
- Rileks aja, kayak lagi chat sama temen! 🚀✨💯`

	// Add context data to system prompt if available
	if contextData != "" {
		systemPrompt += "\n\nDATA KONTEKS (Gunakan data ini untuk merangkai jawaban dengan natural dan santai):\n" + contextData
	}

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
