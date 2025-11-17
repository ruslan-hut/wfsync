# Stripe Hold → Capture Flow

This guide shows how to use the API to create a payment authorization (hold) with Stripe Checkout and then capture the funds later (full or partial capture).

Flow at a glance
- Create a hold: POST /v1/st/hold → returns a Checkout Session ID (cs_...) and a checkout link.
- Customer pays in Stripe Checkout → funds are authorized (held), not captured.
- Webhook receives checkout.session.completed → your server stores the PaymentIntent ID (pi_...) for that session.
- Capture later: POST /v1/st/capture/{cs_id} with a validated body; total defines full or partial capture.
- Cancel later: POST /v1/st/cancel/{cs_id} with optional reason query parameter; releases the held funds without capturing.

All /v1 endpoints require authentication via Bearer token.

## Prerequisites

- You have a valid API token. Send it in the Authorization header:
  - Authorization: Bearer YOUR_TOKEN
- Stripe Checkout must be configured to allow manual capture (this is handled by the server when using the hold endpoint).
- Your Stripe webhook for checkout.session.completed is configured and points to POST /webhook/event. The webhook is responsible for enriching the stored checkout with the payment_id needed for capture.

## Common Request Body Schema

Both hold and capture requests validate the same body shape. For capture, only total is used to determine the capture amount; the remaining fields are validated but ignored by the capture logic.

- client_details: object with customer data
- line_items: non-empty array, each item has name, qty, price (price in minor units, e.g., cents)
- total: integer > 0 (in minor units: e.g., 15000 = 150.00)
- currency: PLN or EUR (validated)
- order_id: string
- success_url: URL string (where Stripe redirects after a successful checkout)

Example body (used below in curl examples):

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

Notes
- Minor units: 15000 means 150.00 in the selected currency.
- total = sum of line_items (qty * price) is recommended.

---

## 1) Create a Hold (Authorization)

Endpoint: POST /v1/st/hold

Description: Creates a Stripe Checkout Session in manual-capture mode. The customer completes the checkout, resulting funds are authorized (held) but not captured.

Curl example

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

Successful response

```json
{
  "data": {
    "amount": 15000,
    "id": "cs_...b1ELPMpzHCbEuE9ab",
    "order_id": "123456",
    "link": "https://checkout.stripe.com/c/pay/cs_...kZmBtamlhYHd2Jz9xd3BgeCUl"
  },
  "success": true,
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

- data.id is the Stripe Checkout Session ID (starts with cs_...).
- data.link directs your customer to Stripe Checkout.

Possible errors (examples)

- 400 Bad Request (invalid body):

```json
{
  "success": false,
  "status_message": "Invalid request: line_items: cannot be blank",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

---

## 2) Webhook: checkout.session.completed

After the customer pays, Stripe sends checkout.session.completed to your webhook at POST /webhook/event.

- The server verifies the signature and parses the event.
- It stores the PaymentIntent ID (pi_...) associated with the session for later capture.

You must keep your webhook configured and reachable for capture to work later.

---

## 3) Capture the Held Amount

Endpoint: POST /v1/st/capture/{id}

- Path parameter id: the Checkout Session ID returned by the hold step (format cs_...).
- Body: same schema as above. Only total is used to determine the capture amount. Other fields are validated but not used by capture.
- Authentication: required.

Full capture example (capture the entire authorized amount)

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

Partial capture example (capture only part of the authorized amount)

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

Successful response

```json
{
  "data": {
    "amount": 8500,
    "id": "pi_...b1ELPMpzHCbEuE9ab",
    "order_id": "123456"
  },
  "success": true,
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

Error responses (examples)

- 400 Bad Request — invalid body or business errors:

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

- 401 Unauthorized — missing/invalid token:

```json
{
  "success": false,
  "status_message": "Unauthorized",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

---

## 4) Cancel the Held Amount

Endpoint: POST /v1/st/cancel/{id}

Description: Cancels a payment authorization (hold) that was created via the hold endpoint. This releases the held funds back to the customer without capturing them. The payment authorization must exist and have been completed (webhook must have processed checkout.session.completed).

- Path parameter id: the Checkout Session ID returned by the hold step (format cs_...).
- Query parameter reason (optional): cancellation reason. Must be one of:
  - `duplicate` — duplicate payment
  - `fraudulent` — fraudulent payment
  - `requested_by_customer` — customer requested cancellation (default if not provided)
  - `abandoned` — payment was abandoned
- Authentication: required.
- Request body: not required.

Curl example (with reason)

```bash
curl -X POST "https://api.example.com/v1/st/cancel/cs_...b1ELPMpzHCbEuE9ab?reason=requested_by_customer" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

Curl example (without reason, defaults to requested_by_customer)

```bash
curl -X POST "https://api.example.com/v1/st/cancel/cs_...b1ELPMpzHCbEuE9ab" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

Successful response

```json
{
  "data": {
    "amount": 15000,
    "id": "pi_...b1ELPMpzHCbEuE9ab",
    "order_id": "123456"
  },
  "success": true,
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

- data.id is the PaymentIntent ID (starts with pi_...).
- data.amount is the canceled amount in minor units.

Error responses (examples)

- 400 Bad Request — invalid reason or business errors:

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

- 401 Unauthorized — missing/invalid token:

```json
{
  "success": false,
  "status_message": "Unauthorized",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

## Tips and Notes

- Use the Checkout Session ID (cs_...) in the capture and cancel path parameters, not a PaymentIntent ID.
- The webhook must have successfully processed checkout.session.completed for the session (it stores payment_id used for capture and cancel).
- Partial captures are allowed as long as total <= authorized amount.
- Zero amount is not allowed by HTTP validation.
- Cancel releases the held funds back to the customer without capturing them.
- For direct payments (no hold/capture), use POST /v1/st/pay (see docs/api.md).
