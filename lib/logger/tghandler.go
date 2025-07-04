package logger

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"wfsync/bot"
)

// TelegramHandler is a slog.Handler that sends log messages to Telegram
type TelegramHandler struct {
	handler  slog.Handler
	bot      *bot.TgBot
	minLevel slog.Level
	mu       sync.Mutex
	attrs    []slog.Attr
	group    string
}

// NewTelegramHandler creates a new TelegramHandler
func NewTelegramHandler(handler slog.Handler, bot *bot.TgBot, minLevel slog.Level) *TelegramHandler {
	return &TelegramHandler{
		handler:  handler,
		bot:      bot,
		minLevel: minLevel,
		attrs:    make([]slog.Attr, 0),
		group:    "",
	}
}

// Enabled implements slog.Handler.Enabled
func (h *TelegramHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.minLevel && h.handler.Enabled(ctx, level)
}

// Handle implements slog.Handler.Handle
func (h *TelegramHandler) Handle(ctx context.Context, record slog.Record) error {
	// First, let the underlying handler handle the record
	err := h.handler.Handle(ctx, record)
	if err != nil {
		return err
	}

	// If the level is high enough, send to Telegram
	if record.Level >= h.minLevel {
		h.mu.Lock()
		defer h.mu.Unlock()

		// Format the log message
		var msg string

		// Add group prefix if present
		if h.group != "" {
			msg = fmt.Sprintf("*%s* `%s.%s`", record.Level.String(), h.group, record.Message)
		} else {
			msg = fmt.Sprintf("*%s* `%s`", record.Level.String(), record.Message)
		}

		// Add attributes from .With() calls
		for _, attr := range h.attrs {
			if attr.Key == "error" {
				msg += fmt.Sprintf("\n%s: ```error %v ```", attr.Key, attr.Value)
			} else {
				msg += bot.Sanitize(fmt.Sprintf("\n%s: %v", attr.Key, attr.Value))
			}
		}

		// Add attributes from the record
		record.Attrs(func(attr slog.Attr) bool {
			msg += bot.Sanitize(fmt.Sprintf("\n%s: %v", attr.Key, attr.Value))
			return true
		})

		// Send to Telegram with the record's log level
		if h.bot != nil {
			h.bot.SendMessageWithLevel(msg, record.Level)
		}
	}

	return nil
}

// WithAttrs implements slog.Handler.WithAttrs
func (h *TelegramHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Create a new handler with the combined attributes
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)

	return &TelegramHandler{
		handler:  h.handler.WithAttrs(attrs),
		bot:      h.bot,
		minLevel: h.minLevel,
		mu:       sync.Mutex{},
		attrs:    newAttrs,
		group:    h.group,
	}
}

// WithGroup implements slog.Handler.WithGroup
func (h *TelegramHandler) WithGroup(name string) slog.Handler {
	var group string
	if h.group != "" {
		group = h.group + "." + name
	} else {
		group = name
	}

	return &TelegramHandler{
		handler:  h.handler.WithGroup(name),
		bot:      h.bot,
		minLevel: h.minLevel,
		mu:       sync.Mutex{},
		attrs:    h.attrs,
		group:    group,
	}
}
