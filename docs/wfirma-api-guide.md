# wFirma API Developer Guide

A practical guide for working with the [wFirma API](https://doc.wfirma.pl/) based on real integration experience. Covers authentication, invoicing, VAT handling, OSS compliance, and known edge cases.

## Table of Contents

- [API Basics](#api-basics)
- [Authentication](#authentication)
- [Request / Response Format](#request--response-format)
- [Modules Reference](#modules-reference)
- [Invoices](#invoices)
- [Contractors](#contractors)
- [Goods (Products)](#goods-products)
- [VAT Codes](#vat-codes)
- [Declaration Countries](#declaration-countries)
- [VAT Handling](#vat-handling)
- [WFSync API VAT Defaults](#wfsync-api-vat-defaults)
- [OSS Invoices (EU B2C)](#oss-invoices-eu-b2c)
- [Pagination](#pagination)
- [Searching and Filtering](#searching-and-filtering)
- [Known Edge Cases and Quirks](#known-edge-cases-and-quirks)
- [Account Configuration](#account-configuration)
- [References](#references)

---

## API Basics

| | |
|---|---|
| **Base URL** | `https://api2.wfirma.pl` |
| **Protocol** | HTTPS only |
| **Format** | JSON (default XML, must opt into JSON) |
| **Endpoint pattern** | `/{module}/{action}[/{id}]?inputFormat=json&outputFormat=json` |

### Common actions per module

| Action | HTTP | Description |
|---|---|---|
| `add` | POST | Create a new record |
| `edit/{id}` | POST | Update an existing record |
| `find` | POST | Search with conditions |
| `get/{id}` | GET | Fetch a single record |
| `delete/{id}` | DELETE | Remove a record |
| `download/{id}` | POST | Download a file (invoices only) |

## Authentication

Three headers are required on every request:

```
accessKey: <your-access-key>
secretKey: <your-secret-key>
appKey: <your-app-key>
```

Keys are obtained from wFirma: **Settings > Security > Applications > API Keys**. The `appKey` is issued per application by wFirma support.

Official docs: [API authorization](https://pomoc.wfirma.pl/-api-interfejs-dla-programistow)

## Request / Response Format

### Request

Always POST JSON with `inputFormat=json&outputFormat=json` query parameters:

```json
{
  "api": {
    "invoices": [{
      "invoice": {
        "type": "normal",
        "contractor": { "id": 12345 },
        "invoicecontents": [
          { "invoicecontent": { "name": "Item", "price": 100.00, "count": 1, "unit": "szt.", "vat": "23" } }
        ]
      }
    }]
  }
}
```

### Response

Responses use **index-keyed objects** instead of arrays:

```json
{
  "status": { "code": "OK" },
  "invoices": {
    "0": { "invoice": { "id": 1001, "fullnumber": "FV 1/2026", ... } }
  }
}
```

> **Important**: Collections in responses are always objects with string index keys (`"0"`, `"1"`, ...), never JSON arrays. Your code must handle `map[string]T` deserialization for all list results.

### Error response

```json
{
  "status": { "code": "ERROR", "message": "Description of the error" }
}
```

Field-level validation errors may also appear nested inside the returned entity (e.g., `contractor.errors`, `invoice.errors`).

## Modules Reference

### Invoices

**Endpoint**: `/invoices/{action}`

#### Invoice types

| Type | Description |
|---|---|
| `normal` | Standard VAT invoice (faktura VAT) |
| `proforma` | Proforma invoice |
| `normal_draft` | Draft invoice — requires KSeF module, not available on all accounts |

#### Price types

| Value | Description |
|---|---|
| `netto` | Net prices (VAT added on top) |
| `brutto` | Gross prices (VAT included) |

#### Payment methods

`transfer`, `cash`, `compensation`, `cod`, `payment_card`

#### Creating an invoice

```
POST /invoices/add?inputFormat=json&outputFormat=json
```

Key fields in `invoice` object:

| Field | Type | Description |
|---|---|---|
| `type` | string | `"normal"` or `"proforma"` |
| `price_type` | string | `"brutto"` or `"netto"` |
| `payment_method` | string | See payment methods above |
| `payment_date` | string | ISO date, e.g. `"2026-02-28"` |
| `disposal_date` | string | Service/delivery date |
| `currency` | string | `"PLN"`, `"EUR"` |
| `description` | string | Invoice description / notes |
| `id_external` | string | External reference (e.g., order ID) |
| `contractor` | object | `{ "id": 12345 }` (existing) or full contractor object |
| `invoicecontents` | array | Line items (see below) |
| `vat_moss_details` | object | OSS evidence — required for EU B2C (see [OSS section](#oss-invoices-eu-b2c)) |

#### Line items (`invoicecontents`)

Each line item is wrapped: `{ "invoicecontent": { ... } }`

| Field | Type | Description |
|---|---|---|
| `name` | string | Item name |
| `count` | number | Quantity |
| `price` | number | Unit price in **major units** (not cents) |
| `unit` | string | Unit of measure, e.g. `"szt."` |
| `vat` | string | VAT rate as string: `"23"`, `"8"`, `"0"`, or code: `"WDT"`, `"EXP"`, `"NP"` |
| `vat_code` | object | `{ "id": "687" }` — preferred over `vat` field, required for OSS |
| `good` | object | `{ "id": 12345 }` — link to goods catalog (optional) |

> **Important**: When both `vat` and `vat_code` are provided, `vat_code` takes precedence. For OSS invoices, you **must** use `vat_code` with a foreign code ID — plain `vat` with a numeric rate will be silently overridden to Polish 23%.

#### Downloading an invoice PDF

```
POST /invoices/download/{id}?inputFormat=json
```

Note: the download endpoint does **not** use `outputFormat=json` — it returns raw PDF bytes. Send an empty JSON body `[{}]`.

#### Editing invoices

```
POST /invoices/edit/{id}?inputFormat=json&outputFormat=json
```

> **Limitation**: Accounts using "księgi rachunkowe" (full accounting books) **cannot edit finalized invoices**. The API returns: "Nie można modyfikować dokumentów, gdy rodzajem ewidencji są księgi rachunkowe". Plan your invoice creation carefully — corrections require issuing a new document.

### Contractors

**Endpoint**: `/contractors/{action}`

#### Creating a contractor

```
POST /contractors/add?inputFormat=json&outputFormat=json
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Company or person name |
| `email` | string | Email address |
| `nip` | string | Tax identification number |
| `tax_id_type` | string | `"none"` (no tax ID) or `"custom"` (has tax ID) |
| `zip` | string | Postal code |
| `city` | string | City |
| `street` | string | Street address |
| `country` | string | Country name |

#### Finding contractors

Search by email:

```json
{
  "api": {
    "contractors": {
      "parameters": {
        "conditions": [{
          "condition": { "field": "email", "operator": "eq", "value": "user@example.com" }
        }]
      }
    }
  }
}
```

### Goods (Products)

**Endpoint**: `/goods/{action}`

Find a product by SKU (`code` field):

```json
{
  "api": {
    "goods": {
      "parameters": {
        "conditions": [{
          "condition": { "field": "code", "operator": "eq", "value": "PRODUCT-SKU" }
        }]
      }
    }
  }
}
```

Returns `id` and `name` for linking to invoice line items via `"good": { "id": ... }`.

### VAT Codes

**Endpoint**: `/vat_codes/{action}`

Returns all VAT codes configured in the account. There are two categories:

| Category | Code IDs | `code` field | `declaration_country.id` | Example |
|---|---|---|---|---|
| **Polish** | 222–234 | Non-empty (`"23"`, `"WDT"`, `"EXP"`) | `"0"` | `{ "id": 222, "code": "23", "rate": "23.00" }` |
| **Foreign (OSS)** | 607+ | **Empty** | Country ID (e.g., `"146"` for DE) | `{ "id": 617, "code": "", "rate": "19.00", "declaration_country": { "id": 146 } }` |

A typical account has ~144 VAT codes total. Polish codes fit on the first page; **foreign codes require paginating through all pages** (see [Pagination](#pagination)).

#### Polish VAT code names

| Code | Rate | Description |
|---|---|---|
| `23` | 23% | Standard rate |
| `8` | 8% | Reduced rate |
| `5` | 5% | Super-reduced rate |
| `0` | 0% | Zero rate (domestic) |
| `WDT` | 0% | Intra-community delivery of goods (EU B2B with valid VAT number) |
| `EXP` | 0% | Export (non-EU) |
| `NP` | — | Not subject to Polish VAT |
| `NPUE` | — | EU reverse charge |
| `ZW` | — | VAT exempt |

### Declaration Countries

**Endpoint**: `/declaration_countries/{action}`

Maps ISO country codes to wFirma internal IDs. Use `find` with a `code` filter:

```json
{
  "api": {
    "declaration_countries": {
      "parameters": {
        "conditions": [{
          "condition": { "field": "code", "operator": "eq", "value": "DE" }
        }]
      }
    }
  }
}
```

Returns the internal `declaration_country_id` needed to look up foreign VAT codes.

## VAT Handling

### Decision tree

The VAT code applied to an invoice line depends on the customer type and location:

```
Country is empty or PL?
  → Use Polish rate (numeric string, e.g. "23")

Country is non-EU?
  → Use "EXP" (0% export)

Country is EU, customer is B2B with valid Tax ID?
  → Use "WDT" (0% intra-community delivery)

Country is EU, customer is B2B without Tax ID?
  → Use Polish rate "23" (cannot prove B2B status)

Country is EU, customer is B2C?
  → OSS applies — use destination country rate
    (requires foreign vat_code ID, see next section)
```

### VIES validation

For B2B EU transactions, validate the customer's VAT number against [VIES](https://ec.europa.eu/taxation_customs/vies/). A failed validation means the transaction falls back to domestic (Polish) VAT rates. We treat VIES as non-blocking — log the result but don't fail the invoice if the service is down.

## WFSync API VAT Defaults

When using the WFSync payload endpoints (`POST /v1/wf/invoice`, `POST /v1/wf/proforma`), the caller may not know or provide the exact tax amount. The system handles this automatically:

### Customer group

The `customer_group` field controls B2B vs B2C treatment:

- **`-1`** — explicit B2B flag for API callers (no OpenCart dependency)
- **`0`** (or omit) — B2C (default)
- **`6, 7, 16, 18, 19`** — OpenCart B2B groups (used internally)

### VAT rate when `tax_value` is not provided

When `tax_value` is 0 or missing, the VAT rate is inferred from the country code:

| Scenario | VAT rate applied |
|---|---|
| PL or no country | 23% (Polish standard) |
| EU B2C | Destination-country standard rate (e.g. DE 19%, SE 25%) |
| EU B2B + `tax_id` | 0% WDT |
| EU B2B without `tax_id` | 23% (Polish rate) |
| Non-EU | 0% EXP (export) |

The destination-country rate is resolved from the dynamic VAT database first (updated from vatlookup.eu), with a hardcoded fallback map as a last resort.

### VAT rate when `tax_value` is provided

The rate is calculated as `tax_value / (total - shipping - tax_value) * 100`. For EU B2C orders, this calculated rate is cross-checked against the internal VAT database — if they differ, the internal rate takes priority and a warning is logged.

## OSS Invoices (EU B2C)

OSS (One Stop Shop) is the EU VAT scheme for cross-border B2C sales. When selling to a consumer in another EU country, the seller charges VAT at the buyer's country rate.

### When OSS applies

```
isOSS = !isB2B && isEU && countryCode != "" && countryCode != "PL"
```

### What the API requires

Both of these must be present **together** in a single `invoices/add` call:

1. **Foreign `vat_code` ID** on each line item — a country-specific code from `vat_codes/find`
2. **`vat_moss_details`** — evidence of buyer location, nested as a **singular object** (not an array)

> **Critical**: Using only one without the other does not work:
> - Foreign `vat_code` without `vat_moss_details` → validation error (missing evidence fields)
> - `vat_moss_details` without foreign `vat_code` → VAT silently reset to Polish 23%
> - Plain `vat: "25"` (without `vat_code`) → always reset to Polish 23%, regardless of `vat_moss_details`

### Resolving a foreign VAT code ID

Three-step resolution chain:

```
1. ISO country code (e.g. "SE")
       ↓
2. declaration_countries/find → declaration_country_id (e.g. 205)
       ↓
3. vat_codes/find (all pages) → find entry where
   declaration_country.id == 205 → vat_code_id (e.g. 687)
```

**Caching strategy**: Fetch all VAT codes once on startup and build two maps:

- `vatCodes`: Polish code name → ID (e.g. `"23" → 222`)
- `ossVatCodes`: declaration_country_id → vat_code_id (e.g. `146 → 617`)

Cache `declCountries` (ISO code → declaration_country_id) per lookup, since new countries are rare.

### `vat_moss_details` structure

This is a **one-to-one relation** (`"pelny, pojedynczy"` in the API docs), not one-to-many:

```json
"vat_moss_details": {
  "vat_moss_detail": {
    "type": "BA",
    "evidence1_type": "A",
    "evidence1_description": "Customer Street, 12345, City, DE",
    "evidence2_type": "F",
    "evidence2_description": "Order delivery address: DE"
  }
}
```

#### Type codes

| Code | Description |
|---|---|
| `BA` | Distance selling of goods (WSTO) |
| `BB` | Domestic delivery of goods by electronic interfaces |
| `SA` | Telecommunication services |
| `SB` | Broadcasting services |
| `SC` | Electronic services |
| `SD` | Other services |
| `SE` | Services provided by intermediaries |

For e-commerce goods, use `"BA"`.

#### Evidence type codes

Two pieces of evidence are required to prove the buyer's location:

| Code | Description |
|---|---|
| `A` | Billing/delivery address |
| `B` | IP address |
| `C` | Bank account details |
| `D` | SIM card country code |
| `E` | Landline location |
| `F` | Other |

### Complete OSS invoice example

```json
{
  "api": {
    "invoices": [{
      "invoice": {
        "type": "normal",
        "price_type": "brutto",
        "payment_method": "transfer",
        "payment_date": "2026-03-07",
        "disposal_date": "2026-02-28",
        "currency": "PLN",
        "contractor": { "id": 56789 },
        "vat_moss_details": {
          "vat_moss_detail": {
            "type": "BA",
            "evidence1_type": "A",
            "evidence1_description": "Kungsgatan 5, 11143, Stockholm, SE",
            "evidence2_type": "F",
            "evidence2_description": "Order delivery address: SE"
          }
        },
        "invoicecontents": [
          {
            "invoicecontent": {
              "name": "Product A",
              "count": 2,
              "price": 49.99,
              "unit": "szt.",
              "vat_code": { "id": "687" }
            }
          },
          {
            "invoicecontent": {
              "name": "Shipping",
              "count": 1,
              "price": 15.00,
              "unit": "szt.",
              "vat_code": { "id": "687" }
            }
          }
        ]
      }
    }]
  }
}
```

### Known country-to-code mappings

| Country | ISO | declaration_country_id | vat_code_id | Standard rate |
|---|---|---|---|---|
| Germany | DE | 146 | 617 | 19% |
| Sweden | SE | 205 | 687 | 25% |
| Denmark | DK | 43 | 616 | 25% |
| Croatia | HR | 39 | 633 | 25% |

> These IDs are account-specific. Always resolve dynamically via the API.

## Pagination

The `find` action supports pagination:

| Parameter | Description |
|---|---|
| `limit` | Results per page (max 100) |
| `page` | Page number, **1-based** |

Request:

```json
{
  "api": {
    "vat_codes": {
      "parameters": {
        "limit": 100,
        "page": 1
      }
    }
  }
}
```

Response includes:

```json
"parameters": {
  "limit": 100,
  "page": 1,
  "total": 144,
  "order": { ... }
}
```

Loop until `page * limit >= total`.

> **Quirk**: The `limit`, `page`, and `total` values are sometimes returned as **strings** (e.g. `"100"`), sometimes as **numbers** (`100`). Your JSON unmarshaling must handle both.

## Searching and Filtering

Use the `conditions` array in `parameters` to filter results:

```json
{
  "api": {
    "contractors": {
      "parameters": {
        "conditions": [
          { "condition": { "field": "email", "operator": "eq", "value": "john@example.com" } },
          { "condition": { "field": "country", "operator": "eq", "value": "Poland" } }
        ]
      }
    }
  }
}
```

Supported operators: `eq`, `ne`, `gt`, `ge`, `lt`, `le`, `like`, `not like`, `in`, `not in`.

Multiple conditions are combined with AND logic.

## Known Edge Cases and Quirks

### Type inconsistencies in JSON responses

| Field | Context | Type | Example |
|---|---|---|---|
| `declaration_country.id` | Inside `vat_codes/find` response | **number** | `146` |
| `declaration_country.id` | In `declaration_countries/find` response | **string** | `"146"` |
| `contractor.id` | In `invoices/add` response | **number** | `12345` |
| `contractor.id` | In `contractors/find` response | **string** | `"12345"` |
| Pagination params | Varies | **string or number** | `"100"` or `100` |

**Recommendation**: Use flexible JSON unmarshaling that handles both string and number for IDs and pagination values. Example approach in Go:

```go
func unmarshalFlexString(data []byte) (string, error) {
    var s string
    if err := json.Unmarshal(data, &s); err == nil {
        return s, nil
    }
    var n json.Number
    if err := json.Unmarshal(data, &n); err == nil {
        return n.String(), nil
    }
    return "", fmt.Errorf("cannot parse as string or number: %s", data)
}
```

### Index-keyed collections

All collection responses use string-indexed objects, not arrays:

```json
// What the API returns:
{ "0": { "invoice": {...} }, "1": { "invoice": {...} } }

// NOT this:
[ { "invoice": {...} }, { "invoice": {...} } ]
```

Deserialize as `map[string]T` and iterate by keys.

### Invoice editing restrictions

- Accounts with "księgi rachunkowe" (full accounting books) **cannot edit finalized invoices**
- The `normal_draft` type requires the KSeF module — not available on all accounts
- There is no workaround via the API; invoices must be correct at creation time

### `vat_moss_details` is not a standalone module

Calling `/vat_moss_details/add` returns a "CONTROLLER NOT FOUND" XML error. It can only be used as a nested sub-resource inside `invoices/add`.

### Silent failures

The API does **not** return errors for certain invalid inputs — it silently ignores them:

- `vat_moss_details` as an array (should be a singular object) — silently ignored
- Plain `vat: "25"` on line items — silently overridden to Polish 23% (`vat_code.id: 222`)
- Unknown fields in payloads — silently ignored

### Download endpoint format

`/invoices/download/{id}` uses `inputFormat=json` but does **not** accept `outputFormat=json`. The response is raw PDF binary data, not JSON.

### Contractor defaults

The API requires `name`, `zip`, and `city` to be non-empty. If the customer hasn't provided these, use sensible defaults (e.g., `"-"`) to avoid validation errors.

## Account Configuration

For OSS functionality, enable the following in the wFirma web UI:

**Settings > Taxes > VAT:**

- "PODATNIK VAT ZAREJESTROWANY W OSS" (VAT taxpayer registered in OSS)
- "ZAGRANICZNA SPRZEDAŻ WYSYŁKOWA" (Foreign distance selling)

Without these settings, foreign VAT codes will not be available in `vat_codes/find`.

## References

- [wFirma API Documentation](https://doc.wfirma.pl/) — official API reference (Polish)
- [wFirma API Help Page](https://pomoc.wfirma.pl/-api-interfejs-dla-programistow) — getting started with the API
- [wFirma OSS Invoice Guide](https://pomoc.wfirma.pl/-faktura-vat-oss-jak-wystawic) — how to issue OSS invoices (UI)
- [wFirma Forum: Foreign Invoices via API](https://forum.wfirma.pl/temat/6470-wystawianie-faktur-dla-klientow-zagranicznych-poprzez-api) — community discussion
- [PHP SDK (dbojdo/wFirma)](https://github.com/dbojdo/wFirma) — third-party PHP client
- [Postman Collection](https://www.postman.com/speeding-moon-225969/my-workspace/documentation/u7c6l5i/wfirma-pl) — community-maintained API examples
