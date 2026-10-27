#!/bin/sh
# Plugin startup only: no client settings, shell hooks, or provider credentials.
set -eu
umask 077

questlock_fail() { printf 'Questlock plugin: %s\n' "$*" >&2; exit 1; }
[ "$#" -eq 2 ] || questlock_fail 'usage: run.sh serve|try|doctor|stop codex|claude-code'
questlock_action=$1
questlock_client=$2
case "$questlock_action" in serve|try|doctor|stop) ;; *) questlock_fail 'unsupported action' ;; esac
case "$questlock_client" in codex|claude-code) ;; *) questlock_fail 'unsupported client' ;; esac

questlock_script_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
questlock_plugin_root=$(CDPATH= cd -P "$questlock_script_dir/.." && pwd)
questlock_version=0.15.0
[ ! -L "$questlock_plugin_root/bin" ] && [ ! -L "$questlock_plugin_root/bin/questlock" ] || questlock_fail 'bundled runtime must not be a symlink'
questlock_binary="$questlock_plugin_root/bin/questlock"

case "$(uname -s)" in Darwin) questlock_os=darwin ;; Linux) questlock_os=linux ;; *) questlock_fail 'macOS or Linux is required' ;; esac
case "$(uname -m)" in arm64|aarch64) questlock_arch=arm64 ;; x86_64|amd64) questlock_arch=amd64 ;; *) questlock_fail 'arm64 or amd64 is required' ;; esac

questlock_mode_owner() {
  if [ "$questlock_os" = darwin ]; then stat -f '%Lp %u' "$1"; else stat -c '%a %u' "$1"; fi
}

questlock_private_path() {
  questlock_current=$1
  while :; do
    [ ! -L "$questlock_current" ] || questlock_fail 'private runtime path must not contain symlinks'
    if [ -e "$questlock_current" ]; then
      [ -d "$questlock_current" ] || questlock_fail 'private runtime path requires directories'
      questlock_meta=$(questlock_mode_owner "$questlock_current") || questlock_fail 'cannot inspect private runtime path'
      questlock_mode=${questlock_meta% *}
      questlock_owner=${questlock_meta##* }
      [ "$questlock_owner" = "$(id -u)" ] && [ "$((0$questlock_mode & 022))" -eq 0 ] || questlock_fail 'private runtime path must be owned by you without group/other write access'
    fi
    [ "$questlock_current" != "$HOME" ] || break
    case "$questlock_current" in "$HOME"/*) ;; *) questlock_fail 'runtime cache must remain below HOME' ;; esac
    questlock_current=${questlock_current%/*}
  done
}

if [ ! -e "$questlock_binary" ]; then
  case "${HOME:-}" in /*) ;; *) questlock_fail 'HOME must be an absolute existing directory' ;; esac
  # Do not normalize a supplied home through symlinks or silently choose another profile.
  case "$HOME" in */|*/./*|*/../*|*/.|*/..) questlock_fail 'HOME must be a clean absolute path' ;; esac
  questlock_cache="$HOME/.local/share/questlock/plugins/runtime/$questlock_version/${questlock_os}_${questlock_arch}"
  questlock_private_path "$questlock_cache"
  questlock_binary="$questlock_cache/questlock"
  [ ! -L "$questlock_binary" ] || questlock_fail 'cached runtime must not be a symlink'
  if [ ! -e "$questlock_binary" ]; then
    case "$questlock_action" in doctor|stop) questlock_fail 'runtime is not installed; run the plugin first task to set it up' ;; esac
    mkdir -p "$questlock_cache" || questlock_fail 'cannot create private runtime cache'
    questlock_private_path "$questlock_cache"
    questlock_lock="$questlock_cache/bootstrap.lock"
    questlock_wait=0
    until mkdir "$questlock_lock" 2>/dev/null; do
      [ ! -L "$questlock_lock" ] && [ -d "$questlock_lock" ] || questlock_fail 'invalid bootstrap lock'
      questlock_wait=$((questlock_wait + 1))
      [ "$questlock_wait" -le 30 ] || questlock_fail 'another runtime bootstrap is still active; no lock or runtime was replaced'
      sleep 1
    done
    questlock_stage=
    questlock_cleanup() {
      if [ -n "$questlock_stage" ]; then rm -rf "$questlock_stage"; fi
      rmdir "$questlock_lock" 2>/dev/null || true
    }
    trap questlock_cleanup 0
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    questlock_private_path "$questlock_cache"
    [ ! -L "$questlock_binary" ] || questlock_fail 'runtime destination changed into a symlink'
    if [ ! -e "$questlock_binary" ]; then
      questlock_stage=$(mktemp -d "$questlock_cache/.bootstrap.XXXXXXXX") || questlock_fail 'cannot stage runtime download'
      # Reuse the release installer and its exact checksum/member checks. Its
      # progress goes to stderr, so MCP stdout contains protocol messages only.
      sh "$questlock_script_dir/install.sh" --version "$questlock_version" --dir "$questlock_stage/runtime" >&2
      questlock_staged="$questlock_stage/runtime/questlock"
      [ "$("$questlock_staged" version)" = "$questlock_version" ] || questlock_fail 'downloaded runtime version does not match the plugin'
      questlock_private_path "$questlock_cache"
      [ ! -e "$questlock_binary" ] && [ ! -L "$questlock_binary" ] || questlock_fail 'runtime destination changed; existing file preserved'
      mv "$questlock_staged" "$questlock_binary" || questlock_fail 'cannot publish the verified runtime'
    fi
    questlock_cleanup
    questlock_stage=
    trap - 0 HUP INT TERM
  fi
fi

[ -f "$questlock_binary" ] && [ -x "$questlock_binary" ] && [ ! -L "$questlock_binary" ] || questlock_fail 'runtime must be a regular executable'
questlock_meta=$(questlock_mode_owner "$questlock_binary") || questlock_fail 'cannot inspect runtime permissions'
questlock_mode=${questlock_meta% *}
[ "$((0$questlock_mode & 022))" -eq 0 ] || questlock_fail 'runtime must not allow group/other writes'
[ "$("$questlock_binary" version)" = "$questlock_version" ] || questlock_fail 'runtime version does not match the plugin; existing file preserved'

# Environment overrides are paths, never shell commands or additional options.
set -- plugin "$questlock_action" --client "$questlock_client"
if [ -n "${QUESTLOCK_PLUGIN_STATE_DIR:-}" ]; then set -- "$@" --state-dir "$QUESTLOCK_PLUGIN_STATE_DIR"; fi
if [ -n "${QUESTLOCK_PLUGIN_WORKSPACE:-}" ]; then set -- "$@" --workspace "$QUESTLOCK_PLUGIN_WORKSPACE"; fi
exec "$questlock_binary" "$@"
