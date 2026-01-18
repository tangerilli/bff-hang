# Coding Agent Guide (BFF Hang)

This repository is a small Go web app deployed to AWS Lambda with DynamoDB, Terraform, and HTML templates. These guidelines help keep changes consistent and production-safe.

## Project layout
- `main.go`: Go HTTP server + Lambda adapter + storage logic.
- `templates/`: HTML templates (embedded into the Go binary for Lambda).
- `terraform/`: Infrastructure (DynamoDB, Lambda, API Gateway, custom domain, Route53).
- `Makefile`: Local build + package + deploy helpers.

## Key behaviors to preserve
- Templates are embedded via `//go:embed templates/*.html` for Lambda.
- Local template hot reload: `DEV_RELOAD_TEMPLATES=true` reloads from disk.
- Poll URLs are user-specific: `/poll/{id}/u/{token}` with cookies for auto-redirect.
- Creator-only controls are based on the creator token.

## Dev workflow
- Local dev: `USE_MEMORY_STORE=true go run .`
- Lambda build/package: `make package-lambda`
- Deploy: `make deploy` (uses Terraform).
- For Terraform, ensure `source_code_hash` is updated if lambda package changes (already wired).

## Templates + UI
- Keep HTML in `templates/` only; do not inline HTML in Go.
- Use the same typography/color system in new templates.
- When adding new templates, ensure they’re covered by the embed glob.

## Storage + DynamoDB
- Single-table design with partition key `pk` and sort key `sk`.
- Adding new DynamoDB actions requires updating IAM policy in `terraform/main.tf`.
- Keep storage interface methods mirrored in both DynamoDB and Memory implementations.

## Infrastructure
- Custom domain is managed via API Gateway HTTP API + ACM + Route53.
- If adding new infra, update `README.md`/`SPECS.md` and ensure outputs are meaningful.
- Avoid changing existing resource names without a migration plan.

## Git + commits
- Commit as you go for each distinct feature/bugfix.
- Update `TODOS.md`, `README.md`, and `SPECS.md` when behavior changes.

## Testing / sanity checks
- Run locally for UI changes.
- For Lambda issues, check CloudWatch logs.
- Add new tests to the test suite as appropriate

## Security / access
- `/admin/stats` is currently unauthenticated; flag if adding more admin routes.
- Deletions should confirm in UI (use `confirm()` or a modal).
