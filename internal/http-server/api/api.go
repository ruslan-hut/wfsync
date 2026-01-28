package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
	"wfsync/internal/config"
	"wfsync/internal/http-server/handlers/b2b"
	"wfsync/internal/http-server/handlers/errors"
	"wfsync/internal/http-server/handlers/payment"
	"wfsync/internal/http-server/handlers/stripehandler"
	"wfsync/internal/http-server/handlers/wfinvoice"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"

	"wfsync/internal/http-server/middleware/authenticate"
	"wfsync/internal/http-server/middleware/timeout"
	"wfsync/lib/sl"
)

type Server struct {
	conf       *config.Config
	httpServer *http.Server
	log        *slog.Logger
}

type Handler interface {
	authenticate.Authenticate
	stripehandler.Core
	wfinvoice.Core
	payment.Core
	b2b.Core
}

func New(conf *config.Config, log *slog.Logger, handler Handler) (*Server, error) {
	server := &Server{
		conf: conf,
		log:  log.With(sl.Module("api.server")),
	}

	router := chi.NewRouter()
	router.Use(timeout.Timeout(30 * time.Second)) // wfirma requests need long timeouts
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(render.SetContentType(render.ContentTypeJSON))

	router.NotFound(errors.NotFound(log))
	router.MethodNotAllowed(errors.NotAllowed(log))

	router.Route("/v1", func(rootApi chi.Router) {
		rootApi.Use(authenticate.New(log, handler))
		rootApi.Route("/wf", func(wf chi.Router) {
			wf.Get("/invoice/{id}", wfinvoice.Download(log, handler))
			wf.Get("/order/{id}", wfinvoice.OrderToInvoice(log, handler))
			wf.Get("/file/proforma/{id}", wfinvoice.FileProforma(log, handler))
			wf.Get("/file/invoice/{id}", wfinvoice.FileInvoice(log, handler))
			wf.Post("/proforma", wfinvoice.CreateProforma(log, handler))
			wf.Post("/invoice", wfinvoice.CreateInvoice(log, handler))
		})
		rootApi.Route("/st", func(st chi.Router) {
			st.Post("/hold", payment.Hold(log, handler))
			st.Post("/pay", payment.Pay(log, handler))
			st.Post("/capture/{id}", payment.Capture(log, handler))
			st.Post("/cancel/{id}", payment.Cancel(log, handler))
		})
		rootApi.Route("/b2b", func(b2bRouter chi.Router) {
			b2bRouter.Post("/proforma", b2b.CreateProforma(log, handler))
			b2bRouter.Post("/invoice", b2b.CreateInvoice(log, handler))
		})
	})
	router.Route("/webhook", func(rootWH chi.Router) {
		rootWH.Post("/event", stripehandler.Event(log, handler))
	})

	httpLog := slog.NewLogLogger(log.Handler(), slog.LevelError)
	server.httpServer = &http.Server{
		Handler:      router,
		ErrorLog:     httpLog,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverAddress := fmt.Sprintf("%s:%s", conf.Listen.BindIp, conf.Listen.Port)
	listener, err := net.Listen("tcp", serverAddress)
	if err != nil {
		return nil, err
	}

	server.log.Info("starting api server", slog.String("address", serverAddress))

	go func() {
		if err := server.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			server.log.Error("http server error", sl.Err(err))
		}
	}()

	return server, nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("shutting down api server")
	return s.httpServer.Shutdown(ctx)
}
