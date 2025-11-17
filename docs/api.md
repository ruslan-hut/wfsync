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

For detailed documentation on Wfirma endpoints, see the descriptions below.

#### Stripe Endpoints

- `POST /v1/st/hold` - Create a payment hold in Stripe
- `POST /v1/st/pay` - Create a direct payment in Stripe
- `POST /v1/st/capture/{id}` - Capture a previously held payment
- `POST /v1/st/cancel/{id}` - Cancel a payment

For detailed documentation on Stripe hold, capture, and cancel operations, see [Stripe Hold → Capture Flow](stripe-hold-capture.md).

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

### Download Invoice from Wfirma

- Endpoint: `GET /v1/wf/invoice/{id}`
- Description: Downloads an invoice PDF from Wfirma by invoice ID. The invoice must exist in the Wfirma system.
- Path parameter:
  - `id` — Wfirma invoice ID (numeric string).
- Authentication: required (send Bearer token in the Authorization header).

How it works

- The API retrieves the invoice from Wfirma by the provided invoice ID.
- The invoice is downloaded and returned as a PDF file with appropriate Content-Type headers.
- The response is a direct file download (binary PDF content).

Example request

```bash
curl -X GET "https://api.example.com/v1/wf/invoice/12345" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  --output invoice.pdf
```

Response

- On success: Returns the PDF file with `Content-Type: application/pdf` header.
- On error: Returns JSON error response with standard API error structure.

Error responses
- 400 Bad Request — invalid invoice ID format.
- 401 Unauthorized — missing/invalid token.
- 500 Internal Server Error — invoice not found in Wfirma, download failed, or Wfirma service unavailable.

### Create Invoice from OpenCart Order

- Endpoint: `GET /v1/wf/order/{id}`
- Description: Creates a Wfirma invoice from an OpenCart order. The order must exist in the OpenCart database. This endpoint requires the authenticated user to have invoice creation permissions.
- Path parameter:
  - `id` — OpenCart order ID (numeric).
- Authentication: required (send Bearer token in the Authorization header).
- Permission: The authenticated user must have `WFirmaAllowInvoice` permission enabled.

How it works

- The API retrieves the order from OpenCart by the provided order ID.
- Order details (line items, customer information, totals) are extracted.
- A new invoice is created in Wfirma with the order information.
- The invoice ID is returned in the response.

Example request

```bash
curl -X GET "https://api.example.com/v1/wf/order/123456" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

Successful response

```json
{
  "data": {
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
    "invoice_id": "98765"
  },
  "success": true,
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

- `data.invoice_id` contains the newly created Wfirma invoice ID.

Error responses
- 400 Bad Request — invalid order ID format or order not found.
- 401 Unauthorized — missing/invalid token.
- 403 Forbidden — user does not have permission to create invoices (`WFirmaAllowInvoice` is false).
- 500 Internal Server Error — OpenCart service unavailable, Wfirma service unavailable, or invoice creation failed.