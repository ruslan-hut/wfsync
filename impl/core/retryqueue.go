// Package core — retryqueue.go implements a persistent retry queue for failed wFirma invoice
// registrations. When the wFirma API is down during a Stripe webhook, the failed job is saved
// to MongoDB and retried with exponential backoff until it succeeds or exhausts max attempts.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
	occlient "wfsync/opencart/oc-client"
)

// RetryDatabase defines the persistence methods the retry queue needs.
type RetryDatabase interface {
	SaveRetryJob(job *entity.RetryJob) error
	GetPendingRetryJobs() ([]*entity.RetryJob, error)
	GetRetryJobsByOrderId(orderId string) ([]*entity.RetryJob, error)
	UpdateRetryJob(job *entity.RetryJob) error
	GetRetryJobByEventId(eventId string) (*entity.RetryJob, error)
	GetCheckoutParamsForEvent(eventId string) (*entity.CheckoutParams, error)
}

// RetryQueue polls for pending retry jobs and attempts to re-register invoices
// with exponential backoff. Follows the same Start/Stop pattern as vatrates.Service.
type RetryQueue struct {
	db          RetryDatabase
	inv         InvoiceService
	oc          *occlient.Opencart
	log         *slog.Logger
	interval    time.Duration
	maxRetries  int
	baseDelay   time.Duration
	maxOrderAge time.Duration
	done        chan struct{}
	stopped     chan struct{}
	// processing serializes the polling loop against operator-triggered retries, so the
	// same job cannot be attempted twice at once. The wFirma layer would catch the
	// duplicate anyway, but only after the work — and the log — has been done twice.
	processing sync.Mutex
}

// NewRetryQueue creates a retry queue. Call Start() to begin background processing.
// maxOrderAgeDays gives up on jobs whose underlying order is older than that many days
// (0 disables the age guard), so chronically failing ancient orders stop retrying.
func NewRetryQueue(log *slog.Logger, intervalMin, maxRetries, baseDelaySec, maxOrderAgeDays int) *RetryQueue {
	if intervalMin <= 0 {
		intervalMin = 5
	}
	if maxRetries <= 0 {
		maxRetries = 10
	}
	if baseDelaySec <= 0 {
		baseDelaySec = 60
	}
	return &RetryQueue{
		log:         log.With(sl.Module("retry-queue")),
		interval:    time.Duration(intervalMin) * time.Minute,
		maxRetries:  maxRetries,
		baseDelay:   time.Duration(baseDelaySec) * time.Second,
		maxOrderAge: time.Duration(maxOrderAgeDays) * 24 * time.Hour,
	}
}

func (rq *RetryQueue) SetDatabase(db RetryDatabase)         { rq.db = db }
func (rq *RetryQueue) SetInvoiceService(inv InvoiceService) { rq.inv = inv }
func (rq *RetryQueue) SetOpencart(oc *occlient.Opencart)    { rq.oc = oc }

// Enqueue records a failed invoice registration as a retry job.
// Idempotent by EventId — if a job for this event already exists, it's a no-op.
//
// A rejection of the buyer's data (entity.CustomerDataError) is stored already failed:
// automatic retries cannot fix it, but the job must exist so /retry can re-run it once
// the order is corrected in OpenCart.
func (rq *RetryQueue) Enqueue(params *entity.CheckoutParams, cause error) {
	// No mutex needed: SaveRetryJob is an upsert by _id (= EventId), so
	// concurrent enqueues for the same event collapse to a single document.
	if rq.db == nil {
		rq.log.Warn("no database configured, cannot enqueue retry job",
			slog.String("event_id", params.EventId))
		return
	}

	existing, _ := rq.db.GetRetryJobByEventId(params.EventId)
	if existing != nil {
		rq.log.Debug("retry job already exists",
			slog.String("event_id", params.EventId))
		return
	}

	job := rq.newJob(params, cause, time.Now())

	if err := rq.db.SaveRetryJob(job); err != nil {
		rq.log.Error("save retry job", sl.Err(err),
			slog.String("event_id", params.EventId))
		return
	}

	// The underlying error has already been reported to Telegram by the wfirma layer;
	// keep this as a local-only log so it doesn't render as a fresh retry-queue error.
	rq.log.Info("retry job enqueued",
		slog.String("event_id", params.EventId),
		slog.String("order_id", params.OrderId),
		slog.String("status", string(job.Status)),
		slog.String("error", job.LastError),
		slog.Bool("tg_skip", true))
}

// newJob builds the retry job for a failed registration, classifying the cause.
func (rq *RetryQueue) newJob(params *entity.CheckoutParams, cause error, now time.Time) *entity.RetryJob {
	job := &entity.RetryJob{
		ID:          params.EventId,
		EventId:     params.EventId,
		OrderId:     params.OrderId,
		Status:      entity.RetryJobPending,
		MaxAttempts: rq.maxRetries,
		LastError:   cause.Error(),
		NextRetryAt: now.Add(rq.baseDelay),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if entity.AsCustomerDataError(cause) != nil {
		job.Status = entity.RetryJobFailed
		job.NextRetryAt = time.Time{}
	}
	return job
}

// Start launches the background polling goroutine.
func (rq *RetryQueue) Start() {
	rq.done = make(chan struct{})
	rq.stopped = make(chan struct{})
	go func() {
		defer close(rq.stopped)

		// Process any overdue jobs immediately on startup
		rq.processJobs()

		ticker := time.NewTicker(rq.interval)
		defer ticker.Stop()
		for {
			select {
			case <-rq.done:
				rq.log.Debug("retry queue stopped")
				return
			case <-ticker.C:
				rq.processJobs()
			}
		}
	}()
}

// Stop signals the background goroutine to exit and waits for it to finish.
func (rq *RetryQueue) Stop() {
	if rq.done != nil {
		rq.log.Debug("stopping retry queue")
		close(rq.done)
		<-rq.stopped
	}
}

// processJobs queries all pending jobs that are due and processes each one.
func (rq *RetryQueue) processJobs() {
	if rq.db == nil {
		return
	}

	jobs, err := rq.db.GetPendingRetryJobs()
	if err != nil {
		rq.log.Error("get pending retry jobs", sl.Err(err))
		return
	}
	if len(jobs) == 0 {
		return
	}

	rq.processing.Lock()
	defer rq.processing.Unlock()

	rq.log.Info("processing retry jobs", slog.Int("count", len(jobs)))
	for _, job := range jobs {
		rq.processOneJob(job, false)
	}
}

// RetryNow runs an order's retry job immediately, ignoring the backoff schedule, and
// returns the job in its post-attempt state so the caller can report the outcome.
//
// It is the operator escape hatch behind the Telegram /retry command, so it deliberately
// overrides the automation's two give-up rules: a job that exhausted MaxAttempts is granted
// one more, and the max-order-age guard is skipped. Both exist to stop the queue grinding on
// hopeless jobs unattended — neither should block a human who has just fixed the cause.
//
// A job that already completed is returned untouched: re-running it would only re-confirm
// the invoice exists, and returning it lets the caller say so.
func (rq *RetryQueue) RetryNow(orderId string) (*entity.RetryJob, error) {
	if rq.db == nil {
		return nil, fmt.Errorf("no database configured")
	}
	if orderId == "" {
		return nil, fmt.Errorf("no order id")
	}

	jobs, err := rq.db.GetRetryJobsByOrderId(orderId)
	if err != nil {
		return nil, fmt.Errorf("look up retry jobs: %w", err)
	}
	job := pickRetryJob(jobs)
	if job == nil {
		return nil, fmt.Errorf("no retry job for order %s", orderId)
	}
	if job.Status == entity.RetryJobCompleted {
		return job, nil
	}

	rq.processing.Lock()
	defer rq.processing.Unlock()

	job.Status = entity.RetryJobPending
	if job.Attempts >= job.MaxAttempts {
		job.MaxAttempts = job.Attempts + 1
	}

	rq.log.With(
		slog.String("event_id", job.EventId),
		slog.String("order_id", job.OrderId),
		slog.Int("attempts", job.Attempts),
	).Info("manual retry requested")

	rq.processOneJob(job, true)
	return job, nil
}

// pickRetryJob selects the job to act on for an order. An order normally has exactly one
// (jobs are keyed by Stripe event id), but a re-paid or re-captured order can accumulate
// several: prefer a completed one — it means the invoice exists and nothing should be
// retried — then the most recent unfinished one (the slice is newest-first).
func pickRetryJob(jobs []*entity.RetryJob) *entity.RetryJob {
	var candidate *entity.RetryJob
	for _, job := range jobs {
		if job.Status == entity.RetryJobCompleted {
			return job
		}
		if candidate == nil {
			candidate = job
		}
	}
	return candidate
}

// processOneJob attempts to register an invoice for a single retry job.
// On success, it saves the result to OpenCart and marks the job completed.
// On failure, it applies exponential backoff or marks the job as failed.
//
// manual marks an operator-triggered attempt (see RetryNow) and exempts the job from the
// max-order-age guard. Callers must hold rq.processing.
func (rq *RetryQueue) processOneJob(job *entity.RetryJob, manual bool) {
	log := rq.log.With(
		slog.String("event_id", job.EventId),
		slog.String("order_id", job.OrderId),
		slog.Int("attempt", job.Attempts+1),
		slog.Bool("manual", manual),
	)

	// Load the original checkout params from the database
	params, err := rq.db.GetCheckoutParamsForEvent(job.EventId)
	if err != nil {
		log.Error("load checkout params for retry", sl.Err(err))
		rq.failJob(job, "load checkout params: "+err.Error())
		return
	}
	if params == nil {
		log.Error("checkout params not found for retry event")
		rq.failJob(job, "checkout params not found")
		return
	}

	// Abandon jobs whose order is older than the configured max age. The job itself may
	// be young (e.g. re-enqueued by a manual re-run or the reconciler), so age is measured
	// from the order/payment date in the stored params, falling back to the job creation
	// time when that is unset.
	if rq.maxOrderAge > 0 && !manual {
		orderDate := params.Created
		if orderDate.IsZero() {
			orderDate = job.CreatedAt
		}
		if age := time.Since(orderDate); age > rq.maxOrderAge {
			log.With(
				slog.String("order_date", orderDate.Format(time.RFC3339)),
				slog.Duration("age", age),
				slog.String("last_error", job.LastError),
				slog.String("tg_topic", entity.TopicError),
			).Warn("retry job abandoned: order older than max age")
			rq.failJob(job, fmt.Sprintf("order older than %s, abandoned after %d attempts", rq.maxOrderAge, job.Attempts))
			return
		}
	}

	rq.refreshClientDetails(params, log)

	// Flag the context as a retry so the wFirma layer keeps per-attempt failures local —
	// the original error was already reported to Telegram when the job was enqueued.
	ctx := entity.WithRetry(context.Background())

	// Same guard as processInvoice, for jobs enqueued before it existed or whose stored
	// params drifted. Terminal: a wrong amount needs a human, not another attempt.
	if err := checkCapturedAmount(params); err != nil {
		log.With(
			slog.String("invoice_total", formatMinor(params.Total)),
			slog.String("captured", formatMinor(params.AmountPaid)),
			slog.String("tg_topic", entity.TopicError),
		).Error("retry job failed: invoice total differs from captured amount", sl.Err(err))
		rq.failJob(job, err.Error())
		return
	}

	// Attempt to register the invoice. RegisterInvoice is order-level idempotent: a faktura
	// a capture / webhook / reconciler already issued is reused (matched on id_external),
	// and a split order that failed part-way resumes at its first missing part. A lookup
	// error surfaces as err and reschedules the job rather than risking a duplicate.
	payment, err := rq.inv.RegisterInvoice(ctx, params)
	job.Attempts++
	job.UpdatedAt = time.Now()

	if err != nil {
		if cde := entity.AsCustomerDataError(err); cde != nil {
			// The operator who ran /retry sees LastError in the reply; only an unattended
			// attempt needs the Telegram alert.
			reportCustomerDataError(rq.log.With(slog.String("event_id", job.EventId)), job.OrderId, cde, !manual)
			rq.failJob(job, err.Error())
			return
		}
		log.Warn("retry invoice registration failed", sl.Err(err))
		rq.retryLater(job, log, err.Error())
		return
	}

	// Success — save invoice ID to OpenCart and mark completed
	if payment != nil && rq.oc != nil {
		if ocErr := rq.oc.SaveInvoiceId(params.OrderId, payment.Id, payment.InvoiceFile); ocErr != nil {
			log.Error("save invoice id to opencart after retry", sl.Err(ocErr))
		}
	}

	job.Status = entity.RetryJobCompleted
	job.LastError = ""
	if dbErr := rq.db.UpdateRetryJob(job); dbErr != nil {
		log.Error("update retry job after success", sl.Err(dbErr))
	}

	log.Info("retry job completed successfully",
		slog.String("invoice_id", payment.Id),
		slog.String("tg_topic", entity.TopicPayment))
}

// refreshClientDetails replaces the stored buyer data with the current OpenCart order,
// the same precedence processInvoice applies. The stored params are a snapshot from the
// failed attempt, so without this a /retry after the order was corrected would resend
// the very data wFirma rejected. A lookup failure keeps the snapshot.
func (rq *RetryQueue) refreshClientDetails(params *entity.CheckoutParams, log *slog.Logger) {
	if rq.oc == nil || params.OrderId == "" {
		return
	}
	orderId, err := rq.oc.ResolveOrderId(params.OrderId)
	if err != nil {
		log.Warn("resolve opencart order for retry, using stored client data", sl.Err(err))
		return
	}
	if orderId == 0 {
		log.Warn("opencart order not resolved for retry, using stored client data")
		return
	}
	order, err := rq.oc.GetOrder(orderId)
	if err != nil {
		log.Warn("load opencart order for retry, using stored client data", sl.Err(err))
		return
	}
	if order == nil || order.ClientDetails == nil {
		log.Warn("opencart order has no client details, using stored client data")
		return
	}
	if order.ClientDetails.Email == "" && params.ClientDetails != nil {
		order.ClientDetails.Email = params.ClientDetails.Email
	}
	params.ClientDetails = order.ClientDetails
}

// reportCustomerDataError raises the alert for a retry attempt blocked by the buyer's
// data. The first failure is reported by the wFirma layer (see Client.invoice), which
// stays silent on retry attempts; notify=false keeps it local when the operator ran
// /retry and sees the outcome in the reply.
func reportCustomerDataError(log *slog.Logger, orderId string, e *entity.CustomerDataError, notify bool) {
	tg := slog.String("tg_topic", entity.TopicError)
	if !notify {
		tg = slog.Bool("tg_skip", true)
	}
	log.With(
		slog.String("order_id", orderId),
		slog.String("field", e.Field),
		slog.String("message", e.Message),
		slog.String("action", fmt.Sprintf("fix %s in OpenCart order %s, then /retry %s", e.Field, orderId, orderId)),
		tg,
	).Error("wfirma rejected customer data, invoice not issued, no auto-retry")
}

// retryLater records a failed attempt: it applies exponential backoff, or marks the job
// permanently failed once MaxAttempts is reached, then persists the change. The caller is
// responsible for having already incremented job.Attempts and set job.UpdatedAt.
func (rq *RetryQueue) retryLater(job *entity.RetryJob, log *slog.Logger, lastError string) {
	job.LastError = lastError

	if job.Attempts >= job.MaxAttempts {
		job.Status = entity.RetryJobFailed
		log.Error("retry job exhausted all attempts",
			slog.String("last_error", job.LastError),
			slog.String("tg_topic", entity.TopicError))
	} else {
		// Exponential backoff: baseDelay * 2^(attempts-1)
		delay := rq.baseDelay * (1 << (job.Attempts - 1))
		job.NextRetryAt = time.Now().Add(delay)
		log.Info("retry job rescheduled",
			slog.String("next_retry_at", job.NextRetryAt.Format(time.RFC3339)),
			slog.Duration("delay", delay))
	}

	if dbErr := rq.db.UpdateRetryJob(job); dbErr != nil {
		log.Error("update retry job after failure", sl.Err(dbErr))
	}
}

// failJob marks a job as permanently failed.
func (rq *RetryQueue) failJob(job *entity.RetryJob, errMsg string) {
	job.Status = entity.RetryJobFailed
	job.LastError = errMsg
	job.UpdatedAt = time.Now()
	if dbErr := rq.db.UpdateRetryJob(job); dbErr != nil {
		rq.log.Error("update failed retry job", sl.Err(dbErr))
	}
}
