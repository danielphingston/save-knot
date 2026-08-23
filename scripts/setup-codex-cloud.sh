#!/usr/bin/env bash

set -Eeuo pipefail

# Go defaults to the latest official stable release. Set SAVEKNOT_GO_VERSION in
# the Codex environment settings to pin a release while testing an upgrade.
readonly GO_VERSION_OVERRIDE="${SAVEKNOT_GO_VERSION:-}"
readonly GOLANGCI_LINT_VERSION="${SAVEKNOT_GOLANGCI_LINT_VERSION:-v2.12.0}"
readonly AGENT_BROWSER_VERSION="${SAVEKNOT_AGENT_BROWSER_VERSION:-latest}"
readonly USER_BIN_DIR="${HOME}/.local/bin"
readonly TOOLCHAIN_ROOT="${XDG_DATA_HOME:-${HOME}/.local/share}/saveknot/toolchains"
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

REPO_ROOT=''
GO_VERSION=''
GO_INSTALL_DIR=''
GO_DOWNLOADS_FILE=''
SETUP_TEMP_DIR=''

log() {
	printf 'saveknot setup: %s\n' "$*"
}

fail() {
	printf 'saveknot setup: error: %s\n' "$*" >&2
	exit 1
}

cleanup() {
	if [[ "${SETUP_TEMP_DIR}" == /tmp/saveknot-setup.* && -d "${SETUP_TEMP_DIR}" ]]; then
		rm -rf -- "${SETUP_TEMP_DIR}"
	fi
}

install_system_dependencies() {
	local -a packages=()

	command -v curl >/dev/null 2>&1 || packages+=(curl)
	command -v git >/dev/null 2>&1 || packages+=(git)
	command -v make >/dev/null 2>&1 || packages+=(make)
	command -v tar >/dev/null 2>&1 || packages+=(tar)
	command -v sha256sum >/dev/null 2>&1 || packages+=(coreutils)
	command -v awk >/dev/null 2>&1 || packages+=(mawk)
	command -v grep >/dev/null 2>&1 || packages+=(grep)
	command -v python3 >/dev/null 2>&1 || packages+=(python3)
	command -v sed >/dev/null 2>&1 || packages+=(sed)
	command -v cc >/dev/null 2>&1 || packages+=(build-essential)
	[[ -r /etc/ssl/certs/ca-certificates.crt ]] || packages+=(ca-certificates)

	((${#packages[@]} == 0)) && return
	command -v apt-get >/dev/null 2>&1 || fail "missing required commands and apt-get is unavailable: ${packages[*]}"

	log "installing system packages: ${packages[*]}"
	if [[ "$(id -u)" == "0" ]]; then
		DEBIAN_FRONTEND=noninteractive apt-get update
		DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
	elif command -v sudo >/dev/null 2>&1; then
		sudo env DEBIAN_FRONTEND=noninteractive apt-get update
		sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
	else
		fail "system packages are missing and neither root access nor sudo is available"
	fi
}

resolve_repo_root() {
	local candidate
	local git_root=''
	local module_path
	local -a candidates=()

	[[ -n "${SAVEKNOT_REPO_ROOT:-}" ]] && candidates+=("${SAVEKNOT_REPO_ROOT}")
	candidates+=("$(pwd -P)")
	if command -v git >/dev/null 2>&1; then
		git_root="$(git -C "$(pwd -P)" rev-parse --show-toplevel 2>/dev/null || true)"
		[[ -n "${git_root}" ]] && candidates+=("${git_root}")
	fi
	candidates+=("${SCRIPT_DIR}/..")

	for candidate in "${candidates[@]}"; do
		candidate="$(cd -- "${candidate}" 2>/dev/null && pwd -P)" || continue
		[[ -f "${candidate}/go.mod" ]] || continue
		module_path="$(awk '$1 == "module" { print $2; exit }' "${candidate}/go.mod")"
		[[ "${module_path}" == "github.com/saveknot/saveknot" ]] || continue
		REPO_ROOT="${candidate}"
		log "using repository ${REPO_ROOT}"
		return
	done

	fail "cannot locate the SaveKnot checkout; run from its repository or set SAVEKNOT_REPO_ROOT"
}

latest_stable_go_version() {
	python3 - "${GO_DOWNLOADS_FILE}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as releases_file:
    releases = json.load(releases_file)

release = next((item for item in releases if item.get("stable")), None)
if release is None:
    raise SystemExit("official Go metadata contains no stable release")
print(release["version"])
PY
}

official_go_checksum() {
	python3 - "${GO_DOWNLOADS_FILE}" "$1" "$2" <<'PY'
import json
import sys

metadata_path, version, filename = sys.argv[1:]
with open(metadata_path, encoding="utf-8") as releases_file:
    releases = json.load(releases_file)

for release in releases:
    if release.get("version") != version:
        continue
    for archive in release.get("files", []):
        if archive.get("filename") == filename:
            print(archive.get("sha256", ""))
            raise SystemExit(0)
raise SystemExit(f"official Go metadata contains no file named {filename}")
PY
}

resolve_go_version() {
	local downloads_url='https://go.dev/dl/?mode=json'
	local minimum_version
	local released_version

	SETUP_TEMP_DIR="$(mktemp -d /tmp/saveknot-setup.XXXXXXXX)"
	trap cleanup EXIT
	GO_DOWNLOADS_FILE="${SETUP_TEMP_DIR}/go-downloads.json"
	[[ -n "${GO_VERSION_OVERRIDE}" ]] && downloads_url='https://go.dev/dl/?mode=json&include=all'
	curl --fail --location --silent --show-error "${downloads_url}" --output "${GO_DOWNLOADS_FILE}"

	minimum_version="$(awk '$1 == "go" { print $2; exit }' "${REPO_ROOT}/go.mod")"
	[[ "${minimum_version}" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || fail "could not read the minimum Go version from go.mod"

	if [[ -n "${GO_VERSION_OVERRIDE}" ]]; then
		GO_VERSION="${GO_VERSION_OVERRIDE#go}"
	else
		released_version="$(latest_stable_go_version)"
		GO_VERSION="${released_version#go}"
	fi
	[[ "${GO_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "invalid Go release: ${GO_VERSION}"

	if [[ "$(printf '%s\n%s\n' "${minimum_version}" "${GO_VERSION}" | sort -V | sed -n '1p')" != "${minimum_version}" ]]; then
		fail "Go ${GO_VERSION} is older than the go.mod requirement ${minimum_version}"
	fi

	GO_INSTALL_DIR="${TOOLCHAIN_ROOT}/go${GO_VERSION}"
	log "using Go ${GO_VERSION} (go.mod requires ${minimum_version} or newer)"
}

go_archive_arch() {
	case "$(uname -m)" in
	x86_64 | amd64)
		printf 'amd64\n'
		;;
	aarch64 | arm64)
		printf 'arm64\n'
		;;
	*)
		fail "unsupported Linux architecture: $(uname -m)"
		;;
	esac
}

persist_user_path() {
	local goroot_line='unset GOROOT'
	local path_line='export PATH="$HOME/.local/bin:$PATH"'

	mkdir -p "${USER_BIN_DIR}"
	touch "${HOME}/.bashrc"
	if ! grep -Fqx "${path_line}" "${HOME}/.bashrc"; then
		printf '\n# SaveKnot Codex cloud toolchain\n%s\n' "${path_line}" >>"${HOME}/.bashrc"
	fi
	if ! grep -Fqx "${goroot_line}" "${HOME}/.bashrc"; then
		printf '%s\n' "${goroot_line}" >>"${HOME}/.bashrc"
	fi
	export PATH="${USER_BIN_DIR}:${PATH}"
	unset GOROOT
	hash -r
}

install_go() {
	local current_version=''
	local archive_arch
	local archive_name
	local archive_url
	local checksum

	if command -v go >/dev/null 2>&1; then
		current_version="$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null || true)"
	fi
	if [[ "${current_version}" == "go${GO_VERSION}" ]]; then
		log "Go ${GO_VERSION} is already active"
		return
	fi

	if [[ -x "${GO_INSTALL_DIR}/bin/go" ]]; then
		current_version="$(GOTOOLCHAIN=local "${GO_INSTALL_DIR}/bin/go" env GOVERSION)"
		[[ "${current_version}" == "go${GO_VERSION}" ]] || fail "${GO_INSTALL_DIR} contains ${current_version}, expected go${GO_VERSION}"
	else
		[[ "$(uname -s)" == "Linux" ]] || fail "this Codex cloud setup supports Linux only"
		archive_arch="$(go_archive_arch)"
		archive_name="go${GO_VERSION}.linux-${archive_arch}.tar.gz"
		archive_url="https://go.dev/dl/${archive_name}"
		checksum="$(official_go_checksum "go${GO_VERSION}" "${archive_name}")"
		[[ "${checksum}" =~ ^[0-9a-f]{64}$ ]] || fail "no official checksum found for ${archive_name}"

		log "downloading ${archive_name}"
		curl --fail --location --silent --show-error "${archive_url}" --output "${SETUP_TEMP_DIR}/${archive_name}"
		printf '%s  %s\n' "${checksum}" "${SETUP_TEMP_DIR}/${archive_name}" | sha256sum --check --status || fail "Go archive checksum verification failed"

		tar -C "${SETUP_TEMP_DIR}" -xzf "${SETUP_TEMP_DIR}/${archive_name}"
		mkdir -p "${TOOLCHAIN_ROOT}"
		mv "${SETUP_TEMP_DIR}/go" "${GO_INSTALL_DIR}"
	fi

	for command_name in go gofmt; do
		if [[ -e "${USER_BIN_DIR}/${command_name}" && ! -L "${USER_BIN_DIR}/${command_name}" ]]; then
			fail "${USER_BIN_DIR}/${command_name} exists and is not a symlink"
		fi
		ln -sfn "${GO_INSTALL_DIR}/bin/${command_name}" "${USER_BIN_DIR}/${command_name}"
	done

	current_version="$(GOTOOLCHAIN=local go env GOVERSION)"
	[[ "${current_version}" == "go${GO_VERSION}" ]] || fail "Go ${GO_VERSION} was installed but ${current_version} is active"
	log "installed Go ${GO_VERSION}"
}

verify_go_toolchain() {
	local compiler_version
	local go_root

	go_root="$(GOTOOLCHAIN=local go env GOROOT)"
	compiler_version="$(GOTOOLCHAIN=local go tool compile -V=full)"
	[[ "${compiler_version}" == *"go${GO_VERSION}"* ]] || fail "${compiler_version} does not match go${GO_VERSION}; GOROOT=${go_root}"
	log "verified ${compiler_version} from ${go_root}"
}

install_browser_tools() {
	command -v npm >/dev/null 2>&1 || fail "npm is required to install agent-browser"

	log "installing agent-browser ${AGENT_BROWSER_VERSION}"
	npm install --global --no-audit --no-fund "agent-browser@${AGENT_BROWSER_VERSION}"
	agent-browser install --with-deps
	agent-browser skills get core >/dev/null
	log "verified $(agent-browser --version) with browser runtime and core skill"
}

prefetch_project_tools() {
	cd "${REPO_ROOT}"
	log "downloading Go module dependencies"
	GOTOOLCHAIN=local go mod download

	log "installing golangci-lint ${GOLANGCI_LINT_VERSION}"
	GOBIN="${USER_BIN_DIR}" GOTOOLCHAIN=local go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"
}

main() {
	install_system_dependencies
	resolve_repo_root
	resolve_go_version
	persist_user_path
	install_go
	verify_go_toolchain
	install_browser_tools
	prefetch_project_tools

	log "environment ready"
	go version
	golangci-lint version
	git --version
	make --version | sed -n '1p'
	cc --version | sed -n '1p'
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
