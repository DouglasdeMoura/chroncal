#!/bin/sh
set -eu

REPO="${GITHUB_REPOSITORY:-DouglasdeMoura/chroncal}"
BINARY="chroncal"
VERIFY_CHECKSUM="${VERIFY_CHECKSUM:-1}"

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'chroncal install: %s\n' "$*" >&2
  exit 1
}

have() {
  command -v "$1" >/dev/null 2>&1
}

download() {
  url="$1"
  out="$2"

  if have curl; then
    curl -fsSL "$url" -o "$out"
  elif have wget; then
    wget -qO "$out" "$url"
  else
    fail "curl or wget is required"
  fi
}

latest_version() {
  tmp="$1"
  download "https://api.github.com/repos/${REPO}/releases/latest" "$tmp"
  sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp" | sed -n '1p'
}

normalize_version() {
  case "$1" in
    v*) printf '%s\n' "$1" ;;
    *) printf 'v%s\n' "$1" ;;
  esac
}

detect_os() {
  case "$(uname -s)" in
    Linux) printf 'linux\n' ;;
    Darwin) printf 'darwin\n' ;;
    FreeBSD) printf 'freebsd\n' ;;
    OpenBSD) printf 'openbsd\n' ;;
    *) fail "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) printf 'amd64\n' ;;
    aarch64 | arm64) printf 'arm64\n' ;;
    armv6l) printf 'armv6\n' ;;
    armv7l) printf 'armv7\n' ;;
    i386 | i686) printf '386\n' ;;
    riscv64) printf 'riscv64\n' ;;
    s390x) printf 's390x\n' ;;
    ppc64le) printf 'ppc64le\n' ;;
    loongarch64) printf 'loong64\n' ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
}

checksum_file() {
  file="$1"
  checksums="$2"

  expected="$(awk -v file="$file" '$2 == file { print $1 }' "$checksums")"
  [ -n "$expected" ] || fail "missing checksum for ${file}"

  if have sha256sum; then
    printf '%s  %s\n' "$expected" "$file" | sha256sum -c - >/dev/null
  elif have shasum; then
    printf '%s  %s\n' "$expected" "$file" | shasum -a 256 -c - >/dev/null
  else
    fail "sha256sum or shasum is required for checksum verification; set VERIFY_CHECKSUM=0 to skip"
  fi
}

expand_install_dir() {
  case "$1" in
    '~') printf '%s\n' "$HOME" ;;
    '~/'*) printf '%s/%s\n' "$HOME" "${1#~/}" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

install_binary() {
  src="$1"
  dir="$2"
  target="${dir}/${BINARY}"

  mkdir -p "$dir" 2>/dev/null || true

  if [ -w "$dir" ]; then
    install -m 0755 "$src" "$target"
    return
  fi

  if have sudo; then
    sudo mkdir -p "$dir"
    sudo install -m 0755 "$src" "$target"
    return
  fi

  fail "${dir} is not writable and sudo is not available; rerun with INSTALL_DIR=\$HOME/.local/bin"
}

tmpdir="$(mktemp -d 2>/dev/null || mktemp -d -t chroncal)"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

if [ -n "${VERSION:-}" ]; then
  tag="$(normalize_version "$VERSION")"
else
  tag="$(latest_version "${tmpdir}/latest.json")"
  [ -n "$tag" ] || fail "could not determine latest release"
fi

version="${tag#v}"
os="$(detect_os)"
arch="$(detect_arch)"
asset="${BINARY}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${tag}"

if [ -n "${INSTALL_DIR:-}" ]; then
  install_dir="$(expand_install_dir "$INSTALL_DIR")"
elif [ -w /usr/local/bin ]; then
  install_dir="/usr/local/bin"
elif have sudo; then
  install_dir="/usr/local/bin"
else
  install_dir="${HOME}/.local/bin"
fi

log "Installing ${BINARY} ${tag} for ${os}/${arch}..."
download "${base_url}/${asset}" "${tmpdir}/${asset}"

if [ "$VERIFY_CHECKSUM" != "0" ]; then
  download "${base_url}/checksums.txt" "${tmpdir}/checksums.txt"
  (cd "$tmpdir" && checksum_file "$asset" checksums.txt)
fi

tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"
[ -x "${tmpdir}/${BINARY}" ] || fail "archive did not contain executable ${BINARY}"

install_binary "${tmpdir}/${BINARY}" "$install_dir"

log "Installed ${BINARY} to ${install_dir}/${BINARY}"

case ":$PATH:" in
  *":${install_dir}:"*) ;;
  *) log "Add ${install_dir} to PATH if your shell cannot find ${BINARY}." ;;
esac

"${install_dir}/${BINARY}" version || true
