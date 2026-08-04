#!/usr/bin/env bash
set -euo pipefail

runtime="${CONTAINER_RUNTIME:-docker}"
engine="${BEAMERS_BROWSER_ENGINE:-chromium}"
role="${BEAMERS_BROWSER_ROLE:-current}"
runs="${BEAMERS_BROWSER_RUNS:-1}"
repository="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repository"

# Log the runner's image identity once up front so a silent failure
# later in the log can be correlated with a specific runner rollout.
# ImageOS/ImageVersion are only set on GitHub-hosted runners; guard
# them for local runs where they are unset.
echo "runner image: ImageOS=${ImageOS:-unset} ImageVersion=${ImageVersion:-unset}" >&2

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

max_attempts=3
backoff_seconds=(5 10)

# dump_failure_diagnostics prints the exit code, any captured stderr,
# the certification container's inspected state (if it exists), and
# the runner's image identity. It exists so a transient container
# failure that used to kill the job with zero output is diagnosable
# and correlatable with runner rollouts after the fact.
dump_failure_diagnostics() {
  local description="$1" status="$2" stderr_file="$3"
  echo "FAILED: $description (exit $status)" >&2
  if [ -f "$stderr_file" ] && [ -s "$stderr_file" ]; then
    echo "--- stderr ---" >&2
    cat "$stderr_file" >&2
  fi
  if "$runtime" inspect "$container" >/dev/null 2>&1; then
    echo "--- container state ($container) ---" >&2
    "$runtime" inspect --format \
      'status={{.State.Status}} exitCode={{.State.ExitCode}} error={{.State.Error}} oomKilled={{.State.OOMKilled}} startedAt={{.State.StartedAt}} finishedAt={{.State.FinishedAt}}' \
      "$container" >&2 || true
  fi
  echo "ImageOS=${ImageOS:-unset} ImageVersion=${ImageVersion:-unset}" >&2
}

# run_with_retry runs its remaining arguments as a command, retrying up
# to max_attempts times with a short backoff between attempts. Every
# failed attempt is diagnosed via dump_failure_diagnostics before the
# function decides whether to retry or give up. The command's exit
# code is captured explicitly (never left to trigger errexit silently)
# so it can be inspected and reported.
run_with_retry() {
  local description="$1"
  shift
  local attempt=1 status stderr_file delay
  stderr_file="$(mktemp)"
  while :; do
    status=0
    "$@" 2>"$stderr_file" || status=$?
    if [ "$status" -eq 0 ]; then
      rm -f "$stderr_file"
      return 0
    fi
    echo "attempt $attempt/$max_attempts failed: $description" >&2
    dump_failure_diagnostics "$description" "$status" "$stderr_file"
    if [ "$attempt" -ge "$max_attempts" ]; then
      break
    fi
    delay="${backoff_seconds[$((attempt - 1))]}"
    echo "retrying $description in ${delay}s" >&2
    sleep "$delay"
    attempt=$((attempt + 1))
  done
  rm -f "$stderr_file"
  return "$status"
}

start_container() {
  # Clear any container left behind by a previous failed attempt so
  # --name does not conflict on retry.
  "$runtime" rm --force "$container" >/dev/null 2>&1 || true
  "$runtime" run \
    --detach \
    --name "$container" \
    --network host \
    --shm-size 2g \
    --env SE_NODE_MAX_SESSIONS=4 \
    --env SE_NODE_OVERRIDE_MAX_SESSIONS=true \
    "$image" >/dev/null
}

run_with_retry "pull $image" "$runtime" pull "$image"
run_with_retry "start certification container" start_container

for _ in {1..60}; do
  if curl --fail --silent http://127.0.0.1:4444/status |
    grep --quiet '"ready": true'; then
    break
  fi
  sleep 1
done
status_response=""
if ! status_response="$(curl --fail --silent http://127.0.0.1:4444/status)"; then
  curl_status=$?
  dump_failure_diagnostics \
    "certification container readiness (status endpoint unreachable)" \
    "$curl_status" /dev/null
  exit "$curl_status"
fi
if ! grep --quiet '"ready": true' <<<"$status_response"; then
  dump_failure_diagnostics \
    "certification container readiness (grid never became ready)" 1 /dev/null
  exit 1
fi

mkdir -p "$repository/artifacts"

# Probe browser and webdriver versions via docker exec on the
# already-running, already-ready certification container, rather than
# launching two extra throwaway containers. This removes two of the
# three container starts that could fail silently, and the evidence
# now reflects the exact container that goes on to certify.
probe_stderr="$(mktemp)"
if ! browser_version="$(
  "$runtime" exec "$container" "$browser_command" --version 2>"$probe_stderr"
)"; then
  status=$?
  dump_failure_diagnostics "probe $browser_command version" "$status" "$probe_stderr"
  exit "$status"
fi
if ! webdriver_version="$(
  "$runtime" exec "$container" "$webdriver_command" --version 2>"$probe_stderr"
)"; then
  status=$?
  dump_failure_diagnostics "probe $webdriver_command version" "$status" "$probe_stderr"
  exit "$status"
fi
rm -f "$probe_stderr"

detected_major="$(grep -oE '[0-9]+' <<<"$browser_version" | head -1)"
expected_major="${BEAMERS_BROWSER_MAJOR:-$detected_major}"
if [ "$detected_major" != "$expected_major" ]; then
  echo \
    "browser image $image has major $detected_major, expected $expected_major" \
    >&2
  exit 1
fi

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
