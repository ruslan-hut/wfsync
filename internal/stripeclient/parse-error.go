package stripeclient

import (
	"encoding/json"
	"fmt"
)

type stripeErrorRaw struct {
	Status        int    `json:"status"`
	Message       string `json:"message"`
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	RequestLogURL string `json:"request_log_url"`
}

func (s *StripeClient) parseErr(err error) error {
	var se stripeErrorRaw
	payload := []byte(err.Error())
	e := json.Unmarshal(payload, &se)
	if e != nil {
		return err
	}
	return fmt.Errorf("status %d: %s", se.Status, se.Message)
}
