package response

import "wfsync/lib/clock"

type Response struct {
	Data          interface{} `json:"data,omitempty"`
	Success       bool        `json:"success" validate:"required"`
	StatusMessage string      `json:"status_message"`
	Timestamp     string      `json:"timestamp"`
}

func Ok(data interface{}) Response {
	return Response{
		Data:          data,
		Success:       true,
		StatusMessage: "Success",
		Timestamp:     clock.Now(),
	}
}

func Error(message string) Response {
	return Response{
		Success:       false,
		StatusMessage: message,
		Timestamp:     clock.Now(),
	}
}
