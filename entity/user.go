package entity

import (
	"net/http"
	"time"
	"wfsync/lib/validate"
)

type TelegramRole string

const (
	RoleNone    TelegramRole = ""
	RolePending TelegramRole = "pending"
	RoleUser    TelegramRole = "user"
	RoleAdmin   TelegramRole = "admin"
)

type SubscriptionTier string

const (
	TierRealtime SubscriptionTier = "realtime"
	TierCritical SubscriptionTier = "critical"
	TierDigest   SubscriptionTier = "digest"
)

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
