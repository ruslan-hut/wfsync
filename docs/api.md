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

- `GET /v1/wf/invoice/{id}` - Download an invoice from Wfirma by ID

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

### Response on Successful Payment Creation

```json
{
    "data": {
        "amount": 15000,
        "id": "cs_test_b1ELPMpzH...CbEuE9ab",
        "order_id": "123456",
        "link": "https://checkout.stripe.com/c/pay/cs_...kZmBtamlhYHd2Jz9xd3BgeCUl"
    },
    "success": true,
    "status_message": "Success",
    "timestamp": "2025-07-07T11:41:40Z"
}
```