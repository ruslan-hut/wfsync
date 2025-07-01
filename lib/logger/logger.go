package logger

import (
	"log"
	"log/slog"
	"os"
	"wfsync/bot"
)

const (
	envLocal = "local"
	envDev   = "dev"
	envProd  = "prod"
)

func SetupLogger(env, logPath string) *slog.Logger {
	var logger *slog.Logger
	var logFile *os.File
	var err error

	if env != envLocal {
		logFile, err = os.OpenFile(logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatal("error opening log file: ", err)
		}
		log.Printf("env: %s; log file: %s", env, logPath)
	}

	switch env {
	case envLocal:
		logger = slog.New(
			slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
		)
	case envDev:
		logger = slog.New(
			slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}),
		)
	case envProd:
		logger = slog.New(
			slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}),
		)
	default:
		log.Fatal("invalid environment: ", env)
	}

	return logger
}

// SetupTelegramHandler adds a Telegram handler to the logger
func SetupTelegramHandler(logger *slog.Logger, tgBot *bot.TgBot, minLevel slog.Level) *slog.Logger {
	if tgBot == nil {
		return logger
	}

	// Get the existing handler from the logger
	existingHandler := logger.Handler()

	// Create a new Telegram handler that wraps the existing handler
	tgHandler := NewTelegramHandler(existingHandler, tgBot, minLevel)

	// Create a new logger with the Telegram handler
	return slog.New(tgHandler)
}
