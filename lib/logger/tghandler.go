package logger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"wfsync/bot"
)

// tgErrorMaxLen caps the error text in an alert: an upstream error page
// (wFirma 502 HTML) would otherwise fill several messages.
const tgErrorMaxLen = 1000

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

		msg, topic, skip := h.format(record)
		if skip {
			return nil
		}

		// Route by topic if available, otherwise fall back to level-based routing
		if h.bot != nil {
			if topic != "" {
				h.bot.SendMessageWithTopic(msg, record.Level, topic)
			} else {
				h.bot.SendMessageWithLevel(msg, record.Level)
			}
		}
	}

	return nil
}

// format builds the MarkdownV2 alert text for a record.
// tg_skip=true suppresses the Telegram dispatch (the local handler still logs).
func (h *TelegramHandler) format(record slog.Record) (msg string, topic string, skip bool) {
	addAttr := func(attr slog.Attr) {
		switch attr.Key {
		case "tg_topic":
			topic = attr.Value.String()
		case "tg_skip":
			skip = skip || attr.Value.Bool()
		case "error":
			msg += "\n" + bot.CodeBlock(attr.Value.String(), tgErrorMaxLen)
		default:
			msg += bot.Sanitize(fmt.Sprintf("\n%s: %v", attr.Key, attr.Value))
		}
	}

	// Attributes from .With() calls, then from the record
	for _, attr := range h.attrs {
		addAttr(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addAttr(attr)
		return true
	})

	// Add group prefix if present
	var header string
	message := bot.EscapeCodeBlock(record.Message)
	if h.group != "" {
		header = fmt.Sprintf("*%s* `%s.%s`", bot.Sanitize(record.Level.String()), bot.EscapeCodeBlock(h.group), message)
	} else {
		header = fmt.Sprintf("*%s* `%s`", bot.Sanitize(record.Level.String()), message)
	}
	if topic != "" {
		header = fmt.Sprintf("*%s* `%s`", bot.Sanitize(strings.ToUpper(topic)), message)
	}
	return header + msg, topic, skip
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
