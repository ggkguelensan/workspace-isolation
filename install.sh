#!/bin/sh
# install.sh — installer for the 'wi' CLI (workspace-isolation)
# Repo:   github.com/ggkguelensan/workspace-isolation
# Usage:  curl -fsSL <raw-url>/install.sh | sh
#         WI_INSTALL_DIR=/usr/local/bin sh install.sh
#         WI_DRY_RUN=1 sh install.sh        (or:  sh install.sh --dry-run)
set -eu

# ----------------------------------------------------------------------------
# Configuration (ground truth)
# ----------------------------------------------------------------------------
REPO="ggkguelensan/workspace-isolation"
MODULE="github.com/ggkguelensan/workspace-isolation"
CMD_PKG="github.com/ggkguelensan/workspace-isolation/cmd/wi"
BIN="wi"
MIN_GO_MAJOR=1
MIN_GO_MINOR=26
PREFIX="wi:"

INSTALL_DIR="${WI_INSTALL_DIR:-${HOME}/.local/bin}"
DRY_RUN="${WI_DRY_RUN:-0}"

# go install requires an absolute GOBIN; normalize a relative install dir so the
# prebuilt-mv path and the go-install path behave identically.
case "$INSTALL_DIR" in
  /*) ;;
  *)  INSTALL_DIR="$(pwd)/$INSTALL_DIR" ;;
esac

# Set when 'go' is present but older than the minimum (affects guidance wording).
GO_PRESENT_TOO_OLD=0
GO_FOUND_VERSION=""

# ----------------------------------------------------------------------------
# Messaging helpers (all human output goes to stderr; stdout stays clean)
# ----------------------------------------------------------------------------
info()  { printf '%s %s\n' "$PREFIX" "$*" >&2; }
warn()  { printf '%s warning: %s\n' "$PREFIX" "$*" >&2; }
err()   { printf '%s error: %s\n' "$PREFIX" "$*" >&2; }
step()  { printf '%s would: %s\n' "$PREFIX" "$*" >&2; }
die()   { err "$*"; exit 1; }

# ----------------------------------------------------------------------------
# Arg parsing
# ----------------------------------------------------------------------------
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help)
      cat >&2 <<EOF
${PREFIX} installer for the 'wi' CLI

Options:
  --dry-run            Print the steps that would run; change nothing.
  -h, --help           Show this help.

Environment:
  WI_INSTALL_DIR       Install directory (default: \${HOME}/.local/bin).
  WI_DRY_RUN=1         Same as --dry-run.
EOF
      exit 0
      ;;
    *) die "unknown argument: $arg (try --help)" ;;
  esac
done

if [ "$DRY_RUN" = "1" ]; then
  info "DRY RUN — no files will be downloaded, written, or installed."
fi

# ----------------------------------------------------------------------------
# Temp dir + cleanup trap
# ----------------------------------------------------------------------------
TMPDIR_WI=""
cleanup() {
  if [ -n "$TMPDIR_WI" ] && [ -d "$TMPDIR_WI" ]; then
    rm -rf "$TMPDIR_WI"
  fi
}
# In POSIX sh a signal handler resumes the script unless it exits; exit
# explicitly so Ctrl-C actually aborts instead of falling through to a fallback.
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

ensure_tmpdir() {
  if [ -z "$TMPDIR_WI" ]; then
    TMPDIR_WI="$(mktemp -d 2>/dev/null || mktemp -d -t wi-install)" \
      || die "failed to create a temporary directory"
  fi
}

# ----------------------------------------------------------------------------
# Tool detection + download abstraction (curl OR wget)
# ----------------------------------------------------------------------------
have() { command -v "$1" >/dev/null 2>&1; }

DL=""
if have curl; then
  DL="curl"
elif have wget; then
  DL="wget"
fi

# download <url> <dest-file>
# HTTPS is pinned on the initial request and on every redirect hop so a
# downgrade-to-http redirect is refused rather than fetched in cleartext.
download() {
  _url="$1"; _dest="$2"
  case "$DL" in
    curl) curl -fsSL --proto '=https' --proto-redir '=https' \
            --connect-timeout 5 --max-time 60 "$_url" -o "$_dest" ;;
    wget) wget -q --https-only --timeout=30 "$_url" -O "$_dest" ;;
    *)    die "need 'curl' or 'wget' to download files" ;;
  esac
}

# resolve_redirect <url> -> prints the final effective URL after redirects
# Used to discover the latest release tag without jq or an API token.
resolve_redirect() {
  _url="$1"
  case "$DL" in
    curl)
      curl -fsSLI --proto '=https' --proto-redir '=https' \
        --connect-timeout 5 --max-time 30 \
        -o /dev/null -w '%{url_effective}' "$_url" 2>/dev/null
      ;;
    wget)
      # Follow redirects with --spider; take the last Location header. HTTP
      # headers are CRLF-terminated, so strip the trailing CR from $2.
      _loc="$(wget -S --spider --https-only --timeout=15 "$_url" 2>&1 \
        | awk 'tolower($1) ~ /^location:/ { gsub(/\r/,"",$2); print $2 }' \
        | tail -n 1)"
      if [ -n "$_loc" ]; then
        printf '%s' "$_loc"
      else
        printf '%s' "$_url"
      fi
      ;;
    *)
      printf '%s' "$_url"
      ;;
  esac
}

# ----------------------------------------------------------------------------
# OS / ARCH detection
# ----------------------------------------------------------------------------
detect_os() {
  _u="$(uname -s 2>/dev/null || echo unknown)"
  case "$_u" in
    Darwin) echo darwin ;;
    Linux)  echo linux ;;
    *)
      die "unsupported operating system: ${_u}. Windows users: use WSL, or install with 'go install ${CMD_PKG}@latest'."
      ;;
  esac
}

detect_arch() {
  _m="$(uname -m 2>/dev/null || echo unknown)"
  case "$_m" in
    x86_64|amd64)   echo amd64 ;;
    arm64|aarch64)  echo arm64 ;;
    *)
      die "unsupported architecture: ${_m}. Supported: amd64, arm64."
      ;;
  esac
}

OS="$(detect_os)" || exit 1
ARCH="$(detect_arch)" || exit 1
info "detected platform: ${OS}/${ARCH}"

# ----------------------------------------------------------------------------
# Install dir
# ----------------------------------------------------------------------------
ensure_install_dir() {
  if [ "$DRY_RUN" = "1" ]; then
    step "mkdir -p ${INSTALL_DIR}"
    return 0
  fi
  mkdir -p "$INSTALL_DIR" || die "could not create install dir: ${INSTALL_DIR}"
}

# ----------------------------------------------------------------------------
# PATH advice
# ----------------------------------------------------------------------------
on_path() {
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) return 0 ;;
    *) return 1 ;;
  esac
}

shell_rc() {
  _sh="$(basename "${SHELL:-sh}")"
  case "$_sh" in
    zsh)  echo "${HOME}/.zshrc" ;;
    bash)
      if [ "$OS" = "darwin" ]; then
        echo "${HOME}/.bash_profile"
      else
        echo "${HOME}/.bashrc"
      fi
      ;;
    fish) echo "${HOME}/.config/fish/config.fish" ;;
    *)    echo "" ;;
  esac
}

print_path_advice() {
  if on_path; then
    return 0
  fi
  _rc="$(shell_rc)"
  info ""
  info "${INSTALL_DIR} is not on your PATH."
  _sh="$(basename "${SHELL:-sh}")"
  if [ "$_sh" = "fish" ]; then
    info "Add it for the fish shell with:"
    info "    fish_add_path ${INSTALL_DIR}"
  else
    info "Add it by running:"
    info "    export PATH=\"${INSTALL_DIR}:\$PATH\""
    if [ -n "$_rc" ]; then
      info "and append that same line to ${_rc} to make it permanent."
    else
      info "and add that same line to your shell's startup file to make it permanent."
    fi
  fi
}

# ----------------------------------------------------------------------------
# sha256 verification
# ----------------------------------------------------------------------------
# Returns 2 when no hashing tool exists, 1 when the tool ran but failed, and
# prints the hex digest on success. Capture the full output first so a tool
# failure surfaces directly instead of being masked by the awk in a pipeline.
sha256_of() {
  _f="$1"
  if have shasum; then
    _out="$(shasum -a 256 "$_f")" || return 1
  elif have sha256sum; then
    _out="$(sha256sum "$_f")" || return 1
  else
    return 2
  fi
  printf '%s\n' "$_out" | awk '{print $1}'
}

# verify_checksum <file> <checksums.txt> <asset-name>
# ABORTS if no checksum tool is available or if the sum does not match — an
# unverified binary is never installed.
verify_checksum() {
  _file="$1"; _sums="$2"; _name="$3"
  _actual="$(sha256_of "$_file")" || \
    die "cannot verify download: no 'shasum'/'sha256sum' or hashing failed. Refusing to install unverified binary."
  if [ -z "$_actual" ]; then
    die "cannot verify download: empty checksum computed. Refusing to install unverified binary."
  fi
  _expected="$(awk -v n="$_name" '
    { f=$2; sub(/^\*/,"",f); if (f==n) { print $1; exit } }
  ' "$_sums")"
  if [ -z "$_expected" ]; then
    die "checksum for ${_name} not found in checksums.txt; refusing to install."
  fi
  if [ "$_actual" != "$_expected" ]; then
    err "checksum mismatch for ${_name}:"
    err "  expected: ${_expected}"
    err "  actual:   ${_actual}"
    die "refusing to install a binary that failed verification."
  fi
  info "checksum verified (sha256)."
}

# ----------------------------------------------------------------------------
# Method 1: prebuilt binary from the latest GitHub release
# ----------------------------------------------------------------------------
LATEST_URL="https://github.com/${REPO}/releases/latest"

# Prints the tag on stdout if a release exists; empty string otherwise.
# The tag is parsed from an untrusted redirect URL, so it is whitelisted to a
# version shape before it is allowed to flow into download URLs/filenames.
resolve_latest_tag() {
  [ -n "$DL" ] || return 0
  _eff="$(resolve_redirect "$LATEST_URL" 2>/dev/null || true)"
  case "$_eff" in
    */releases/tag/*)
      _t="${_eff##*/releases/tag/}"
      _t="${_t%%/*}"          # drop any trailing path/query
      case "$_t" in
        v[0-9]*|[0-9]*) printf '%s' "$_t" ;;
        *)              printf '%s' "" ;;
      esac
      ;;
    *)
      printf '%s' ""
      ;;
  esac
}

try_prebuilt() {
  if [ -z "$DL" ]; then
    info "no curl/wget available; skipping prebuilt-binary method."
    return 1
  fi

  info "checking for a published release..."
  _tag="$(resolve_latest_tag)"
  if [ -z "$_tag" ]; then
    info "no published GitHub release found yet for ${REPO}."
    return 1
  fi

  _version="${_tag#v}"
  _asset="${BIN}_${_version}_${OS}_${ARCH}.tar.gz"
  _base="https://github.com/${REPO}/releases/download/${_tag}"
  _tar_url="${_base}/${_asset}"
  _sum_url="${_base}/checksums.txt"

  if [ "$DRY_RUN" = "1" ]; then
    step "use method: prebuilt binary (release ${_tag})"
    step "download asset: ${_tar_url}"
    step "download checksums: ${_sum_url}"
    step "verify sha256 of ${_asset} against checksums.txt"
    step "extract '${BIN}' into ${INSTALL_DIR} and chmod +x"
    return 0
  fi

  ensure_tmpdir
  info "downloading ${_asset} (release ${_tag})..."
  if ! download "$_tar_url" "${TMPDIR_WI}/${_asset}"; then
    warn "could not download prebuilt asset for ${OS}/${ARCH} (${_asset})."
    return 1
  fi
  if ! download "$_sum_url" "${TMPDIR_WI}/checksums.txt"; then
    warn "could not download checksums.txt; refusing to install unverified."
    return 1
  fi

  verify_checksum "${TMPDIR_WI}/${_asset}" "${TMPDIR_WI}/checksums.txt" "$_asset"

  have tar || die "'tar' is required to extract the release archive."

  ensure_install_dir
  info "extracting '${BIN}'..."
  tar -xzf "${TMPDIR_WI}/${_asset}" -C "$TMPDIR_WI" "$BIN" \
    || die "failed to extract '${BIN}' from ${_asset}."
  [ -f "${TMPDIR_WI}/${BIN}" ] || die "'${BIN}' binary not found inside ${_asset}."

  mv -f "${TMPDIR_WI}/${BIN}" "${INSTALL_DIR}/${BIN}" \
    || die "failed to move '${BIN}' into ${INSTALL_DIR}."
  chmod +x "${INSTALL_DIR}/${BIN}" || die "failed to chmod +x ${INSTALL_DIR}/${BIN}."

  info "installed prebuilt binary to ${INSTALL_DIR}/${BIN}"
  return 0
}

# ----------------------------------------------------------------------------
# Method 2: go install
# ----------------------------------------------------------------------------
go_version_ok() {
  have go || return 1
  _v="$(go version 2>/dev/null | awk '{print $3}')"   # e.g. go1.26.2
  _v="${_v#go}"                                        # 1.26.2
  _major="${_v%%.*}"
  _rest="${_v#*.}"
  _minor="${_rest%%.*}"
  # Trim a trailing non-digit suffix so prereleases (1.26rc1) parse as 26.
  _major="${_major%%[!0-9]*}"
  _minor="${_minor%%[!0-9]*}"
  case "$_major" in ''|*[!0-9]*) return 1 ;; esac
  case "$_minor" in ''|*[!0-9]*) return 1 ;; esac
  if [ "$_major" -gt "$MIN_GO_MAJOR" ]; then
    return 0
  fi
  if [ "$_major" -eq "$MIN_GO_MAJOR" ] && [ "$_minor" -ge "$MIN_GO_MINOR" ]; then
    return 0
  fi
  return 1
}

try_go_install() {
  if ! have go; then
    info "'go' not found on PATH; skipping go install method."
    return 1
  fi
  if ! go_version_ok; then
    GO_FOUND_VERSION="$(go version 2>/dev/null | awk '{print $3}' || echo unknown)"
    GO_PRESENT_TOO_OLD=1
    info "found ${GO_FOUND_VERSION}, but Go >= ${MIN_GO_MAJOR}.${MIN_GO_MINOR} is required; skipping go install."
    return 1
  fi

  if [ "$DRY_RUN" = "1" ]; then
    step "use method: go install"
    step "GOBIN=\"${INSTALL_DIR}\" go install ${CMD_PKG}@latest"
    return 0
  fi

  ensure_install_dir
  info "building with: GOBIN=\"${INSTALL_DIR}\" go install ${CMD_PKG}@latest"
  if GOBIN="${INSTALL_DIR}" go install "${CMD_PKG}@latest"; then
    info "installed via go install to ${INSTALL_DIR}/${BIN}"
    return 0
  fi
  warn "go install failed."
  return 1
}

# ----------------------------------------------------------------------------
# Method 3: guidance (and exit 1)
# ----------------------------------------------------------------------------
print_guidance() {
  err ""
  err "Could not install '${BIN}' automatically."
  err ""
  err "  * No prebuilt release is published yet for ${REPO}, and"
  if [ "$GO_PRESENT_TOO_OLD" = "1" ]; then
    err "  * the Go on your PATH (${GO_FOUND_VERSION}) is older than ${MIN_GO_MAJOR}.${MIN_GO_MINOR}."
    err ""
    err "To upgrade Go:"
  else
    err "  * Go >= ${MIN_GO_MAJOR}.${MIN_GO_MINOR} was not found on your PATH."
    err ""
    err "To install Go:"
  fi
  if [ "$OS" = "darwin" ]; then
    err "    brew install go            # macOS (Homebrew)"
  fi
  err "    https://go.dev/dl/         # any OS — download the installer"
  err ""
  err "Then re-run this installer, or install directly:"
  err "    GOBIN=\"${INSTALL_DIR}\" go install ${CMD_PKG}@latest"
  err ""
  err "Or build from a clone:"
  err "    git clone https://github.com/${REPO}.git"
  err "    cd workspace-isolation"
  err "    GOBIN=\"${INSTALL_DIR}\" go install ./cmd/wi"
}

# ----------------------------------------------------------------------------
# Success summary
# ----------------------------------------------------------------------------
finish_success() {
  print_path_advice
  info ""
  if [ "$DRY_RUN" = "1" ]; then
    info "DRY RUN complete — nothing was changed."
    info "Would install to: ${INSTALL_DIR}/${BIN}"
  else
    info "Done. '${BIN}' is installed at: ${INSTALL_DIR}/${BIN}"
  fi
  if on_path; then
    info "Verify with:  ${BIN} help"
  else
    info "Verify with:  ${INSTALL_DIR}/${BIN} help   (or add it to PATH, then: ${BIN} help)"
  fi
}

# ----------------------------------------------------------------------------
# Main: try each method in order, stop at first success
# ----------------------------------------------------------------------------
main() {
  if try_prebuilt; then
    finish_success
    exit 0
  fi

  info "falling back to building from source via 'go install'..."
  if try_go_install; then
    finish_success
    exit 0
  fi

  print_guidance
  exit 1
}

main
