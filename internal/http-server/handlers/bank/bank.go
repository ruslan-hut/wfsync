// Package bank exposes the bank account authorization endpoints.
//
// The two handlers sit on opposite sides of the authentication boundary and must stay
// there. StartAuth is our own call and lives under /v1 behind the bearer token.
// Callback is driven by the bank through the account holder's browser and carries no
// token of ours, so it is registered outside /v1 — the same placement as the Stripe
// webhook. It is protected instead by the state value, which must match an
// authorization this service started and has not yet completed.
package bank

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"wfsync/entity"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

// Core defines the methods the bank handlers need.
type Core interface {
	BankStartAuthorization(ctx context.Context) (string, error)
	BankCompleteAuthorization(ctx context.Context, state, code string) (*entity.BankSession, error)
	BankSessionStatus() (*entity.BankSession, error)
	BankTransactions(ctx context.Context, from, to string) ([]*entity.BankTransaction, error)
}

// datePattern guards the range parameters; the stored dates are "YYYY-MM-DD" strings and
// are compared as such, so a differently shaped value would silently match nothing.
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// authResponse carries the URL the account holder has to open.
type authResponse struct {
	// URL is the bank's authorization page. It is single-use and short-lived.
	URL string `json:"url"`
	// Instructions spell out that a person must complete this by hand, because the
	// caller is typically an operator triggering a renewal rather than a program.
	Instructions string `json:"instructions"`
}

// statusResponse reports the state of the current authorization.
type statusResponse struct {
	Authorized bool                    `json:"authorized"`
	Status     string                  `json:"status,omitempty"`
	ASPSP      string                  `json:"aspsp,omitempty"`
	ValidUntil string                  `json:"valid_until,omitempty"`
	DaysLeft   int                     `json:"days_left,omitempty"`
	Accounts   []entity.BankAccountRef `json:"accounts,omitempty"`
}

// StartAuth handles POST /v1/bank/auth — returns the URL to open at the bank.
func StartAuth(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.With(
			sl.Module("http.handlers.bank"),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			log.Error("bank service not available")
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, response.Error("Bank service not available"))
			return
		}

		url, err := handler.BankStartAuthorization(r.Context())
		if err != nil {
			log.Error("start bank authorization", sl.Err(err))
			render.Status(r, http.StatusBadGateway)
			render.JSON(w, r, response.Error(fmt.Sprintf("Start authorization: %v", err)))
			return
		}

		render.JSON(w, r, response.Ok(authResponse{
			URL: url,
			Instructions: "Open this URL in a browser, sign in to the bank and approve access " +
				"in the bank's mobile app. The link is single-use.",
		}))
	}
}

// Status handles GET /v1/bank/status — reports the live authorization, including how
// long it has left. Operations watch this to schedule the next re-authorization.
func Status(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.With(
			sl.Module("http.handlers.bank"),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, response.Error("Bank service not available"))
			return
		}

		session, err := handler.BankSessionStatus()
		if err != nil {
			log.Error("bank session status", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Session status: %v", err)))
			return
		}
		if session == nil {
			render.JSON(w, r, response.Ok(statusResponse{Authorized: false}))
			return
		}

		render.JSON(w, r, response.Ok(statusResponse{
			Authorized: session.IsUsable(time.Now()),
			Status:     session.Status,
			ASPSP:      session.ASPSP,
			ValidUntil: session.ValidUntil.Format(time.RFC3339),
			DaysLeft:   session.DaysLeft(time.Now()),
			Accounts:   session.Accounts,
		}))
	}
}

// Transactions handles GET /v1/bank/transactions?from=&to= — the raw statement feed
// consumed by the ERP. It reports what the bank booked, with no matching applied.
func Transactions(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.With(
			sl.Module("http.handlers.bank"),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, response.Error("Bank service not available"))
			return
		}

		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if !datePattern.MatchString(from) || !datePattern.MatchString(to) {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, response.Error("Invalid date format, expected YYYY-MM-DD"))
			return
		}

		txs, err := handler.BankTransactions(r.Context(), from, to)
		if err != nil {
			log.Error("bank transactions", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Transactions: %v", err)))
			return
		}

		render.JSON(w, r, response.Ok(transactionsResponse{
			From:  from,
			To:    to,
			Count: len(txs),
			Items: txs,
		}))
	}
}

// transactionsResponse wraps the statement feed with the range it covers, so a consumer
// can tell an empty range from a failed one.
type transactionsResponse struct {
	From  string                    `json:"from"`
	To    string                    `json:"to"`
	Count int                       `json:"count"`
	Items []*entity.BankTransaction `json:"items"`
}

// Callback handles GET /bank/callback — where the bank returns the account holder.
//
// It answers in HTML rather than JSON: the reader is a person looking at a browser tab
// they were redirected into, not a program. Nothing sensitive is echoed back, since
// the page is rendered from a URL that may be logged or shared.
func Callback(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.With(
			sl.Module("http.handlers.bank"),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		q := r.URL.Query()
		state := q.Get("state")
		code := q.Get("code")

		// The bank reports a refusal or a timeout this way instead of sending a code.
		if bankErr := q.Get("error"); bankErr != "" {
			log.Warn("bank returned an authorization error",
				slog.String("error", bankErr),
				slog.String("description", q.Get("error_description")))
			writePage(w, http.StatusOK, "Authorization was not completed",
				"The bank did not grant access. Please start the authorization again.")
			return
		}

		if state == "" || code == "" {
			log.Warn("callback without state or code")
			writePage(w, http.StatusBadRequest, "Invalid request",
				"This link is missing required parameters.")
			return
		}

		if handler == nil {
			log.Error("bank service not available")
			writePage(w, http.StatusServiceUnavailable, "Service unavailable",
				"Please try again later.")
			return
		}

		session, err := handler.BankCompleteAuthorization(r.Context(), state, code)
		if err != nil {
			log.Error("complete bank authorization", sl.Err(err))
			writePage(w, http.StatusOK, "Authorization failed",
				"Access could not be established. Please start the authorization again.")
			return
		}

		log.Info("bank authorization completed via callback",
			slog.Int("accounts", len(session.Accounts)),
			slog.Int("days_left", session.DaysLeft(time.Now())))

		writePage(w, http.StatusOK, "Access granted", fmt.Sprintf(
			"%d account(s) connected. Access is valid until %s.",
			len(session.Accounts), session.ValidUntil.Format("2006-01-02")))
	}
}

// writePage renders the minimal confirmation page shown to the account holder.
func writePage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>body{font-family:system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f7f9;color:#1a1a1a}
.card{background:#fff;padding:2rem 2.5rem;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,.1);max-width:26rem;text-align:center}
h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:0;color:#555;line-height:1.5}</style>
</head><body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}
