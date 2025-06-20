package main

import (
	"flag"
	"log/slog"
	"wfsync/impl/core"
	"wfsync/internal/config"
	"wfsync/internal/http-server/api"
	"wfsync/internal/stripeclient"
	"wfsync/internal/wfirma"
	"wfsync/lib/logger"
	"wfsync/lib/sl"
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
	log := logger.SetupLogger(conf.Env, *logPath)
	log.Info("starting wfsync", slog.String("config", *configPath), slog.String("env", conf.Env))

	wfirmaClient := wfirma.NewClient(wfirma.Config(conf.WFirma), log)
	stripeClient := stripeclient.New(conf.Stripe.APIKey, conf.Stripe.WebhookSecret, wfirmaClient, log)

	handler := core.New(stripeClient, log)

	// *** blocking start with http server ***
	err := api.New(conf, log, handler)
	if err != nil {
		log.Error("server start", sl.Err(err))
		return
	}
	log.Error("service stopped")
}
