# Payment Reconciler

## Problem

A manual capture (`POST /v1/st/capture/{id}`) does not emit a Stripe webhook the service
handles, so a captured (succeeded) PaymentIntent could be left without a wFirma invoice.
Holds can also be canceled on the Stripe side (manually, or by Stripe's ~7-day auto-cancel
of an uncaptured authorization) without the local order state catching up.

## Solution

A periodic background job that scans unresolved held payments and reconciles each one
against its **live** Stripe PaymentIntent status, taking action only where needed.

## How It Works

1. A background goroutine polls at a configurable interval (`impl/core/reconciler.go`).
2. It loads unresolved holds from MongoDB — checkout params that have a `payment_id`,
   no `invoice_id`, and have not been closed by a prior run. Sessions that never produced
   a PaymentIntent (abandoned before authorization) are excluded; they never held funds.
3. For each, it fetches the live PaymentIntent status and acts:

   | Live status | Action |
   |-------------|--------|
   | `succeeded` | Mark the order paid; register the wFirma invoice if none exists (re-using the shared invoice path with retry-queue fallback); close the record. Idempotent — an already-invoiced order is just closed. |
   | `canceled` | Reflect the cancellation into OpenCart; close the record. |
   | `requires_capture` | Active hold awaiting a capture decision — **watched only, never auto-canceled**. |
   | `processing` / `requires_action` / `requires_confirmation` / `requires_payment_method` | Not settled yet; left open and re-checked next tick. |

4. Records are "closed" by stamping `closed` so subsequent ticks skip them; pending holds
   stay open and are re-checked until they settle or cancel.

## Notifications

Telegram messages are emitted **only when the job takes an action** — an invoice is
requested (`invoice` topic) or a cancellation is reflected (`payment` topic). Idle ticks
and watched-but-unchanged holds produce structured logs only, no Telegram.

## Configuration

```yaml
payment_reconciler:
  enabled: true       # enable/disable the reconciler
  interval_min: 15    # how often to scan unresolved holds (minutes)
```

Requires MongoDB. Shares the invoice registration and retry-queue paths with the Stripe
webhook and the manual capture flow.
