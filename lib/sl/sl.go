package sl

import (
	"fmt"
	"log/slog"
)

func Err(err error) slog.Attr {
	return slog.Attr{
		Key:   "error",
		Value: slog.StringValue(err.Error()),
	}
}

// Secret returns a string with the first 5 characters of the input string
// used to hide sensitive information in logs
func Secret(key, value string) slog.Attr {
	r := "***"
	if len(value) > 5 {
		r = fmt.Sprintf("%s***", value[0:5])
	}
	if value == "" {
		r = "?"
	}
	return slog.Attr{
		Key:   key,
		Value: slog.StringValue(r),
	}
}

func Module(mod string) slog.Attr {
	return slog.Attr{
		Key:   "mod",
		Value: slog.StringValue(mod),
	}
}
