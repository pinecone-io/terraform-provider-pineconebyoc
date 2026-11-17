# Contributing

## Requirements

- [Go](https://go.dev/doc/install), at the version in `go.mod`
- [Terraform](https://developer.hashicorp.com/terraform/install) 1.10 or later, or [OpenTofu](https://opentofu.org/docs/intro/install/) 1.11 or later
- [Docker](https://docs.docker.com/get-docker/), for the pinned golangci-lint and goreleaser images
- [just](https://github.com/casey/just), optional. Each recipe is one command you can run by hand.

## Building the provider

1.  Clone the repository:

    ```sh
    git clone https://github.com/pinecone-io/terraform-provider-pineconebyoc
    ```

1.  Build the provider:

    ```sh
    go build ./...     # or: just build
    ```

## Testing a local build

`scripts/local-mirror.sh` builds the provider into a filesystem mirror and writes a CLI configuration for it. Roots keep their `source` and `version`, and `terraform init` works as usual:

```sh
scripts/local-mirror.sh            # or: just local-mirror
export TF_CLI_CONFIG_FILE="$PWD/.local-mirror/terraformrc"
```

It builds version 0.1.0 into `.local-mirror/` (`--version` and `--dir` change them), and rebuilds only when the source changes. After a rebuild, delete the root's `.terraform.lock.hcl`: the version is the same but the checksum isn't. OpenTofu reads the same file.

For a quick edit loop, `dev_overrides` skips `terraform init`:

```hcl
provider_installation {
  dev_overrides {
    "pinecone-io/pineconebyoc" = "/path/to/go/bin"
  }
  direct {}
}
```

`go install .` puts the binary in `$(go env GOPATH)/bin`. Point `TF_CLI_CONFIG_FILE` at that file. The provider then reports version `dev`.

## Running tests and lint

Unit tests need no Terraform and no credentials:

```sh
go test ./...      # or: just test
```

CI runs golangci-lint v2.12.2 with `.golangci.yml`. `go vet` alone misses linters such as `godot` and `forcetypeassert`.

```sh
docker run --rm -v "$PWD":/app -w /app golangci/golangci-lint:v2.12.2 golangci-lint run    # or: just lint
```

The acceptance tests run the Terraform CLI against an in-process mock of the Pinecone API. They need no credentials, and any request not bound for the mock fails the test. They run only with `TF_ACC=1`:

```sh
TF_ACC=1 go test -race -count=1 -v ./internal/provider/ -run TestAcc        # or: just testacc
TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(command -v tofu)" \
  TF_ACC_PROVIDER_HOST=registry.opentofu.org \
  go test -race -count=1 -v ./internal/provider/ -run TestAcc                # or: just testacc tofu
```

Narrow with `-run`, for example `-run TestAccEnvironmentLifecycle`.

There are no acceptance tests against the live Pinecone API.

One test reads public DNS. It sends only NS queries, and runs only when `PINECONEBYOC_LIVE_DNS` names a delegated name:

```sh
PINECONEBYOC_LIVE_DNS=<delegated name> go test -count=1 -v ./internal/delegation/ -run TestLive
```

Keep fabricated credentials in tests under 20 characters, so secret scanners can tell them from live keys.

## Generating documentation

tfplugindocs generates `docs/` from the schema descriptions, `examples/` and `templates/`. After changing any of them, regenerate and commit the result:

```sh
go generate ./...  # or: just docs
```

CI fails if `docs/` doesn't match the generated output.

## Repository layout

| Path | Contents |
| --- | --- |
| `main.go` | Entry point, version ldflags, `go:generate` for the docs |
| `internal/provider/` | The provider, one file per resource, schema tests, acceptance tests against the mock |
| `internal/client/` | Calls to the Pinecone API and Auth0, retries, User-Agent |
| `internal/delegation/` | The wait until the parent zone serves a DNS delegation |
| `internal/mock/` | An in-process mock of the Pinecone API and Auth0, for tests only |
| `examples/`, `templates/` | Inputs to the generated docs |
| `docs/` | Generated. Never edited by hand. |
| `scripts/local-mirror.sh` | Local filesystem mirror for roots on this machine |
| `scripts/mirror-from-release.sh` | Filesystem or network mirror filled from a GitHub release |

## Submitting changes

Run `go build ./...`, `go test ./...`, the mock acceptance tests, the lint and `go generate ./...`. CI runs each of them, plus govulncheck.

Commits are [conventional](https://www.conventionalcommits.org/) and cover one concern each.

## Releasing

Maintainers cut a release with a `vX.Y.Z` tag. The release workflow runs goreleaser and publishes the GitHub release:

```sh
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```
