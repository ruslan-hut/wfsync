package entity

import "net/http"

type Payment struct {
	Amount int64  `json:"amount"`
	Id     string `json:"id" validate:"required"`
	Link   string `json:"link"`
}

func (p *Payment) Bind(_ *http.Request) error {
	return nil
}
