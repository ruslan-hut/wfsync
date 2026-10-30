package bot

import (
	"log/slog"
	"strings"
	"testing"
	"time"
	"wfsync/internal/tgtest"
)

func TestFormatDigestKeepsAlertMarkup(t *testing.T) {
	alert := "*ERROR* `proforma creation`" +
		Sanitize("\norder_number: PL-670") +
		"\n" + CodeBlock("wfirma 502 Bad Gateway: <html> `x` </html>", 1000)
	entries := []DigestEntry{
		{Message: alert, Topic: "invoice_error", Level: slog.LevelError, Timestamp: time.Now()},
		{Message: "*WARN* `retry`", Topic: "system", Level: slog.LevelWarn + 2, Timestamp: time.Now()},
	}

	got := formatDigest(entries)
	if err := tgtest.CheckMarkdownV2(got); err != nil {
		t.Fatalf("digest is not valid MarkdownV2: %v\n%s", err, got)
	}
	if !strings.Contains(got, "PL\\-670") || strings.Contains(got, "\\\\") {
		t.Errorf("alert escaped twice:\n%s", got)
	}
}
