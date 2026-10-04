#!/bin/sh
# tokenmaxr installer for macOS and Linux (and Git Bash on Windows).
#   curl -fsSL https://github.com/7-of-9/tokenmaxr/releases/latest/download/install.sh | sh
# Flags (--endpoint URL, --label NAME, --no-prompts, --no-app, ...) go to "tokenmaxr install".
# Downloads the release listed in latest.json (the manifest the app's signed self-update
# reads), checks SHA-256, then runs "tokenmaxr install", which copies itself into the
# state directory's bin/, sets up autostart and starts the app (menu bar on macOS; on
# Linux, headless: a tick every minute). Publish with "tokenmaxr github login".
set -eu

die() {
  printf 'tokenmaxr: %s\n' "$*" >&2
  exit 1
}

# field KEY NAME prints files.KEY.NAME from latest.json, which is written one
# key per line in a fixed layout, so sed is enough (no jq on stock macOS).
field() {
  printf '%s\n' "$manifest" | sed -n "/^    \"$1\": {/,/^    }/p" | sed -n "s/^ *\"$2\": \"*\([^\",]*\).*/\1/p"
}

sha256() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1"; else sha256sum "$1"; fi | awk '{print $1}'
}

main() {
  base=${TOKENMAXR_RELEASE:-https://github.com/7-of-9/tokenmaxr/releases/latest/download}
  base=${base%/}

  os=$(uname -s)
  extra=
  case $os in
    Darwin)
      arch=$(uname -m)
      # A Rosetta shell reports x86_64 on Apple silicon.
      if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then arch=arm64; fi
      case $arch in
        arm64) keys=darwin-arm64 ;;
        x86_64) keys=darwin-amd64 ;;
        *) die "unsupported Mac CPU: $arch" ;;
      esac
      exe=tokenmaxr
      ;;
    Linux)
      case $(uname -m) in
        x86_64 | amd64) keys=linux-amd64 ;;
        aarch64 | arm64) keys=linux-arm64 ;;
        *) die "unsupported Linux CPU: $(uname -m)" ;;
      esac
      exe=tokenmaxr
      # No tray or autostart on Linux: cron runs a tick every minute (below).
      extra="--no-app --no-autostart"
      ;;
    MINGW* | MSYS* | CYGWIN*)
      cpu=${PROCESSOR_ARCHITEW6432:-${PROCESSOR_ARCHITECTURE:-}}
      case ${PROCESSOR_IDENTIFIER:-} in ARM*) cpu=ARM64 ;; esac
      case $cpu in
        AMD64) keys="windows-amd64 windows-amd64-w" ;;
        ARM64) keys="windows-arm64 windows-arm64-w" ;;
        *) die "unsupported Windows CPU: $cpu (x64 or ARM64 only)" ;;
      esac
      exe=tokenmaxr.exe
      ;;
    *) die "unsupported OS: $os" ;;
  esac

  manifest=$(curl -fsSL --retry 3 "$base/latest.json") || die "could not fetch $base/latest.json"
  version=$(printf '%s\n' "$manifest" | sed -n 's/^  "version": "\([^"]*\)".*/\1/p')
  [ -n "$version" ] || die "$base/latest.json is not a tokenmaxr release manifest"
  echo "Installing tokenmaxr $version ($keys)"

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/tokenmaxr.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 130' INT TERM
  for key in $keys; do
    url=$(field "$key" url)
    want=$(field "$key" sha256)
    if [ -z "$url" ] || [ -z "$want" ]; then die "latest.json has no '$key' build"; fi
    case $key in
      *-w) file=$tmp/tokenmaxrw.exe ;;
      windows-*) file=$tmp/tokenmaxr.exe ;;
      *) file=$tmp/tokenmaxr ;;
    esac
    curl -fsSL --retry 3 -o "$file" "$url" || die "download failed: $url"
    got=$(sha256 "$file")
    [ "$got" = "$want" ] || die "SHA-256 mismatch for $url; not installing"
    chmod +x "$file"
    if [ "$os" = Darwin ]; then xattr -d com.apple.quarantine "$file" 2>/dev/null || true; fi
  done

  set -- install ${extra} "$@"
  # Our stdin is this script when piped from curl; give install the terminal instead.
  status=0
  if (: </dev/tty) 2>/dev/null; then
    "$tmp/$exe" "$@" </dev/tty || status=$?
  else
    "$tmp/$exe" "$@" </dev/null || status=$?
  fi
  [ "$status" -eq 0 ] || die "install exited with code $status"
  if [ "$os" = Linux ]; then
    state=${TOKENMAXR_HOME:-${XDG_STATE_HOME:-$HOME/.local/state}/tokenmaxr}
    echo "Run it every minute from cron (crontab -e):"
    echo "  * * * * * \"$state/bin/tokenmaxr\" run >/dev/null 2>&1"
  fi
}

main "$@"
