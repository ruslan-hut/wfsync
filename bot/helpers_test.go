package bot

import (
	"strings"
	"testing"
	"unicode/utf8"
	"wfsync/internal/tgtest"
)

func TestSanitize(t *testing.T) {
	in := "a_b*c[d]e(f)g~h`i>j#k+l-m=n|o{p}q.r!s\\t"
	got := Sanitize(in)
	if err := tgtest.CheckMarkdownV2(got); err != nil {
		t.Fatalf("Sanitize(%q) = %q: %v", in, got, err)
	}
	if got := Sanitize("PL-670 <ok>"); got != "PL\\-670 <ok\\>" {
		t.Errorf("Sanitize = %q", got)
	}
}

func TestCodeBlock(t *testing.T) {
	got := CodeBlock("a `b` \\c ```d```", 100)
	if err := tgtest.CheckMarkdownV2(got); err != nil {
		t.Fatalf("CodeBlock = %q: %v", got, err)
	}

	long := strings.Repeat("ж", 600) // 1200 bytes
	got = CodeBlock(long, 1001)
	if !utf8.ValidString(got) {
		t.Errorf("CodeBlock cut a rune: %q", got[len(got)-10:])
	}
	if !strings.Contains(got, "…") {
		t.Errorf("CodeBlock did not mark truncation")
	}
}

func TestSplitMessageKeepsEntitiesClosed(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 300; i++ {
		body.WriteString("<div class=\"x\">line with `tick` and > - . !</div>\n")
	}
	text := "*ERROR* `proforma creation`\n" + CodeBlock(body.String(), 20000) + "\norder\\_number: PL\\-670"

	parts := splitMessage(text, tgMaxMessageLen)
	if len(parts) < 2 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	for i, part := range parts {
		if len(part) > tgMaxMessageLen {
			t.Errorf("part %d: %d bytes", i, len(part))
		}
		if err := tgtest.CheckMarkdownV2(part); err != nil {
			t.Errorf("part %d: %v", i, err)
		}
	}
}

func TestSplitMessageWithoutNewlines(t *testing.T) {
	text := strings.Repeat("ж\\-", 3000)
	for i, part := range splitMessage(text, tgMaxMessageLen) {
		if !utf8.ValidString(part) {
			t.Errorf("part %d: invalid UTF-8", i)
		}
		if err := tgtest.CheckMarkdownV2(part); err != nil {
			t.Errorf("part %d: %v", i, err)
		}
	}
}
