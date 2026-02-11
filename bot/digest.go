package bot

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// maxTelegramMessageLen is Telegram's hard limit per message.
// Messages exceeding this are split at newline boundaries by splitMessage.
const maxTelegramMessageLen = 4096

// DigestEntry is a single buffered notification waiting for the next flush.
type DigestEntry struct {
	Message   string
	Topic     string
	Level     slog.Level
	Timestamp time.Time
}

// DigestBuffer collects notifications for users on the "digest" tier
// and flushes them as grouped summaries at a configurable interval.
// Thread-safe: Add() can be called concurrently from multiple goroutines.
type DigestBuffer struct {
	mu       sync.Mutex
	entries  map[int64][]DigestEntry // telegram_id → pending entries
	interval time.Duration
	bot      *TgBot
	stopCh   chan struct{}
	done     chan struct{}
}

func NewDigestBuffer(bot *TgBot, interval time.Duration) *DigestBuffer {
	return &DigestBuffer{
		entries:  make(map[int64][]DigestEntry),
		interval: interval,
		bot:      bot,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (d *DigestBuffer) Add(chatId int64, msg string, topic string, level slog.Level) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries[chatId] = append(d.entries[chatId], DigestEntry{
		Message:   msg,
		Topic:     topic,
		Level:     level,
		Timestamp: time.Now(),
	})
}

// StartTicker launches a background goroutine that flushes accumulated entries
// at the configured interval. Performs a final flush on Stop().
func (d *DigestBuffer) StartTicker() {
	go func() {
		defer close(d.done)
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.Flush()
			case <-d.stopCh:
				d.Flush() // final flush
				return
			}
		}
	}()
}

// Flush atomically swaps out all buffered entries and sends formatted digests.
// Safe to call concurrently — uses mutex swap to minimize lock duration.
func (d *DigestBuffer) Flush() {
	d.mu.Lock()
	snapshot := d.entries
	d.entries = make(map[int64][]DigestEntry)
	d.mu.Unlock()

	for chatId, entries := range snapshot {
		if len(entries) == 0 {
			continue
		}
		digest := formatDigest(entries)
		parts := splitMessage(digest, maxTelegramMessageLen)
		for _, part := range parts {
			d.bot.plainResponse(chatId, part)
		}
	}
}

func (d *DigestBuffer) Stop() {
	close(d.stopCh)
	<-d.done
}

// formatDigest groups entries by topic and formats them as a MarkdownV2 summary.
func formatDigest(entries []DigestEntry) string {
	// Group by topic
	grouped := make(map[string][]DigestEntry)
	for _, e := range entries {
		grouped[e.Topic] = append(grouped[e.Topic], e)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Digest* \\(%d messages\\)\n\n", len(entries)))

	for topic, topicEntries := range grouped {
		sb.WriteString(fmt.Sprintf("*%s* \\(%d\\):\n", Sanitize(topic), len(topicEntries)))
		for _, e := range topicEntries {
			ts := e.Timestamp.Format("15:04")
			sb.WriteString(fmt.Sprintf("  `%s` %s %s\n", ts, e.Level.String(), Sanitize(e.Message)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
