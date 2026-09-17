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
	"wfsync/internal/http-server/handlers/bank"
	"wfsync/internal/http-server/handlers/errors"
	"wfsync/internal/http-server/handlers/payment"
	"wfsync/internal/http-server/handlers/stripehandler"
	"wfsync/internal/http-server/handlers/wfinvoice"
	"wfsync/internal/http-server/handlers/wfsync"

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
	wfsync.Core
	payment.Core
	b2b.Core
	b2b.PaymentStatusCore
	bank.Core
}

// Request deadlines. documentRequestTimeout covers the worst case of a split order: four
// parts, each an invoices/add that can be resubmitted once after a stock error.
const (
	defaultRequestTimeout  = 60 * time.Second
	documentRequestTimeout = 5 * time.Minute
)

func New(conf *config.Config, log *slog.Logger, handler Handler) (*Server, error) {
	server := &Server{
		conf: conf,
		log:  log.With(sl.Module("api.server")),
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(render.SetContentType(render.ContentTypeJSON))

	router.NotFound(errors.NotFound(log))
	router.MethodNotAllowed(errors.NotAllowed(log))

	// Routes that issue wFirma documents get a longer deadline than the rest: a split order
	// is one invoices/add call per part, each able to take tens of seconds (twice over when
	// a stock error forces a resubmit), so four parts overrun defaultRequestTimeout. A nested
	// middleware cannot extend a deadline set further up, so the timeout is applied per
	// group instead of once on the root router.
	defaultTimeout := timeout.Timeout(defaultRequestTimeout)
	documentTimeout := timeout.Timeout(documentRequestTimeout)

	router.Route("/v1", func(rootApi chi.Router) {
		rootApi.Use(authenticate.New(log, handler))
		rootApi.Route("/wf", func(wf chi.Router) {
			wf.Group(func(docs chi.Router) {
				docs.Use(documentTimeout)
				docs.Get("/order/{id}", wfinvoice.OrderToInvoice(log, handler))
				docs.Get("/file/proforma/{id}", wfinvoice.FileProforma(log, handler))
				docs.Get("/file/invoice/{id}", wfinvoice.FileInvoice(log, handler))
				docs.Post("/proforma", wfinvoice.CreateProforma(log, handler))
				docs.Post("/invoice", wfinvoice.CreateInvoice(log, handler))
			})
			wf.Group(func(rest chi.Router) {
				rest.Use(defaultTimeout)
				rest.Get("/invoice/{id}", wfinvoice.Download(log, handler))
				rest.Post("/sync/pull", wfsync.SyncFromRemote(log, handler))
				rest.Post("/sync/push", wfsync.SyncToRemote(log, handler))
				rest.Get("/list", wfsync.InvoiceList(log, handler))
			})
		})
		rootApi.Route("/st", func(st chi.Router) {
			st.Use(defaultTimeout)
			st.Post("/hold", payment.Hold(log, handler))
			st.Post("/pay", payment.Pay(log, handler))
			st.Post("/capture/{id}", payment.Capture(log, handler))
			st.Post("/cancel/{id}", payment.Cancel(log, handler))
			st.Get("/status/{id}", payment.Status(log, handler))
			st.Get("/queue", payment.Queue(log, handler))
		})
		rootApi.Route("/bank", func(bankRouter chi.Router) {
			bankRouter.Use(defaultTimeout)
			bankRouter.Post("/auth", bank.StartAuth(log, handler))
			bankRouter.Get("/status", bank.Status(log, handler))
			bankRouter.Get("/transactions", bank.Transactions(log, handler))
			bankRouter.Get("/payments", bank.Payments(log, handler))
			bankRouter.Get("/unmatched", bank.Unmatched(log, handler))
			bankRouter.Post("/match", bank.Match(log, handler))
		})
		rootApi.Route("/b2b", func(b2bRouter chi.Router) {
			b2bRouter.Group(func(docs chi.Router) {
				docs.Use(documentTimeout)
				docs.Post("/proforma", b2b.CreateProforma(log, handler))
				docs.Post("/invoice", b2b.CreateInvoice(log, handler))
			})
			b2bRouter.Group(func(rest chi.Router) {
				rest.Use(defaultTimeout)
				rest.Get("/status/{order_uid}", b2b.PaymentStatus(log, handler))
				rest.Post("/status", b2b.PaymentStatusBatch(log, handler))
			})
		})
	})
	router.Route("/webhook", func(rootWH chi.Router) {
		rootWH.Use(defaultTimeout)
		rootWH.Post("/event", stripehandler.Event(log, handler))
	})
	// The bank redirects the account holder here in their own browser, with no bearer
	// token of ours, so it cannot live under /v1. The state parameter authenticates it.
	router.With(defaultTimeout).Get("/bank/callback", bank.Callback(log, handler))

	httpLog := slog.NewLogLogger(log.Handler(), slog.LevelError)
	server.httpServer = &http.Server{
		Handler:      router,
		ErrorLog:     httpLog,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: documentRequestTimeout + 5*time.Second, // must exceed the longest context deadline to avoid premature connection close
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
	s.log.Debug("shutting down api server")
	return s.httpServer.Shutdown(ctx)
}
