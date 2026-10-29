#!/bin/sh
# Action entrypoint: receives "--flag=value" arguments from action.yml and
# drops the ones whose value is empty, so unset inputs keep their defaults.
set -eu

cmd="$1"
shift
for arg in "$@"; do
  shift
  case "$arg" in
    --*=) ;;                    # empty input: skip
    --token=*) export PUBLISH_PLEASE_TOKEN="${arg#--token=}" ;;
    *) set -- "$@" "$arg" ;;
  esac
done
exec publish-please "$cmd" "$@"
