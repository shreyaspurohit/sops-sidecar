# Contributing

Thanks for helping. Please read [ARCHITECTURE.md](ARCHITECTURE.md) first: it defines the package boundaries and the vocabulary (secret, key, value, map file, output dir, ready marker). Scope is deliberately small; see "Explicitly not doing" in [DECISIONS.md](DECISIONS.md) before proposing a feature.

## Tool setup

Tool versions live in `.tool-versions` and are installed with [asdf](https://asdf-vm.com) (v0.16 or newer):

```sh
asdf plugin add golang
asdf plugin add golangci-lint
asdf plugin add gofumpt
asdf plugin add actionlint
asdf plugin add goreleaser
asdf plugin add cosign
asdf plugin add trivy
asdf plugin add sops
asdf plugin add age
asdf plugin add shellcheck
asdf plugin add uv
asdf plugin add nodejs
asdf plugin add syft
asdf install
```

- `zizmor` has no asdf plugin; `scripts/zizmor.sh` runs it through `uvx` (uv comes from asdf).
- Go dev tools (`staticcheck`, `govulncheck`, `deadcode`) are `tool` directives in `go.mod` and run with `go tool`.
- Docker and Docker Compose are not managed by asdf; install them yourself.

## Checks to run before a pull request

```sh
./scripts/coverage.sh                  # unit tests with -race and the coverage gates
go vet ./...
go vet -tags integration ./test/...
golangci-lint run
gofumpt -l .                           # must print nothing
shellcheck scripts/*.sh
go tool staticcheck -tags integration ./...
./scripts/deadcode.sh
go tool govulncheck ./...
actionlint
scripts/zizmor.sh .github/workflows
```

Integration tests (need Docker; they download the pinned Compose binary into `.cache/`):

```sh
docker build -t sops-sidecar:test .
SIDECAR_IMAGE=sops-sidecar:test COMPOSE_BIN="$(scripts/fetch-compose.sh v2.20.0)" \
  go test -tags integration -count=1 -v ./test/integration/...
```

## Standards

- Idiomatic Go: Effective Go, Go Code Review Comments, Google Go Style Guide. Standard library first; every new dependency needs a one-line justification in `DECISIONS.md`.
- Functional core, imperative shell: parsing, validation and planning are pure; I/O sits at the edges.
- Never log or return secret values, in any level or error. Tests assert this with a canary value.
- No dead code, no commented-out code, doc comments on every exported identifier. Comments say what is guaranteed and why, not who calls the code.
- Table-driven tests through the public API. Coverage gates: at least 90% per `internal/` package and 80% overall, enforced on Linux (CI). On other platforms `scripts/coverage.sh` prints the numbers without failing, because Linux-only code such as the tmpfs check cannot run there.
- Every default is documented in the README configuration table.

Use [Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`).

## Required status checks

Branch protection on `main` and Renovate automerge require these CI job names:

- `lint`
- `workflow-lint`
- `test`
- `govulncheck`
- `docker`
- `integration (compose minimum)`
- `integration (compose latest)`

## License

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
