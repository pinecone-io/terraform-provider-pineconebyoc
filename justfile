# Build the provider binary
build:
    go build -v .

# Run unit tests, no Terraform or credentials needed
test:
    go test ./...

# Run acceptance tests against the in-process mock, with CLI=terraform or CLI=tofu from PATH
testacc CLI="terraform" TESTARGS="":
    TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(command -v {{CLI}})" {{ if CLI == "tofu" { "TF_ACC_PROVIDER_HOST=registry.opentofu.org" } else { "" } }} go test -race -count=1 -timeout 15m -v ./internal/provider/ -run TestAcc {{TESTARGS}}

# Lint with the golangci-lint version CI runs
lint:
    docker run --rm -v "{{justfile_directory()}}":/app -w /app golangci/golangci-lint:v2.12.2 golangci-lint run

# Format examples and regenerate docs/
docs:
    go generate ./...

# Build every release artifact into dist/, unsigned and unpublished
snapshot:
    docker run --rm -v "{{justfile_directory()}}":/src -w /src goreleaser/goreleaser:v2.20.0 release --snapshot --clean --skip=sign

# Build this machine's provider into a filesystem mirror, see scripts/local-mirror.sh --help
local-mirror *ARGS:
    scripts/local-mirror.sh {{ARGS}}

# Mirror a GitHub release for Terraform and OpenTofu, see scripts/mirror-from-release.sh --help
mirror-from-release *ARGS:
    scripts/mirror-from-release.sh {{ARGS}}

# Format all Go source files
fmt:
    @gofmt -w $(find . -name "*.go" -not -path "*/vendor/*")
