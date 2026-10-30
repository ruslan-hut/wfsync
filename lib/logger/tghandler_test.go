package logger

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"wfsync/internal/tgtest"
)

// wFirma answered 502 with an HTML page on 2026-10-21 (PL-670); the alert was
// rejected by Telegram with "can't parse entities" and resent as plain text.
const wfirma502 = `wfirma 502 Bad Gateway: <!DOCTYPE html>
<html>
    <head>
        <meta http-equiv="Content-Type" content="text/html; charset=UTF-8">
        <title>502 - Bad Gateway</title>
        <script>var msg = ` + "`retry in ${t}s`" + `;</script>
    </head>
    <body><h1>Error 502!</h1> <p>Upstream -> timeout.</p></body>
</html>`

func TestFormatErrorWithHTMLBody(t *testing.T) {
	h := NewTelegramHandler(slog.NewTextHandler(nil, nil), nil, slog.LevelDebug)
	hw := h.WithAttrs([]slog.Attr{slog.String("mod", "http.handlers.b2b")}).(*TelegramHandler)

	err := errors.New("create contractor: " + wfirma502 + strings.Repeat("\n<p>padding</p>", 200))
	record := slog.NewRecord(time.Now(), slog.LevelError, "proforma creation", 0)
	record.AddAttrs(
		slog.String("request_id", "darkbyrior.com/x36qhTbsSi-000302"),
		slog.String("order_number", "PL-670"),
		slog.Any("error", err),
	)

	msg, topic, skip := hw.format(record)
	if skip || topic != "" {
		t.Fatalf("skip=%t topic=%q", skip, topic)
	}
	if e := tgtest.CheckMarkdownV2(msg); e != nil {
		t.Fatalf("alert is not valid MarkdownV2: %v\n%s", e, msg)
	}
	if len(msg) > 1500 {
		t.Errorf("alert is %d bytes, error text not truncated", len(msg))
	}
	if !strings.Contains(msg, "PL\\-670") {
		t.Errorf("order number missing: %s", msg)
	}
}

func TestFormatHeaderEscaping(t *testing.T) {
	h := NewTelegramHandler(slog.NewTextHandler(nil, nil), nil, slog.LevelDebug)
	hw := h.WithGroup("wfirma").WithAttrs([]slog.Attr{slog.String("tg_topic", "invoice_error")}).(*TelegramHandler)

	record := slog.NewRecord(time.Now(), slog.LevelWarn+2, "bad `name` > 1", 0)
	msg, _, _ := hw.format(record)
	if e := tgtest.CheckMarkdownV2(msg); e != nil {
		t.Fatalf("header is not valid MarkdownV2: %v\n%s", e, msg)
	}

	plain := h.WithGroup("wfirma")
	msg, _, _ = plain.(*TelegramHandler).format(slog.NewRecord(time.Now(), slog.LevelWarn+2, "x", 0))
	if e := tgtest.CheckMarkdownV2(msg); e != nil {
		t.Fatalf("level header is not valid MarkdownV2: %v\n%s", e, msg)
	}
}
