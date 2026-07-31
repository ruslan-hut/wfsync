package wfsync

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"wfsync/entity"
	"wfsync/lib/api/cont"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

// Core defines the methods required by the sync handlers.
type Core interface {
	WFirmaSyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	WFirmaSyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	InvoiceList(ctx context.Context, from, to string) (*entity.InvoiceListResult, error)
}

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// SyncFromRemote handles POST /v1/wf/sync/pull — pulls invoices from wFirma to local DB.
func SyncFromRemote(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.wfsync")
		user := cont.GetUser(r.Context())

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("user", user.Username),
		)

		if !user.WFirmaAllowInvoice {
			log.Warn("invoice sync not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice sync not allowed"))
			return
		}

		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if !datePattern.MatchString(from) || !datePattern.MatchString(to) {
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid date format, expected YYYY-MM-DD"))
			return
		}

		result, err := handler.WFirmaSyncFromRemote(r.Context(), from, to)
		if err != nil {
			log.Error("sync from remote", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Sync failed: %v", err)))
			return
		}

		render.JSON(w, r, response.Ok(result))
	}
}

// SyncToRemote handles POST /v1/wf/sync/push — pushes local invoices to wFirma.
func SyncToRemote(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.wfsync")
		user := cont.GetUser(r.Context())

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("user", user.Username),
		)

		if !user.WFirmaAllowInvoice {
			log.Warn("invoice sync not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice sync not allowed"))
			return
		}

		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if !datePattern.MatchString(from) || !datePattern.MatchString(to) {
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid date format, expected YYYY-MM-DD"))
			return
		}

		result, err := handler.WFirmaSyncToRemote(r.Context(), from, to)
		if err != nil {
			log.Error("sync to remote", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Sync failed: %v", err)))
			return
		}

		render.JSON(w, r, response.Ok(result))
	}
}

// InvoiceList handles GET /v1/wf/list — reports which orders in a date range were
// registered as invoices in wFirma, merging OpenCart, checkout_params and wFirma.
// With ?format=csv the items are streamed as a spreadsheet instead of JSON.
func InvoiceList(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.wfsync")
		user := cont.GetUser(r.Context())

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("user", user.Username),
		)

		if !user.WFirmaAllowInvoice {
			log.Warn("invoice list not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice list not allowed"))
			return
		}

		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if !datePattern.MatchString(from) || !datePattern.MatchString(to) {
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid date format, expected YYYY-MM-DD"))
			return
		}

		result, err := handler.InvoiceList(r.Context(), from, to)
		if err != nil {
			log.Error("invoice list", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Request failed: %v", err)))
			return
		}

		if r.URL.Query().Get("format") == "csv" {
			writeInvoiceListCSV(w, result)
			return
		}

		render.JSON(w, r, response.Ok(result))
	}
}

// writeInvoiceListCSV writes the invoice list items as a CSV file response. Only the
// items are exported; the summary and per-source status stay in the JSON form.
func writeInvoiceListCSV(w http.ResponseWriter, result *entity.InvoiceListResult) {
	fileName := fmt.Sprintf("invoices_%s_%s.csv", result.From, result.To)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))

	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Header row
	_ = cw.Write([]string{
		"Order Date", "Invoice Date", "Order Status", "Order ID", "External ID", "Source",
		"Invoiced", "Document", "Invoice Number", "Parts", "Duplicate?",
		"Contractor Name", "B2B", "Stripe", "Total PLN", "Total EUR", "Total USD", "Currency",
	})

	for _, item := range result.Items {
		_ = cw.Write([]string{
			item.OrderDate,
			item.InvoiceDate,
			strconv.Itoa(item.OrderStatus),
			item.OrderId,
			item.ExternalId,
			string(item.Source),
			boolYesNo(item.Invoiced),
			string(item.DocumentType),
			item.InvoiceNumber,
			formatParts(item.InvoiceParts),
			formatDuplicate(item),
			item.ContractorName,
			boolYesNo(item.IsB2B),
			boolYesNo(item.IsStripe),
			formatCents(item.TotalPLN),
			formatCents(item.TotalEUR),
			formatCents(item.TotalUSD),
			item.Currency,
		})
	}
}

// formatParts renders the split-invoice part count, blank for the ordinary single-part case.
func formatParts(n int) string {
	if n <= 1 {
		return ""
	}
	return strconv.Itoa(n)
}

// formatDuplicate renders the duplicate hint only for multi-document orders: "Yes" when
// an amount repeats across the order's documents, "No" when the amounts all differ (an
// ordinary split), blank for the single-document case where the question does not arise.
func formatDuplicate(item *entity.InvoiceListItem) string {
	if item.InvoiceParts <= 1 {
		return ""
	}
	return boolYesNo(item.DuplicateSuspect)
}

func boolYesNo(v bool) string {
	if v {
		return "Yes"
	}
	return "No"
}

// formatCents converts minor units (cents) to a decimal string, e.g. 12345 → "123.45".
// Returns empty string for zero values.
func formatCents(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(float64(v)/100, 'f', 2, 64)
}
