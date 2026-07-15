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

	// KSefDraftFallback, when true, makes invoice creation fall back to a draft
	// (wersja robocza, type "normal_draft") if wFirma rejects a normal invoice with a
	// KSeF authorization error. The draft is not sent to KSeF, so it succeeds without
	// the API user's KSeF authorization, but must be accepted manually in wFirma to
	// become a legal invoice. See docs/wfirma-ksef-draft-fallback.md.
	KSefDraftFallback bool `yaml:"ksef_draft_fallback" env-default:"false"`

	// KSefDownloadWaitSeconds bounds how long DownloadInvoice waits for a KSeF-submitted
	// invoice to finish processing before falling back to a best-effort download. Until an
	// invoice is processed by KSeF, wFirma can only render an interim "transaction
	// confirmation" (a QR-only summary, not the full invoice), so we poll invoices/get for
	// the assigned KSeF number first. 0 disables the gate (download immediately, legacy
	// behavior). See docs/wfirma-ksef-download-confirmation.md.
	KSefDownloadWaitSeconds int `yaml:"ksef_download_wait_seconds" env-default:"30"`
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

type VATRates struct {
	Enabled      bool `yaml:"enabled" env-default:"false"`
	RefreshHours int  `yaml:"refresh_hours" env-default:"24"`
	TrustDB      bool `yaml:"trust_db" env-default:"false"`
}

type VIES struct {
	Enabled    bool `yaml:"enabled" env-default:"false"`
	CacheHours int  `yaml:"cache_hours" env-default:"720"`
}

type RetryQueue struct {
	Enabled         bool `yaml:"enabled" env-default:"false"`
	IntervalMin     int  `yaml:"interval_min" env-default:"5"`
	MaxRetries      int  `yaml:"max_retries" env-default:"10"`
	BaseDelaySec    int  `yaml:"base_delay_sec" env-default:"60"`
	MaxOrderAgeDays int  `yaml:"max_order_age_days" env-default:"60"`
}

// PaymentReconciler configures the periodic job that reconciles held Stripe payments
// against their live status (invoicing captured holds, reflecting cancellations).
type PaymentReconciler struct {
	Enabled     bool `yaml:"enabled" env-default:"false"`
	IntervalMin int  `yaml:"interval_min" env-default:"15"`
}

type Config struct {
	Stripe            StripeConfig      `yaml:"stripe"`
	WFirma            WfirmaConfig      `yaml:"wfirma"`
	Listen            Listen            `yaml:"listen"`
	Mongo             Mongo             `yaml:"mongo"`
	OpenCart          OpenCart          `yaml:"opencart"`
	Telegram          Telegram          `yaml:"telegram"`
	VATRates          VATRates          `yaml:"vatrates"`
	VIES              VIES              `yaml:"vies"`
	RetryQueue        RetryQueue        `yaml:"retry_queue"`
	PaymentReconciler PaymentReconciler `yaml:"payment_reconciler"`
	Env               string            `yaml:"env" env-default:"local"`
	Log               string            `yaml:"log"`
	Location          string            `yaml:"location" env-default:"UTC"`
	FilePath          string            `yaml:"file_path" env-default:""`
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
