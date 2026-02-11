package entity

import (
	"net/http"
	"time"
	"wfsync/lib/validate"
)

// TelegramRole controls access level within the bot.
// Role hierarchy: RoleNone < RolePending < RoleUser < RoleAdmin.
// Changing role via SetTelegramRole also toggles telegram_enabled in the DB.
type TelegramRole string

const (
	RoleNone    TelegramRole = ""        // unregistered or revoked
	RolePending TelegramRole = "pending" // registered, awaiting admin approval
	RoleUser    TelegramRole = "user"    // approved, can receive notifications
	RoleAdmin   TelegramRole = "admin"   // full access, can manage other users
)

// SubscriptionTier controls how notifications are delivered to a user.
type SubscriptionTier string

const (
	TierRealtime SubscriptionTier = "realtime" // immediate delivery (default)
	TierCritical SubscriptionTier = "critical" // only ERROR+ level, immediate
	TierDigest   SubscriptionTier = "digest"   // batched summary at configured interval
)

// User represents both an API user (Token-based auth) and a Telegram bot subscriber.
// Telegram-specific fields are populated during bot registration (/start command).
type User struct {
	Username           string           `json:"username" bson:"username" validate:"required"`
	Name               string           `json:"name" bson:"name" validate:"omitempty"`
	Email              string           `json:"email" bson:"email" validate:"omitempty"`
	Token              string           `json:"token" bson:"token" validate:"required,min=1"`
	TelegramId         int64            `json:"telegram_id" bson:"telegram_id" validate:"omitempty"`
	LogLevel           int              `json:"log_level" bson:"log_level" validate:"omitempty"`
	TelegramEnabled    bool             `json:"telegram_enabled" bson:"telegram_enabled" validate:"omitempty"`
	WFirmaAllowInvoice bool             `json:"wfirma_allow_invoice" bson:"wfirma_allow_invoice" validate:"omitempty"`
	TelegramUsername   string           `json:"telegram_username" bson:"telegram_username"`
	TelegramRole       TelegramRole     `json:"telegram_role" bson:"telegram_role"`
	TelegramTopics     []string         `json:"telegram_topics" bson:"telegram_topics"`
	SubscriptionTier   SubscriptionTier `json:"subscription_tier" bson:"subscription_tier"`
	DigestSchedule     string           `json:"digest_schedule" bson:"digest_schedule"`
	RegisteredAt       time.Time        `json:"registered_at" bson:"registered_at"`
}

func (u *User) Bind(_ *http.Request) error {
	return validate.Struct(u)
}

func (u *User) IsAdmin() bool {
	return u.TelegramRole == RoleAdmin
}

func (u *User) IsApproved() bool {
	return u.TelegramRole == RoleUser || u.TelegramRole == RoleAdmin
}

func (u *User) IsPending() bool {
	return u.TelegramRole == RolePending
}

// HasTopic checks if the user is subscribed to a given notification topic.
// Convention: empty TelegramTopics = subscribed to all (backward compat).
// The sentinel value "none" means unsubscribed from everything.
func (u *User) HasTopic(topic string) bool {
	if len(u.TelegramTopics) == 0 {
		return true // empty = subscribed to everything
	}
	for _, t := range u.TelegramTopics {
		if t == "none" {
			return false
		}
		if t == topic {
			return true
		}
	}
	return false
}
