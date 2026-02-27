# wFirma OSS Invoice Investigation

## Problem

When creating invoices via the wFirma API for B2C clients in EU countries with non-Polish VAT rates (e.g., Sweden 25%, Italy 22%, Germany 19%), the API resets the VAT rate to 23% (Polish default).

## Root Cause

wFirma requires **two things together** for OSS invoices to accept a foreign VAT rate:

1. **Foreign `vat_code` ID** on each line item — country-specific codes resolved via `declaration_countries/find` → `vat_codes/find`
2. **`vat_moss_details`** — OSS evidence nested inside the invoice as a singular object (buyer's country proof)

Using a plain `vat` field with a numeric rate (e.g. `"25"`) is silently overridden to Polish 23%. The `type_of_sale` field is not required.

## What We Tried (and Failed)

### Attempt 1: Polish `vat_code` IDs from `vat_codes/find`

Initially only fetched the first page of vat_codes with `limit: 20`, which returned only Polish codes.
Using `vat_code: {"id": 222}` always maps to Polish 23%.
Foreign EU codes (IDs 607+) are available but require fetching all pages and have empty `code` fields (keyed by `declaration_country.id` instead).

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

### Attempt 5: Draft approach (`normal_draft`)

Created invoice as `type: "normal_draft"` hoping to edit the draft (bypassing "księgi rachunkowe") and then approve it. API rejected the draft creation entirely — `normal_draft` type not supported for this account (requires KSeF module).

## Solution (Working — Foreign vat_code IDs + vat_moss_details)

### Discovery

Examining a working OSS invoice (FV 627/2025, Germany 19%) created through the wFirma UI revealed:
- Line items use `vat_code: {"id": "617"}` — a **foreign** vat_code specific to Germany
- `type_of_sale` is empty

The wFirma `vat_codes/find` endpoint returns **both** Polish and foreign codes (144 total):
- Polish codes (IDs 222-234): have a non-empty `code` field ("23", "WDT", etc.) and `declaration_country.id = "0"`
- Foreign codes (IDs 607+): have an empty `code` field, a numeric `rate`, and `declaration_country.id > 0`

### Resolution chain

```
ISO country code → declaration_country_id → foreign vat_code_id
```

1. `declaration_countries/find` with a `code` filter maps the ISO code to an internal ID (e.g. SE → 205)
2. `vat_codes/find` (paginated, all pages) returns foreign codes keyed by `declaration_country.id` (e.g. 205 → vat_code 687)
3. Use `vat_code: {"id": "687"}` on line items + `vat_moss_details` with buyer evidence

### Payload structure

```json
{
  "api": {
    "invoices": [{
      "invoice": {
        "type": "normal",
        "vat_moss_details": {
          "vat_moss_detail": {
            "type": "BA",
            "evidence1_type": "A",
            "evidence1_description": "Street, Zip, City, Country",
            "evidence2_type": "F",
            "evidence2_description": "Order delivery address: SE"
          }
        },
        "invoicecontents": [
          {"invoicecontent": {"name": "Product", "vat_code": {"id": "687"}, "price": 20.63, ...}}
        ]
      }
    }]
  }
}
```

### OSS detection logic

```
isOSS = !isB2B && isEU && countryCode != "" && countryCode != "PL"
```

- B2B groups (6, 7, 16, 18, 19) use WDT/EXP, not OSS
- Polish B2C uses standard Polish rates
- Only non-PL EU B2C triggers OSS

### Key vat_code examples

| Country | declaration_country_id | vat_code_id | Rate |
|---|---|---|---|
| DE | 146 | 617 | 19% |
| SE | 205 | 687 | 25% |
| DK | 43 | 616 | 25% |
| HR | 39 | 633 | 25% |

### API quirks encountered

- `declaration_country.id` is returned as a **number** when nested in vat_code responses, but as a **string** in declaration_countries/find — requires flexible JSON unmarshaling
- Pagination parameters (`limit`, `page`, `total`) are sometimes strings, sometimes numbers
- wFirma uses **1-based** page numbering
- Using a foreign `vat_code` without `vat_moss_details` triggers validation errors (type, evidence1_type, evidence2_type are required)
- Using a plain `vat` field with `vat_moss_details` (without foreign `vat_code`) silently resets the rate to Polish 23%

## wFirma Account Requirements

The following must be enabled in wFirma Settings → Taxes → VAT:

- "PODATNIK VAT ZAREJESTROWANY W OSS" (VAT taxpayer registered in OSS)
- "ZAGRANICZNA SPRZEDAŻ WYSYŁKOWA" (Foreign distance selling)

## Test Orders

| Order | Country | Rate | Customer Group | Result |
|---|---|---|---|---|
| 11562 | IE | 23% | 3 (B2C) | Inconclusive — IE rate = PL rate |
| 11321 | ES | 21% | 7 (B2B) | Not OSS — B2B uses WDT |
| 11594 | SE | 25% | 3 (B2C) | Fixed — vat_code 687 applied correctly (FV 351/2026) |

## References

- [wFirma API docs](https://doc.wfirma.pl/)
- [PHP SDK (dbojdo/wFirma)](https://github.com/dbojdo/wFirma)
- [wFirma OSS help](https://pomoc.wfirma.pl/-faktura-vat-oss-jak-wystawic)
- [wFirma forum: foreign invoices via API](https://forum.wfirma.pl/temat/6470-wystawianie-faktur-dla-klientow-zagranicznych-poprzez-api)
