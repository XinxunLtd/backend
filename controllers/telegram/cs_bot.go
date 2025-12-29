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

// ConversationHistory stores messages per user
type ConversationHistory struct {
	Messages []utils.GroqMessage
	LastSeen time.Time
}

// RateLimiter tracks user rate limits
type RateLimiter struct {
	mu       sync.RWMutex
	lastCall map[int64]time.Time
}

var (
	conversationHistory = make(map[int64]*ConversationHistory)
	historyMutex        sync.RWMutex
	rateLimiter         = NewRateLimiter()
	allowedGroupIDs     []int64
)

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

func init() {
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
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
	ReplyToID int64  `json:"reply_to_message_id,omitempty"`
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

	return nil
}

// getWIBTime returns current time in WIB
func getWIBTime() time.Time {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.FixedZone("WIB", 7*60*60)
	}
	return time.Now().In(loc)
}

// getTimeGreeting returns appropriate greeting based on hour
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

// getUserName returns user's display name
func getUserName(user *struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(user.FirstName)
	if name == "" {
		name = user.Username
	}
	// Filter out obviously fake names
	nameLower := strings.ToLower(name)
	fakeNames := []string{"user", "test", "admin", "bot", "member", "guest"}
	for _, fake := range fakeNames {
		if nameLower == fake {
			return ""
		}
	}
	return name
}

// ShouldRespond checks if bot should respond to the message
func ShouldRespond(update *TelegramUpdate) bool {
	if update.Message == nil || update.Message.From == nil || update.Message.From.IsBot {
		return false
	}

	text := strings.TrimSpace(update.Message.Text)
	if text == "" || strings.HasPrefix(text, "/") {
		return false
	}

	chatType := update.Message.Chat.Type

	// Private chat - always respond
	if chatType == "private" {
		return true
	}

	// Group chat
	if chatType == "group" || chatType == "supergroup" {
		// Check allowed groups
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
		return isMessageForBot(update, text)
	}

	return false
}

// isMessageForBot determines if message is directed to bot
func isMessageForBot(update *TelegramUpdate, text string) bool {
	textLower := strings.ToLower(text)

	// Check bot mention
	botUsername := strings.ToLower(strings.TrimPrefix(os.Getenv("TELEGRAM_BOT_USERNAME"), "@"))
	if botUsername != "" && strings.Contains(textLower, "@"+botUsername) {
		return true
	}

	// Reply to bot
	if update.Message.ReplyToMessage != nil {
		if update.Message.ReplyToMessage.From != nil && update.Message.ReplyToMessage.From.IsBot {
			return true
		}
	}

	// Check for bot trigger words
	botTriggers := []string{
		"bot", "cs", "admin", "min", "xinxun",
		"bantuan", "help", "tolong",
	}
	for _, trigger := range botTriggers {
		if strings.Contains(textLower, trigger) {
			return true
		}
	}

	// Check if it's a Xinxun-related question
	xinxunKeywords := []string{
		"produk", "harga", "profit", "penarikan", "withdraw", "tarik",
		"investasi", "router", "mifi", "powerbank", "daftar", "beli",
		"deposit", "komisi", "referral", "vip", "level", "task",
		"spin", "event", "saldo", "bonus", "kontrak", "durasi",
		"error", "gagal", "pending", "gak bisa", "tidak bisa", "kenapa",
		"gimana", "cara", "bagaimana",
	}

	for _, keyword := range xinxunKeywords {
		if strings.Contains(textLower, keyword) {
			return true
		}
	}

	// Check for questions
	if strings.Contains(text, "?") {
		return true
	}

	// Check conversation history - if bot asked question, this might be answer
	history := GetConversationHistory(update.Message.From.ID)
	if len(history) > 0 {
		lastMsg := history[len(history)-1]
		if lastMsg.Role == "assistant" && strings.Contains(lastMsg.Content, "?") {
			return true
		}
	}

	// Simple greetings to bot (not "pagi semua", "halo semua")
	greetings := []string{"halo", "hai", "hi", "hello", "pagi", "siang", "sore", "malam"}
	words := strings.Fields(textLower)
	if len(words) <= 2 {
		for _, greet := range greetings {
			if textLower == greet || (len(words) == 2 && words[0] == greet) {
				// Exclude "pagi semua", "halo semua" etc
				if len(words) == 2 && (words[1] == "semua" || words[1] == "all" || words[1] == "guys") {
					return false
				}
				return true
			}
		}
	}

	return false
}

// GetConversationHistory returns conversation history for user
func GetConversationHistory(userID int64) []utils.GroqMessage {
	historyMutex.RLock()
	defer historyMutex.RUnlock()

	history, exists := conversationHistory[userID]
	if !exists {
		return []utils.GroqMessage{}
	}

	maxMessages := 15
	if len(history.Messages) > maxMessages {
		return history.Messages[len(history.Messages)-maxMessages:]
	}
	return history.Messages
}

// AddToConversationHistory adds message to history
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

	// Keep last 15 messages
	if len(conversationHistory[userID].Messages) > 15 {
		conversationHistory[userID].Messages = conversationHistory[userID].Messages[len(conversationHistory[userID].Messages)-15:]
	}

	conversationHistory[userID].LastSeen = time.Now()
}

// detectMessageType detects what kind of message this is
func detectMessageType(text string) string {
	textLower := strings.ToLower(text)
	words := strings.Fields(textLower)

	// Simple greeting (1-2 words)
	greetings := []string{"halo", "hai", "hi", "hello", "pagi", "siang", "sore", "malam", "selamat"}
	if len(words) <= 3 {
		for _, word := range words {
			for _, greet := range greetings {
				if word == greet {
					return "greeting"
				}
			}
		}
	}

	// Complaint/Problem
	complaintWords := []string{
		"error", "gagal", "pending", "gak bisa", "tidak bisa", "gabisa",
		"kenapa", "masalah", "problem", "issue", "bug", "stuck",
		"hilang", "kehilangan", "dicuri", "hack", "scam", "tipu", "penipuan",
		"lama", "lambat", "belum masuk", "gak masuk", "tidak masuk",
	}
	for _, word := range complaintWords {
		if strings.Contains(textLower, word) {
			return "complaint"
		}
	}

	// Question
	questionWords := []string{
		"gimana", "bagaimana", "cara", "apa", "berapa", "kapan", "dimana",
		"siapa", "mengapa", "kenapa", "bisa", "boleh", "apakah",
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
	thanksWords := []string{"makasih", "terima kasih", "thanks", "thank you", "thx", "tq"}
	for _, word := range thanksWords {
		if strings.Contains(textLower, word) {
			return "thanks"
		}
	}

	return "general"
}

// getContextData returns relevant context based on message
func getContextData(text string) string {
	textLower := strings.ToLower(text)
	var contexts []string

	// Product info
	if containsAny(textLower, []string{"produk", "harga", "router", "mifi", "powerbank", "beli", "investasi"}) {
		contexts = append(contexts, getProductData())
	}

	// Withdrawal
	if containsAny(textLower, []string{"penarikan", "withdraw", "tarik", "wd"}) {
		contexts = append(contexts, getWithdrawalData())
	}

	// Profit router
	if containsAny(textLower, []string{"profit", "keuntungan"}) && containsAny(textLower, []string{"router", "gak masuk", "tidak masuk", "belum", "locked", "terkunci"}) {
		contexts = append(contexts, getProfitRouterData())
	}

	// VIP
	if containsAny(textLower, []string{"vip", "level"}) {
		contexts = append(contexts, getVIPData())
	}

	// Registration
	if containsAny(textLower, []string{"daftar", "register", "registrasi", "buat akun"}) {
		contexts = append(contexts, getRegistrationData())
	}

	// Deposit - IMPORTANT: No separate deposit
	if containsAny(textLower, []string{"deposit", "depo", "isi saldo", "top up"}) {
		contexts = append(contexts, getDepositData())
	}

	// Commission
	if containsAny(textLower, []string{"komisi", "referral", "undang", "ajak"}) {
		contexts = append(contexts, getCommissionData())
	}

	// Forgot password
	if containsAny(textLower, []string{"lupa password", "forgot password", "reset password", "ganti password"}) {
		contexts = append(contexts, "Reset password: https://xinxun.us/forgot-password → masukkan nomor → OTP via WA → ganti password baru")
	}

	if len(contexts) == 0 {
		return ""
	}

	return strings.Join(contexts, "\n\n")
}

func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func getProductData() string {
	db := database.DB
	var products []models.Product
	if err := db.Where("status = ?", "Active").Preload("Category").Order("category_id ASC, amount ASC").Find(&products).Error; err != nil {
		return ""
	}

	if len(products) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("DAFTAR PRODUK:\n")

	categoryMap := make(map[string][]models.Product)
	for _, p := range products {
		cat := "Lainnya"
		if p.Category != nil {
			cat = p.Category.Name
		}
		categoryMap[cat] = append(categoryMap[cat], p)
	}

	for cat, prods := range categoryMap {
		sb.WriteString(fmt.Sprintf("\n[%s]\n", cat))
		for _, p := range prods {
			sb.WriteString(fmt.Sprintf("• %s: Rp%.0f, profit Rp%.0f/hari, %d hari", p.Name, p.Amount, p.DailyProfit, p.Duration))
			if cat == "Router" {
				sb.WriteString(" (VIP 0 bisa beli, profit dikumpulkan)")
			} else if p.RequiredVIP > 0 {
				sb.WriteString(fmt.Sprintf(" (min VIP %d)", p.RequiredVIP))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\nPENTING ROUTER: Profit router TIDAK masuk harian, tapi dikumpulkan & dibayar sekaligus + modal saat kontrak selesai (70 hari). Ini NORMAL, bukan error!")

	return sb.String()
}

func getWithdrawalData() string {
	sqlDB, err := database.DB.DB()
	if err != nil {
		return ""
	}
	setting, err := models.GetSetting(sqlDB)
	if err != nil {
		return ""
	}

	return fmt.Sprintf(`PENARIKAN:
• Minimal: Rp%.0f
• Maksimal: Rp%.0f
• Biaya admin: Rp%.0f
• Jam: Senin-Sabtu, 09:00-17:00 WIB
• Batas: 1x per hari
• Cara: Menu Withdraw → pilih bank → masukkan nominal → konfirmasi`, setting.MinWithdraw, setting.MaxWithdraw, setting.WithdrawCharge)
}

func getProfitRouterData() string {
	return `PROFIT ROUTER (PENTING!):
Router punya sistem profit BERBEDA dari produk lain:
• Profit TIDAK masuk setiap hari (ini NORMAL!)
• Profit dikumpulkan selama 70 hari
• Setelah kontrak selesai: Modal + Semua Profit dibayar SEKALIGUS
• Router fisik juga dikirim setelah kontrak selesai

Jadi kalau beli router dan profit "tidak masuk" → itu BUKAN error, memang sistemnya begitu!`
}

func getVIPData() string {
	return `LEVEL VIP:
• VIP 0: Bisa beli semua Router (user baru mulai dari sini)
• VIP 1 (Rp50rb): Unlock Mifi 1
• VIP 2 (Rp1.2jt): Unlock Mifi 2  
• VIP 3 (Rp7jt): Unlock Mifi 3 + Powerbank
• VIP 4 (Rp30jt): Unlock Mifi 4
• VIP 5 (Rp150jt): Unlock semua

Naik VIP: Investasi di produk ROUTER (bukan Mifi/Powerbank)`
}

func getRegistrationData() string {
	return `CARA DAFTAR:
1. Buka https://xinxun.us/register
2. Isi nama, nomor HP, password (min 6 karakter)
3. Masukkan kode referral (kalau ada)
4. Klik Daftar
5. Dapat bonus Rp2.000!`
}

func getDepositData() string {
	return `DEPOSIT DI XINXUN:
⚠️ PENTING: Xinxun TIDAK ada menu deposit terpisah!

Cara "deposit":
• Langsung pilih produk yang mau dibeli
• Bayar via QRIS atau Virtual Account
• Tidak ada minimal deposit - tergantung harga produk

Jadi deposit = langsung investasi produk, bayar, selesai!`
}

func getCommissionData() string {
	return `KOMISI REFERRAL:
• Dapat 30% dari investasi teman yang kamu undang
• Contoh: Teman invest Rp100rb → kamu dapat Rp30rb
• Unlimited, gak ada batas maksimal
• Link referral: https://xinxun.us/referral`
}

// buildSystemPrompt creates the AI system prompt
func buildSystemPrompt(userName string, messageType string, contextData string) string {
	now := getWIBTime()
	timeGreeting := getTimeGreeting()

	prompt := fmt.Sprintf(`Kamu CS Xinxun yang santai & friendly. Jawab kayak chat WA sama temen.

INFO WAKTU:
- Sekarang: %s WIB (%s)
- Sapaan yang tepat: "%s"

USER: %s

`, now.Format("15:04"), now.Format("Monday, 2 Jan 2006"), timeGreeting, func() string {
		if userName != "" {
			return userName
		}
		return "(tidak diketahui)"
	}())

	// Add context if available
	if contextData != "" {
		prompt += fmt.Sprintf(`DATA RELEVAN:
%s

`, contextData)
	}

	prompt += `ATURAN WAJIB:

1. GAYA BAHASA:
   - Santai, gaul, kayak temen chat
   - Pakai: "nih", "sih", "dong", "ya", "gitu", "aja", "banget"
   - JANGAN: "gue/lo", formal, kaku, robot
   - Emoji secukupnya (1-3 per pesan), jangan lebay
   - JANGAN template kayak "Ada yang bisa dibantu? 🤔" terus-terusan

2. SAPAAN:
   - Kalau nama valid: "Kak [nama]" atau langsung nama
   - Kalau nama gak valid/kosong: skip aja, langsung jawab
   - JANGAN "Bro" kalau udah tau namanya

3. GREETING:
   - User bilang "pagi" → bales "Pagi!" atau "Pagi juga!" (SESUAIKAN sama greeting mereka, bukan waktu sekarang)
   - User bilang "halo" → bales "Halo!" atau "Hai!"
   - JANGAN "Selamat malam" kalau user bilang "pagi" (ikutin sapaan user!)
   - Bisa tambahin "Ada apa nih?" atau "Gimana kabarnya?" tapi jangan template

4. JAWAB SESUAI TIPE:
   - Greeting → bales singkat, friendly, bisa tanya kabar
   - Pertanyaan → jawab langsung, to the point
   - Keluhan/masalah → empati dulu, baru solusi
   - Makasih → "Sama-sama!" atau "Siap!"

5. PANJANG JAWABAN:
   - Greeting/simple: 1-2 kalimat
   - Pertanyaan biasa: 2-4 kalimat
   - Masalah kompleks: max 6-8 kalimat
   - JANGAN kepanjangan kalau gak perlu

6. KONTEKS CHAT:
   - Baca history chat, pahami konteksnya
   - Kalau user jawab pertanyaan kamu sebelumnya, LANJUTKAN percakapan
   - JANGAN ulang pertanyaan yang sama

7. ANTI NGACO:
   - Kalau gak tau, bilang "Wah kurang tau nih, coba tanya CS langsung ya @xinxun_forindo"
   - JANGAN ngarang info yang gak ada di data
   - JANGAN ngarang harga/produk

8. SCAM WARNING (kalau relevan):
   - CS resmi CUMA @xinxun_forindo
   - Xinxun GAK PERNAH minta password/OTP
   - Pembayaran CUMA via QRIS/VA resmi

9. FORMAT:
   - Pakai HTML: <b>bold</b> untuk penekanan
   - Jangan berlebihan, secukupnya aja

LINK PENTING:
- Register: https://xinxun.us/register
- Login: https://xinxun.us/login  
- Dashboard: https://xinxun.us/dashboard
- Withdraw: https://xinxun.us/withdraw
- Referral: https://xinxun.us/referral
- CS: @xinxun_forindo
- Grup: https://t.me/+R4rZNjqcQ9FhMDRl

INGAT: Kamu temen yang kebetulan kerja di Xinxun, bukan robot customer service!`

	return prompt
}

// CSBotWebhookHandler handles Telegram webhook updates
func CSBotWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var update TelegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("[TG] Invalid request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Check if should respond
	if !ShouldRespond(&update) {
		w.WriteHeader(http.StatusOK)
		return
	}

	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID
	messageID := update.Message.MessageID
	userMessage := strings.TrimSpace(update.Message.Text)
	userName := getUserName(update.Message.From)

	// Log incoming message
	log.Printf("[TG] User %d (%s): %s", userID, userName, userMessage)

	// Rate limit
	if !rateLimiter.CanProceed(userID) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get conversation history
	history := GetConversationHistory(userID)

	// Detect message type and get context
	messageType := detectMessageType(userMessage)
	contextData := getContextData(userMessage)

	// Build system prompt
	systemPrompt := buildSystemPrompt(userName, messageType, contextData)

	// Build messages for AI
	messages := append(history, utils.GroqMessage{
		Role:    "user",
		Content: userMessage,
	})

	// Call AI
	response, err := utils.CallGroqAPI(messages, systemPrompt)
	if err != nil {
		log.Printf("[TG] AI error: %v", err)
		errorMsg := "Aduh maaf, lagi error nih sistemnya 😅 Coba lagi bentar ya, atau langsung chat @xinxun_forindo aja!"
		SendTelegramMessage(chatID, errorMsg, messageID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Clean response
	response = strings.TrimSpace(response)
	if response == "" || strings.ToLower(response) == "skip" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Send response
	if err := SendTelegramMessage(chatID, response, messageID); err != nil {
		log.Printf("[TG] Send error: %v", err)
	} else {
		log.Printf("[TG] Bot response: %s", response)
	}

	// Save to history
	AddToConversationHistory(userID, "user", userMessage)
	AddToConversationHistory(userID, "assistant", response)

	w.WriteHeader(http.StatusOK)
}
