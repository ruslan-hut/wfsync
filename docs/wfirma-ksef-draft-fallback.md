# wFirma KSeF Draft Fallback

## Problem

When KSeF integration is enabled on the wFirma account, a normal VAT invoice
(`type: normal`) is **automatically submitted to KSeF the moment it is issued**.
KSeF authorization in wFirma is *per logged-in user* and is **not inherited** from the
account administrator. If the technical user whose API keys WFSync uses does not have an
active KSeF authorization (no/expired certificate, or only ZAW-FA administrative rights),
wFirma rejects issuance with:

```
Brak autoryzacji w KSeF 2.0. Zautoryzuj się w zakładce PRZYCHODY » KSEF I INTEGRACJE
```

In WFSync this surfaces from `submitInvoice` as `wFirma error: <message>` (the text comes
from `status.message` in the API response).

## The proper fix (no code)

Give the API user a real KSeF authorization, then this fallback never triggers:

1. A KSeF-authorized wFirma user opens **PRZYCHODY » KSEF I INTEGRACJE » ZAAWANSOWANE**
   and ticks **"Udostępnij moją autoryzację do połączeń API"**.
2. The account administrator selects that user under **"Użytkownik do autoryzacji API"**.

## The fallback (this feature)

When `wfirma.ksef_draft_fallback: true`, if wFirma rejects a `normal` invoice with a KSeF
authorization error, WFSync re-submits the **same payload** as a draft
(*wersja robocza faktury*, `type: normal_draft`, informally "WRF").

A draft is **not** sent to KSeF, so it registers successfully without KSeF authorization.

### Important: a draft is not yet a legal invoice

- It has **no final number** and no KSeF UID until accepted.
- It creates **no VAT-register / accounting entry**.
- A human must **accept it in wFirma** (PRZYCHODY » FAKTURY) once KSeF authorization is
  restored; acceptance assigns the number and (with KSeF on) transmits it.

Because of this, every draft-fallback creation raises a **Telegram alert on the error
topic** — including on retry-queue attempts — so the draft is not silently left pending.

## Configuration

```yaml
wfirma:
  enabled: true
  access_key: "..."
  secret_key: "..."
  app_id: "..."
  ksef_draft_fallback: true   # default false — opt in explicitly
```

## Scope / behavior

- Only `normal` invoices fall back. Proformas are not sent to KSeF and never hit this path;
  drafts of drafts are impossible (the type guard prevents loops).
- Detection (`isKSefAuthError`) requires both a KSeF mention **and** authorization wording
  ("autoryzac…"), case-insensitively, so unrelated validation/structure errors do not
  trigger a draft.
- If the draft submission itself fails, the original error is preserved in the logs and the
  call returns an error as before — nothing is lost.

## Code references

- `internal/config/config.go` — `WfirmaConfig.KSefDraftFallback`
- `internal/wfirma/invoice.go` — `invoiceNormalDraft` constant, `isKSefAuthError`,
  `submitDraftFallback`, and the fallback branch in `submitInvoice`
- `internal/wfirma/invoice_test.go` — `TestIsKSefAuthError`
