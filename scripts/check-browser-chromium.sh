#!/usr/bin/env bash
set -euo pipefail

repository="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
BEAMERS_BROWSER_ENGINE=chromium exec "$repository/scripts/check-browser.sh" "$@"
