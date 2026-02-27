# wFirma OSS Invoice Investigation

## Problem

When creating invoices via the wFirma API for B2C clients in EU countries with non-Polish VAT rates (e.g., Sweden 25%, Italy 22%, Germany 19%), the API resets the VAT rate to 23% (Polish default).

## Root Cause

wFirma requires **three things together** for OSS invoices to accept a foreign VAT rate:

1. `type_of_sale` — JSON-encoded array string, e.g. `["SW"]`
2. `vat` field on line items — plain numeric rate string, e.g. `"25"`
3. `vat_moss_details` — OSS evidence nested inside the invoice (buyer's country proof)

Without all three, the API silently resets the rate to Polish 23%.

## What We Tried (and Failed)

### Attempt 1: `vat_code` ID from `vat_codes/find`

The `vat_codes/find` endpoint only returns **Polish** VAT codes:

```
23 → ID 222, 22 → ID 225, 8 → ID 223, 7 → ID 226, 5 → ID 224, 3 → ID 227, 0 → ID 234
WDT → 228, EXP → 229, NP → 230, NPUE → 231, VAT_BUYER → 232, ZW → 233
```

Using `vat_code: {"id": 222}` always maps to Polish 23%, even for Ireland (which also has 23%).
Foreign EU rates are **not available** through this endpoint.

### Attempt 2: Plain `vat` field + `type_of_sale`

Sent `"vat": "25"` with `"type_of_sale": "[\"SW\"]"` — rate was still reset to Polish 23% (`vat_code: {"id": 222}` in response). The API needs `vat_moss_details` to activate the OSS mode.

### Attempt 3: `vat_moss_details/add` as separate API call

Called `https://api2.wfirma.pl/vat_moss_details/add` — returned XML error:

```xml
<api><status><code>CONTROLLER NOT FOUND</code></status></api>
```

**`vat_moss_details` is NOT a standalone API controller.** It's a nested sub-resource of invoices.

### Attempt 4: Nested `vat_moss_details` as array (wrong format)

Nested `vat_moss_details` inside the invoice payload using **array** format (like `invoicecontents`). The API silently ignored it. Tested with Ireland (23%) so the rate issue was inconclusive — IE and PL both use 23%.

## Solution (Previous Attempt — Failed)

### Discovery

The wFirma API documentation lists `vat_moss_details` as a **"pelny, pojedynczy"** (full, singular) related module of invoices. This means:

- It's a **one-to-one** relation (singular), NOT one-to-many (like `invoicecontents`)
- It must be nested as a **single object**, not an array
- Correct: `"vat_moss_details": {"vat_moss_detail": {...}}`
- Wrong: `"vat_moss_details": [{"vat_moss_detail": {...}}]`

### What failed

Two-step approach:

1. **Primary**: Nest `vat_moss_details` in `invoices/add` payload with singular format
2. **Fallback**: After creation, call `invoices/edit/{id}` with `vat_moss_details`

**Result (order #11594, SE 25%):**
- `invoices/add` silently ignored `vat_moss_details` — all line items came back with `vat_code: {"id": 222}` (Polish 23%)
- `invoices/edit` fallback failed: "Nie można modyfikować dokumentów, gdy rodzajem ewidencji są księgi rachunkowe" (Cannot modify documents when record type is full accounting books)
- `type_of_sale: ["SW"]` was accepted, but without `vat_moss_details` the VAT was still Polish 23%

## Solution (Current — Draft Approach)

### Rationale

Finalized invoices (`type: "normal"`) are immediately booked into "księgi rachunkowe" (full accounting books) and become immutable via API. Draft invoices (`type: "normal_draft"`) are **editable** because they aren't booked yet.

### Three-step flow for OSS invoices

1. **Create as draft**: `invoices/add` with `type: "normal_draft"` — includes `vat_moss_details`, `type_of_sale`, and plain `vat` on line items
2. **Edit the draft**: `invoices/edit/{id}` to attach `vat_moss_details` — drafts bypass the "księgi rachunkowe" restriction
3. **Approve the draft**: `invoices/edit/{id}` changing `type` to `"normal"` — assigns a number and books it

#### Create payload (step 1)

```json
{
  "api": {
    "invoices": [{
      "invoice": {
        "type": "normal_draft",
        "type_of_sale": "[\"SW\"]",
        "invoicecontents": [
          {"invoicecontent": {"name": "Product", "vat": "25", "price": 20.63, ...}}
        ],
        "vat_moss_details": {
          "vat_moss_detail": {
            "type": "BA",
            "evidence1_type": "A",
            "evidence1_description": "Street, Zip, City, Country",
            "evidence2_type": "F",
            "evidence2_description": "Order delivery address: SE"
          }
        }
      }
    }]
  }
}
```

#### Edit draft payload (step 2)

```json
{
  "api": {
    "invoices": [{
      "invoice": {
        "id": "{draft_id}",
        "vat_moss_details": {
          "vat_moss_detail": { ... }
        }
      }
    }]
  }
}
```

#### Approve draft payload (step 3)

```json
{
  "api": {
    "invoices": [{
      "invoice": {
        "id": "{draft_id}",
        "type": "normal"
      }
    }]
  }
}
```

#### OSS detection logic

```
isOSS = !isB2B && isEU && countryCode != "" && countryCode != "PL"
```

- B2B groups (6, 7, 16, 18, 19) use WDT/EXP, not OSS
- Polish B2C uses standard Polish rates
- Only non-PL EU B2C triggers OSS

#### VAT rate on line items

For OSS invoices, always use the **plain `vat` field** with the numeric rate (e.g. `"25"`).
Never use `vat_code` IDs for OSS — all IDs from `vat_codes/find` are Polish.

## `vat_moss_details` Fields

| Field | Description | Values |
|---|---|---|
| `type` | Service code | `BA`, `BB` (goods/WSTO); `SA`-`SE` (services); `TA`-`TK` (telecom) |
| `evidence1_type` | First evidence type | `A` (address), `B` (IP), `C` (bank), `D` (SIM), `E` (landline), `F` (other) |
| `evidence1_description` | First evidence detail | Customer's shipping/billing address |
| `evidence2_type` | Second evidence type | Same codes as above |
| `evidence2_description` | Second evidence detail | Delivery country or other proof |

## wFirma Account Requirements

The following must be enabled in wFirma Settings → Taxes → VAT:

- "PODATNIK VAT ZAREJESTROWANY W OSS" (VAT taxpayer registered in OSS)
- "ZAGRANICZNA SPRZEDAŻ WYSYŁKOWA" (Foreign distance selling)

## Test Orders

| Order | Country | Rate | Customer Group | Result |
|---|---|---|---|---|
| 11562 | IE | 23% | 3 (B2C) | Inconclusive — IE rate = PL rate |
| 11321 | ES | 21% | 7 (B2B) | Not OSS — B2B uses WDT |
| 11594 | SE | 25% | 3 (B2C) | Rate reset to 23% (before fix) |

## References

- [wFirma API docs](https://doc.wfirma.pl/)
- [PHP SDK (dbojdo/wFirma)](https://github.com/dbojdo/wFirma)
- [wFirma OSS help](https://pomoc.wfirma.pl/-faktura-vat-oss-jak-wystawic)
- [wFirma forum: foreign invoices via API](https://forum.wfirma.pl/temat/6470-wystawianie-faktur-dla-klientow-zagranicznych-poprzez-api)
