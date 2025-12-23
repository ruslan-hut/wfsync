# API Documentation

## Overview

WFSync provides a REST API for payment processing (Stripe) and invoice management (Wfirma) integrated with OpenCart e-commerce platform.

## Base URL

```
https://api.example.com
```

## Authentication

All endpoints under `/v1` require Bearer token authentication.

```bash
curl -X GET 'https://api.example.com/v1/endpoint' \
  -H 'Authorization: Bearer YOUR_TOKEN'
```

## Response Format

All API responses follow a standard JSON structure:

```json
{
  "success": true,
  "data": { ... },
  "status_message": "Success",
  "timestamp": "2025-07-01T09:27:19Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `success` | boolean | Indicates if the request was successful |
| `data` | object/null | Response payload (varies by endpoint) |
| `status_message` | string | Human-readable status or error message |
| `timestamp` | string | ISO 8601 formatted timestamp |

## Error Responses

On error, `success` is `false` and `status_message` contains the error description:

```json
{
  "success": false,
  "status_message": "Invalid request: field validation failed",
  "timestamp": "2025-07-01T09:27:19Z"
}
```

### Common HTTP Status Codes

| Code | Description |
|------|-------------|
| 200 | Success |
| 400 | Bad Request - Invalid input or validation error |
| 401 | Unauthorized - Missing or invalid token |
| 403 | Forbidden - Insufficient permissions |
| 404 | Not Found - Resource not found |
| 500 | Internal Server Error |

## Endpoints Overview

### Wfirma Endpoints (Invoice Management)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/v1/wf/invoice/{id}` | Download invoice PDF by Wfirma ID |
| GET | `/v1/wf/order/{id}` | Create invoice from OpenCart order |
| GET | `/v1/wf/file/proforma/{id}` | Get proforma file for OpenCart order |
| GET | `/v1/wf/file/invoice/{id}` | Get invoice file for OpenCart order |
| POST | `/v1/wf/proforma` | Create proforma from payload |
| POST | `/v1/wf/invoice` | Create invoice from payload |

See [Wfirma API Documentation](api-wfirma.md) for details.

### Stripe Endpoints (Payment Processing)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/st/hold` | Create payment authorization (hold) |
| POST | `/v1/st/pay` | Create direct payment |
| POST | `/v1/st/capture/{id}` | Capture held payment |
| POST | `/v1/st/cancel/{id}` | Cancel held payment |

See [Stripe API Documentation](api-stripe.md) for details.

### Webhook Endpoints (Public)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/webhook/event` | Stripe webhook receiver |

The webhook endpoint does not require Bearer token authentication. It uses Stripe signature verification.

## Common Data Types

### Currency

Supported currencies: `PLN`, `EUR`

### Monetary Amounts

All monetary amounts are in **minor units** (cents/grosze):
- `15000` = 150.00 PLN/EUR
- `8500` = 85.00 PLN/EUR

## Related Documentation

- [Wfirma API](api-wfirma.md) - Invoice and proforma management
- [Stripe API](api-stripe.md) - Payment processing
- [Stripe Hold-Capture Flow](stripe-hold-capture.md) - Detailed payment authorization guide
