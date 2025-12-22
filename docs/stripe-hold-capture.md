# Stripe Hold-Capture Flow

This guide explains how to use the API to create a payment authorization (hold) with Stripe Checkout and then capture the funds later (full or partial capture).

## Flow Overview

```
1. POST /v1/st/hold     → Returns Checkout Session ID (cs_...) and checkout link
2. Customer pays        → Funds are authorized (held), not captured
3. Webhook received     → Server stores PaymentIntent ID (pi_...)
4. POST /v1/st/capture  → Capture full or partial amount
   OR
   POST /v1/st/cancel   → Release held funds
```

All `/v1` endpoints require authentication via Bearer token.

## Prerequisites

- Valid API token for `Authorization: Bearer YOUR_TOKEN` header
- Stripe Checkout configured for manual capture (handled automatically by hold endpoint)
- Stripe webhook configured at `POST /webhook/event` for `checkout.session.completed`

---

## Request Body Schema

Both hold and capture endpoints validate the same body structure.

### CheckoutParams

| Field | Type | Required | Validation | Description |
|-------|------|----------|------------|-------------|
| `client_details` | object | Yes | - | Customer information |
| `line_items` | array | Yes | min: 1 item | Order line items |
| `total` | integer | Yes | min: 1 | Total amount in minor units |
| `currency` | string | Yes | `PLN` or `EUR` | Currency code |
| `order_id` | string | Yes | 1-32 chars | Unique order identifier |
| `success_url` | string | Yes | valid URL | Redirect URL after payment |

### client_details Object

| Field | Type | Required | Validation | Description |
|-------|------|----------|------------|-------------|
| `name` | string | Yes | non-empty | Customer full name |
| `email` | string | Yes | valid email | Customer email address |
| `phone` | string | No | - | Phone number |
| `country` | string | No | - | Country code (e.g., "PL") |
| `zip_code` | string | No | - | Postal code |
| `city` | string | No | - | City name |
| `street` | string | No | - | Street address |
| `tax_id` | string | No | - | Tax identification number |

### line_items Array Item

| Field | Type | Required | Validation | Description |
|-------|------|----------|------------|-------------|
| `name` | string | Yes | non-empty | Product/service name |
| `qty` | integer | Yes | min: 1 | Quantity |
| `price` | integer | Yes | min: 1 | Unit price in minor units |
| `sku` | string | No | - | Product SKU |

### Example Body

```json
{
  "client_details": {
    "name": "Contractor",
    "email": "test@example.com",
    "phone": "0005544688",
    "country": "PL",
    "zip_code": "01-120",
    "city": "Warszawa",
    "street": ""
  },
  "line_items": [
    {"name": "DARK Top Bez Wycierania, 30 ml", "qty": 1, "price": 8500},
    {"name": "DARK Scotch Base (ulepszona formuła), 15 ml", "qty": 1, "price": 6500}
  ],
  "total": 15000,
  "currency": "PLN",
  "order_id": "123456",
  "success_url": "https://example.com/after-payment"
}
```

**Notes:**
- Minor units: `15000` means 150.00 in the selected currency
- `total` should equal sum of line_items (`qty * price`)

---

## 1) Create a Hold (Authorization)

**Endpoint:** `POST /v1/st/hold`

Creates a Stripe Checkout Session in manual-capture mode. The customer completes the checkout, funds are authorized (held) but not captured.

### Request

```bash
curl -X POST "https://api.example.com/v1/st/hold" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {
      "name": "Contractor",
      "email": "test@example.com",
      "phone": "0005544688",
      "country": "PL",
      "zip_code": "01-120",
      "city": "Warszawa",
      "street": ""
    },
    "line_items": [
      {"name": "DARK Top Bez Wycierania, 30 ml", "qty": 1, "price": 8500},
      {"name": "DARK Scotch Base (ulepszona formuła), 15 ml", "qty": 1, "price": 6500}
    ],
    "total": 15000,
    "currency": "PLN",
    "order_id": "123456",
    "success_url": "https://example.com/after-payment"
  }'
```

### Response

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "cs_...b1ELPMpzHCbEuE9ab",
    "order_id": "123456",
    "link": "https://checkout.stripe.com/c/pay/cs_...kZmBtamlhYHd2Jz9xd3BgeCUl"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

### Response Fields (Payment)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Authorized amount in minor units |
| `id` | string | Stripe Checkout Session ID (cs_...) |
| `order_id` | string | Your order ID |
| `link` | string | Stripe Checkout URL - redirect customer here |

### Errors

```json
{
  "success": false,
  "status_message": "Invalid request: line_items: cannot be blank",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

---

## 2) Webhook: checkout.session.completed

After the customer pays, Stripe sends `checkout.session.completed` to your webhook at `POST /webhook/event`.

- The server verifies the signature and parses the event
- It stores the PaymentIntent ID (pi_...) for later capture

**Important:** Keep your webhook configured and reachable for capture to work.

---

## 3) Capture the Held Amount

**Endpoint:** `POST /v1/st/capture/{id}`

### Path Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | string | Checkout Session ID (cs_...) from hold response |

### Request Body

Same schema as hold. Only `total` determines the capture amount; other fields are validated but not used.

### Full Capture Example

Capture the entire authorized amount:

```bash
curl -X POST "https://api.example.com/v1/st/capture/cs_...b1ELPMpzHCbEuE9ab" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {
      "name": "Contractor",
      "email": "test@example.com",
      "phone": "0005544688",
      "country": "PL",
      "zip_code": "01-120",
      "city": "Warszawa",
      "street": ""
    },
    "line_items": [
      {"name": "DARK Top Bez Wycierania, 30 ml", "qty": 1, "price": 8500},
      {"name": "DARK Scotch Base (ulepszona formuła), 15 ml", "qty": 1, "price": 6500}
    ],
    "total": 15000,
    "currency": "PLN",
    "order_id": "123456",
    "success_url": "https://example.com/after-payment"
  }'
```

### Partial Capture Example

Capture only part of the authorized amount:

```bash
curl -X POST "https://api.example.com/v1/st/capture/cs_...b1ELPMpzHCbEuE9ab" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_details": {
      "name": "Contractor",
      "email": "test@example.com",
      "phone": "0005544688",
      "country": "PL",
      "zip_code": "01-120",
      "city": "Warszawa",
      "street": ""
    },
    "line_items": [
      {"name": "DARK Top Bez Wycierania, 30 ml", "qty": 1, "price": 8500}
    ],
    "total": 8500,
    "currency": "PLN",
    "order_id": "123456",
    "success_url": "https://example.com/after-payment"
  }'
```

### Response

```json
{
  "success": true,
  "data": {
    "amount": 8500,
    "id": "pi_...b1ELPMpzHCbEuE9ab",
    "order_id": "123456"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

### Response Fields (Payment)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Captured amount in minor units |
| `id` | string | Stripe PaymentIntent ID (pi_...) |
| `order_id` | string | Your order ID |

### Errors

```json
{
  "success": false,
  "status_message": "Invalid request: total: must be greater than 0",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

```json
{
  "success": false,
  "status_message": "Capture: session not found or payment_id missing",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

```json
{
  "success": false,
  "status_message": "Unauthorized",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

---

## 4) Cancel the Held Amount

**Endpoint:** `POST /v1/st/cancel/{id}`

Cancels a payment authorization and releases held funds back to the customer without capturing.

### Path Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | string | Checkout Session ID (cs_...) from hold response |

### Query Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `reason` | string | No | `requested_by_customer` | Cancellation reason |

### Valid Reason Values

| Value | Description |
|-------|-------------|
| `duplicate` | Duplicate payment |
| `fraudulent` | Fraudulent payment |
| `requested_by_customer` | Customer requested cancellation |
| `abandoned` | Payment was abandoned |

### Request with Reason

```bash
curl -X POST "https://api.example.com/v1/st/cancel/cs_...b1ELPMpzHCbEuE9ab?reason=requested_by_customer" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

### Request without Reason

```bash
curl -X POST "https://api.example.com/v1/st/cancel/cs_...b1ELPMpzHCbEuE9ab" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

### Response

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "pi_...b1ELPMpzHCbEuE9ab",
    "order_id": "123456"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

### Response Fields (Payment)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Canceled amount in minor units |
| `id` | string | Stripe PaymentIntent ID (pi_...) |
| `order_id` | string | Your order ID |

### Errors

```json
{
  "success": false,
  "status_message": "Invalid reason",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

```json
{
  "success": false,
  "status_message": "Cancel payment: session not found",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

```json
{
  "success": false,
  "status_message": "Cancel payment: payment id not found",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

---

## Tips and Notes

- Use the **Checkout Session ID** (cs_...) in capture/cancel path, not PaymentIntent ID
- Webhook must process `checkout.session.completed` before capture/cancel works
- Partial captures are allowed if `total` <= authorized amount
- Zero amount is rejected by validation
- Cancel releases funds without capturing
- For direct payments (no hold), use `POST /v1/st/pay`

## Related Documentation

- [Stripe API](api-stripe.md) - All Stripe endpoints reference
- [API Overview](api.md) - General API documentation
