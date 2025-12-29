# Setup Telegram CS Bot

Dokumentasi untuk setup bot Customer Service Telegram menggunakan AI Groq.

## Fitur

- ✅ FAQ-based responses - jawab otomatis pertanyaan umum
- ✅ AI-powered - integrasi Groq API dengan model Llama
- ✅ Smart trigger - respons kalau di-mention, di-reply, ada "?", atau ada kata "min/admin/cs"
- ✅ Rate limiting - anti-spam, 1 response per 5 detik per user
- ✅ Conversation history - ingat konteks 10 pesan terakhir
- ✅ Database access terbatas - hanya akses informasi produk, minimal penarikan, waktu penarikan, dll
- ✅ Group-only - bot hanya merespons di grup yang di-set, mengabaikan chat private

## Prerequisites

1. **Telegram Bot Token**
   - Buka [@BotFather](https://t.me/BotFather) di Telegram
   - Kirim `/newbot` dan ikuti instruksi
   - Simpan bot token yang diberikan

2. **Groq API Key**
   - Daftar di [Groq Console](https://console.groq.com/)
   - Buat API key baru
   - Simpan API key

3. **Bot Username**
   - Setelah membuat bot, catat username bot (contoh: `@xinxun_cs_bot`)

## Environment Variables

Tambahkan variabel berikut ke file `.env` atau environment:

```env
# Telegram Bot Configuration
TELEGRAM_BOT_TOKEN=your_bot_token_here
TELEGRAM_BOT_USERNAME=xinxun_cs_bot
TELEGRAM_ALLOWED_GROUP_IDS=-1001234567890,-1001234567891

# Groq API Configuration
GROQ_API_KEY=your_groq_api_key_here
GROQ_MODEL=llama-3.1-70b-versatile
```

### Penjelasan Environment Variables

- `TELEGRAM_BOT_TOKEN`: Token bot dari BotFather
- `TELEGRAM_BOT_USERNAME`: Username bot (tanpa @)
- `TELEGRAM_ALLOWED_GROUP_IDS`: ID grup yang diizinkan (pisahkan dengan koma). Kosongkan jika ingin semua grup bisa menggunakan bot
- `GROQ_API_KEY`: API key dari Groq
- `GROQ_MODEL`: Model Groq yang digunakan (default: `llama-3.1-70b-versatile`)

## Cara Mendapatkan Group ID

1. Tambahkan bot ke grup
2. Kirim pesan di grup
3. Buka URL: `https://api.telegram.org/bot<BOT_TOKEN>/getUpdates`
4. Cari `"chat":{"id":-1001234567890}` - angka tersebut adalah Group ID
5. Atau gunakan bot seperti [@userinfobot](https://t.me/userinfobot) untuk mendapatkan ID grup

## Setup Webhook

Setelah aplikasi berjalan, setup webhook Telegram:

```bash
curl -X POST "https://api.telegram.org/bot<BOT_TOKEN>/setWebhook" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://your-domain.com/v3/telegram/webhook"
  }'
```

Atau gunakan script berikut (ganti dengan domain Anda):

```bash
# Windows PowerShell
$BOT_TOKEN = "your_bot_token"
$WEBHOOK_URL = "https://your-domain.com/v3/telegram/webhook"

Invoke-RestMethod -Uri "https://api.telegram.org/bot$BOT_TOKEN/setWebhook" `
  -Method Post `
  -ContentType "application/json" `
  -Body (@{
    url = $WEBHOOK_URL
  } | ConvertTo-Json)
```

## Testing

1. Tambahkan bot ke grup Telegram
2. Pastikan grup ID sudah ditambahkan ke `TELEGRAM_ALLOWED_GROUP_IDS` (atau kosongkan untuk semua grup)
3. Test dengan trigger berikut:
   - Mention bot: `@xinxun_cs_bot harga produk`
   - Reply ke pesan bot
   - Tanya dengan tanda tanya: `Berapa minimal penarikan?`
   - Gunakan kata trigger: `min admin, cara daftar?`

## FAQ yang Didukung

Bot akan otomatis menjawab pertanyaan tentang:

- **Harga produk**: "harga", "price", "produk", "product"
- **Detail produk**: "detail produk", "penjelasan produk"
- **Minimal penarikan**: "minimal penarikan", "min penarikan", "minimal withdraw"
- **Waktu penarikan**: "waktu penarikan", "jam penarikan", "withdrawal time"
- **Cara daftar**: "cara daftar", "cara mendaftar", "register", "pendaftaran"
- **Cara penarikan**: "cara penarikan", "cara withdraw", "withdraw"
- **Cara pembelian**: "cara beli", "cara pembelian", "beli produk", "pembelian"

Untuk pertanyaan lain, bot akan menggunakan AI Groq untuk memberikan jawaban.

## Rate Limiting

Bot memiliki rate limiting:
- **1 response per 5 detik per user**
- Jika user mengirim pesan terlalu cepat, bot akan mengabaikan pesan tersebut

## Conversation History

Bot mengingat **10 pesan terakhir** per user untuk memberikan konteks yang lebih baik dalam percakapan.

## Troubleshooting

### Bot tidak merespons

1. Pastikan bot sudah ditambahkan ke grup
2. Pastikan grup ID sudah ditambahkan ke `TELEGRAM_ALLOWED_GROUP_IDS` (atau kosongkan)
3. Pastikan webhook sudah di-set dengan benar
4. Cek log aplikasi untuk error

### Error "GROQ_API_KEY not set"

- Pastikan environment variable `GROQ_API_KEY` sudah di-set
- Restart aplikasi setelah menambahkan environment variable

### Error "TELEGRAM_BOT_TOKEN not set"

- Pastikan environment variable `TELEGRAM_BOT_TOKEN` sudah di-set
- Pastikan token valid dan bot masih aktif

### Bot merespons di private chat

- Bot seharusnya hanya merespons di grup
- Jika bot merespons di private chat, cek kode di `ShouldRespond()` function

## Security Notes

- Jangan commit bot token atau API key ke repository
- Gunakan environment variables untuk semua credentials
- Rate limiting membantu mencegah abuse
- Bot hanya mengakses database untuk informasi read-only (produk, settings, dll)

## API Endpoint

```
POST /v3/telegram/webhook
```

Endpoint ini menerima webhook dari Telegram dan memproses pesan sesuai dengan konfigurasi bot.

