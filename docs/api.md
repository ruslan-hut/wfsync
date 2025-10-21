## API Endpoints

### Authentication Required Endpoints

All endpoints under `/v1` require authentication. To authenticate, send a Bearer token in the Authorization header.

Example of an authenticated request:

```bash
curl -X GET 'https://api.example.com/v1/wf/invoice/123' \
-H 'Authorization: Bearer your-token-here'
```
### Response structure

API response always has the following structure

```json
{
    "success": true,
    "data": {"response":  "payload"},
    "status_message": "Success",
    "timestamp": "2025-07-01T09:27:19Z"
}
```
`data` section may differ depending on endpoint purpose or may be null

#### Wfirma Endpoints

- `GET /v1/wf/invoice/{id}` - Download an invoice from Wfirma by ID in PDF format
- `GET /v1/wf/order/{id}` - Create a Wfirma invoice from an OpenCart Order with given ID

#### Stripe Endpoints

- `POST /v1/st/hold` - Create a payment hold in Stripe
- `POST /v1/st/pay` - Create a direct payment in Stripe
- `POST /v1/st/capture/{id}` - Capture a previously held payment
- `POST /v1/st/cancel/{id}` - Cancel a payment

### Public Endpoints

- `POST /webhook/event` - Webhook endpoint for Stripe events, requests authorized via Stripe authorization headers as described in the documentation

## Usage Examples

### Creating a Payment Hold or Direct
Body payload example, all fields with values in the example are mandatory.
Field `price` is in cents, so `8500` means `85.00`. Total amount for each line item is calculated as `qty * price`.

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
  "line_items":[
    {"name":"DARK Top Bez Wycierania, 30 ml","qty":1,"price":8500},
    {"name":"DARK Scotch Base (ulepszona formuła), 15 ml","qty":1,"price":6500}
  ],
  "total":15000,
  "currency":"PLN",
  "order_id": "123456",
  "success_url": ""
}
```

Response on Successful Payment Creation

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

### Capture a Held Payment

- Endpoint: `POST /v1/st/capture/{id}`
- Description: Capture funds for a previously authorized (held) payment created via Stripe Checkout in manual-capture mode.
- Path parameter:
  - `id` — Stripe Checkout Session ID (format `cs_...`).
- Authentication: required (send Bearer token in the Authorization header).

How it works

- The API looks up your original checkout record in the database by the provided Checkout Session ID (`cs_...`).
- That record is written when you created the hold (`POST /v1/st/hold`) and later enriched when your Stripe webhook processes `checkout.session.completed` (it stores the `payment_id` for capture).
- The handler captures against the stored Stripe PaymentIntent ID (`pi_...`).

Request body

The body uses the same schema as payment creation and must pass validation. During capture, only the `total` is used for the capture amount; the other fields are validated but ignored by the capture logic.

- Required fields (validation):
  - `client_details` — object
  - `line_items` — non-empty array
  - `total` — integer > 0; amount to capture in the smallest currency unit (e.g., cents)
  - `currency` — one of: `PLN`, `EUR` (validated but not used during capture)
  - `order_id` — string (persisted for bookkeeping)
  - `success_url` — URL (validated but not used during capture)

Important:
- Partial capture: set `total` to a value less than or equal to the originally authorized amount.
- Full capture: set `total` equal to the originally authorized amount.
- Zero amount is not allowed by validation (even though the lower layer can default 0 to full amount, the HTTP validation requires `total >= 1`).

Example request body

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

Successful response

On success, `data.id` contains the Stripe PaymentIntent ID (`pi_...`) that was captured, and `amount` is the captured amount in minor units.

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

Error responses
- 400 Bad Request — invalid body (fails validation); unknown session ID; missing `payment_id` in stored checkout; or Stripe returned an error (see `status_message`).
- 401 Unauthorized — missing/invalid token.

Notes
- The Checkout Session must have been created with manual capture via the hold flow.
- Your Stripe webhook for `checkout.session.completed` must be active and able to update the database with the `payment_id` for the session.
- Ensure the `id` is the Checkout Session ID (`cs_...`). Using a PaymentIntent ID (`pi_...`) will fail.