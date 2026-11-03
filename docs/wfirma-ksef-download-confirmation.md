# wFirma KSeF Download — Transaction Confirmation vs. Full Invoice

## Symptom

For some invoices, `DownloadInvoice` (`internal/wfirma/invoice.go`, endpoint
`invoices/download/{id}` with `page=invoice`) returns a **QR-code summary form** instead of
the real invoice:

- seller / buyer data, `Kwota należności ogółem`, **two QR codes**
  (`Sprawdź fakturę w KSeF` / `Zweryfikuj wystawcę faktury`),
- **no line items, no invoice number, no KSeF number**.

Downloading the same invoice manually from the wFirma web cabinet a little later yields the
full standard invoice (with `Numer w KSeF …` and `Data przetworzenia w KSeF …`).

## Cause

The QR-only form is wFirma's KSeF **"Potwierdzenie transakcji"** (transaction confirmation).
Per wFirma's own help pages: *when an invoice has been sent to KSeF but has not yet been
processed, the full invoice cannot be printed — only a transaction confirmation is available.*
The full invoice PDF becomes downloadable only **after** KSeF processing assigns the KSeF
number.

Our download runs right after invoice creation (capture flow / OpenCart invoice handler),
while the invoice is still being processed by KSeF, so wFirma hands back the confirmation.
The manual web download worked only because it happened after processing completed.

This is **not** controlled by any `invoices/download` parameter (the endpoint's parameters —
`page`, `address`, `leaflet`, `duplicate`, `payment_cashbox_documents`,
`warehouse_documents` — do not select confirmation vs. invoice), and it is **unrelated** to
the "Zezwól na wybór typu dokumentu przy drukowaniu faktury offline" account setting.

## Fix

Before downloading, `DownloadInvoice` polls `invoices/get/{id}` and waits until the invoice
is KSeF-processed (a KSeF number / registration date is assigned, or `ksef_status` is `ok`),
then downloads the full invoice.

Behavior:

- **Bounded**: waits up to `wfirma.ksef_download_wait_seconds` (default `30`), polling every
  3 seconds. `0` disables the gate and restores the legacy immediate download.
- **Fail-open / best-effort**: any ambiguity — a non-KSeF invoice, an unreadable state, or
  the wait budget being exhausted — results in downloading anyway. The gate can never do
  worse than the legacy behavior; it only ever delays a download we can positively tell is
  still pending in KSeF.
- Respects context cancellation.

KSeF field names in the `invoices/get` response are matched loosely (any key containing
`ksef`) to stay robust to wFirma's exact response shape. A debug log (`invoice not yet
processed by KSeF; waiting before download`) is emitted when the gate waits, which can be
used to confirm detection against live responses.

This gate does not rule confirmations out. `ksef_registration_date` is set when the invoice
is sent, so the gate reads it as processed at once. Waiting for `ksef_acquisition_timestamp`
does not work either: history shows full files for most foreign buyers well before it, and
confirmations for Polish and Slovak buyers until it (up to about 10 minutes; why these two
differ is unknown).

## The B2B portal: checking the file

`DownloadFinalInvoice`, used by `POST /v1/b2b/invoice`, checks the downloaded file. A
confirmation is deleted and the request answers `503` with `Retry-After` and
`invoice file not ready`. Asking again is safe: the request finds the registered invoice by
`id_external` and creates no duplicate. Comex repeats it by itself.

The text is in encrypted streams, so the check reads the unencrypted PDF structure: exactly
one page and three images. On 88 stored files this separated all 26 confirmations from 62
invoices (2 or 4 images). If wFirma changes the layout, a confirmation passes as an invoice,
as it did before this check. Every other flow keeps `DownloadInvoice` and is not checked.

## Alternative (non-code) remedy

Uploading a **Type 2 (offline) KSeF certificate** for the API user makes wFirma render the
full invoice (with QR codes) even before KSeF processing completes, so early downloads get
the proper document without waiting.

## Config

```yaml
wfirma:
  ksef_download_wait_seconds: 30   # default; 0 disables the gate
```

## Code

- `internal/wfirma/invoice.go` — `waitForKSefProcessed`, `ksefReadiness`,
  `classifyKSefFields`, `rawJSONString`, and the gate call at the top of `DownloadInvoice`
- `internal/config/config.go` — `WfirmaConfig.KSefDownloadWaitSeconds`
- `internal/wfirma/invoice_test.go` — `TestClassifyKSefFields`, `TestRawJSONString`

See also: [wFirma KSeF Draft Fallback](wfirma-ksef-draft-fallback.md).
