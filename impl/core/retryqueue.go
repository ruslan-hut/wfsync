// Package core — retryqueue.go implements a persistent retry queue for failed wFirma invoice
// registrations. When the wFirma API is down during a Stripe webhook, the failed job is saved
// to MongoDB and retried with exponential backoff until it succeeds or exhausts max attempts.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
	occlient "wfsync/opencart/oc-client"
)

// RetryDatabase defines the persistence methods the retry queue needs.
type RetryDatabase interface {
	SaveRetryJob(job *entity.RetryJob) error
	GetPendingRetryJobs() ([]*entity.RetryJob, error)
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

// Enqueue creates a pending retry job for a failed invoice registration.
// Idempotent by EventId — if a job for this event already exists, it's a no-op.
func (rq *RetryQueue) Enqueue(params *entity.CheckoutParams, errMsg string) {
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

	now := time.Now()
	job := &entity.RetryJob{
		ID:          params.EventId,
		EventId:     params.EventId,
		OrderId:     params.OrderId,
		Status:      entity.RetryJobPending,
		Attempts:    0,
		MaxAttempts: rq.maxRetries,
		LastError:   errMsg,
		NextRetryAt: now.Add(rq.baseDelay),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

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
		slog.String("error", errMsg),
		slog.Bool("tg_skip", true))
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

	rq.log.Info("processing retry jobs", slog.Int("count", len(jobs)))
	for _, job := range jobs {
		rq.processOneJob(job)
	}
}

// processOneJob attempts to register an invoice for a single retry job.
// On success, it saves the result to OpenCart and marks the job completed.
// On failure, it applies exponential backoff or marks the job as failed.
func (rq *RetryQueue) processOneJob(job *entity.RetryJob) {
	log := rq.log.With(
		slog.String("event_id", job.EventId),
		slog.String("order_id", job.OrderId),
		slog.Int("attempt", job.Attempts+1),
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
	if rq.maxOrderAge > 0 {
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

	// Flag the context as a retry so the wFirma layer keeps per-attempt failures local —
	// the original error was already reported to Telegram when the job was enqueued.
	ctx := entity.WithRetry(context.Background())

	// Order-level idempotency: a capture / webhook / reconciler may have issued the faktura
	// for this order already. The job holds no invoice id to verify with InvoiceExists, so
	// match on the order via id_external before creating. On a lookup error, reschedule
	// rather than create — proceeding blind could produce a duplicate faktura.
	if params.OrderId != "" {
		existingId, findErr := rq.inv.FindInvoiceByOrderId(ctx, params.OrderId)
		if findErr != nil {
			job.Attempts++
			job.UpdatedAt = time.Now()
			log.Warn("check existing invoice before retry", sl.Err(findErr))
			rq.retryLater(job, log, "check existing invoice: "+findErr.Error())
			return
		}
		if existingId != "" {
			job.Attempts++
			job.UpdatedAt = time.Now()
			if rq.oc != nil {
				if ocErr := rq.oc.SaveInvoiceId(params.OrderId, existingId, ""); ocErr != nil {
					log.Error("save existing invoice id to opencart", sl.Err(ocErr))
				}
			}
			job.Status = entity.RetryJobCompleted
			job.LastError = ""
			if dbErr := rq.db.UpdateRetryJob(job); dbErr != nil {
				log.Error("update retry job after dedup", sl.Err(dbErr))
			}
			log.With(
				slog.String("invoice_id", existingId),
				slog.String("tg_topic", entity.TopicPayment),
			).Info("retry job resolved: faktura already exists for order")
			return
		}
	}

	// Attempt to register the invoice.
	payment, err := rq.inv.RegisterInvoice(ctx, params)
	job.Attempts++
	job.UpdatedAt = time.Now()

	if err != nil {
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
