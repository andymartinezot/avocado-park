#!/bin/sh
# Everything that must pass before code reaches main. The pre-push hook runs
# this on the commits being pushed; run it by hand any time.
#
# The end-to-end step uses a private herdr with its own home directory, never
# the one you work in. The last step proves that: your real config and parked
# sessions must be exactly as they were.
set -u

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root" || exit 1

config_home=${XDG_CONFIG_HOME:-$HOME/.config}
state_home=${XDG_STATE_HOME:-$HOME/.local/state}
real_files="$config_home/herdr/config.toml
$config_home/herdr/plugins/config/andymartinezot.avocado-park/config.toml
${AVOCADO_PARK_STATE_DIR:-$state_home/avocado-park}/parked.json"

fingerprint() {
  printf '%s\n' "$real_files" | while IFS= read -r f; do
    cksum "$f" 2>/dev/null || echo "absent $f"
  done
}

log=$(mktemp) || exit 1
trap 'rm -f "$log"' EXIT

step() {
  name=$1
  shift
  printf '  %-28s' "$name"
  if "$@" >"$log" 2>&1; then
    echo ok
  else
    echo FAILED
    cat "$log"
    echo "qa: $name failed" >&2
    exit 1
  fi
}

formatted() { test -z "$(gofmt -l .)" || { gofmt -l .; return 1; }; }
real_files_unchanged() {
  after=$(fingerprint)
  test "$before" = "$after" || {
    echo "These changed while QA ran. If that wasn't you, QA is touching real files:"
    printf '%s\n%s\n' "$before" "$after" | sort | uniq -u
    return 1
  }
}

command -v herdr >/dev/null 2>&1 || {
  echo "qa: herdr is not installed, so the end-to-end step can't run." >&2
  exit 1
}

before=$(fingerprint)
echo "qa: $(herdr --version 2>/dev/null | head -n 1), $(go version | cut -d' ' -f3)"
step "format" formatted
step "go.mod is tidy" go mod tidy -diff
step "vet" go vet -tags e2e ./...
step "unit tests (race)" go test -race -count=1 ./...
step "end to end, private herdr" go test -tags e2e -count=1 ./e2e
step "real config untouched" real_files_unchanged
echo "qa: all passed"
