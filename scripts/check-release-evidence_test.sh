#!/bin/sh

set -eu

repository=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
mkdir "$temporary/bin"

cat >"$temporary/bin/gh" <<'EOF'
#!/bin/sh

case "$*" in
	*"issues/51/comments"*)
		case "${FAKE_CERTIFICATION_ASSOCIATION:-OWNER}:$*" in
			NONE:*author_association*)
				;;
			*)
				printf 'Certified commit: %s\n' \
					"${FAKE_CERTIFICATION_COMMIT:-0123456789abcdef}"
				;;
		esac
		;;
	*"issues/51"*)
		printf '%s\n' "${FAKE_CERTIFICATION_STATE:-closed}"
		;;
	*"workflows/browsers.yml/runs"*\
*"head_sha=0123456789abcdef"*\
*"status=success"*\
*"event=push"*)
		printf '%s\n' 101
		;;
	*"workflows/capacity.yml/runs"*\
*"head_sha=0123456789abcdef"*\
*"status=success"*\
*"event=workflow_dispatch"*)
		printf '%s\n' 202
		;;
	*"runs/101/artifacts"*)
		printf '%s\n' \
			browser-chromium-current-101 \
			browser-chromium-previous-101 \
			browser-firefox-current-101
		if [ "${FAKE_BROWSER_COMPLETE:-1}" = 1 ]; then
			printf '%s\n' browser-firefox-previous-101
		fi
		;;
	*"runs/202/artifacts"*)
		printf '%s\n' capacity-rated-202
		if [ "${FAKE_CAPACITY_COMPLETE:-1}" = 1 ]; then
			printf '%s\n' \
				capacity-small-conference-202 \
				capacity-small-demoparty-202 \
				capacity-large-conference-202 \
				capacity-large-demoparty-202 \
				capacity-stress-202
		fi
		;;
	*)
		printf 'unexpected gh call: %s\n' "$*" >&2
		exit 1
		;;
esac
EOF
chmod +x "$temporary/bin/gh"

run_check() {
	PATH="$temporary/bin:$PATH" \
		GITHUB_REPOSITORY=dotwaffle/beamers \
		GITHUB_SHA=0123456789abcdef \
		sh "$repository/scripts/check-release-evidence.sh"
}

run_check >"$temporary/output"
grep -Fq 'Browser run 101' "$temporary/output"
grep -Fq 'Capacity run 202' "$temporary/output"

if FAKE_CERTIFICATION_STATE=open run_check >"$temporary/output" 2>&1; then
	echo "open manual certification issue passed" >&2
	exit 1
fi
grep -Fq 'certification issue #51 is open' "$temporary/output"

if FAKE_CERTIFICATION_COMMIT=deadbeef run_check >"$temporary/output" 2>&1; then
	echo "stale manual certification passed" >&2
	exit 1
fi
grep -Fq 'does not certify 0123456789abcdef' "$temporary/output"

if FAKE_CERTIFICATION_ASSOCIATION=NONE run_check >"$temporary/output" 2>&1; then
	echo "untrusted manual certification passed" >&2
	exit 1
fi
grep -Fq 'does not certify 0123456789abcdef' "$temporary/output"

if FAKE_BROWSER_COMPLETE=0 run_check >"$temporary/output" 2>&1; then
	echo "incomplete Browser evidence passed" >&2
	exit 1
fi
grep -Fq 'complete Browser evidence' "$temporary/output"

if FAKE_CAPACITY_COMPLETE=0 run_check >"$temporary/output" 2>&1; then
	echo "incomplete Capacity evidence passed" >&2
	exit 1
fi
grep -Fq 'complete full Capacity evidence' "$temporary/output"
