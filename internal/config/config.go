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
	APIKey        string `yaml:"api_key" env-default:""`
	WebhookSecret string `yaml:"webhook_secret" env-default:""`
}

type WfirmaConfig struct {
	AccessKey string `yaml:"access_key" env-default:""`
	SecretKey string `yaml:"secret_key" env-default:""`
	AppID     string `yaml:"app_id" env-default:""`
}

type Config struct {
	Stripe StripeConfig `yaml:"stripe"`
	WFirma WfirmaConfig `yaml:"wfirma"`
	Listen Listen       `yaml:"listen"`
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
