# Wfirma API Documentation

Wfirma endpoints manage invoices and proformas through integration with the Wfirma accounting system and OpenCart e-commerce platform.

## Authentication

All endpoints require Bearer token authentication:

```bash
curl -H "Authorization: Bearer YOUR_TOKEN" ...
```

---

## Endpoints

### Download Invoice PDF

Downloads an invoice PDF from Wfirma by invoice ID.

```
GET /v1/wf/invoice/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Wfirma invoice ID (numeric) |

#### Response

Returns binary PDF file with headers:
- `Content-Type: application/pdf`
- `Content-Length: <file_size>`

#### Example

```bash
curl -X GET "https://api.example.com/v1/wf/invoice/12345" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  --output invoice.pdf
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid invoice ID format |
| 401 | Unauthorized |
| 500 | Invoice not found or download failed |

---

### Create Invoice from OpenCart Order

Creates a Wfirma invoice from an existing OpenCart order.

```
GET /v1/wf/order/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | OpenCart order ID (numeric) |

#### Permissions

Requires `WFirmaAllowInvoice` permission.

#### Response

Returns `CheckoutParams` object with the created invoice ID:

```json
{
  "success": true,
  "data": {
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
      {"name": "Product Name", "qty": 1, "price": 8500}
    ],
    "total": 8500,
    "currency": "PLN",
    "order_id": "123456",
    "invoice_id": "98765"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `client_details` | object | Customer information |
| `line_items` | array | Order line items |
| `total` | integer | Total amount in minor units |
| `currency` | string | Currency code (PLN/EUR) |
| `order_id` | string | OpenCart order ID |
| `invoice_id` | string | Created Wfirma invoice ID |

#### Example

```bash
curl -X GET "https://api.example.com/v1/wf/order/123456" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid order ID or order not found |
| 401 | Unauthorized |
| 403 | User lacks `WFirmaAllowInvoice` permission |
| 500 | OpenCart or Wfirma service unavailable |

---

### Get Proforma File for Order

Retrieves or creates a proforma invoice for an OpenCart order and returns the file link.

```
GET /v1/wf/file/proforma/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | OpenCart order ID (numeric) |

#### How It Works

1. Fetches order data from OpenCart
2. Creates or updates proforma in Wfirma
3. Downloads proforma PDF file
4. Returns file link and metadata

#### Response

Returns `Payment` object:

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "wfirma_proforma_id",
    "order_id": "123456",
    "link": "https://files.example.com/uuid.pdf",
    "invoice_file": "uuid.pdf"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Response Fields (Payment)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Total amount in minor units |
| `id` | string | Wfirma proforma ID |
| `order_id` | string | OpenCart order ID |
| `link` | string | Public URL to download the PDF file |
| `invoice_file` | string | Filename of the PDF file |

#### Example

```bash
curl -X GET "https://api.example.com/v1/wf/file/proforma/123456" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid order ID or order not found |
| 401 | Unauthorized |
| 500 | OpenCart or Wfirma service unavailable |

---

### Get Invoice File for Order

Retrieves or creates an invoice for an OpenCart order and returns the file link.

```
GET /v1/wf/file/invoice/{id}
```

#### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | OpenCart order ID (numeric) |

#### How It Works

1. Fetches order data from OpenCart
2. Creates invoice in Wfirma (if not already created)
3. Downloads invoice PDF file
4. Returns file link and metadata

#### Response

Returns `Payment` object:

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "wfirma_invoice_id",
    "order_id": "123456",
    "link": "https://files.example.com/uuid.pdf",
    "invoice_file": "uuid.pdf"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Response Fields (Payment)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Total amount in minor units |
| `id` | string | Wfirma invoice ID |
| `order_id` | string | OpenCart order ID |
| `link` | string | Public URL to download the PDF file |
| `invoice_file` | string | Filename of the PDF file |

#### Example

```bash
curl -X GET "https://api.example.com/v1/wf/file/invoice/123456" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid order ID or order not found |
| 401 | Unauthorized |
| 500 | OpenCart or Wfirma service unavailable |

---

### Create Proforma from Payload

Creates a proforma invoice in Wfirma using provided checkout data (without requiring OpenCart).

```
POST /v1/wf/proforma
```

#### Request Body (CheckoutParams)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `client_details` | object | Yes | Customer information |
| `line_items` | array | Yes | Order line items (min: 1) |
| `total` | integer | Yes | Total amount in minor units (min: 1) |
| `currency` | string | Yes | Currency code: `PLN` or `EUR` |
| `order_id` | string | Yes | Unique order identifier (1-32 chars) |
| `success_url` | string | Yes | URL (required for validation) |

#### Example Request

```bash
curl -X POST "https://api.example.com/v1/wf/proforma" \
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
      "street": "ul. Example 1",
      "tax_id": "1234567890"
    },
    "line_items": [
      {"name": "Product A", "qty": 1, "price": 8500},
      {"name": "Product B", "qty": 2, "price": 3250}
    ],
    "total": 15000,
    "currency": "PLN",
    "order_id": "ORD-123456",
    "success_url": "https://example.com"
  }'
```

#### Response

Returns `Payment` object:

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "wfirma_proforma_id",
    "order_id": "ORD-123456",
    "link": "https://files.example.com/uuid.pdf",
    "invoice_file": "uuid.pdf"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request body or validation error |
| 401 | Unauthorized |
| 500 | Wfirma service unavailable |

---

### Create Invoice from Payload

Creates an invoice in Wfirma using provided checkout data (without requiring OpenCart).

```
POST /v1/wf/invoice
```

#### Request Body (CheckoutParams)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `client_details` | object | Yes | Customer information |
| `line_items` | array | Yes | Order line items (min: 1) |
| `total` | integer | Yes | Total amount in minor units (min: 1) |
| `currency` | string | Yes | Currency code: `PLN` or `EUR` |
| `order_id` | string | Yes | Unique order identifier (1-32 chars) |
| `success_url` | string | Yes | URL (required for validation) |

#### Example Request

```bash
curl -X POST "https://api.example.com/v1/wf/invoice" \
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
      "street": "ul. Example 1",
      "tax_id": "1234567890"
    },
    "line_items": [
      {"name": "Product A", "qty": 1, "price": 8500},
      {"name": "Product B", "qty": 2, "price": 3250}
    ],
    "total": 15000,
    "currency": "PLN",
    "order_id": "ORD-123456",
    "success_url": "https://example.com"
  }'
```

#### Response

Returns `Payment` object:

```json
{
  "success": true,
  "data": {
    "amount": 15000,
    "id": "wfirma_invoice_id",
    "order_id": "ORD-123456",
    "link": "https://files.example.com/uuid.pdf",
    "invoice_file": "uuid.pdf"
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request body or validation error |
| 401 | Unauthorized |
| 500 | Wfirma service unavailable |

---

## Data Structures

### ClientDetails

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Customer full name |
| `email` | string | Yes | Customer email address |
| `phone` | string | No | Customer phone number |
| `country` | string | No | Country code (e.g., "PL") |
| `zip_code` | string | No | Postal code |
| `city` | string | No | City name |
| `street` | string | No | Street address |
| `tax_id` | string | No | Tax identification number (NIP) |

### LineItem

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Product/service name |
| `qty` | integer | Yes | Quantity (min: 1) |
| `price` | integer | Yes | Unit price in minor units (min: 1) |
| `sku` | string | No | Product SKU |
| `shipping` | boolean | No | Indicates if this is a shipping line item |

### Payment (Response)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Total amount in minor units |
| `id` | string | Wfirma invoice/proforma ID |
| `order_id` | string | OpenCart order ID |
| `link` | string | Public URL to the PDF file |
| `invoice_file` | string | PDF filename |
