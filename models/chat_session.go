package models

import "time"

// ChatSession represents a live chat session
type ChatSession struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	UserID        *uint      `gorm:"column:user_id;index" json:"user_id,omitempty"` // null for non-auth users
	UserName      string     `gorm:"column:user_name;size:100" json:"user_name"`    // name for non-auth or user name
	IsAuth        bool       `gorm:"column:is_auth;default:false" json:"is_auth"`   // true if user is authenticated
	Status        string     `gorm:"type:enum('active','ended');default:'active'" json:"status"`
	EndedAt       *time.Time `gorm:"column:ended_at" json:"ended_at,omitempty"`
	EndReason     string     `gorm:"column:end_reason;size:50" json:"end_reason,omitempty"` // 'user', 'timeout', 'auto'
	LastMessageAt time.Time  `gorm:"column:last_message_at" json:"last_message_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`

	// Relations
	User     *User         `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Messages []ChatMessage `gorm:"foreignKey:SessionID" json:"messages,omitempty"`
}

func (ChatSession) TableName() string {
	return "chat_sessions"
}

// ChatMessage represents a message in a chat session
type ChatMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	SessionID uint      `gorm:"column:session_id;not null;index" json:"session_id"`
	Role      string    `gorm:"type:enum('user','assistant');not null" json:"role"` // 'user' or 'assistant'
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`

	// Relations
	Session *ChatSession `gorm:"foreignKey:SessionID" json:"session,omitempty"`
}

func (ChatMessage) TableName() string {
	return "chat_messages"
}
