# Stripe API Documentation

Stripe endpoints handle payment processing through Stripe Checkout, including direct payments, payment holds (authorizations), captures, and cancellations.

## Authentication

All endpoints require Bearer token authentication:

```bash
curl -H "Authorization: Bearer YOUR_TOKEN" ...
```

---

## Endpoints

### Create Payment Hold

Creates a Stripe Checkout session in manual-capture mode. Funds are authorized (held) but not captured until explicitly requested.

```
POST /v1/st/hold
```

#### Request Body

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `client_details` | object | Yes | Customer information |
| `line_items` | array | Yes | Order line items (min: 1) |
| `total` | integer | Yes | Total amount in minor units (min: 1) |
| `currency` | string | Yes | Currency code: `PLN` or `EUR` |
| `order_id` | string | Yes | Unique order identifier (1-32 chars) |
| `success_url` | string | Yes | URL to redirect after successful payment |

##### client_details Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Customer full name |
| `email` | string | Yes | Customer email address |
| `phone` | string | No | Customer phone number |
| `country` | string | No | Country code (e.g., "PL") |
| `zip_code` | string | No | Postal code |
| `city` | string | No | City name |
| `street` | string | No | Street address |

##### line_items Array Item

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Product/service name |
| `qty` | integer | Yes | Quantity (min: 1) |
| `price` | integer | Yes | Unit price in minor units (min: 1) |

#### Example Request

```bash
curl -X POST "https://api.example.com/v1/st/hold" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {
      "name": "Customer Name",
      "email": "customer@example.com",
      "phone": "123456789",
      "country": "PL",
      "zip_code": "01-120",
      "city": "Warszawa",
      "street": "ul. Example 1"
    },
    "line_items": [
      {"name": "Product A", "qty": 1, "price": 8500},
      {"name": "Product B", "qty": 2, "price": 3250}
    ],
    "total": 15000,
    "currency": "PLN",
    "order_id": "ORD-123456",
    "success_url": "https://shop.example.com/thank-you"
  }'
```

#### Response

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "cs_live_abc123...",
    "order_id": "ORD-123456",
    "link": "https://checkout.stripe.com/c/pay/cs_live_abc123..."
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Response Fields (Payment)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Authorized amount in minor units |
| `id` | string | Stripe Checkout Session ID (cs_...) |
| `order_id` | string | Your order ID |
| `link` | string | Stripe Checkout URL for customer |

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request body or validation error |
| 401 | Unauthorized |
| 500 | Stripe service error |

---

### Create Direct Payment

Creates a Stripe Checkout session for immediate payment capture.

```
POST /v1/st/pay
```

#### Request Body

Same as [Create Payment Hold](#create-payment-hold).

#### Example Request

```bash
curl -X POST "https://api.example.com/v1/st/pay" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {
      "name": "Customer Name",
      "email": "customer@example.com"
    },
    "line_items": [
      {"name": "Product A", "qty": 1, "price": 15000}
    ],
    "total": 15000,
    "currency": "PLN",
    "order_id": "ORD-123456",
    "success_url": "https://shop.example.com/thank-you"
  }'
```

#### Response

Same format as [Create Payment Hold](#create-payment-hold).

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request body or validation error |
| 401 | Unauthorized |
| 500 | Stripe service error |

---

### Capture Held Payment

Captures a previously authorized (held) payment. Can capture full or partial amount.

```
POST /v1/st/capture/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Checkout Session ID (cs_...) from hold response |

#### Request Body

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `client_details` | object | Yes | Customer information (validated but not used) |
| `line_items` | array | Yes | Line items (validated but not used) |
| `total` | integer | Yes | Amount to capture in minor units |
| `currency` | string | Yes | Currency code (validated but not used) |
| `order_id` | string | Yes | Order ID (validated but not used) |
| `success_url` | string | Yes | URL (validated but not used) |

Only `total` is used for capture amount; other fields are validated but ignored.

#### Example Request (Full Capture)

```bash
curl -X POST "https://api.example.com/v1/st/capture/cs_live_abc123" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {"name": "Customer", "email": "c@example.com"},
    "line_items": [{"name": "Product", "qty": 1, "price": 15000}],
    "total": 15000,
    "currency": "PLN",
    "order_id": "ORD-123456",
    "success_url": "https://example.com"
  }'
```

#### Example Request (Partial Capture)

```bash
curl -X POST "https://api.example.com/v1/st/capture/cs_live_abc123" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {"name": "Customer", "email": "c@example.com"},
    "line_items": [{"name": "Product", "qty": 1, "price": 8500}],
    "total": 8500,
    "currency": "PLN",
    "order_id": "ORD-123456",
    "success_url": "https://example.com"
  }'
```

#### Response

```json
{
  "success": true,
  "data": {
    "amount": 8500,
    "id": "pi_abc123...",
    "order_id": "ORD-123456"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Captured amount in minor units |
| `id` | string | Stripe PaymentIntent ID (pi_...) |
| `order_id` | string | Your order ID |

#### Invoice Registration

A successful capture **asynchronously enqueues a wFirma invoice** for the order. The
capture response returns immediately and does not wait for wFirma, so a slow wFirma
call can never time out the capture. If invoice registration fails it is retried by
the invoice retry queue. A manual capture emits no Stripe webhook the service handles,
so this is the only invoice trigger for held-then-captured payments.

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request, session not found, or payment_id missing |
| 401 | Unauthorized |
| 500 | Stripe service error |

---

### Cancel Held Payment

Cancels a payment authorization and releases the held funds back to the customer.

```
POST /v1/st/cancel/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Checkout Session ID (cs_...) from hold response |

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `reason` | string | No | Cancellation reason (default: `requested_by_customer`) |

##### Valid Reason Values

| Value | Description |
|-------|-------------|
| `duplicate` | Duplicate payment |
| `fraudulent` | Fraudulent payment |
| `requested_by_customer` | Customer requested cancellation (default) |
| `abandoned` | Payment was abandoned |

#### Example Request

```bash
# With reason
curl -X POST "https://api.example.com/v1/st/cancel/cs_live_abc123?reason=requested_by_customer" \
  -H "Authorization: Bearer YOUR_TOKEN"

# Without reason (defaults to requested_by_customer)
curl -X POST "https://api.example.com/v1/st/cancel/cs_live_abc123" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Response

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "pi_abc123...",
    "order_id": "ORD-123456"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Canceled amount in minor units |
| `id` | string | Stripe PaymentIntent ID (pi_...) |
| `order_id` | string | Your order ID |

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid reason, session not found, or payment_id missing |
| 401 | Unauthorized |
| 500 | Stripe service error |

---

### Refund Captured Payment

Refunds a captured payment, fully or partially. Partial refunds can be repeated until
their sum reaches the captured amount; Stripe rejects anything above the unrefunded
remainder. Works for held-then-captured payments and for payments taken without a hold
(`capture_method=automatic`), for which a refund is the only way to return money. An
uncaptured hold cannot be refunded — release it with [Cancel](#cancel-held-payment).

```
POST /v1/st/refund/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Stripe Checkout Session ID (cs_...), the same id as for capture/cancel |

#### Headers

| Header | Required | Description |
|--------|----------|-------------|
| `Idempotency-Key` | Yes | Caller-chosen unique key per refund (≤ 255 chars). A repeat with the same key returns the first refund (`replayed: true`) instead of refunding again. Use a new key for each intended refund. |

#### Request Body (optional)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `amount` | integer | No | Amount to refund in minor units; `0` or omitted refunds the whole unrefunded remainder |
| `reason` | string | No | Free-text reason (≤ 255 chars), stored with the refund and in Stripe metadata. `duplicate`, `fraudulent`, `requested_by_customer` are also passed as Stripe's refund reason |

#### Example Request (Partial Refund)

```bash
curl -X POST "https://api.example.com/v1/st/refund/cs_live_abc123..." \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Idempotency-Key: 18400-refund-1" \
  -H "Content-Type: application/json" \
  -d '{"amount": 2500, "reason": "item out of stock"}'
```

#### Example Request (Full Refund)

```bash
curl -X POST "https://api.example.com/v1/st/refund/cs_live_abc123..." \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Idempotency-Key: 18400-refund-full"
```

#### Response

```json
{
  "success": true,
  "data": {
    "refund": {
      "refund_id": "re_3Q...",
      "payment_id": "pi_3Q...",
      "session_id": "cs_live_abc123...",
      "order_id": "18400",
      "amount": 2500,
      "currency": "PLN",
      "status": "succeeded",
      "reason": "item out of stock",
      "requested_by": "zoho.support",
      "idempotency_key": "18400-refund-1",
      "source": "api",
      "created": "2026-11-25T12:00:00Z",
      "recorded": "2026-11-25T12:00:01Z"
    },
    "order_id": "18400",
    "amount_received": 10000,
    "amount_refunded": 2500,
    "payment_status": "partially_refunded"
  },
  "status_message": "Success",
  "timestamp": "2026-11-25T12:00:01Z"
}
```

#### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `refund` | object | The refund (see fields above); `status` is Stripe's: `pending`, `requires_action`, `succeeded`, `failed`, `canceled` |
| `amount_received` | integer | Captured amount, minor units |
| `amount_refunded` | integer | Total refunded on the payment so far, minor units |
| `payment_status` | string | `paid`, `partially_refunded` or `refunded` — also written to `oc_order.wf_payment_status` |
| `replayed` | boolean | `true` when the key matched an earlier refund and nothing new was refunded |

#### Side Effects

- The refund is stored in MongoDB (`stripe_refunds`, unique by Stripe refund id) with
  amount, time, reason, the calling user and the idempotency key; `GET /v1/st/status/{id}`
  lists it.
- `oc_order.wf_payment_status` becomes `partially_refunded` or `refunded`;
  `wf_payment_amount` keeps the captured amount and `wf_payment_time` keeps the payment time.
- A Telegram message is sent. The wFirma side is **not** automated: when the order was
  invoiced the message (`error` topic) asks for a correcting invoice (faktura korygująca)
  to be issued manually.

#### Errors

| Code | Description |
|------|-------------|
| 400 | Missing `Idempotency-Key`, invalid body, session not found, payment not captured (hold or failed), amount above the unrefunded remainder, or Stripe error |
| 401 | Unauthorized |
| 409 | `Idempotency-Key` already used for a refund of another payment or of a different amount |

---

### Get Payment Status

Returns the live Stripe payment state for an OpenCart order. The status is read
directly from Stripe (PaymentIntent, falling back to the Checkout Session), so it
reflects the current state rather than the value last stored locally.

```
GET /v1/st/status/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | OpenCart order ID |

#### Example Request

```bash
curl "https://api.example.com/v1/st/status/16463" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Response

```json
{
  "success": true,
  "data": {
    "order_id": "16463",
    "payment_id": "pi_abc123...",
    "session_id": "cs_live_abc123...",
    "status": "requires_capture",
    "amount": 25999,
    "amount_received": 0,
    "currency": "PLN",
    "paid": false,
    "captured": false,
    "source": "payment_intent"
  },
  "status_message": "Success",
  "timestamp": "2026-05-28T08:53:08Z"
}
```

#### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `order_id` | string | OpenCart order ID |
| `payment_id` | string | Stripe PaymentIntent ID (pi_...), if known |
| `session_id` | string | Stripe Checkout Session ID (cs_...), if known |
| `status` | string | Live Stripe status (e.g. `requires_capture`, `succeeded`, `canceled`) |
| `amount` | integer | Order/intent amount in minor units |
| `amount_received` | integer | Amount captured so far, in minor units |
| `currency` | string | Currency code |
| `paid` | boolean | True when the PaymentIntent has succeeded |
| `captured` | boolean | True when any funds have been captured |
| `invoice_id` | string | wFirma invoice ID, if already registered |
| `source` | string | Where the status was read: `payment_intent`, `checkout_session`, or `stored` |
| `amount_refunded` | integer | Live total refunded on the charge, minor units (omitted when nothing was refunded) |
| `payment_status` | string | For a succeeded payment: `paid`, `partially_refunded` or `refunded` |
| `refunds` | array | Recorded refunds, oldest first (same fields as the refund object of [Refund](#refund-captured-payment)), including refunds made in the Stripe Dashboard |

#### Errors

| Code | Description |
|------|-------------|
| 400 | Order not found or Stripe service error |
| 401 | Unauthorized |

---

## Webhook

### Stripe Event Webhook

Receives and processes Stripe webhook events. This endpoint does not require Bearer token authentication; it uses Stripe signature verification.

```
POST /webhook/event
```

#### Headers

| Header | Description |
|--------|-------------|
| `Stripe-Signature` | Stripe webhook signature for verification |

#### Supported Events

- `checkout.session.completed` - Processes completed checkout sessions
- `invoice.finalized` - Processes finalized Stripe invoices
- `payment_intent.amount_capturable.updated` - Marks a hold as confirmed (capturable)
- `payment_intent.succeeded` - Marks a PaymentIntent as captured/paid and registers the invoice in real time. Critically, this fires for captures done **outside the API** (e.g. in the Stripe Dashboard), which otherwise leave no capture trace until the reconciler notices. Logged as `payment captured`. Invoice creation is idempotent across triggers (capture API, this webhook, reconciler), so no duplicate is created.
- `charge.refunded`, `charge.refund.updated` - Syncs the payment's refunds from Stripe into `stripe_refunds` and `oc_order.wf_payment_status`. Catches refunds made in the Stripe Dashboard in real time; refunds of PaymentIntents unknown to the service are ignored.

#### Notes

- The webhook must be configured and reachable for capture/cancel operations to work
- To capture Dashboard captures in real time, enable `payment_intent.succeeded` in your Stripe webhook event selection
- To reflect Dashboard refunds in real time, enable `charge.refunded` and `charge.refund.updated`; without them the reconciler picks refunds up on its next tick
- After a successful checkout, the webhook stores the PaymentIntent ID needed for capture
- Configure your Stripe webhook URL to point to this endpoint
- When wFirma invoice creation fails during webhook processing (e.g., API downtime), the job is automatically enqueued for retry with exponential backoff if the retry queue is enabled (see [Configuration](#retry-queue-configuration))

#### Retry Queue Configuration

When enabled, failed invoice registrations triggered by Stripe webhooks are persisted to MongoDB and retried automatically with exponential backoff. This ensures invoices are eventually created even when the wFirma API is temporarily unavailable.

```yaml
retry_queue:
  enabled: true
  interval_min: 5       # polling interval in minutes
  max_retries: 10       # max retry attempts before giving up
  base_delay_sec: 60    # base delay for exponential backoff (delay = base * 2^(attempt-1))
```

Requires MongoDB to be enabled. Retry jobs are stored in the `retry_jobs` collection. Failed jobs emit Telegram notifications with the `error` topic; successful retries emit `payment` topic notifications.

---

## Payment Flow Examples

### Direct Payment Flow

1. `POST /v1/st/pay` - Create checkout session
2. Customer completes payment at Stripe Checkout URL
3. Stripe sends `checkout.session.completed` webhook
4. Payment is captured automatically

### Hold/Capture Flow

1. `POST /v1/st/hold` - Create authorization
2. Customer completes authorization at Stripe Checkout URL
3. Stripe sends `checkout.session.completed` webhook
4. Later: `POST /v1/st/capture/{id}` - Capture funds
5. Or: `POST /v1/st/cancel/{id}` - Release funds

For detailed hold/capture documentation, see [Stripe Hold-Capture Flow](stripe-hold-capture.md).
