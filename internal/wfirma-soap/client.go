package wfirma_soap

import (
	"context"
	"fmt"
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
		baseURL:  "http://api.wfirma.pl",
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
		return "", fmt.Errorf("api client init failed")
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
		return "", fmt.Errorf("login failed")
	}
	c.log.With(
		slog.Any("loginRes", loginRes),
	).Debug("login response")
	return "", nil
}
