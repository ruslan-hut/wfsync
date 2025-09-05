package main

import (
	"flag"
	"log/slog"
	"wfsync/bot"
	"wfsync/impl/auth"
	"wfsync/impl/core"
	"wfsync/internal/config"
	"wfsync/internal/database"
	"wfsync/internal/http-server/api"
	"wfsync/internal/stripeclient"
	"wfsync/internal/wfirma"
	"wfsync/lib/logger"
	"wfsync/lib/sl"
	occlient "wfsync/opencart/oc-client"
)

func main() {
	configPath := flag.String("conf", "config.yml", "path to config file")
	logPath := flag.String("log", "", "path to log file directory")
	flag.Parse()

	conf := config.MustLoad(*configPath)
	if *logPath == "" {
		logPath = &conf.Log
	}
	log := logger.SetupLogger(conf.Env, *logPath)
	log.With(
		slog.String("config", *configPath),
		slog.String("env", conf.Env),
		slog.String("log", *logPath),
		slog.String("location", conf.Location),
	).Info("config loaded")

	mongo := database.NewMongoClient(conf)
	if mongo != nil {
		log.With(
			sl.Secret("mongo_db", conf.Mongo.Database),
		).Info("connected to mongo")
	}

	// Initialize Telegram bot if enabled
	var tgBot *bot.TgBot
	if conf.Telegram.Enabled {
		var err error
		tgBot, err = bot.NewTgBot(conf.Telegram.ApiKey, mongo, log)
		if err != nil {
			log.Error("initialize telegram bot", sl.Err(err))
		} else {
			// Set up Telegram handler for the logger
			log = logger.SetupTelegramHandler(log, tgBot, slog.LevelDebug)
			// Start the bot in a goroutine
			go func() {
				if err = tgBot.Start(); err != nil {
					log.Error("telegram bot", sl.Err(err))
				}
			}()
			log.Info("telegram bot started")
		}
	}

	oc, err := occlient.New(conf, log)
	if err != nil {
		log.Error("opencart client", sl.Err(err))
	}

	wfirmaClient := wfirma.NewClient(conf, log)
	wfirmaClient.SetDatabase(mongo)

	stripeClient := stripeclient.New(conf, log)
	stripeClient.SetDatabase(mongo)

	handler := core.New(conf, log)
	handler.SetStripeClient(stripeClient)
	handler.SetInvoiceService(wfirmaClient)
	handler.SetOpencart(oc)

	authenticate := auth.New(mongo)
	handler.SetAuthService(authenticate)

	// *** blocking start with http server ***
	err = api.New(conf, log, &handler)
	if err != nil {
		log.Error("server start", sl.Err(err))
		return
	}
	log.Error("service stopped")
}
