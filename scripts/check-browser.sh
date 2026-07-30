#!/usr/bin/env bash
set -euo pipefail

runtime="${CONTAINER_RUNTIME:-docker}"
engine="${BEAMERS_BROWSER_ENGINE:-chromium}"
role="${BEAMERS_BROWSER_ROLE:-current}"
runs="${BEAMERS_BROWSER_RUNS:-1}"
repository="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repository"

case "$engine" in
chromium)
  image="${BEAMERS_SELENIUM_IMAGE:-selenium/standalone-chrome:latest}"
  browser_command=google-chrome
  webdriver_command=chromedriver
  ;;
firefox)
  image="${BEAMERS_SELENIUM_IMAGE:-selenium/standalone-firefox:latest}"
  browser_command=firefox
  webdriver_command=geckodriver
  ;;
*)
  echo "unsupported browser engine: $engine" >&2
  exit 2
  ;;
esac

container="beamers-browser-certification-${engine}-$$"

cleanup() {
  "$runtime" rm --force "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

"$runtime" pull "$image"
browser_version="$(
  "$runtime" run --rm --entrypoint "$browser_command" "$image" --version
)"
webdriver_version="$(
  "$runtime" run --rm --entrypoint "$webdriver_command" "$image" --version
)"
detected_major="$(grep -oE '[0-9]+' <<<"$browser_version" | head -1)"
expected_major="${BEAMERS_BROWSER_MAJOR:-$detected_major}"
if [ "$detected_major" != "$expected_major" ]; then
  echo \
    "browser image $image has major $detected_major, expected $expected_major" \
    >&2
  exit 1
fi

"$runtime" run \
  --detach \
  --name "$container" \
  --network host \
  --shm-size 2g \
  --env SE_NODE_MAX_SESSIONS=4 \
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
  report="${BEAMERS_BROWSER_REPORT:-}"
  if [ -z "$report" ]; then
    report="$repository/artifacts/browser-local-${engine}-${detected_major}-$$-${run}.json"
  elif [ "$runs" -gt 1 ]; then
    report="${report%.json}-${run}.json"
  fi
  BEAMERS_BROWSER_CERTIFICATION=1 \
  BEAMERS_BROWSER_ENGINE="$engine" \
  BEAMERS_BROWSER_ROLE="$role" \
  BEAMERS_BROWSER_MAJOR="$expected_major" \
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
