# Invoice Retry Queue

## Problem

When a Stripe webhook triggers invoice creation via the wFirma API, the API may be temporarily unavailable (e.g., "OUT OF SERVICE" errors, network timeouts). Without retry logic, the payment is received but no invoice is generated, requiring manual intervention.

## Solution

A persistent MongoDB-backed retry queue with exponential backoff. Failed invoice registrations are saved as retry jobs and automatically retried until the invoice is successfully created or the maximum number of attempts is exhausted.

## How It Works

1. A Stripe webhook arrives and triggers `StripeEvent()` in `impl/core/core.go`
2. `RegisterInvoice()` is called to create the invoice in wFirma
3. If it fails, the retry queue's `Enqueue()` method saves a `RetryJob` to MongoDB and returns early (the webhook responds with 200 to Stripe to prevent Stripe's own retries from causing duplicate processing)
4. A background goroutine polls for pending retry jobs at a configurable interval
5. Each due job loads its original `CheckoutParams` from MongoDB and retries `RegisterInvoice()`
6. On success: the invoice ID is saved to OpenCart and the job is marked `completed`
7. On failure: exponential backoff is applied and the job is rescheduled
8. After exhausting all attempts, the job is marked `failed`

## Configuration

```yaml
retry_queue:
  enabled: true         # enable/disable the retry queue
  interval_min: 5       # how often to poll for due jobs (minutes)
  max_retries: 10       # max attempts before giving up
  base_delay_sec: 60    # base delay for exponential backoff (seconds)
```

Requires `mongo.enabled: true` — retry jobs are stored in the `retry_jobs` MongoDB collection.

## Exponential Backoff

The delay before each retry is calculated as: `base_delay_sec * 2^(attempt - 1)`

### Example: `base_delay_sec: 60` (1 minute)

| Attempt | Delay  | Cumulative |
|---------|--------|------------|
| 1       | 1m     | 1m         |
| 2       | 2m     | 3m         |
| 3       | 4m     | 7m         |
| 4       | 8m     | 15m        |
| 5       | 16m    | 31m        |
| 6       | 32m    | ~1h        |
| 7       | 64m    | ~2h        |
| 8       | 128m   | ~4h        |
| 9       | 256m   | ~8.5h      |
| 10      | give up| --         |

### Example: `base_delay_sec: 600` (10 minutes)

| Attempt | Delay   | Cumulative   |
|---------|---------|--------------|
| 1       | 10m     | 10m          |
| 2       | 20m     | 30m          |
| 3       | 40m     | 1h 10m       |
| 4       | 1h 20m  | 2h 30m       |
| 5       | 2h 40m  | 5h 10m       |
| 6       | 5h 20m  | 10h 30m      |
| 7       | 10h 40m | 21h 10m      |
| 8       | 21h 20m | ~42h 30m     |
| 9       | 42h 40m | ~85h (3.5d)  |
| 10      | give up | --           |

## Retry Job Entity

Stored in the `retry_jobs` MongoDB collection:

| Field         | Type     | Description                              |
|---------------|----------|------------------------------------------|
| `_id`         | string   | Same as `event_id` (Stripe event ID)     |
| `event_id`    | string   | Stripe event that triggered the invoice  |
| `order_id`    | string   | OpenCart order ID                         |
| `status`      | string   | `pending`, `completed`, or `failed`      |
| `attempts`    | int      | Number of attempts so far                |
| `max_attempts`| int      | Max allowed attempts                     |
| `last_error`  | string   | Error message from the last failed attempt|
| `next_retry_at`| datetime| When the next retry should happen        |
| `created_at`  | datetime | When the job was first enqueued           |
| `updated_at`  | datetime | Last modification time                   |

## Idempotency

- Each Stripe event produces at most one retry job (keyed by `event_id`)
- If `Enqueue()` is called for an event that already has a retry job, it's a no-op
- The original `CheckoutParams` (with line items, customer details, etc.) are loaded from the `checkout_params` collection using the event ID

## Telegram Notifications

- **On enqueue**: logs an error with `tg_topic: error` so admins are notified of the failure
- **On permanent failure** (max attempts exhausted): logs an error with `tg_topic: error`
- **On successful retry**: logs with `tg_topic: payment` confirming the invoice was created

## Key Files

| File | Description |
|------|-------------|
| `entity/retry-job.go` | `RetryJob` struct and status constants |
| `impl/core/retryqueue.go` | Background worker, `Enqueue()`, exponential backoff logic |
| `impl/core/core.go` | Wiring: enqueues on `RegisterInvoice` failure in `StripeEvent()` |
| `internal/database/mongo.go` | CRUD operations for `retry_jobs` collection |
| `internal/config/config.go` | `RetryQueue` config struct |
| `cmd/server/main.go` | Lifecycle: creates, starts, and stops the retry queue |

## Graceful Shutdown

The retry queue follows the same `Start()`/`Stop()` pattern as `vatrates.Service`. On shutdown signal, `Stop()` is called after OpenCart stops but before MongoDB closes, ensuring any in-progress job processing completes cleanly.

## Manual Testing

1. Temporarily break wFirma credentials in the config
2. Trigger a Stripe webhook (e.g., complete a test checkout)
3. Verify a retry job appears in the `retry_jobs` collection with status `pending`
4. Check logs for `retry-queue` module messages
5. Restore correct wFirma credentials
6. Wait for the next poll cycle (or restart the service)
7. Verify the job status changes to `completed` and the invoice appears in wFirma
