# BFF Hang

A lightweight poll app for coordinating hangout days.

Highlights:
- Create a poll from upcoming dates with an option to add more in 14-day blocks.
- Share a unique link and copy it with one click.
- Creator is included in the availability list right away.
- See availability update live with HTMX.

## Requirements

- Go 1.22+

## Run locally

1. Start the server with the in-memory store:

   ```bash
   USE_MEMORY_STORE=true go run .
   ```

   For live template reloads in local dev, add `DEV_RELOAD_TEMPLATES=true`.
   If you want Go hot reload, run a watcher like `air` (if installed).

2. Open the app at <http://localhost:8080>.

## Configuration

| Variable | Purpose | Default |
| --- | --- | --- |
| `USE_MEMORY_STORE` | Use in-memory storage instead of DynamoDB (recommended for local dev). | `false` |
| `DYNAMODB_TABLE` | DynamoDB table name when using DynamoDB storage. | `bff-hang` |
| `APP_BASE_URL` | Public base URL used to render share links. | derived from request |
| `DEV_RELOAD_TEMPLATES` | Reload HTML templates on every request (local dev helper). | `false` |

## AWS Lambda

The app automatically runs as an AWS Lambda handler when `AWS_LAMBDA_FUNCTION_NAME` is set (as it is in Lambda environments). Deploy the compiled binary with an API Gateway or Lambda Function URL.

## Terraform infrastructure

Terraform config lives in `terraform/` and provisions:

- DynamoDB table for poll data
- Lambda function (custom runtime) and IAM role
- Lambda Function URL for public access

### Deploy

1. Build the Lambda binary for Amazon Linux and zip it:

   ```bash
   GOOS=linux GOARCH=arm64 go build -o bootstrap
   zip function.zip bootstrap
   ```

2. Initialize and apply Terraform:

   ```bash
   cd terraform
   terraform init
   terraform apply -var="lambda_package_path=../function.zip" -var="app_base_url=https://your-domain.example"
   ```

3. Use the output `lambda_function_url` as the public endpoint.
