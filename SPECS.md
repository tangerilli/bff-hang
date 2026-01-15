# Hang Doodle Specs

## Product summary

Hang Doodle is a minimal Doodle-style polling app for coordinating hangouts. A poll creator selects all available days (no times) and receives a unique, hard-to-guess shareable URL. Friends respond by entering their name and selecting which of the poll’s days they can attend. The UI highlights days that work for everyone who has responded.

## Core user flows

### Create poll

1. User visits the homepage.
2. User enters a poll title and selects all available days from the next 14 days.
3. Server creates a poll and redirects to the poll page.
4. Poll page displays a shareable link for friends.

### Respond to poll

1. User opens a poll URL.
2. User enters their name and selects available days.
3. The poll summary updates (via HTMX) and highlights days that work for all respondents.

## Requirements (implemented)

- No login required; access is controlled by an unguessable poll URL.
- Unique shareable URLs for each poll.
- Day-only availability selection.
- Each respondent enters a name.
- Highlight days that are available for everyone who has responded.
- Backend in Go.
- Frontend in basic HTML with HTMX for partial updates.
- Runnable in AWS Lambda.
- Persistence using DynamoDB (with an in-memory fallback for local development).

## Tech stack

- Go 1.22+ backend.
- HTML + HTMX frontend.
- AWS Lambda custom runtime (`provided.al2023`).
- AWS DynamoDB for persistence.
- Terraform for infra provisioning.

## Application architecture

### Routes

- `GET /` renders the poll creation page.
- `POST /polls` creates a poll and redirects to its URL.
- `GET /poll/{id}` shows the poll details and response form.
- `POST /poll/{id}` records a response and returns updated results (HTMX) or full page.

### Data model

**Poll**

- `id` (random, base32-encoded)
- `title`
- `days` (YYYY-MM-DD strings)
- `created_at`

**Response**

- `id` (random, base32-encoded)
- `name`
- `days` (subset of poll days)
- `created_at`

### Storage

**DynamoDB**

Single-table design using partition key `pk` and sort key `sk`:

- Poll item: `pk = POLL#{id}`, `sk = POLL`, `type = poll`, plus title/days/timestamps.
- Response items: `pk = POLL#{id}`, `sk = RESP#{response_id}`, `type = response`, plus name/days/timestamps.

**Memory**

In-memory maps used when `USE_MEMORY_STORE=true` for local development.

### Availability summarization

For each poll day, responses are aggregated into a list of names. A day is flagged as `all-available` when every response includes that day.

## Frontend behavior

- Home page lists the next 14 days as checkbox options.
- Poll page allows name entry and day selection.
- Results table lists availability by day and highlights rows where everyone is free.
- HTMX updates the results panel without full page reloads.

## Configuration

| Variable | Purpose | Default |
| --- | --- | --- |
| `USE_MEMORY_STORE` | Use in-memory storage for local dev. | `false` |
| `DYNAMODB_TABLE` | DynamoDB table name. | `bff-hang` |
| `APP_BASE_URL` | Public base URL for share links. | derived from request |

## Deployment

### Local development

```bash
USE_MEMORY_STORE=true go run .
```

App runs at <http://localhost:8080>.

### AWS Lambda

- Uses the Lambda HTTP adapter when `AWS_LAMBDA_FUNCTION_NAME` is set.
- Deploy as custom runtime `provided.al2023` with `bootstrap` handler.
- Configure the Lambda with `DYNAMODB_TABLE` and optional `APP_BASE_URL`.

### Terraform

Terraform config provisions:

- DynamoDB table
- IAM role + policies for Lambda logging and DynamoDB access
- Lambda function
- Lambda Function URL (public)

Packaging and deployment steps are documented in `README.md`.

## Known limitations

- No authentication, rate limiting, or validation beyond required fields.
- No time-of-day scheduling (days only).
- No editing or deleting polls or responses.
- No pagination of responses; intended for small groups.

## Future improvements

- Add CSRF protection and spam prevention.
- Allow organizers to add custom date ranges.
- Provide poll closing or locking options.
- Add a lightweight UI to show who selected which days per respondent.
- Add response deletion or editing via unique response links.
