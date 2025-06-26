package wfirma_soap

import (
	"context"
	"github.com/tiaguinho/gosoap"
	"log/slog"
	"net/http"
	"time"
	"wfsync/lib/sl"
)

type Client struct {
	hc       *http.Client
	baseURL  string
	username string
	password string
	log      *slog.Logger
}

type Config struct {
	Username string
	Password string
}

func NewClient(conf Config, logger *slog.Logger) *Client {
	return &Client{
		hc:       &http.Client{Timeout: 10 * time.Second},
		baseURL:  "https://api.wfirma.pl/soap.php?wsdl",
		username: conf.Username,
		password: conf.Password,
		log:      logger.With(sl.Module("wf-soap")),
	}
}

func (c *Client) Download(_ context.Context, invoiceID string) (string, error) {
	// Создаём SOAP-клиент
	soap, err := gosoap.SoapClient(c.baseURL, c.hc)
	if err != nil {
		c.log.Error("failed to create SOAP client",
			sl.Err(err),
		)
		return "", err
	}
	//loginRes := struct {
	//	SID string `xml:"sid"`
	//}{}
	loginRes, err := soap.Call("login", gosoap.Params{
		"username": c.username,
		"password": c.password,
	})
	if err != nil {
		c.log.Error("failed to login",
			sl.Err(err),
		)
		return "", err
	}
	c.log.With(
		slog.Any("loginRes", loginRes),
	).Debug("login response")
	return "", nil
}
