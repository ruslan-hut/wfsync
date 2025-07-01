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
	).Info("starting")

	mongo := database.NewMongoClient(conf)
	if mongo != nil {
		log.Info("connected to mongo")
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
			log.Info("telegram bot initialized")

			// Start the bot in a goroutine
			go func() {
				if err = tgBot.Start(); err != nil {
					log.Error("telegram bot", sl.Err(err))
				}
			}()
		}
	}

	wfirmaClient := wfirma.NewClient(wfirma.Config(conf.WFirma), log)
	stripeKey := conf.Stripe.APIKey
	if conf.Stripe.TestMode {
		stripeKey = conf.Stripe.TestKey
		log.Info("using test mode for stripe", slog.String("key", stripeKey))
	}
	stripeClient := stripeclient.New(stripeKey, conf.Stripe.WebhookSecret, wfirmaClient, log)
	stripeClient.SetDatabase(mongo)
	stripeClient.SetSuccessUrl(conf.Stripe.SuccessURL)

	handler := core.New(stripeClient, log)

	//wfSoap := wfirma_soap.NewClient(wfirma_soap.Config(conf.WFirmaSoap), log)
	handler.SetInvoiceService(wfirmaClient)

	authenticate := auth.New(mongo)
	handler.SetAuthService(authenticate)

	// *** blocking start with http server ***
	err := api.New(conf, log, &handler)
	if err != nil {
		log.Error("server start", sl.Err(err))
		return
	}
	log.Error("service stopped")
}
