# WFSync

WFSync is a Go application designed to synchronize invoices between Stripe and Wfirma services. It provides a seamless integration between payment processing (Stripe) and invoice management (Wfirma).

## Features

- Stripe payment processing integration
- Wfirma invoice management
- Webhook handling for Stripe events
- Invoice download from Wfirma
- Payment operations (hold, capture, cancel)
- Authentication system
- MongoDB storage for data persistence

## Installation

### Prerequisites

- Go 1.24 or higher
- MongoDB (optional, can be enabled in config)
- Stripe account with API keys
- Wfirma account with API credentials

### Steps

1. Clone the repository:
   ```bash
   git clone <repository-url>
   cd wfsync
   ```

2. Install dependencies:
   ```bash
   go mod download
   ```

3. Create a configuration file (see Configuration section)

4. Build the application:
   ```bash
   go build -o wfsync cmd/server/main.go
   ```

5. Run the application:
   ```bash
   ./wfsync -conf config.yml
   ```

## Configuration

The application uses a YAML configuration file. Create a `config.yml` file in the project root with the following structure:

```yaml
env: "development" # or "production"

listen:
  bind_ip: "0.0.0.0"
  port: "8080"

stripe:
  test_mode: true # set to false for production
  api_key: "your_stripe_live_api_key"
  test_key: "your_stripe_test_api_key"
  webhook_secret: "your_stripe_webhook_secret"

wfirma:
  access_key: "your_wfirma_access_key"
  secret_key: "your_wfirma_secret_key"
  app_id: "your_wfirma_app_id"

mongo:
  enabled: true
  host: "localhost"
  port: "27017"
  user: "admin"
  password: "password"
  database: "wfsync"
  save_url: ""

# Optional configurations
opencart:
  enabled: false
  driver: "mysql"
  hostname: "localhost"
  username: "root"
  password: ""
  database: ""
  port: "3306"
  prefix: ""
  file_path: ""

telegram:
  enabled: false
  api_key: ""
```

You can also use environment variables to override these settings.

## API Endpoints

See description for details: [API](/docs/api.md)

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

## Contributing

For any questions or inquiries, please contact developer at [dev@nomadus.net](mailto:dev@nomadus.net).