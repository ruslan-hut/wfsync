package config

import (
	"fmt"
	"log"
	"sync"

	"github.com/ilyakaznacheev/cleanenv"
)

type Listen struct {
	BindIp string `yaml:"bind_ip" env-default:"0.0.0.0"`
	Port   string `yaml:"port" env-default:"8080"`
}

type StripeConfig struct {
	TestMode          bool   `yaml:"test_mode" env-default:"false"`
	APIKey            string `yaml:"api_key" env-default:""`
	WebhookSecret     string `yaml:"webhook_secret" env-default:""`
	TestKey           string `yaml:"test_key" env-default:""`
	TestWebhookSecret string `yaml:"webhook_test_secret" env-default:""`
	SuccessURL        string `yaml:"success_url" env-default:""`
}

type WfirmaConfig struct {
	Enabled   bool   `yaml:"enabled" env-default:"false"`
	AccessKey string `yaml:"access_key" env-default:""`
	SecretKey string `yaml:"secret_key" env-default:""`
	AppID     string `yaml:"app_id" env-default:""`
}

type Mongo struct {
	Enabled  bool   `yaml:"enabled" env-default:"false"`
	Host     string `yaml:"host" env-default:"127.0.0.1"`
	Port     string `yaml:"port" env-default:"27017"`
	User     string `yaml:"user" env-default:"admin"`
	Password string `yaml:"password" env-default:"pass"`
	Database string `yaml:"database" env-default:""`
}

type OpenCart struct {
	Enabled               bool   `yaml:"enabled" env-default:"false"`
	Driver                string `yaml:"driver" env-default:"mysql"`
	HostName              string `yaml:"hostname" env-default:"localhost"`
	UserName              string `yaml:"username" env-default:"root"`
	Password              string `yaml:"password" env-default:""`
	Database              string `yaml:"database" env-default:""`
	Port                  string `yaml:"port" env-default:"3306"`
	Prefix                string `yaml:"prefix" env-default:""`
	FileUrl               string `yaml:"file_url" env-default:""`
	StatusUrlRequest      string `yaml:"status_url_request" env-default:""`
	StatusUrlResult       string `yaml:"status_url_result" env-default:""`
	StatusProformaRequest string `yaml:"status_proforma_request" env-default:""`
	StatusProformaResult  string `yaml:"status_proforma_result" env-default:""`
	StatusInvoiceRequest  string `yaml:"status_invoice_request" env-default:""`
	StatusInvoiceResult   string `yaml:"status_invoice_result" env-default:""`
	CustomFieldNIP        string `yaml:"custom_field_nip" env-default:""`
}

type Telegram struct {
	Enabled           bool   `yaml:"enabled" env-default:"false"`
	ApiKey            string `yaml:"api_key" env-default:""`
	RequireApproval   bool   `yaml:"require_approval" env-default:"true"`
	DigestIntervalMin int    `yaml:"digest_interval_min" env-default:"60"`
	DefaultTier       string `yaml:"default_tier" env-default:"realtime"`
	InviteCodeLength  int    `yaml:"invite_code_length" env-default:"8"`
}

type Config struct {
	Stripe   StripeConfig `yaml:"stripe"`
	WFirma   WfirmaConfig `yaml:"wfirma"`
	Listen   Listen       `yaml:"listen"`
	Mongo    Mongo        `yaml:"mongo"`
	OpenCart OpenCart     `yaml:"opencart"`
	Telegram Telegram     `yaml:"telegram"`
	Env      string       `yaml:"env" env-default:"local"`
	Log      string       `yaml:"log"`
	Location string       `yaml:"location" env-default:"UTC"`
	FilePath string       `yaml:"file_path" env-default:""`
}

var instance *Config
var once sync.Once

func MustLoad(path string) *Config {
	var err error
	once.Do(func() {
		instance = &Config{}
		if err = cleanenv.ReadConfig(path, instance); err != nil {
			desc, _ := cleanenv.GetDescription(instance, nil)
			err = fmt.Errorf("config: %s; %s", err, desc)
			instance = nil
			log.Fatal(err)
		}
	})
	return instance
}
