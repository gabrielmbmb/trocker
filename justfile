set shell := ["bash", "-euo", "pipefail", "-c"]

export GOTOOLCHAIN := `awk '/^go / { print "go" $2 }' go.mod`
golangci_version := "2.15.0"
golangci_installer_sha256 := "1022ddb4d87ed252350ed03fc9677e250a4ae95cc6bcd4658c2a20a8a23d390f"
govulncheck_version := "1.9.0"
exe := if os() == "windows" { ".exe" } else { "" }
lint_dir := ".tools/golangci-lint/" + golangci_version
lint_bin := lint_dir + "/golangci-lint" + exe
vuln_dir := ".tools/govulncheck/" + govulncheck_version
vuln_bin := vuln_dir + "/govulncheck" + exe
binary := "bin/trocker" + exe

default:
    @just --list

# Download pinned development tools into this checkout, not the global PATH.
tools: _install-lint _install-vuln

# Analyze code and formatting without rewriting either.
lint: _install-lint
    "{{lint_bin}}" config verify
    "{{lint_bin}}" run ./...

# Include Docker integration test sources in static analysis.
lint-integration: _install-lint
    "{{lint_bin}}" run --build-tags=integration ./...

# Apply the same formatting rules checked by lint and CI.
fmt: _install-lint
    "{{lint_bin}}" fmt ./...

mod-check:
    go mod tidy -diff

test:
    go test -race -shuffle=on ./...

# Requires the selected local Docker daemon; tests never use existing stacks as fixtures.
integration:
    go test -race -shuffle=on -tags=integration ./...

build:
    go build -trimpath -o "{{binary}}" ./cmd/trocker

[positional-arguments]
run *args:
    go run ./cmd/trocker "$@"

# Keep the network-dependent vulnerability scan separate from the local check.
vuln: _install-vuln
    "{{vuln_bin}}" ./...

bench:
    go test -run '^$' -bench . -benchmem ./...

# Exercise the executable's exit codes and Docker-free informational commands.
smoke: build
    #!/usr/bin/env bash
    set -euo pipefail
    binary="$(pwd)/{{binary}}"
    temporary="$(mktemp -d)"
    trap 'rm -rf "$temporary"' EXIT
    cd "$temporary"
    export DOCKER_HOST="unix://$temporary/no-docker.sock"
    "$binary" --help
    "$binary" version
    if "$binary" unknown >stdout.log 2>stderr.log; then
        echo "Unknown command unexpectedly succeeded" >&2
        exit 1
    else
        code="$?"
    fi
    test "$code" -eq 2
    test ! -s stdout.log
    grep -q 'unknown command "unknown"' stderr.log

check: mod-check lint test smoke

_install-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ -x "{{lint_bin}}" ]]; then exit 0; fi
    temporary="$(mktemp -d)"
    trap 'rm -rf "$temporary"' EXIT
    curl --fail --silent --show-error --location \
        "https://raw.githubusercontent.com/golangci/golangci-lint/v{{golangci_version}}/install.sh" \
        --output "$temporary/install.sh"
    if command -v sha256sum >/dev/null 2>&1; then
        actual="$(sha256sum "$temporary/install.sh" | awk '{print $1}')"
    else
        actual="$(shasum -a 256 "$temporary/install.sh" | awk '{print $1}')"
    fi
    if [[ "$actual" != "{{golangci_installer_sha256}}" ]]; then
        echo "golangci-lint installer checksum mismatch" >&2
        exit 1
    fi
    sh "$temporary/install.sh" -b "{{lint_dir}}" "v{{golangci_version}}"

_install-vuln:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ -x "{{vuln_bin}}" ]]; then exit 0; fi
    mkdir -p "{{vuln_dir}}"
    GOBIN="$(pwd)/{{vuln_dir}}" go install golang.org/x/vuln/cmd/govulncheck@v{{govulncheck_version}}
