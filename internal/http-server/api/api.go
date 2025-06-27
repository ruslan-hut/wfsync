package api

import (
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"log/slog"
	"net"
	"net/http"
	"time"
	"wfsync/internal/config"
	"wfsync/internal/http-server/handlers/errors"
	"wfsync/internal/http-server/handlers/payment"
	"wfsync/internal/http-server/handlers/stripehandler"
	"wfsync/internal/http-server/handlers/wfinvoice"

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
}

func New(conf *config.Config, log *slog.Logger, handler Handler) error {

	server := Server{
		conf: conf,
		log:  log.With(sl.Module("api.server")),
	}

	router := chi.NewRouter()
	router.Use(timeout.Timeout(5))
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(render.SetContentType(render.ContentTypeJSON))

	router.NotFound(errors.NotFound(log))
	router.MethodNotAllowed(errors.NotAllowed(log))

	router.Route("/v1", func(rootApi chi.Router) {
		rootApi.Use(authenticate.New(log, handler))
		rootApi.Route("/wf", func(wf chi.Router) {
			wf.Route("/invoice", func(inv chi.Router) {
				inv.Get("/download/{id}", wfinvoice.Download(log, handler))
			})
		})
		rootApi.Route("/st", func(st chi.Router) {
			st.Get("/hold/{sum}", payment.Hold(log, handler))
			st.Get("/capture/{sum}", payment.Hold(log, handler))
			st.Get("/cancel/{sum}", payment.Hold(log, handler))
		})
	})
	router.Route("/webhook", func(rootWH chi.Router) {
		rootWH.Post("/stripe", stripehandler.Event(log, handler))
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
		return err
	}

	server.log.Info("starting api server", slog.String("address", serverAddress))

	return server.httpServer.Serve(listener)
}
