package logger

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	envLocal    = "local"
	envDev      = "dev"
	envProd     = "prod"
	logFileName = "wfsync.log"
)

func SetupLogger(env, path string) *slog.Logger {
	var logger *slog.Logger
	var logFile *os.File
	var err error

	if env != envLocal {
		logPath := logFilePath(path)
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
			slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}),
		)
	case envProd:
		logger = slog.New(
			slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}),
		)
	default:
		log.Fatal("invalid environment: ", env)
	}

	return logger
}

func logFilePath(path string) string {
	return filepath.Join(path, logFileName)
}
