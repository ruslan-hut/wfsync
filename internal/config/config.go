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
	// Attempts is the total number of VIES calls per validation. Member states
	// throttle with transient userError codes (MS_MAX_CONCURRENT_REQ and friends),
	// and a single throttled reply would otherwise cost a B2B customer their 0% WDT
	// rate, so a retryable answer is retried up to this many times.
	Attempts int `yaml:"attempts" env-default:"3"`
	// RetryDelayMs is the delay before the second attempt; it doubles for each
	// further one and carries jitter, so bursts from concurrent orders spread out.
	RetryDelayMs int `yaml:"retry_delay_ms" env-default:"1000"`
}

type RetryQueue struct {
	Enabled         bool `yaml:"enabled" env-default:"false"`
	IntervalMin     int  `yaml:"interval_min" env-default:"5"`
	MaxRetries      int  `yaml:"max_retries" env-default:"10"`
	BaseDelaySec    int  `yaml:"base_delay_sec" env-default:"60"`
	MaxOrderAgeDays int  `yaml:"max_order_age_days" env-default:"60"`
}

// EnableBanking configures access to the Enable Banking PSD2 aggregation API, which
// supplies bank account statements used to match incoming transfers against issued
// documents. Requests are authorized with an RS256 JWT signed by PrivateKeyPath and
// keyed by AppID, so the private key never leaves this service.
type EnableBanking struct {
	Enabled bool   `yaml:"enabled" env-default:"false"`
	BaseURL string `yaml:"base_url" env-default:"https://api.enablebanking.com"`

	// AppID is the Enable Banking application id. It doubles as the JWT "kid".
	AppID string `yaml:"app_id" env-default:""`
	// PrivateKeyPath points to the PEM-encoded RSA private key whose public certificate
	// is registered with the application. Keep it outside the repository, mode 0600.
	PrivateKeyPath string `yaml:"private_key_path" env-default:""`
	// RedirectURL must exactly match one of the redirect URLs registered for the
	// application; the bank sends the account holder back to it after authorization.
	RedirectURL string `yaml:"redirect_url" env-default:""`

	// ASPSPName and ASPSPCountry select the bank connector (e.g. "PKO" / "PL").
	ASPSPName    string `yaml:"aspsp_name" env-default:""`
	ASPSPCountry string `yaml:"aspsp_country" env-default:"PL"`
	// PSUType is "business" for company accounts, "personal" otherwise.
	PSUType string `yaml:"psu_type" env-default:"business"`

	// IBANs limits which of the authorized accounts are polled. Empty means all of them.
	IBANs []string `yaml:"ibans"`

	// PollIntervalMin is how often booked transactions are fetched. Mind that PSD2
	// allows only a limited number of daily requests without the account holder
	// present (4 per day is the regulatory floor), so a short interval may be refused
	// by the bank regardless of what is configured here.
	PollIntervalMin int `yaml:"poll_interval_min" env-default:"30"`
	// LookbackDays sizes the sliding window re-read on every poll. Banks can book or
	// amend entries with a back-date, so polling "since the last seen transaction"
	// would silently miss them.
	LookbackDays int `yaml:"lookback_days" env-default:"7"`
	// ReauthWarnDays is how long before a session expires the service starts warning.
	// Sessions cannot be renewed — only re-authorized by the account holder in person.
	ReauthWarnDays int `yaml:"reauth_warn_days" env-default:"30"`

	// RequestTimeoutSec bounds a single Enable Banking API call.
	RequestTimeoutSec int `yaml:"request_timeout_sec" env-default:"30"`
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
	EnableBanking     EnableBanking     `yaml:"enable_banking"`
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
