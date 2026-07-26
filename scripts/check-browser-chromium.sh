#!/usr/bin/env bash
set -euo pipefail

runtime="${CONTAINER_RUNTIME:-docker}"
image="${BEAMERS_SELENIUM_IMAGE:-selenium/standalone-chrome:latest}"
runs="${BEAMERS_BROWSER_RUNS:-1}"
container="beamers-browser-certification-$$"
repository="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repository"

cleanup() {
  "$runtime" rm --force "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

"$runtime" pull "$image"
browser_version="$(
  "$runtime" run --rm --entrypoint google-chrome "$image" --version
)"
webdriver_version="$(
  "$runtime" run --rm --entrypoint chromedriver "$image" --version
)"
browser_major="$(grep -oE '[0-9]+' <<<"$browser_version" | head -1)"

"$runtime" run \
  --detach \
  --name "$container" \
  --network host \
  --shm-size 2g \
  --env SE_NODE_MAX_SESSIONS=3 \
  --env SE_NODE_OVERRIDE_MAX_SESSIONS=true \
  "$image" >/dev/null

for _ in {1..60}; do
  if curl --fail --silent http://127.0.0.1:4444/status |
    grep --quiet '"ready": true'; then
    break
  fi
  sleep 1
done
curl --fail --silent http://127.0.0.1:4444/status |
  grep --quiet '"ready": true'

mkdir -p "$repository/artifacts"
for run in $(seq 1 "$runs"); do
  report="$repository/artifacts/browser-local-${browser_major}-$$-${run}.json"
  BEAMERS_BROWSER_CERTIFICATION=1 \
  BEAMERS_BROWSER_ENGINE=chromium \
  BEAMERS_BROWSER_ROLE=current \
  BEAMERS_BROWSER_MAJOR="$browser_major" \
  BEAMERS_WEBDRIVER_ENDPOINT=http://127.0.0.1:4444/wd/hub \
  BEAMERS_WEBDRIVER_VERSION="$webdriver_version" \
  BEAMERS_BROWSER_REPORT="$report" \
  go test ./acceptance \
    -run '^TestBrowserCertification$' \
    -count=1 \
    -timeout 25m \
    -v
  echo "Browser evidence: $report"
done
