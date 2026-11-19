package bot

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	const token = "123456789:AAH-secret_token-value"
	reqURL := "https://api.telegram.org/bot" + token + "/getUpdates"
	tests := []struct {
		name  string
		token string
		err   error
		want  string
	}{
		{
			name:  "url error from polling",
			token: token,
			err: fmt.Errorf("failed to execute POST request to getUpdates: %w",
				&url.Error{Op: "Post", URL: reqURL, Err: errors.New("context deadline exceeded")}),
			want: `failed to execute POST request to getUpdates: Post "https://api.telegram.org/bot***/getUpdates": context deadline exceeded`,
		},
		{
			name:  "no token in error",
			token: token,
			err:   errors.New("unable to sendMessage: Bad Request: chat not found"),
			want:  "unable to sendMessage: Bad Request: chat not found",
		},
		{
			name:  "empty token keeps error",
			token: "",
			err:   errors.New("boom"),
			want:  "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot := &TgBot{token: tt.token}
			got := bot.redact(tt.err)
			if got != tt.want {
				t.Errorf("redact() = %q, want %q", got, tt.want)
			}
			if tt.token != "" && strings.Contains(got, tt.token) {
				t.Errorf("redact() leaks the token: %q", got)
			}
		})
	}
}

func TestNewHTTPClientPingsHTTP2(t *testing.T) {
	c := newHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.Transport)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("HTTP/2 is not attempted")
	}
	h2 := tr.HTTP2
	if h2 == nil || h2.SendPingTimeout <= 0 || h2.PingTimeout <= 0 {
		t.Fatalf("HTTP/2 health check is off: %+v", h2)
	}
}
