package entity

import (
	"net/http"
	"wfsync/lib/validate"
)

type User struct {
	Username           string `json:"username" bson:"username" validate:"required"`
	Name               string `json:"name" bson:"name" validate:"omitempty"`
	Email              string `json:"email" bson:"email" validate:"omitempty"`
	Token              string `json:"token" bson:"token" validate:"required,min=1"`
	TelegramId         int64  `json:"telegram_id" bson:"telegram_id" validate:"omitempty"`
	LogLevel           int    `json:"log_level" bson:"log_level" validate:"omitempty"`
	TelegramEnabled    bool   `json:"telegram_enabled" bson:"telegram_enabled" validate:"omitempty"`
	WFirmaAllowInvoice bool   `json:"wfirma_allow_invoice" bson:"wfirma_allow_invoice" validate:"omitempty"`
}

func (u *User) Bind(_ *http.Request) error {
	return validate.Struct(u)
}
