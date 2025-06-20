package config

import (
	"fmt"
	"github.com/ilyakaznacheev/cleanenv"
	"log"
	"sync"
)

type Listen struct {
	BindIp string `yaml:"bind_ip" env-default:"0.0.0.0"`
	Port   string `yaml:"port" env-default:"8080"`
}

type StripeConfig struct {
	TestMode      bool   `yaml:"test_mode" env-default:"false"`
	APIKey        string `yaml:"api_key" env-default:""`
	WebhookSecret string `yaml:"webhook_secret" env-default:""`
	TestKey       string `yaml:"test_key" env-default:""`
}

type WfirmaConfig struct {
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
	SaveUrl  string `yaml:"save_url" env-default:""`
}

type Config struct {
	Stripe StripeConfig `yaml:"stripe"`
	WFirma WfirmaConfig `yaml:"wfirma"`
	Listen Listen       `yaml:"listen"`
	Mongo  Mongo        `yaml:"mongo"`
	Env    string       `yaml:"env" env-default:"local"`
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
