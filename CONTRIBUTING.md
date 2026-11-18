# Contributing

Read [ARCHITECTURE.md](ARCHITECTURE.md) first. It defines the package boundaries and the vocabulary (secret, key, value, map file, output dir, ready marker). The scope is deliberately small, so see "Explicitly not doing" in [DECISIONS.md](DECISIONS.md) before proposing a feature.

## Tool setup

Tool versions live in `.tool-versions` and are installed with [asdf](https://asdf-vm.com) (v0.16 or newer):

```sh
asdf plugin add golang
asdf plugin add golangci-lint
asdf plugin add gofumpt
asdf plugin add actionlint
asdf plugin add goreleaser
asdf plugin add trivy
asdf plugin add sops
asdf plugin add age
asdf plugin add shellcheck
asdf plugin add uv
asdf plugin add nodejs
asdf plugin add syft
asdf install
```

- `zizmor` has no asdf plugin, so `scripts/zizmor.sh` runs it through `uvx` (uv comes from asdf).
- Go dev tools (`staticcheck`, `govulncheck`, `deadcode`) are `tool` directives in `go.mod` and run with `go tool`.
- Install Docker and Docker Compose separately. asdf does not manage them.
- cosign is needed only to verify a release (see [SECURITY.md](SECURITY.md)). The release workflow installs it with `sigstore/cosign-installer`.

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

- Idiomatic Go: Effective Go, Go Code Review Comments, Google Go Style Guide. Standard library first. Every new dependency needs a one-line justification in `DECISIONS.md`.
- Functional core, imperative shell: parsing, validation and planning are pure, and I/O sits at the edges.
- Never log or return secret values, at any level or in any error. Tests assert this with a canary value.
- No dead code, no commented-out code, doc comments on every exported identifier. Comments say what is guaranteed and why, and leave out who calls the code.
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

A change that touches only Markdown, `docs/`, the `*.example` walkthroughs or `LICENSE` skips these jobs, and the skipped jobs count as passed. The `changes` job decides this. If it fails, every job runs.

## License

Contributions are licensed under the [MIT License](LICENSE).
