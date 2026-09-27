# Development shortcuts. Everything here is a plain `go` command — nothing in
# this repo requires make, and nothing requires AWS credentials.

BINARY := preflight
PKG    := ./cmd/preflight
FIXTURE := internal/plan/testdata/simple.tfplan.json

.PHONY: all
all: fmt vet test race build

.PHONY: build
build:
	go build -o $(BINARY) $(PKG)

# No unit test may touch AWS. The environment is scrubbed so that a stray real
# call fails loudly here rather than quietly succeeding on a developer's laptop
# and then failing in CI — or worse, passing everywhere because it silently used
# someone's credentials.
AWS_SCRUB := AWS_ACCESS_KEY_ID= AWS_SECRET_ACCESS_KEY= AWS_SESSION_TOKEN= \
             AWS_PROFILE= AWS_REGION= AWS_DEFAULT_REGION= \
             AWS_EC2_METADATA_DISABLED=true

.PHONY: test
test:
	$(AWS_SCRUB) go test ./...

.PHONY: race
race:
	$(AWS_SCRUB) go test -race ./...

# Opt-in contract tests against the fixtures in testfixtures/aws. These DO talk
# to AWS. Guarded by both the build tag and PREFLIGHT_CONTRACT_ACCOUNT.
.PHONY: contract
contract:
	@test -n "$(PREFLIGHT_CONTRACT_ACCOUNT)" || \
		{ echo "set PREFLIGHT_CONTRACT_ACCOUNT=<account-id> first"; exit 1; }
	go test -tags awsintegration -v -run TestContract ./internal/simulate/...

# Contract tests are excluded from the default build, so this keeps them
# compiling.
.PHONY: contract-build
contract-build:
	go build -tags awsintegration ./...
	go vet -tags awsintegration ./...

# Mapping derivation. This CREATES REAL, BILLABLE AWS RESOURCES, so it is behind
# its own build tag rather than reusing awsintegration: the contract tests are
# documented as costing zero with no blast radius, and that must stay true for
# anyone running them.
.PHONY: derive
derive:
	@test -n "$(TYPE)" || { echo "usage: make derive TYPE=aws_vpc FIXTURE=./derivefixtures/aws_vpc"; exit 1; }
	@test -n "$(FIXTURE)" || { echo "usage: make derive TYPE=aws_vpc FIXTURE=./derivefixtures/aws_vpc"; exit 1; }
	@test -n "$(PREFLIGHT_DERIVE_ACCOUNT)" || { echo "set PREFLIGHT_DERIVE_ACCOUNT=<scratch-account-id>"; exit 1; }
	PREFLIGHT_DERIVE_CONFIRM=creates-real-resources \
		go run -tags awsderive ./cmd/derive --type $(TYPE) --fixture $(FIXTURE) \
			--operation $(or $(OPERATION),create) \
			$(if $(SUPPORT),--support $(SUPPORT),)

# The tagged code is excluded from every normal build, so without this it would
# rot unnoticed. Mirrors contract-build.
.PHONY: derive-build
derive-build:
	go build -tags awsderive ./...
	go vet -tags awsderive ./...

.PHONY: cover
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: tidy
tidy:
	go mod tidy

# Print current mapping coverage.
.PHONY: mappings
mappings:
	go run $(PKG) mappings list

# Run against the checked-in fixture with a fake principal. No AWS calls.
.PHONY: demo
demo:
	go run $(PKG) check \
		--plan $(FIXTURE) \
		--principal "arn:aws:sts::123456789012:assumed-role/deploy-role/GitHubActions" \
		--region us-east-1

.PHONY: clean
clean:
	rm -f $(BINARY) $(BINARY).exe coverage.out
