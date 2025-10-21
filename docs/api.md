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
- Description: Capture the funds from a previously authorized (held) Stripe payment.
- Path parameter:
  - `id` — Stripe PaymentIntent ID (format: `pi_...`). Note: this is NOT the Checkout Session ID.
- Authentication: required (send Bearer token in Authorization header).

Request body

The body uses the same schema as payment creation and must pass validation. For capture, only these fields affect processing:
- `total` — amount to capture in the smallest currency unit (e.g., cents). Can be equal to or less than the authorized amount to perform a partial capture.
- `order_id` — your internal order identifier, saved for bookkeeping.

Other fields are required by validation and should mirror the original authorization context:
- `currency` — must match the currency of the original authorization (one of: PLN, EUR).
- `client_details`, `line_items`, `success_url` — included for validation; not used during capture.

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

On successful capture the API returns an OK envelope with `data` containing the captured amount, the original PaymentIntent ID, and the order ID.

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

- 400 Bad Request — invalid body, validation failed, or Stripe returned an error (message in `status_message`).
- 401 Unauthorized — missing/invalid token.

Notes

- Partial capture: set `total` to the amount you want to capture (<= authorized). The remaining authorization will follow Stripe’s standard behavior for uncaptured amounts.
- Ensure the `id` is the PaymentIntent ID (`pi_...`). Using a Checkout Session ID (`cs_...`) will fail.