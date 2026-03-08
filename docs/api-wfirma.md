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
| `client_details` | object | Yes | Customer information (see [ClientDetails](#clientdetails)) |
| `line_items` | array | Yes | Order line items (min: 1) |
| `total` | integer | Yes | Total amount in minor units (min: 1) |
| `currency` | string | Yes | Currency code: `PLN` or `EUR` |
| `order_id` | string | Yes | Unique order identifier (1-32 chars) |
| `success_url` | string | Yes | URL (required for validation) |
| `customer_group` | integer | No | `-1` for B2B, `0` or omit for B2C. See [VAT & Customer Group](#vat--customer-group) |
| `tax_value` | integer | No | Tax amount in minor units. When omitted, VAT rate is auto-detected from country. See [VAT & Customer Group](#vat--customer-group) |
| `sub_total` | integer | No | Subtotal before tax in minor units. Improves VAT rate calculation accuracy |
| `shipping` | integer | No | Shipping amount in minor units |

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
| `client_details` | object | Yes | Customer information (see [ClientDetails](#clientdetails)) |
| `line_items` | array | Yes | Order line items (min: 1) |
| `total` | integer | Yes | Total amount in minor units (min: 1) |
| `currency` | string | Yes | Currency code: `PLN` or `EUR` |
| `order_id` | string | Yes | Unique order identifier (1-32 chars) |
| `success_url` | string | Yes | URL (required for validation) |
| `customer_group` | integer | No | `-1` for B2B, `0` or omit for B2C. See [VAT & Customer Group](#vat--customer-group) |
| `tax_value` | integer | No | Tax amount in minor units. When omitted, VAT rate is auto-detected from country. See [VAT & Customer Group](#vat--customer-group) |
| `sub_total` | integer | No | Subtotal before tax in minor units. Improves VAT rate calculation accuracy |
| `shipping` | integer | No | Shipping amount in minor units |

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

### Create B2B Proforma

Creates a proforma invoice in Wfirma from a B2B order payload. The order is converted to `CheckoutParams` internally with B2B customer group and then processed through the standard proforma creation flow.

```
POST /v1/b2b/proforma
```

#### Permissions

Requires `WFirmaAllowInvoice` permission.

#### Request Body (B2BOrder)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `order_uid` | string | Yes | Unique order identifier |
| `order_number` | string | Yes | Order number (used as `order_id` in Wfirma) |
| `client_uid` | string | No | Client unique identifier |
| `client_name` | string | Yes | Client full name or company name |
| `client_email` | string | Yes | Client email address |
| `client_phone` | string | No | Client phone number |
| `client_vat` | string | No | Client VAT number (tax ID) |
| `client_country` | string | Yes | Country code (e.g., "PL", "DE") |
| `client_city` | string | No | City name |
| `client_address` | string | No | Street address |
| `client_zipcode` | string | No | Postal code |
| `store_uid` | string | No | Store identifier |
| `status` | string | No | Order status |
| `total` | number | Yes | Total amount in major units, e.g. `150.00` (must be > 0) |
| `subtotal` | number | No | Subtotal before tax |
| `total_vat` | number | No | VAT amount in major units |
| `discount_percent` | number | No | Discount percentage |
| `discount_amount` | number | No | Discount amount in major units |
| `currency_code` | string | Yes | Currency code: `PLN` or `EUR` |
| `created_at` | string | No | Order creation timestamp (ISO 8601) |
| `items` | array | Yes | Order line items (min: 1, see [B2BItem](#b2bitem)) |

**Note:** Unlike `/v1/wf/proforma`, amounts are in **major units** (e.g., `150.00` not `15000`). They are converted to minor units internally.

#### Example Request

```bash
curl -X POST "https://api.example.com/v1/b2b/proforma" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "order_uid": "b2b-uid-001",
    "order_number": "B2B-123456",
    "client_name": "GmbH Berlin",
    "client_email": "billing@gmbh.de",
    "client_vat": "DE123456789",
    "client_country": "DE",
    "client_city": "Berlin",
    "client_address": "Hauptstr. 1",
    "client_zipcode": "10115",
    "total": 150.00,
    "total_vat": 0,
    "currency_code": "EUR",
    "items": [
      {
        "product_name": "Product A",
        "product_sku": "SKU-001",
        "quantity": 1,
        "price": 100.00
      },
      {
        "product_name": "Product B",
        "product_sku": "SKU-002",
        "quantity": 2,
        "price": 25.00
      }
    ]
  }'
```

#### Response

Returns `Payment` object with the URL to the generated proforma PDF:

```json
{
  "url": "https://files.example.com/uuid.pdf"
}
```

#### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `url` | string | Public URL to download the proforma PDF |

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request body or validation error |
| 401 | User not found / unauthorized |
| 403 | User lacks `WFirmaAllowInvoice` permission |
| 500 | B2B service unavailable or proforma creation failed |

---

### Create B2B Invoice

Creates an invoice in Wfirma from a B2B order payload. Identical request format to [Create B2B Proforma](#create-b2b-proforma) but creates a finalized invoice instead.

```
POST /v1/b2b/invoice
```

#### Permissions

Requires `WFirmaAllowInvoice` permission.

#### Request Body

Same as [B2BOrder](#create-b2b-proforma) (see Create B2B Proforma).

#### Response

```json
{
  "url": "https://files.example.com/uuid.pdf"
}
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid request body or validation error |
| 401 | User not found / unauthorized |
| 403 | User lacks `WFirmaAllowInvoice` permission |
| 500 | B2B service unavailable or invoice creation failed |

---

### Sync Invoices from Remote (Pull)

Pulls invoices from Wfirma for a date range and syncs them to the local MongoDB collection. Upserts remote invoices locally and deletes local records that no longer exist on Wfirma.

```
POST /v1/wf/sync/pull?from=YYYY-MM-DD&to=YYYY-MM-DD
```

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `from` | string | Yes | Start date (inclusive), format `YYYY-MM-DD` |
| `to` | string | Yes | End date (inclusive), format `YYYY-MM-DD` |

#### Permissions

Requires `WFirmaAllowInvoice` permission.

#### How It Works

1. Fetches all normal invoices from Wfirma for the date range
2. Upserts each remote invoice into the local `wfirma_invoice` collection (including the `number` field)
3. Finds local invoices for the same range whose IDs are absent from the remote set
4. Deletes those orphaned local records

#### Response

Returns `SyncResult` object:

```json
{
  "success": true,
  "data": {
    "remote_count": 15,
    "local_count": 14,
    "upserted": 15,
    "deleted": 1,
    "recreated": 0
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Example

```bash
curl -X POST "https://api.example.com/v1/wf/sync/pull?from=2025-01-01&to=2025-01-31" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid date format (expected `YYYY-MM-DD`) |
| 401 | Unauthorized |
| 403 | User lacks `WFirmaAllowInvoice` permission |
| 500 | Wfirma or database unavailable |

---

### Sync Invoices to Remote (Push)

Checks local invoices against Wfirma for a date range and re-creates any that are missing on the remote side.

```
POST /v1/wf/sync/push?from=YYYY-MM-DD&to=YYYY-MM-DD
```

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `from` | string | Yes | Start date (inclusive), format `YYYY-MM-DD` |
| `to` | string | Yes | End date (inclusive), format `YYYY-MM-DD` |

#### Permissions

Requires `WFirmaAllowInvoice` permission.

#### How It Works

1. Reads local invoices from MongoDB for the date range
2. Fetches remote invoices from Wfirma for the same range
3. Finds local IDs that are absent from the remote set
4. Re-creates each missing invoice on Wfirma using the stored data (contractor, line items, etc.)
5. Deletes the old local record and saves a new one with the updated ID and invoice number

#### Response

Returns `SyncResult` object:

```json
{
  "success": true,
  "data": {
    "remote_count": 14,
    "local_count": 15,
    "upserted": 0,
    "deleted": 0,
    "recreated": 1
  },
  "status_message": "Success",
  "timestamp": "2025-07-07T11:41:40Z"
}
```

#### Example

```bash
curl -X POST "https://api.example.com/v1/wf/sync/push?from=2025-01-01&to=2025-01-31" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

#### Errors

| Code | Description |
|------|-------------|
| 400 | Invalid date format (expected `YYYY-MM-DD`) |
| 401 | Unauthorized |
| 403 | User lacks `WFirmaAllowInvoice` permission |
| 500 | Wfirma or database unavailable |

---

## VAT & Customer Group

Applies to `POST /v1/wf/proforma` and `POST /v1/wf/invoice` endpoints.

### Customer group

Use `customer_group` to control B2B vs B2C treatment:

| Value | Meaning |
|-------|---------|
| `-1` | **B2B** — explicit B2B flag for API callers |
| `0` (or omit) | **B2C** — default, consumer invoice |

B2B affects VAT handling for EU customers: B2B + valid `tax_id` gets 0% WDT (intra-community delivery), B2B without `tax_id` gets 23% Polish rate. B2C always uses the destination-country rate (OSS scheme).

### VAT rate auto-detection

When `tax_value` is omitted (or 0), the VAT rate is determined automatically from the customer's country:

| Country | B2C result | B2B + tax_id result | B2B without tax_id result |
|---------|------------|---------------------|---------------------------|
| PL (or empty) | 23% | 23% | 23% |
| EU country | Destination-country rate (e.g. 19% for DE, 25% for SE) | 0% WDT | 23% |
| Non-EU | 0% EXP (export) | 0% EXP | 0% EXP |

When `tax_value` **is** provided, the rate is calculated from the order totals (`tax_value / (total - shipping - tax_value) * 100`). This calculated rate is cross-checked against the internal VAT database for EU countries.

### Examples

**B2C invoice for a German customer (auto VAT):**

```json
{
  "client_details": {
    "name": "Max Mustermann",
    "email": "max@example.de",
    "country": "DE",
    "city": "Berlin",
    "street": "Hauptstr. 1",
    "zip_code": "10115"
  },
  "line_items": [{"name": "Product", "qty": 1, "price": 5000}],
  "total": 5000,
  "currency": "EUR",
  "order_id": "DE-001",
  "success_url": "https://example.com"
}
```

Result: 19% VAT (German rate), OSS invoice with `vat_moss_details`.

**B2B invoice for a German customer with VAT number:**

```json
{
  "client_details": {
    "name": "GmbH Berlin",
    "email": "billing@gmbh.de",
    "country": "DE",
    "tax_id": "DE123456789"
  },
  "line_items": [{"name": "Product", "qty": 1, "price": 5000}],
  "total": 5000,
  "currency": "EUR",
  "order_id": "DE-002",
  "success_url": "https://example.com",
  "customer_group": -1
}
```

Result: 0% WDT (intra-community delivery).

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

### B2BItem

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `product_uid` | string | No | Product unique identifier |
| `product_sku` | string | No | Product SKU |
| `product_name` | string | Yes | Product/service name |
| `quantity` | integer | Yes | Quantity (min: 1) |
| `price` | number | Yes | Unit price in major units (must be > 0) |
| `discount` | number | No | Discount amount |
| `price_discount` | number | No | Discounted unit price (used instead of `price` when > 0) |
| `tax` | number | No | Tax amount per item |
| `total` | number | No | Total amount per item |

### Payment (Response)

| Field | Type | Description |
|-------|------|-------------|
| `amount` | integer | Total amount in minor units |
| `id` | string | Wfirma invoice/proforma ID |
| `order_id` | string | OpenCart order ID |
| `link` | string | Public URL to the PDF file |
| `invoice_file` | string | PDF filename |

### SyncResult (Response)

| Field | Type | Description |
|-------|------|-------------|
| `remote_count` | integer | Number of invoices found on Wfirma |
| `local_count` | integer | Number of invoices found in local DB |
| `upserted` | integer | Records upserted to local DB (pull only) |
| `deleted` | integer | Orphaned records removed from local DB (pull only) |
| `recreated` | integer | Invoices re-created on Wfirma (push only) |
