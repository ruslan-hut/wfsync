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

#### Notes

- The webhook must be configured and reachable for capture/cancel operations to work
- After a successful checkout, the webhook stores the PaymentIntent ID needed for capture
- Configure your Stripe webhook URL to point to this endpoint

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
