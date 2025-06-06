package main

import (
	"errors"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"wfsync/internal/config"
	"wfsync/internal/stripehandler"
	"wfsync/internal/wfirma"

	"github.com/go-chi/chi/v5"
)

const (
	envLocal    = "local"
	envDev      = "dev"
	envProd     = "prod"
	logFileName = "wfsync.log"
)

func main() {
	configPath := flag.String("conf", "config.yml", "path to config file")
	logPath := flag.String("log", "/var/log/", "path to log file directory")
	flag.Parse()

	conf := config.MustLoad(*configPath)
	logger := setupLogger(conf.Env, *logPath)
	logger.Info("starting wfsync", slog.String("config", *configPath), slog.String("env", conf.Env))

	wfirmaClient := wfirma.NewClient(wfirma.Config(conf.WFirma), logger)
	stripeHandler := stripehandler.New(conf.Stripe.APIKey, conf.Stripe.WebhookSecret, wfirmaClient, logger)

	r := chi.NewRouter()
	r.Post("/webhook/stripe", stripeHandler.HandleWebhook)

	srv := &http.Server{
		Addr:         conf.Listen.BindIp + ":" + conf.Listen.Port,
		Handler:      r,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	logger.With(
		slog.String("addr", srv.Addr),
	).Info("listening")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.With(
			slog.String("error", err.Error()),
		).Error("error starting server")
	}
}

func setupLogger(env, path string) *slog.Logger {
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

func logFilePath(path string) string {
	return filepath.Join(path, logFileName)
}
