#!/bin/sh
# Install a pinned public release without root privileges or executing downloads.
set -eu

questlock_script_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
questlock_repo_dir=$(CDPATH= cd -P "$questlock_script_dir/.." && pwd)
questlock_install_dir="$questlock_repo_dir/bin"
questlock_version=0.1.1
if [ -r "$questlock_repo_dir/VERSION" ]; then
  questlock_version=$(cat "$questlock_repo_dir/VERSION")
fi

questlock_die() {
  printf 'Questlock installer: %s\n' "$*" >&2
  exit 1
}

questlock_usage() {
  cat <<'USAGE'
Usage: sh scripts/install.sh [--version vX.Y.Z] [--dir DIRECTORY]

Install the pinned release from VERSION (default 0.1.1) into this repo's bin/.
Supported platforms: macOS and Linux, amd64 and arm64. No sudo is used.
An optional destination directory may contain spaces; quote it in your shell.

Downloads use HTTPS from github.com/spfuzzylink/questlock. The installer checks
the release's SHA256 manifest before extracting the binary. Checksums verify
download integrity; they are not a signed statement of provenance.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || questlock_die '--version requires vX.Y.Z'
      questlock_version=$2
      shift 2
      ;;
    --dir)
      [ "$#" -ge 2 ] && [ -n "$2" ] || questlock_die '--dir requires a directory'
      questlock_install_dir=$2
      shift 2
      ;;
    -h|--help)
      questlock_usage
      exit 0
      ;;
    *) questlock_die "unknown argument: $1 (use --help)" ;;
  esac
done

questlock_version=${questlock_version#v}
case "$questlock_version" in
  ''|*[!0-9.]*) questlock_die 'version must have the form vX.Y.Z (for example v0.1.0)' ;;
esac
if ! printf '%s\n' "$questlock_version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  questlock_die 'version must have the form vX.Y.Z (for example v0.1.0)'
fi

case "$(uname -s)" in
  Darwin) questlock_os=darwin ;;
  Linux) questlock_os=linux ;;
  *) questlock_die 'unsupported operating system; release binaries support macOS and Linux' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) questlock_arch=amd64 ;;
  arm64|aarch64) questlock_arch=arm64 ;;
  *) questlock_die 'unsupported architecture; release binaries support amd64 and arm64' ;;
esac

command -v curl >/dev/null 2>&1 || questlock_die 'curl is required'
command -v tar >/dev/null 2>&1 || questlock_die 'tar is required'
if command -v sha256sum >/dev/null 2>&1; then
  questlock_hash_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  questlock_hash_tool=shasum
else
  questlock_die 'SHA256 verification requires sha256sum or shasum'
fi

# Make relative destinations absolute so utility arguments cannot become options.
case "$questlock_install_dir" in
  /*) ;;
  *) questlock_install_dir="$(pwd)/$questlock_install_dir" ;;
esac
# A trailing slash or dot must not hide a final symlink from test -L.
while [ "$questlock_install_dir" != / ]; do
  case "$questlock_install_dir" in
    */) questlock_install_dir=${questlock_install_dir%/} ;;
    */.) questlock_install_dir=${questlock_install_dir%/.} ;;
    *) break ;;
  esac
done
questlock_destination="$questlock_install_dir/questlock"
[ ! -L "$questlock_install_dir" ] || questlock_die 'refusing a symlink destination directory'
[ ! -L "$questlock_destination" ] || questlock_die 'refusing to replace a symlink binary'
if [ -e "$questlock_destination" ] && [ ! -f "$questlock_destination" ]; then
  questlock_die 'the destination binary exists and is not a regular file'
fi

umask 077
mkdir -p "$questlock_install_dir" || questlock_die 'cannot create the destination directory'
questlock_temp=$(mktemp -d "$questlock_install_dir/.questlock-install.XXXXXXXX") || questlock_die 'cannot create a staging directory'
trap 'rm -rf "$questlock_temp"' 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

questlock_asset="questlock_${questlock_version}_${questlock_os}_${questlock_arch}.tar.gz"
questlock_base_url="https://github.com/spfuzzylink/questlock/releases/download/v${questlock_version}"
questlock_archive="$questlock_temp/$questlock_asset"
questlock_manifest="$questlock_temp/checksums.txt"

questlock_download() {
  # --disable must be first: ignore ~/.curlrc and its authentication/settings.
  curl --disable --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 120 \
    --output "$2" "$1"
}

printf 'Downloading Questlock v%s for %s/%s...\n' "$questlock_version" "$questlock_os" "$questlock_arch"
questlock_download "$questlock_base_url/checksums.txt" "$questlock_manifest" || questlock_die 'checksum manifest download failed; the existing binary was preserved'
questlock_download "$questlock_base_url/$questlock_asset" "$questlock_archive" || questlock_die 'release download failed; the existing binary was preserved'

# Accept exactly one entry for this exact asset name, never a substring match.
questlock_expected=$(LC_ALL=C awk -v asset="$questlock_asset" '
  $2 == asset || $2 == "*" asset {
    count++
    if (NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-fA-F]+$/) bad=1
    digest=tolower($1)
  }
  END { if (count != 1 || bad) exit 1; print digest }
' "$questlock_manifest") || questlock_die 'the manifest must contain one valid SHA256 entry for the exact release asset'

if [ "$questlock_hash_tool" = sha256sum ]; then
  questlock_hash_output=$(sha256sum "$questlock_archive") || questlock_die 'SHA256 calculation failed'
else
  questlock_hash_output=$(shasum -a 256 "$questlock_archive") || questlock_die 'SHA256 calculation failed'
fi
questlock_actual=${questlock_hash_output%% *}
[ "$questlock_actual" = "$questlock_expected" ] || questlock_die 'SHA256 mismatch; the existing binary was preserved'

# Inspect only the expected member. Reject duplicate names, directories, and
# links. Extract to stdout so no archive paths can be written to the filesystem.
# GNU tar accepts environment options, including command execution hooks.
unset TAR_OPTIONS GZIP
questlock_members=$(tar -tzf "$questlock_archive" questlock) || questlock_die 'release archive does not contain the expected questlock binary'
[ "$questlock_members" = questlock ] || questlock_die 'release archive must contain exactly one top-level questlock binary'
questlock_member_details=$(tar -tvzf "$questlock_archive" questlock) || questlock_die 'cannot inspect the release binary'
case "$questlock_member_details" in
  -*) ;;
  *) questlock_die 'release binary must be a regular file, not a directory or link' ;;
esac
questlock_staged_binary="$questlock_temp/questlock"
tar -xOzf "$questlock_archive" questlock > "$questlock_staged_binary" || questlock_die 'release extraction failed; the existing binary was preserved'
[ -s "$questlock_staged_binary" ] || questlock_die 'release binary is empty'
chmod 0755 "$questlock_staged_binary" || questlock_die 'cannot make the staged binary executable'

# Staging is on the destination filesystem, so the final rename is atomic.
[ ! -L "$questlock_install_dir" ] && [ ! -L "$questlock_destination" ] || questlock_die 'destination changed into a symlink during installation'
if [ -e "$questlock_destination" ] && [ ! -f "$questlock_destination" ]; then
  questlock_die 'destination changed into a non-regular file during installation'
fi
mv -f "$questlock_staged_binary" "$questlock_destination" || questlock_die 'cannot replace the destination binary'
printf 'Installed Questlock v%s at %s\n' "$questlock_version" "$questlock_destination"
