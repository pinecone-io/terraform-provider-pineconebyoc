# AGENTS.md

Guidance for AI coding agents working in this repository. For human-facing setup, commands and layout, see [CONTRIBUTING.md](CONTRIBUTING.md) and [README.md](README.md).

## Overview

The Terraform provider for Pinecone BYOC (`pinecone-io/pineconebyoc`), written in Go on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework) with its own small HTTP client. Its resources are `pineconebyoc_environment`, `pineconebyoc_dns_delegation` and `pineconebyoc_amp_access`. The BYOC Terraform module is its only caller.

## Commands

```sh
go build ./...                 # build
go test ./...                  # unit tests, no credentials
TF_ACC=1 go test -race -count=1 ./internal/provider/ -run TestAcc   # acceptance tests against the mock
docker run --rm -v "$PWD":/app -w /app golangci/golangci-lint:v2.12.2 golangci-lint run   # lint, as CI
go generate ./...              # format examples and regenerate docs/
```

Run all of them before you call a change done. CI also runs govulncheck.

### Acceptance tests

The acceptance tests run against `internal/mock`, never against Pinecone, and need no credentials. `hostGuard` in `provider_test.go` fails any test that calls a host other than the mock, and `setup(t)` has the mock stand in for public DNS. Start every new test with `setup(t)`.

## Generated docs

`docs/` is generated from the schema descriptions, `examples/` and `templates/`. After changing any of them, run `go generate ./...` and commit `docs/`. CI fails on drift. `go generate` passes `--provider-name`, so it works from a worktree with another directory name.

## Code conventions

- Match the surrounding code's naming, comment density and idiom. Comment only a non-obvious why.
- Routes, fields, call order and retries follow the Pinecone BYOC registration API. Flag any change to them before you make it.
- Read makes no API call: Pinecone has no read route for these records. Don't add one.
- Mark credentials `Sensitive` in the schema. On Terraform 1.11 and 1.12 a module output's own `sensitive = true` can be lost on the way to the root.
- Use checked type assertions (`v, ok := x.(T)`). `forcetypeassert` is enabled.
- End comments on declarations with a period. `godot` is enabled.
- In tests, use `t.Context()` and `t.Setenv`. `usetesting` is enabled.

## Secrets

The provider handles live credentials: the customer's API key and every key the environment mints.

- An error carries only the server's `message` field, never a response body, which can echo the request.
- The Auth0 token call reports only its route and status. Its request carries the client secret.
- Never log a request, a header or a credential.
- Keep fabricated keys in tests under 20 characters.
- Never commit credentials. `.env` is gitignored for local secrets.

## Compatibility

Customers' state depends on this provider. These changes are breaking. Call them out before making them:

- Removing or renaming an attribute, or changing its type
- Switching an attribute between in-place update and `RequiresReplace`
- Dropping `Sensitive` from an attribute
- Changing how existing state is read, so that a plan shows a diff with no config change

## Git and release workflow

- Don't commit, push or tag unless asked.
- A `vX.Y.Z` tag cuts a release with goreleaser (`.goreleaser.yml`, `.github/workflows/release.yml`).
