# BFF Hang Specs

## Product summary

BFF Hang is a minimal polling app for coordinating hangouts. A poll creator selects all available days (no times), can optionally add venue/activity options, and receives a unique, hard-to-guess shareable URL. Friends respond by entering their name, selecting which poll days they can attend, optionally voting on one or more venue/activity options, and writing in their own venue/activity suggestions. The UI highlights days that work for everyone who has responded and shows venue/activity rankings by votes.

## Core user flows

### Create poll

1. User visits the homepage.
2. User enters a poll title, their name, and selects all available days from the next 14 days, with the option to append more dates in 14-day blocks.
3. User can optionally add venue/activity options (title required, URL/description optional).
4. Server creates a poll and redirects to the poll page.
5. Poll page displays a shareable link with a one-click copy button, redirects the creator to a user-specific URL, and includes the creator in the availability list.

### Respond to poll

1. User opens a poll URL.
2. Server redirects them to a user-specific URL and stores a cookie for future visits. Invalid poll links redirect to the homepage with an error message.
3. User enters their name, selects available days, and can optionally vote on venue/activity options or write in a new suggestion.
4. The poll summary updates (via HTMX), highlights days that work for all respondents, and shows venue/activity rankings.
5. Re-submitting from the same user-specific URL updates the existing response and pre-fills day and venue selections.
6. A write-in suggestion is added to the poll's venue/activity list and automatically counted as a vote from the submitting user.

### Manage poll (creator)

1. Creator visits their user-specific URL.
2. Creator can delete responses from individual users.
3. Creator can update the available dates from a full list of upcoming days and prune existing responses to match.
4. Creator can create or edit the optional venue/activity list; removed options are removed from existing responses.
5. New dates added by the creator are automatically added to the creator's availability.
6. Creator can duplicate a poll into a new creator-owned poll that keeps the same venue/activity options but starts with no dates or responses.

## Requirements (implemented)

- No login required; access is controlled by an unguessable poll URL.
- Unique shareable URLs for each poll.
- Day-only availability selection.
- Optional venue/activity voting with ranked results and responder write-in suggestions.
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
- `GET /admin/stats` shows poll and response counts.

### Data model

**Poll**

- `id` (random, base32-encoded)
- `title`
- `days` (YYYY-MM-DD strings)
- `creator_token` (random, base32-encoded)
- `venues` (optional list of `{id,title,url,description}`)
- `created_at`

**Response**

- `id` (random, base32-encoded)
- `name`
- `days` (subset of poll days)
- `venue_votes` (subset of poll venue IDs)
- `user_token` (random, base32-encoded)
- `created_at`

### Storage

**DynamoDB**

Single-table design using partition key `pk` and sort key `sk`:

- Poll item: `pk = POLL#{id}`, `sk = POLL`, `type = poll`, plus title/days/timestamps.
- Poll item includes optional `venues`.
- Poll item includes `creator_token` for creator-only actions.
- Response items: `pk = POLL#{id}`, `sk = RESP#{response_id}`, `type = response`, plus name/days/venue votes/user token/timestamps.

**Memory**

In-memory maps used when `USE_MEMORY_STORE=true` for local development.

### Availability and venue summarization

For each poll day, responses are aggregated into a list of names. A day is flagged as `all-available` when every response includes that day.
For each venue/activity option, responses are aggregated into vote counts and voter names, then ranked by vote count descending.

## Frontend behavior

- Home page includes a creator name field and lists the next 14 days as checkbox options, with a "More days" button.
- Poll page allows name entry and day selection.
- Poll page optionally includes venue/activity selection.
- Poll page includes a copy button for the share link.
- Poll page redirects visitors to user-specific URLs and stores a cookie to return them to the same link.
- Submitting from the same user-specific URL updates the existing response instead of adding a duplicate.
- Creator-only controls allow deleting responses and editing available dates.
- Creator-only controls allow creating/editing venue/activity options.
- Creator-only controls allow duplicating a poll into a fresh copy with the same venue/activity options and no dates.
- Invalid poll links redirect to the homepage and show an error banner.
- Admin stats page shows total polls and responses (no auth yet).
- Creator edits to add dates automatically mark the creator as available for those dates.
- Results table lists availability by day and highlights rows where everyone is free.
- Results include a ranked venue/activity table with vote counts and voter names.
- Poll response form de-emphasizes days that no longer work for every respondent, while highlighting days that do.
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

For local development, `DEV_RELOAD_TEMPLATES=true` reloads HTML templates on each request (templates are embedded for Lambda deploys).

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
- API Gateway HTTP API with custom domain + ACM certificate
- Route53 DNS records for the custom domain

Packaging and deployment steps are documented in `README.md`.

## Makefile

A Makefile provides quick targets:

- `make build-lambda` builds the bootstrap binary.
- `make package-lambda` builds and zips the Lambda package.
- `make deploy` runs Terraform deploy with the latest package.

## Known limitations

- No authentication, rate limiting, or validation beyond required fields.
- No time-of-day scheduling (days only).
- No editing or deleting polls or responses.
- No pagination of responses; intended for small groups.

## Future improvements

- Add CSRF protection and spam prevention.
- Allow organizers to set custom date ranges beyond 14-day increments.
- Provide poll closing or locking options.
- Add a lightweight UI to show who selected which days per respondent.
- Add response deletion or editing via unique response links.
