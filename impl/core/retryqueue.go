// Package core — retryqueue.go implements a persistent retry queue for failed wFirma invoice
// registrations. When the wFirma API is down during a Stripe webhook, the failed job is saved
// to MongoDB and retried with exponential backoff until it succeeds or exhausts max attempts.
package core

import (
	"context"
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
	UpdateRetryJob(job *entity.RetryJob) error
	GetRetryJobByEventId(eventId string) (*entity.RetryJob, error)
	GetCheckoutParamsForEvent(eventId string) (*entity.CheckoutParams, error)
}

// RetryQueue polls for pending retry jobs and attempts to re-register invoices
// with exponential backoff. Follows the same Start/Stop pattern as vatrates.Service.
type RetryQueue struct {
	db           RetryDatabase
	inv          InvoiceService
	oc           *occlient.Opencart
	log          *slog.Logger
	interval     time.Duration
	maxRetries   int
	baseDelay    time.Duration
	done         chan struct{}
	stopped      chan struct{}
	mu           sync.Mutex
}

// NewRetryQueue creates a retry queue. Call Start() to begin background processing.
func NewRetryQueue(log *slog.Logger, intervalMin, maxRetries, baseDelaySec int) *RetryQueue {
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
		log:        log.With(sl.Module("retry-queue")),
		interval:   time.Duration(intervalMin) * time.Minute,
		maxRetries: maxRetries,
		baseDelay:  time.Duration(baseDelaySec) * time.Second,
	}
}

func (rq *RetryQueue) SetDatabase(db RetryDatabase)       { rq.db = db }
func (rq *RetryQueue) SetInvoiceService(inv InvoiceService) { rq.inv = inv }
func (rq *RetryQueue) SetOpencart(oc *occlient.Opencart)    { rq.oc = oc }

// Enqueue creates a pending retry job for a failed invoice registration.
// Idempotent by EventId — if a job for this event already exists, it's a no-op.
func (rq *RetryQueue) Enqueue(params *entity.CheckoutParams, errMsg string) {
	rq.mu.Lock()
	defer rq.mu.Unlock()

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

	rq.log.Info("retry job enqueued",
		slog.String("event_id", params.EventId),
		slog.String("order_id", params.OrderId),
		slog.String("error", errMsg),
		slog.String("tg_topic", entity.TopicError))
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

	// Attempt to register the invoice
	ctx := context.Background()
	payment, err := rq.inv.RegisterInvoice(ctx, params)
	job.Attempts++
	job.UpdatedAt = time.Now()

	if err != nil {
		log.Warn("retry invoice registration failed", sl.Err(err))
		job.LastError = err.Error()

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

// failJob marks a job as permanently failed.
func (rq *RetryQueue) failJob(job *entity.RetryJob, errMsg string) {
	job.Status = entity.RetryJobFailed
	job.LastError = errMsg
	job.UpdatedAt = time.Now()
	if dbErr := rq.db.UpdateRetryJob(job); dbErr != nil {
		rq.log.Error("update failed retry job", sl.Err(dbErr))
	}
}
