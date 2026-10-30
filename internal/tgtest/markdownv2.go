// Package tgtest holds test helpers for Telegram message formatting.
package tgtest

import (
	"fmt"
	"strings"
)

// CheckMarkdownV2 reports the first construct Telegram would reject with
// "can't parse entities": an unescaped reserved character outside entities,
// an unclosed bold/italic/code/pre entity. It covers the subset the bot uses
// (* _ ~ inline code, fenced pre), not links or spoilers.
func CheckMarkdownV2(text string) error {
	const reserved = "_*[]()~`>#+-=|{}.!"
	open := map[byte]bool{}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\\':
			if i+1 >= len(text) {
				return fmt.Errorf("trailing backslash")
			}
			i++
		case strings.HasPrefix(text[i:], "```"):
			end, err := closeCode(text, i+3, "```")
			if err != nil {
				return fmt.Errorf("pre at byte %d: %w", i, err)
			}
			i = end + 2
		case c == '`':
			end, err := closeCode(text, i+1, "`")
			if err != nil {
				return fmt.Errorf("code at byte %d: %w", i, err)
			}
			i = end
		case c == '*' || c == '_' || c == '~':
			open[c] = !open[c]
		case strings.IndexByte(reserved, c) >= 0:
			return fmt.Errorf("character %q at byte %d is reserved", c, i)
		}
	}
	for c, isOpen := range open {
		if isOpen {
			return fmt.Errorf("unclosed %q entity", c)
		}
	}
	return nil
}

// closeCode returns the index of the closing delimiter of a code entity
// starting at from; inside it only backslash and backtick are special.
func closeCode(text string, from int, delim string) (int, error) {
	for i := from; i < len(text); i++ {
		switch {
		case text[i] == '\\':
			i++
		case strings.HasPrefix(text[i:], delim):
			return i, nil
		case text[i] == '`':
			return 0, fmt.Errorf("unescaped backtick at byte %d", i)
		}
	}
	return 0, fmt.Errorf("no closing %s", delim)
}
