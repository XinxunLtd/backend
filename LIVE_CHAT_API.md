# Live Chat AI API Documentation

## Overview

Live Chat AI adalah sistem chat customer service berbasis AI untuk platform Xinxun. Sistem ini menggunakan Groq AI dengan model Llama untuk memberikan respons yang ramah, gaul, dan membantu.

## Fitur Utama

- ✅ **Auth & Non-Auth Support**: Mendukung user yang sudah login (pakai nama) dan user guest (pakai "Kak")
- ✅ **Auto End Chat**: Chat otomatis berakhir jika:
  - User bilang "sudah", "sudah jelas", "terima kasih", dll
  - 15 menit tidak ada respon dari user
  - User end chat manual
- ✅ **Riwayat Chat**: Semua chat tersimpan dan bisa dilihat kembali
- ✅ **Session Management**: Setiap chat memiliki session ID unik
- ✅ **AI-Powered**: Menggunakan Groq AI dengan prompt yang sama seperti Telegram bot

## Base URL

```
https://api.xinxun.us/v3
```

## Authentication

### Auth Users (Logged In)
- Gunakan header `Authorization: Bearer <token>`
- Bot akan memanggil user dengan nama mereka
- Chat session akan terhubung dengan user ID

### Non-Auth Users (Guest)
- Tidak perlu header Authorization
- Bot akan memanggil user dengan "Kak"
- Chat session tidak terhubung dengan user ID

## Endpoints

### 1. Start Chat

Memulai chat session baru.

**Endpoint:** `POST /livechat/start`

**Headers:**
- `Content-Type: application/json`
- `Authorization: Bearer <token>` (optional - hanya jika user sudah login)

**Request Body:**
```json
{
  "name": "John Doe"  // Optional: untuk non-auth users
}
```

**Response (200 OK):**
```json
{
  "success": true,
  "message": "Chat session started",
  "data": {
    "session_id": 123,
    "status": "active",
    "message": "Halo John Doe! 👋 Saya CS Xinxun, ada yang bisa dibantu? 😊"
  }
}
```

**Response (Non-Auth):**
```json
{
  "success": true,
  "message": "Chat session started",
  "data": {
    "session_id": 124,
    "status": "active",
    "message": "Halo Kak! 👋 Saya CS Xinxun, ada yang bisa dibantu? 😊"
  }
}
```

### 2. Send Message

Mengirim pesan ke AI dan mendapatkan respons.

**Endpoint:** `POST /livechat/{session_id}/message`

**Headers:**
- `Content-Type: application/json`
- `Authorization: Bearer <token>` (required jika session adalah auth session)

**URL Parameters:**
- `session_id` (uint): ID session chat

**Request Body:**
```json
{
  "message": "Cara deposit di Xinxun?"
}
```

**Response (200 OK):**
```json
{
  "success": true,
  "message": "Message sent",
  "data": {
    "message": "Halo! Deposit di Xinxun gampang banget...",
    "session_id": 123,
    "status": "active",
    "ended": false
  }
}
```

**Response (Chat Ended):**
```json
{
  "success": true,
  "message": "Message sent",
  "data": {
    "message": "Terima kasih!...\n\nSama-sama! 😊 Semoga membantu ya...",
    "session_id": 123,
    "status": "ended",
    "ended": true
  }
}
```

**Error Responses:**

- `400 Bad Request`: Session sudah ended atau expired
- `401 Unauthorized`: Session adalah auth session tapi user tidak authenticated
- `404 Not Found`: Session tidak ditemukan

### 3. End Chat

Mengakhiri chat session secara manual.

**Endpoint:** `POST /livechat/{session_id}/end`

**Headers:**
- `Authorization: Bearer <token>` (required jika session adalah auth session)

**URL Parameters:**
- `session_id` (uint): ID session chat

**Response (200 OK):**
```json
{
  "success": true,
  "message": "Chat session ended",
  "data": {
    "session_id": 123,
    "status": "ended",
    "message": "Terima kasih sudah chat dengan kami! 😊 Semoga membantu ya. Kalau ada pertanyaan lagi, jangan ragu untuk chat lagi! 👋"
  }
}
```

### 4. Get Chat History

Mendapatkan riwayat chat dari session tertentu.

**Endpoint:** `GET /livechat/{session_id}/history`

**Headers:**
- `Authorization: Bearer <token>` (required jika session adalah auth session)

**URL Parameters:**
- `session_id` (uint): ID session chat

**Response (200 OK):**
```json
{
  "success": true,
  "message": "Chat history retrieved",
  "data": {
    "session_id": 123,
    "status": "ended",
    "messages": [
      {
        "role": "assistant",
        "content": "Halo John Doe! 👋 Saya CS Xinxun, ada yang bisa dibantu? 😊",
        "created_at": "2025-12-30T01:00:00Z"
      },
      {
        "role": "user",
        "content": "Cara deposit?",
        "created_at": "2025-12-30T01:01:00Z"
      },
      {
        "role": "assistant",
        "content": "Halo! Deposit di Xinxun...",
        "created_at": "2025-12-30T01:01:05Z"
      }
    ],
    "created_at": "2025-12-30T01:00:00Z",
    "ended_at": "2025-12-30T01:05:00Z"
  }
}
```

**Note:** Setelah chat ended, tidak bisa dilanjut lagi. Hanya bisa dilihat sebagai riwayat.

### 5. Get All Chat Sessions

Mendapatkan daftar semua chat sessions user (hanya untuk authenticated users).

**Endpoint:** `GET /livechat/sessions`

**Headers:**
- `Authorization: Bearer <token>` (required)

**Response (200 OK):**
```json
{
  "success": true,
  "message": "Chat sessions retrieved",
  "data": [
    {
      "id": 123,
      "status": "ended",
      "created_at": "2025-12-30T01:00:00Z",
      "ended_at": "2025-12-30T01:05:00Z",
      "last_message": "Terima kasih sudah chat dengan kami! 😊"
    },
    {
      "id": 122,
      "status": "active",
      "created_at": "2025-12-29T10:00:00Z",
      "ended_at": null,
      "last_message": "Halo! Ada yang bisa dibantu?"
    }
  ]
}
```

## Auto End Chat

Chat akan otomatis berakhir jika:

1. **User bilang sudah/selesai:**
   - "sudah"
   - "sudah jelas"
   - "terima kasih"
   - "makasih"
   - "thanks"
   - "selesai"
   - "sudah selesai"
   - "sudah cukup"
   - "cukup"
   - "oke sudah"
   - dll

2. **Timeout (15 menit):**
   - Jika user tidak mengirim pesan selama 15 menit, session akan expired
   - User harus start chat baru

3. **User end manual:**
   - User bisa end chat dengan memanggil endpoint `/livechat/{session_id}/end`

## Session Status

- `active`: Chat masih berlangsung
- `ended`: Chat sudah berakhir (tidak bisa dilanjut)

## Error Handling

### Common Errors

**400 Bad Request:**
```json
{
  "success": false,
  "message": "Chat session has ended. Please start a new chat."
}
```

**401 Unauthorized:**
```json
{
  "success": false,
  "message": "Unauthorized"
}
```

**404 Not Found:**
```json
{
  "success": false,
  "message": "Chat session not found"
}
```

**500 Internal Server Error:**
```json
{
  "success": false,
  "message": "Failed to create chat session"
}
```

## Rate Limiting

- **Start Chat**: 60 requests per IP per 5 menit
- **Send Message**: 60 requests per IP per 5 menit
- **End Chat**: 60 requests per IP per 5 menit
- **Get History**: 60 requests per IP per 5 menit
- **Get Sessions**: 120 requests per user per menit (auth required)

## AI Behavior

AI akan:
- Menggunakan bahasa santai, gaul, tapi sopan
- Memanggil user dengan nama (jika auth) atau "Kak" (jika non-auth)
- Menjawab pertanyaan tentang Xinxun, produk, investasi, dll
- Memberikan informasi dari database (harga produk, withdrawal info, dll)
- Menggunakan emoji secukupnya (1-3 per pesan)
- Tidak menggunakan "gue", "lo", "bro" - menggunakan "saya", "kamu"

## Frontend Integration Example

### React/Next.js Example

```typescript
// Start chat
const startChat = async (name?: string) => {
  const response = await fetch('https://api.xinxun.us/v3/livechat/start', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token && { 'Authorization': `Bearer ${token}` })
    },
    body: JSON.stringify({ name })
  });
  const data = await response.json();
  return data.data.session_id;
};

// Send message
const sendMessage = async (sessionId: number, message: string) => {
  const response = await fetch(`https://api.xinxun.us/v3/livechat/${sessionId}/message`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token && { 'Authorization': `Bearer ${token}` })
    },
    body: JSON.stringify({ message })
  });
  const data = await response.json();
  return data.data;
};

// Get history
const getHistory = async (sessionId: number) => {
  const response = await fetch(`https://api.xinxun.us/v3/livechat/${sessionId}/history`, {
    headers: {
      ...(token && { 'Authorization': `Bearer ${token}` })
    }
  });
  const data = await response.json();
  return data.data.messages;
};

// End chat
const endChat = async (sessionId: number) => {
  const response = await fetch(`https://api.xinxun.us/v3/livechat/${sessionId}/end`, {
    method: 'POST',
    headers: {
      ...(token && { 'Authorization': `Bearer ${token}` })
    }
  });
  return response.json();
};
```

## Database Schema

### chat_sessions
- `id` (uint, primary key)
- `user_id` (uint, nullable, index) - null untuk non-auth users
- `user_name` (string) - nama user atau "Guest"
- `is_auth` (bool) - true jika user authenticated
- `status` (enum: 'active', 'ended')
- `ended_at` (timestamp, nullable)
- `end_reason` (string) - 'user', 'timeout', 'auto'
- `last_message_at` (timestamp)
- `created_at` (timestamp)
- `updated_at` (timestamp)

### chat_messages
- `id` (uint, primary key)
- `session_id` (uint, index)
- `role` (enum: 'user', 'assistant')
- `content` (text)
- `created_at` (timestamp)

## Database Migration

### Development Mode
Jika menggunakan development mode (`ENV=development`), tabel akan otomatis dibuat saat aplikasi start menggunakan GORM AutoMigrate.

### Production Mode
Untuk production, jalankan migration SQL manual:

```bash
# Login ke MySQL
mysql -u root -p

# Pilih database
USE your_database_name;

# Jalankan migration
SOURCE migrations/create_chat_sessions_table.sql;
```

Atau copy-paste isi file `migrations/create_chat_sessions_table.sql` ke MySQL prompt atau phpMyAdmin.

### Migration File
File migration: `migrations/create_chat_sessions_table.sql`

Migration ini akan membuat:
- Tabel `chat_sessions` dengan foreign key ke `users`
- Tabel `chat_messages` dengan foreign key ke `chat_sessions` (CASCADE delete)

## Notes

- Setelah chat ended, tidak bisa dilanjut lagi
- Chat yang sudah ended hanya bisa dilihat sebagai riwayat
- Timeout adalah 15 menit dari last message
- AI menggunakan prompt yang sama dengan Telegram bot
- Format response menggunakan HTML (bukan Markdown)

