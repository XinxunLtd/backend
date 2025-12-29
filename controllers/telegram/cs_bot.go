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

	if time.Since(lastCall) < 3*time.Second {
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

// getWIBTime returns current time in WIB timezone
func getWIBTime() time.Time {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.FixedZone("WIB", 7*60*60)
	}
	return time.Now().In(loc)
}

// getTimeGreeting returns appropriate greeting based on current hour
func getTimeGreeting() string {
	hour := getWIBTime().Hour()
	switch {
	case hour >= 5 && hour < 11:
		return "pagi"
	case hour >= 11 && hour < 15:
		return "siang"
	case hour >= 15 && hour < 18:
		return "sore"
	default:
		return "malam"
	}
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
	words := strings.Fields(textLower)

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
		"bisa bantu", "bisa tolong", "mau tanya", "xinxun",
	}

	for _, keyword := range botKeywords {
		if strings.Contains(textLower, keyword) {
			return true
		}
	}

	// Check if message is a question (with or without question mark)
	xinxunKeywords := []string{
		"produk", "harga", "profit", "penarikan", "withdraw",
		"investasi", "router", "mifi", "powerbank", "daftar", "beli",
		"deposit", "komisi", "referral", "vip", "level", "task", "tugas",
		"spin", "hadiah", "event", "berita", "news", "forum", "bank",
		"rekening", "saldo", "bonus", "kontrak", "durasi", "lisensi",
		"publisher", "lupa password", "forgot password",
		"error", "gagal", "pending", "gak bisa", "tidak bisa", "masalah",
	}

	// Check if it's a question (has question mark OR question words)
	isQuestion := strings.Contains(text, "?") ||
		strings.Contains(textLower, "cara ") ||
		strings.Contains(textLower, "bagaimana ") ||
		strings.Contains(textLower, "kenapa ") ||
		strings.Contains(textLower, "mengapa ") ||
		strings.Contains(textLower, "gimana ") ||
		strings.Contains(textLower, "apa ") ||
		strings.Contains(textLower, "kapan ") ||
		strings.Contains(textLower, "dimana ") ||
		strings.Contains(textLower, "berapa ") ||
		strings.Contains(textLower, "bisa ") ||
		strings.Contains(textLower, "boleh ")

	if isQuestion && len(text) < 300 {
		for _, keyword := range xinxunKeywords {
			if strings.Contains(textLower, keyword) {
				return true
			}
		}
	}

	// Check conversation history - if bot asked a question recently, this might be an answer
	history := GetConversationHistory(update.Message.From.ID)
	if len(history) > 0 {
		lastMessage := history[len(history)-1]
		if lastMessage.Role == "assistant" {
			lastContent := strings.ToLower(lastMessage.Content)
			if strings.Contains(lastContent, "?") ||
				strings.Contains(lastContent, "kamu baru") ||
				strings.Contains(lastContent, "sudah pernah") ||
				strings.Contains(lastContent, "level vip") ||
				strings.Contains(lastContent, "baru di xinxun") {
				return true
			}
		}
	}

	// Simple greetings directed to bot (NOT "pagi semua", "halo semua", etc)
	greetings := []string{"halo", "hai", "hi", "hello", "hey", "pagi", "siang", "sore", "malam", "selamat", "assalamualaikum"}
	if len(words) <= 3 {
		for _, word := range words {
			for _, greet := range greetings {
				if word == greet || strings.HasPrefix(word, greet) {
					// Exclude group greetings
					for _, w := range words {
						if w == "semua" || w == "all" || w == "guys" || w == "gaes" || w == "gais" || w == "kawan" || w == "teman" || w == "bro" || w == "sis" {
							return false
						}
					}
					return true
				}
			}
		}
	}

	return false
}

// GetConversationHistory returns the last 20 messages for a user
func GetConversationHistory(userID int64) []utils.GroqMessage {
	historyMutex.RLock()
	defer historyMutex.RUnlock()

	history, exists := conversationHistory[userID]
	if !exists {
		return []utils.GroqMessage{}
	}

	if len(history.Messages) > 20 {
		return history.Messages[len(history.Messages)-20:]
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

	if len(conversationHistory[userID].Messages) > 20 {
		conversationHistory[userID].Messages = conversationHistory[userID].Messages[len(conversationHistory[userID].Messages)-20:]
	}

	conversationHistory[userID].LastSeen = time.Now()
}

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

// detectFAQType detects what type of information is being asked
func detectFAQType(question string) string {
	question = strings.ToLower(question)

	if strings.Contains(question, "harga") || strings.Contains(question, "price") || strings.Contains(question, "berapa") && strings.Contains(question, "produk") {
		return "prices"
	}
	if strings.Contains(question, "produk") || strings.Contains(question, "product") || strings.Contains(question, "router") || strings.Contains(question, "mifi") || strings.Contains(question, "powerbank") {
		return "products"
	}
	if strings.Contains(question, "minimal penarikan") || strings.Contains(question, "min penarikan") || strings.Contains(question, "minimal withdraw") || strings.Contains(question, "min wd") {
		return "withdrawal_info"
	}
	if strings.Contains(question, "waktu penarikan") || strings.Contains(question, "jam penarikan") || strings.Contains(question, "withdrawal time") || strings.Contains(question, "jam wd") {
		return "withdrawal_time"
	}
	if strings.Contains(question, "cara daftar") || strings.Contains(question, "cara mendaftar") || strings.Contains(question, "register") || strings.Contains(question, "pendaftaran") || strings.Contains(question, "buat akun") {
		return "registration"
	}
	if strings.Contains(question, "cara penarikan") || strings.Contains(question, "cara withdraw") || strings.Contains(question, "cara wd") || strings.Contains(question, "cara tarik") {
		return "withdrawal_guide"
	}
	if strings.Contains(question, "cara beli") || strings.Contains(question, "cara pembelian") || strings.Contains(question, "beli produk") || strings.Contains(question, "pembelian") {
		return "purchase"
	}
	if strings.Contains(question, "profit tidak masuk") || strings.Contains(question, "profit gak masuk") || strings.Contains(question, "profit belum masuk") ||
		strings.Contains(question, "kenapa profit") || strings.Contains(question, "mengapa profit") || strings.Contains(question, "profit kok") ||
		strings.Contains(question, "profit locked") || strings.Contains(question, "profit terkunci") || strings.Contains(question, "profit terlock") ||
		strings.Contains(question, "keuntungan gak masuk") || strings.Contains(question, "keuntungan tidak masuk") {
		return "profit_router"
	}
	if strings.Contains(question, "deposit") || strings.Contains(question, "minimal deposit") || strings.Contains(question, "min deposit") || strings.Contains(question, "depo") {
		return "deposit"
	}
	if strings.Contains(question, "komisi") || strings.Contains(question, "referral") || strings.Contains(question, "undang") || strings.Contains(question, "ajak teman") {
		return "commission"
	}
	if strings.Contains(question, "vip") || strings.Contains(question, "level") {
		return "vip"
	}
	if strings.Contains(question, "event") || strings.Contains(question, "tiktok") || strings.Contains(question, "youtube") || strings.Contains(question, "upload") {
		return "event"
	}
	if strings.Contains(question, "berita") || strings.Contains(question, "news") || strings.Contains(question, "artikel") {
		return "news"
	}
	if strings.Contains(question, "task") || strings.Contains(question, "tugas") {
		return "task"
	}
	if strings.Contains(question, "spin") || strings.Contains(question, "hadiah") || strings.Contains(question, "prize") {
		return "spin"
	}
	if strings.Contains(question, "forum") || strings.Contains(question, "bukti penarikan") || strings.Contains(question, "bukti wd") {
		return "forum"
	}
	if strings.Contains(question, "bank") || strings.Contains(question, "rekening") {
		return "bank"
	}
	if strings.Contains(question, "tentang xinxun") || strings.Contains(question, "about") || strings.Contains(question, "apa itu xinxun") {
		return "about"
	}
	if strings.Contains(question, "lisensi") || strings.Contains(question, "legal") || strings.Contains(question, "sertifikat") || strings.Contains(question, "izin") {
		return "license"
	}
	if strings.Contains(question, "publisher") || strings.Contains(question, "menjadi publisher") || strings.Contains(question, "jadi publisher") {
		return "publisher"
	}
	if strings.Contains(question, "lupa password") || strings.Contains(question, "forgot password") || strings.Contains(question, "reset password") || strings.Contains(question, "ganti password") {
		return "forgot_password"
	}
	if strings.Contains(question, "jam berapa") || strings.Contains(question, "waktu sekarang") || strings.Contains(question, "jam sekarang") || strings.Contains(question, "pukul berapa") {
		return "current_time"
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
		return "Waktu penarikan: Senin-Sabtu, pukul 09:00-17:00 WIB. Di luar jam itu gak bisa proses ya."
	case "registration":
		return `CARA DAFTAR:
1. Buka https://xinxun.us/register
2. Isi nama, nomor HP, password (min 6 karakter)
3. Masukkan kode referral kalau ada
4. Klik Daftar
5. Dapat bonus Rp2.000 langsung!`
	case "withdrawal_guide":
		return `CARA WITHDRAW:
1. Pastikan saldo cukup & jam operasional (Senin-Sabtu, 09:00-17:00 WIB)
2. Buka menu Withdraw
3. Tambah rekening bank kalau belum ada
4. Masukkan nominal
5. Pilih rekening tujuan
6. Konfirmasi
Note: Cuma bisa 1x withdraw per hari`
	case "purchase":
		return `CARA BELI PRODUK:
1. Buka app Xinxun
2. Pilih menu Produk/Investasi
3. Pilih produk yang mau dibeli
4. Cek detail (harga, profit, durasi)
5. Pilih metode bayar (QRIS/VA)
6. Bayar sesuai instruksi
7. Produk langsung aktif setelah bayar`
	case "profit_router":
		return getProfitRouterInfo()
	case "deposit":
		return getDepositInfo()
	case "commission":
		return getCommissionInfo()
	case "vip":
		return getVIPInfo()
	case "event":
		return getEventInfo()
	case "news":
		return getNewsInfo()
	case "task":
		return getTaskInfo()
	case "spin":
		return getSpinInfo()
	case "forum":
		return "Forum bukti penarikan: https://xinxun.us/forum - Bisa liat semua bukti withdraw member lain di sini."
	case "bank":
		return "Maksimal 3 rekening bank yang bisa ditambahkan. Tambah rekening di: https://xinxun.us/bank/add"
	case "about":
		return getAboutXinxun()
	case "license":
		return getLicenseInfo()
	case "publisher":
		return "Mau jadi publisher news Xinxun? Setiap posting artikel dapat hadiah saldo. Daftar via CS @xinxun_forindo ya. Portal publisher: https://news.xinxun.us/publisher/login"
	case "forgot_password":
		return `RESET PASSWORD:
1. Buka https://xinxun.us/forgot-password
2. Masukkan nomor HP yang terdaftar
3. Dapat OTP via WhatsApp
4. Masukkan OTP & password baru
Gampang kan!`
	case "current_time":
		now := getWIBTime()
		return fmt.Sprintf("Sekarang jam %s WIB", now.Format("15:04"))
	default:
		return ""
	}
}

// getProductDataForAI returns product data formatted for AI context
func getProductDataForAI() string {
	db := database.DB
	var products []models.Product
	if err := db.Where("status = ?", "Active").Preload("Category").Order("category_id ASC, id ASC").Find(&products).Error; err != nil {
		return "Data produk lagi gak bisa diakses nih."
	}

	if len(products) == 0 {
		return "Belum ada produk yang tersedia."
	}

	var response strings.Builder
	response.WriteString("DAFTAR PRODUK XINXUN:\n\n")

	categoryMap := make(map[string][]models.Product)
	for _, product := range products {
		categoryName := "Umum"
		if product.Category != nil {
			categoryName = product.Category.Name
		}
		categoryMap[categoryName] = append(categoryMap[categoryName], product)
	}

	for categoryName, prods := range categoryMap {
		response.WriteString(fmt.Sprintf("[%s]\n", categoryName))
		for _, product := range prods {
			response.WriteString(fmt.Sprintf("• %s - Rp%.0f, profit Rp%.0f/hari, %d hari",
				product.Name, product.Amount, product.DailyProfit, product.Duration))
			if categoryName == "Router" {
				response.WriteString(" (VIP 0 bisa beli, profit dikumpulkan)")
			} else if product.RequiredVIP > 0 {
				response.WriteString(fmt.Sprintf(" (min VIP %d)", product.RequiredVIP))
			}
			if product.PurchaseLimit > 0 {
				response.WriteString(fmt.Sprintf(" [max %dx]", product.PurchaseLimit))
			}
			response.WriteString("\n")
		}
		response.WriteString("\n")
	}

	response.WriteString(`INFO PENTING ROUTER:
• Router fisik dikirim SETELAH kontrak selesai
• Profit router TIDAK masuk harian - dikumpulkan & dibayar sekaligus + modal pas kontrak selesai (70 hari)
• Semua Router bisa dibeli dari VIP 0 (user baru)
• Investasi Router = naik level VIP`)

	return response.String()
}

// getProfitRouterInfo returns information about router profit system
func getProfitRouterInfo() string {
	return `SISTEM PROFIT ROUTER:

Kalau beli produk ROUTER, profit TIDAK masuk setiap hari. Ini NORMAL, bukan error!

Cara kerjanya:
• Profit dikumpulkan selama kontrak (70 hari)
• Setelah kontrak selesai: Modal + Semua Profit dibayar SEKALIGUS
• Router fisik juga dikirim setelah kontrak selesai
• Total dapat = Modal + (Profit Harian × 70 hari)

Jadi kalau ada yang tanya "profit gak masuk" dan dia beli Router → jelasin ini sistemnya emang gitu, bukan bug!

Note: Cuma Router yang sistemnya gini. Produk lain (Mifi, Powerbank) profit masuk harian seperti biasa.`
}

// getDepositInfo returns deposit information
func getDepositInfo() string {
	return `DEPOSIT DI XINXUN:

PENTING: Xinxun GAK ADA menu deposit terpisah!

Cara "deposit" di Xinxun:
• Langsung pilih produk yang mau dibeli
• Bayar via QRIS atau Virtual Account
• Gak ada minimal deposit terpisah - tergantung harga produk yang dipilih
• Gak ada biaya deposit tambahan

Jadi: Deposit = Langsung investasi produk, bayar, selesai!

SARAN PRODUK UNTUK USER BARU:
• User baru (VIP 0) → Sarankan produk ROUTER dulu
• Semua Router bisa dibeli dari VIP 0
• Setelah invest Router, level VIP naik
• Baru bisa beli Mifi/Powerbank sesuai level VIP

JANGAN langsung sarankan Mifi/Powerbank ke user baru karena butuh VIP level tertentu!`
}

// getCommissionInfo returns information about referral commission
func getCommissionInfo() string {
	return `KOMISI REFERRAL:

• Dapat 30% langsung dari investasi teman yang kamu undang
• Contoh: Teman invest Rp100rb → kamu dapat Rp30rb
• Unlimited, gak ada batas maksimal
• Gak perlu invest dulu buat mulai ngundang

Link referral kamu: https://xinxun.us/referral`
}

// getVIPInfo returns information about VIP levels
func getVIPInfo() string {
	return `LEVEL VIP XINXUN:

• VIP 0 (Basic): Bisa beli semua Router - user baru mulai dari sini
• VIP 1 (Rp50rb): Unlock Mifi 1
• VIP 2 (Rp1.2jt): Unlock Mifi 2
• VIP 3 (Rp7jt): Unlock Mifi 3 + Powerbank
• VIP 4 (Rp30jt): Unlock Mifi 4
• VIP 5 (Rp150jt): Unlock semua produk

Cara naik VIP: Investasi di produk ROUTER (bukan Mifi/Powerbank)

Tips: Router kasih return total pas selesai + naikin level VIP. Makin tinggi level, makin banyak produk eksklusif!`
}

// getEventInfo returns information about social media event
func getEventInfo() string {
	return `EVENT UPLOAD SOSMED:

Buat konten promosi Xinxun di TikTok/YouTube, raih views, claim hadiahnya!

Hadiah:
• 20K views = Rp100rb
• 50K views = Rp300rb
• 100K views = Rp700rb
• 250K views = Rp1jt
• 500K views = Rp2jt

Syarat:
• Video original HD, bukan re-upload
• Gak boleh pakai bot/fake views
• Cantumkan link referral di bio/deskripsi
• Claim hadiah via CS @xinxun_forindo`
}

// getNewsInfo fetches and returns news from API
func getNewsInfo() string {
	resp, err := http.Get("https://api-news.xinxun.us/v1/xinxun/newest")
	if err != nil {
		return "Cek berita terbaru di: https://news.xinxun.us"
	}
	defer resp.Body.Close()

	var newsResponse struct {
		Success bool `json:"success"`
		Data    []struct {
			Title   string `json:"title"`
			Excerpt string `json:"excerpt"`
			Href    string `json:"href"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&newsResponse); err != nil {
		return "Cek berita terbaru di: https://news.xinxun.us"
	}

	if len(newsResponse.Data) == 0 {
		return "Belum ada berita baru. Cek di: https://news.xinxun.us"
	}

	var response strings.Builder
	response.WriteString("BERITA TERBARU:\n\n")

	maxNews := 3
	if len(newsResponse.Data) < maxNews {
		maxNews = len(newsResponse.Data)
	}

	for i := 0; i < maxNews; i++ {
		news := newsResponse.Data[i]
		response.WriteString(fmt.Sprintf("%d. %s\n%s\n\n", i+1, news.Title, news.Href))
	}

	response.WriteString("Berita lainnya: https://news.xinxun.us")

	return response.String()
}

// getTaskInfo returns information about tasks
func getTaskInfo() string {
	db := database.DB
	var tasks []models.Task
	if err := db.Where("status = ?", "Active").Order("required_level ASC").Find(&tasks).Error; err != nil {
		return "Cek tugas di: https://xinxun.us/referral"
	}

	if len(tasks) == 0 {
		return "Belum ada tugas. Cek di: https://xinxun.us/referral"
	}

	var response strings.Builder
	response.WriteString("DAFTAR TUGAS:\n\n")

	for _, task := range tasks {
		response.WriteString(fmt.Sprintf("• %s - Hadiah Rp%.0f (Level %d, %d member aktif)\n",
			task.Name, task.Reward, task.RequiredLevel, task.RequiredActiveMembers))
	}

	response.WriteString("\nDetail & claim: https://xinxun.us/referral")

	return response.String()
}

// getSpinInfo returns information about spin wheel
func getSpinInfo() string {
	return `SPIN WHEEL BERHADIAH:

Cara dapat tiket spin:
• Undang teman invest di atas Rp100rb → dapat tiket gratis
• Hadiah langsung masuk ke saldo

Main spin: https://xinxun.us/spin-wheel`
}

// getAboutXinxun returns information about Xinxun
func getAboutXinxun() string {
	return `TENTANG XINXUN:

Platform investasi properti #1 di Indonesia, berpusat di Dongguan, China.

Didirikan XinXun, Ltd dengan visi akses investasi properti premium untuk semua kalangan.

Fitur:
• Akses global untuk investor berbagai negara
• Aset properti bernilai tinggi
• Manajemen profesional
• Transparan & aman

Sertifikat: ECT2019E05006

Info lengkap: https://xinxun.us/about-us`
}

// getLicenseInfo returns information about licenses
func getLicenseInfo() string {
	return `LISENSI XINXUN:

Indonesia:
• OJK: PT Xdana Investa Indonesia
• Kominfo: Xinxun, Ltd

International:
• China: CSRC
• Hong Kong: SFC
• Singapore: MAS & GIC
• Malaysia: SC Malaysia
• Philippines: SEC
• Thailand: SEC
• Vietnam: MPI

Detail: https://xinxun.us/licenses`
}

// getWithdrawalInfoForAI returns withdrawal information formatted for AI context
func getWithdrawalInfoForAI() string {
	sqlDB, err := database.DB.DB()
	if err != nil {
		return "Info withdraw lagi gak bisa diakses."
	}

	setting, err := models.GetSetting(sqlDB)
	if err != nil {
		return "Info withdraw lagi gak bisa diakses."
	}

	return fmt.Sprintf(`INFO PENARIKAN:
• Minimal: Rp%.0f
• Maksimal: Rp%.0f
• Biaya admin: Rp%.0f
• Jam: Senin-Sabtu, 09:00-17:00 WIB
• Batas: 1x per hari`,
		setting.MinWithdraw, setting.MaxWithdraw, setting.WithdrawCharge)
}

// isValidName checks if a name looks like a real person's name
func isValidName(name string) bool {
	if len(name) < 2 || len(name) > 50 {
		return false
	}

	name = strings.ToLower(strings.TrimSpace(name))

	fakeNames := []string{
		"user", "test", "admin", "bot", "cs", "customer", "service",
		"xinxun", "member", "guest", "anonymous", "unknown", "null", "undefined",
	}

	for _, fake := range fakeNames {
		if name == fake {
			return false
		}
	}

	hasLetter := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
	}

	return hasLetter
}

// getUserDisplayName returns user's display name for greeting
func getUserDisplayName(user *struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}) string {
	if user == nil {
		return ""
	}

	firstName := strings.TrimSpace(user.FirstName)
	if firstName == "" {
		firstName = user.Username
	}

	if isValidName(firstName) {
		return firstName
	}

	return ""
}

// buildSystemPrompt creates the AI system prompt
func buildSystemPrompt(userName string, userMessage string, messageType string, userGreeting string, contextData string) string {
	now := getWIBTime()
	timeGreeting := getTimeGreeting()

	// Build prompt
	prompt := fmt.Sprintf(`Kamu CS Xinxun yang santai, friendly, dan helpful. Chat kayak temen biasa, bukan robot.

===== INFO =====
Waktu: %s WIB (%s, %d %s %d)
Sapaan waktu: %s
User: %s
Tipe pesan: %s
`, now.Format("15:04"), getDayName(now.Weekday()), now.Day(), getMonthName(now.Month()), now.Year(), timeGreeting, func() string {
		if userName != "" {
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

	prompt += `
===== CARA JAWAB =====

GAYA BAHASA:
• Santai, gaul, kayak chat sama temen
• Pakai: "nih", "sih", "dong", "deh", "ya", "gitu", "aja", "banget", "kali", "coba"
• Emoji secukupnya (1-3 aja), jangan lebay
• JANGAN: "gue/lo", terlalu formal, kaku, template robot

SAPAAN:
• Nama valid → panggil "Kak [nama]" atau langsung nama aja
• Nama gak valid/kosong → langsung jawab tanpa sapaan
• JANGAN "Bro" kalau udah tau nama asli

GREETING (PENTING!):
• User bilang "pagi" → bales "Pagi!" atau "Pagi juga!"
• User bilang "malam" → bales "Malam!" atau "Malam juga!"
• User bilang "halo" → bales "Halo!" atau "Hai!"
• User bilang "assalamualaikum" → bales "Waalaikumsalam!"
• IKUTIN sapaan user, JANGAN ganti berdasarkan waktu sekarang!
• Contoh SALAH: User bilang "pagi" jam 1 malam → jawab "Malam!" (INI SALAH!)
• Contoh BENAR: User bilang "pagi" jam 1 malam → jawab "Pagi! Belum tidur nih?" (IKUTIN greeting user)

PANJANG JAWABAN:
• Greeting/simple → 1-2 kalimat, santai aja
• Pertanyaan biasa → 2-4 kalimat, to the point
• Keluhan/masalah → empati + solusi, max 5-6 kalimat
• Pertanyaan kompleks → max 8 kalimat, pakai bullet kalau perlu

HANDLE BERDASARKAN TIPE:
• greeting → bales santai, bisa tanya kabar/ada apa
• question → jawab langsung, kasih info yang diminta
• complaint → empati dulu ("wah sorry ya.."), baru kasih solusi
• urgent → prioritas tinggi, arahkan ke CS @xinxun_forindo kalau perlu
• scam_alert → warning keras, kasih info CS resmi
• thanks → "Sama-sama!", "Siap!", "Yoi!"
• confirmation → lanjutin konteks pembicaraan sebelumnya

KONTEKS PERCAKAPAN:
• Baca history chat, pahami alurnya
• Kalau user jawab pertanyaan kamu sebelumnya → LANJUTKAN, jangan ulang
• Kalau user konfirmasi/jawab singkat → respond sesuai konteks

ANTI NGACO:
• Gak tau jawabannya → bilang "Wah kurang tau nih, coba langsung tanya CS @xinxun_forindo ya"
• JANGAN ngarang info yang gak ada di data
• JANGAN ngarang harga/produk/fitur

SCAM WARNING (sampaikan kalau relevan):
• CS resmi CUMA @xinxun_forindo
• Xinxun GAK PERNAH minta password/OTP
• Pembayaran CUMA via QRIS/VA resmi, bukan transfer ke rekening pribadi
• Ada yang minta transfer ke rekening pribadi = PENIPUAN

FORMAT (PENTING!):
• Telegram pakai HTML, BUKAN Markdown!
• Bold: <b>teks</b> (JANGAN **teks**)
• Italic: <i>teks</i> (JANGAN *teks*)
• Code: <code>teks</code> (JANGAN pakai backtick untuk code)
• JANGAN pakai ** atau * untuk formatting!
• Contoh BENAR: <b>Router 1</b> - Rp50.000
• Contoh SALAH: **Router 1** - Rp50.000

===== LINK PENTING =====
• Register: https://xinxun.us/register
• Login: https://xinxun.us/login
• Dashboard: https://xinxun.us/dashboard
• Withdraw: https://xinxun.us/withdraw
• Referral: https://xinxun.us/referral
• Spin: https://xinxun.us/spin-wheel
• Forum: https://xinxun.us/forum
• News: https://news.xinxun.us
• CS Resmi: @xinxun_forindo
• Grup: https://t.me/+R4rZNjqcQ9FhMDRl

===== INGAT =====
Kamu temen yang kebetulan kerja di Xinxun. Bantuin dengan santai, jangan kayak robot customer service yang kaku dan template. Tiap jawaban harus natural dan sesuai konteks chat.`

	return prompt
}

// getDayName returns Indonesian day name
func getDayName(weekday time.Weekday) string {
	days := []string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	return days[weekday]
}

// sanitizeResponse cleans and converts AI response for Telegram
// Converts Markdown formatting to HTML and fixes common issues
func sanitizeResponse(text string) string {
	// Step 1: Convert Markdown bold **text** to HTML <b>text</b>
	result := convertMarkdownBoldToHTML(text)

	// Step 2: Convert Markdown italic *text* to HTML <i>text</i>
	result = convertMarkdownItalicToHTML(result)

	// Step 3: Convert Markdown code `text` to HTML <code>text</code>
	result = convertMarkdownCodeToHTML(result)

	// Step 4: Remove any remaining standalone asterisks
	result = cleanupAsterisks(result)

	// Step 5: Fix spacing issues around HTML tags
	result = fixHTMLSpacing(result)

	return result
}

// convertMarkdownBoldToHTML converts **text** to <b>text</b>
func convertMarkdownBoldToHTML(text string) string {
	var result strings.Builder
	i := 0

	for i < len(text) {
		// Check for ** pattern
		if i+1 < len(text) && text[i] == '*' && text[i+1] == '*' {
			// Find closing **
			endIdx := strings.Index(text[i+2:], "**")
			if endIdx != -1 {
				content := text[i+2 : i+2+endIdx]
				if len(strings.TrimSpace(content)) > 0 && !strings.Contains(content, "\n") {
					result.WriteString("<b>")
					result.WriteString(strings.TrimSpace(content))
					result.WriteString("</b>")
					i = i + 2 + endIdx + 2
					continue
				}
			}
		}
		result.WriteByte(text[i])
		i++
	}

	return result.String()
}

// convertMarkdownItalicToHTML converts *text* to <i>text</i>
func convertMarkdownItalicToHTML(text string) string {
	var result strings.Builder
	i := 0

	for i < len(text) {
		if text[i] == '*' {
			// Skip if part of **
			if i+1 < len(text) && text[i+1] == '*' {
				result.WriteByte(text[i])
				i++
				continue
			}

			// Find closing single *
			endIdx := -1
			for j := i + 1; j < len(text); j++ {
				if text[j] == '*' {
					if j+1 < len(text) && text[j+1] == '*' {
						continue
					}
					endIdx = j
					break
				}
				if text[j] == '\n' {
					break
				}
			}

			if endIdx != -1 && endIdx > i+1 {
				content := text[i+1 : endIdx]
				if len(strings.TrimSpace(content)) > 0 {
					result.WriteString("<i>")
					result.WriteString(strings.TrimSpace(content))
					result.WriteString("</i>")
					i = endIdx + 1
					continue
				}
			}
		}
		result.WriteByte(text[i])
		i++
	}

	return result.String()
}

// convertMarkdownCodeToHTML converts `text` to <code>text</code>
func convertMarkdownCodeToHTML(text string) string {
	var result strings.Builder
	i := 0

	for i < len(text) {
		if text[i] == '`' {
			endIdx := strings.Index(text[i+1:], "`")
			if endIdx != -1 {
				content := text[i+1 : i+1+endIdx]
				if len(content) > 0 && !strings.Contains(content, "\n") {
					result.WriteString("<code>")
					result.WriteString(content)
					result.WriteString("</code>")
					i = i + 1 + endIdx + 1
					continue
				}
			}
		}
		result.WriteByte(text[i])
		i++
	}

	return result.String()
}

// cleanupAsterisks removes standalone asterisks that weren't converted
func cleanupAsterisks(text string) string {
	lines := strings.Split(text, "\n")
	var cleanedLines []string

	for _, line := range lines {
		cleaned := line

		// Remove orphaned ** at start
		if strings.HasPrefix(cleaned, "**") && strings.Count(cleaned, "**") == 1 {
			cleaned = strings.TrimPrefix(cleaned, "**")
		}

		// Remove orphaned ** at end
		if strings.HasSuffix(cleaned, "**") && strings.Count(cleaned, "**") == 1 {
			cleaned = strings.TrimSuffix(cleaned, "**")
		}

		cleanedLines = append(cleanedLines, cleaned)
	}

	return strings.Join(cleanedLines, "\n")
}

// fixHTMLSpacing ensures proper spacing around HTML tags
func fixHTMLSpacing(text string) string {
	result := text

	replacements := []struct {
		old string
		new string
	}{
		{"-<b>", "- <b>"},
		{"•<b>", "• <b>"},
		{":<b>", ": <b>"},
		{"(<b>", "(<b>"},
		{"</b>-", "</b> -"},
		{"</b>:", "</b>:"},
		{"-<i>", "- <i>"},
		{"•<i>", "• <i>"},
		{"</i>-", "</i> -"},
	}

	for _, r := range replacements {
		result = strings.ReplaceAll(result, r.old, r.new)
	}

	return result
}

// getMonthName returns Indonesian month name
func getMonthName(month time.Month) string {
	months := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
	return months[month]
}

// CSBotWebhookHandler handles Telegram webhook updates
func CSBotWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var update TelegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("[TG] Invalid request body: %v", err)
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
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get message details
	userMessage := strings.TrimSpace(update.Message.Text)
	chatID := update.Message.Chat.ID
	messageID := update.Message.MessageID
	userName := getUserDisplayName(update.Message.From)

	// Log incoming message
	log.Printf("[TG] User %d (%s) in chat %d: %s", userID, userName, chatID, userMessage)

	// Get conversation history
	history := GetConversationHistory(userID)

	// Detect message type and user greeting
	messageType := detectMessageType(userMessage)
	userGreeting := detectUserGreeting(userMessage)

	// Get relevant context data
	var contextData string
	if faqType := detectFAQType(userMessage); faqType != "" {
		contextData = getContextData(faqType)
	}

	// Build system prompt
	systemPrompt := buildSystemPrompt(userName, userMessage, messageType, userGreeting, contextData)

	// Build messages for AI
	messages := append(history, utils.GroqMessage{
		Role:    "user",
		Content: userMessage,
	})

	// Call Groq API
	response, err := utils.CallGroqAPI(messages, systemPrompt)
	if err != nil {
		log.Printf("[TG] Groq API error: %v", err)
		errorMsg := "Aduh maaf, lagi error nih sistemnya 😅 Coba lagi bentar ya, atau langsung chat CS @xinxun_forindo"
		if err := SendTelegramMessage(chatID, errorMsg, messageID); err != nil {
			log.Printf("[TG] Error sending error message: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Clean response
	response = strings.TrimSpace(response)

	// Skip empty or "skip" responses
	responseLower := strings.ToLower(response)
	if response == "" || responseLower == "skip" || strings.HasPrefix(responseLower, "skip") {
		log.Printf("[TG] AI decided to skip response")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Sanitize response: convert Markdown to HTML for Telegram
	response = sanitizeResponse(response)

	// Send response
	if err := SendTelegramMessage(chatID, response, messageID); err != nil {
		log.Printf("[TG] Error sending message: %v", err)
	} else {
		log.Printf("[TG] Bot response to %d: %s", userID, response)
	}

	// Update conversation history
	AddToConversationHistory(userID, "user", userMessage)
	AddToConversationHistory(userID, "assistant", response)

	w.WriteHeader(http.StatusOK)
}
