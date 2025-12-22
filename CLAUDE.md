# CLAUDE.md - Project Guide for AI Assistants

## Project Overview

WFSync is a Go-based payment and invoice management service that integrates Stripe payment processing with Wfirma invoice management and OpenCart e-commerce platform.

## Tech Stack

- **Language**: Go 1.24
- **HTTP Router**: chi v5.2.2
- **Database**: MongoDB (optional), MySQL (OpenCart)
- **Payment**: Stripe API (stripe-go/v76)
- **Invoice**: Wfirma API
- **Notifications**: Telegram Bot

## Project Structure

```
cmd/server/main.go          # Entry point
internal/                   # Private application code
  ├── config/               # Configuration management
  ├── database/             # MongoDB implementation
  ├── http-server/          # HTTP server, handlers, middleware
  ├── stripeclient/         # Stripe API client
  └── wfirma/               # Wfirma API client
impl/                       # Business logic implementations
  ├── auth/                 # Authentication service
  └── core/                 # Core business logic (main orchestration)
entity/                     # Domain entities and DTOs
opencart/                   # OpenCart integration
bot/                        # Telegram bot
lib/                        # Shared libraries (logging, validation, etc.)
docs/                       # API documentation
```

## Build & Run

```bash
# Build
go build -o wfsync cmd/server/main.go

# Run with config
./wfsync -conf config.yml

# Run with custom log path
./wfsync -conf config.yml -log /var/log/custom.log
```

## Configuration

Configuration via YAML files:
- `config.yml` - Local development
- `wfsync-dev.yml` - Development environment
- `wfsync.yml` - Production environment

Key config sections: `listen`, `stripe`, `wfirma`, `mongo`, `opencart`, `telegram`

## API Endpoints

All `/v1/*` endpoints require `Authorization: Bearer TOKEN`

### Payment
- `POST /v1/st/hold` - Create payment hold
- `POST /v1/st/pay` - Create direct payment
- `POST /v1/st/capture/{id}` - Capture held payment
- `POST /v1/st/cancel/{id}` - Cancel payment (with reason)

### Invoice (Wfirma)
- `GET /v1/wf/invoice/{id}` - Download invoice PDF by Wfirma ID
- `GET /v1/wf/order/{id}` - Create invoice from OpenCart order
- `GET /v1/wf/file/proforma/{id}` - Get proforma file for OpenCart order
- `GET /v1/wf/file/invoice/{id}` - Get invoice file for OpenCart order

### Webhook
- `POST /webhook/event` - Stripe webhook (signature-verified)

## Testing

```bash
go test ./...
go test -cover ./...
```

Note: Test coverage is currently minimal.

## Code Conventions

### Naming
- Packages: lowercase single word (`stripeclient`, `wfirma`)
- Files: kebab-case (`stripe-client.go`, `checkout-params.go`)
- Interfaces: capability-based (`Database`, `InvoiceService`)

### Logging
- Structured logging with `log/slog`
- Use helpers from `lib/sl/`: `sl.Err(err)`, `sl.Secret(key, val)`, `sl.Module(name)`
- Sensitive data automatically redacted in logs

### Error Handling
- Wrap errors with context: `fmt.Errorf("operation: %w", err)`
- HTTP errors return standard JSON response format

### Response Format
```json
{
  "success": true,
  "data": { ... },
  "status_message": "Success",
  "timestamp": "2025-01-01T00:00:00Z"
}
```

### Validation
- Use `go-playground/validator` tags
- Monetary amounts in minor units (cents)
- Supported currencies: PLN, EUR

## Deployment

GitHub Actions CI/CD:
- Push to `master` triggers production deployment
- Service runs via systemd: `wfsync.service`
- Binary deployed to `/usr/local/bin/wfsync`
- Config at `/etc/conf/wfsync.yml`

## Key Files

- `cmd/server/main.go` - Application bootstrap
- `impl/core/core.go` - Main business logic orchestration
- `internal/http-server/api/api.go` - Route definitions
- `entity/checkout-params.go` - Payment/order data structure

## API Documentation

- `docs/api.md` - API overview and common info
- `docs/api-wfirma.md` - Wfirma endpoints documentation
- `docs/api-stripe.md` - Stripe endpoints documentation
- `docs/stripe-hold-capture.md` - Hold-capture flow guide
