# INO Explore coding instructions

- Read [the project contract](docs/project-contract.md), [architecture](docs/architecture.md),
  existing code and tests before editing. Preserve user changes and prior decisions.
- Complete only the explicitly authorized prompt/phase. Prompt 0 ends at startup
  and operational endpoints. Record completion and leave later decisions open.
- Keep wiring in `cmd/api`; configuration and adapters in `internal/`. Domain
  and use cases must not import Gin, pgx, Redis or transport/persistence packages.
  HTTP translates requests/responses, use cases own business rules, adapters
  own infrastructure. Define interfaces at their consumer when needed.
- Add packages only when they contain real code; avoid speculative abstractions.
  Never commit credentials or local environment files. `.env.example` contains
  local development defaults only.
- Run: `docker compose up --build`. Stop: `docker compose down` (retains data).
- Verify: `go test ./...`, `go vet ./...`, `docker compose config`. For runtime
  checks use a unique Compose project and free API port; see README. Never
  disrupt another project's containers or volumes.
- Before handing off, update phase status and report changed files, decisions,
  checks actually run and their results, and checks that could not run and why.
