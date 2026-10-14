#!/bin/sh
set -eu

questlock_bin=${QUESTLOCK_BIN:-"$HOME/.local/bin/questlock"}
if [ ! -x "$questlock_bin" ]; then
  echo 'Questlock executable is unavailable; run questlock model install first or set QUESTLOCK_BIN.' >&2
  exit 1
fi
if [ "$#" -eq 0 ]; then
  echo 'Usage: sh scripts/run.sh plan|run|quest|worker-run|worker-serve [flags]' >&2
  exit 2
fi
questlock_command=$1
shift
case "$questlock_command" in
  plan|quest) ;;
  run|worker-run|worker-serve)
    if [ -z "${QUESTLOCK_TOKEN:-}" ]; then
      questlock_token_file=${QUESTLOCK_TOKEN_FILE:-"$HOME/.local/share/questlock/agent-workflows/worker.token"}
      if [ ! -r "$questlock_token_file" ]; then
        echo 'Broker token is unavailable; set QUESTLOCK_TOKEN or configure QUESTLOCK_TOKEN_FILE.' >&2
        exit 1
      fi
      QUESTLOCK_TOKEN=
      IFS= read -r QUESTLOCK_TOKEN < "$questlock_token_file" || [ -n "$QUESTLOCK_TOKEN" ]
      if [ -z "$QUESTLOCK_TOKEN" ]; then
        echo 'Broker token file is empty.' >&2
        exit 1
      fi
      export QUESTLOCK_TOKEN
    fi
    ;;
  *)
    echo 'Usage: sh scripts/run.sh plan|run|quest|worker-run|worker-serve [flags]' >&2
    exit 2
    ;;
esac
case "$questlock_command" in
  worker-run) exec "$questlock_bin" worker run "$@" ;;
  worker-serve) exec "$questlock_bin" worker serve "$@" ;;
esac
exec "$questlock_bin" model "$questlock_command" "$@"
