#!/bin/sh

set -eu

repository=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}
commit=${GITHUB_SHA:?GITHUB_SHA is required}
certification_issue=51

find_complete_run() {
	workflow=$1
	event=$2
	shift 2

	runs=$(
		gh api --method GET \
			"repos/$repository/actions/workflows/$workflow/runs" \
			-f "head_sha=$commit" \
			-f status=success \
			-f "event=$event" \
			--paginate \
			--jq '.workflow_runs[].id'
	)

	for run in $runs; do
		artifacts=$(
			gh api \
				"repos/$repository/actions/runs/$run/artifacts" \
				--paginate \
				--jq '.artifacts[] | select(.expired == false) | .name'
		)
		complete=true
		for name in "$@"; do
			if ! printf '%s\n' "$artifacts" |
				grep -Fqx "$name-$run"; then
				complete=false
				break
			fi
		done
		if [ "$complete" = true ]; then
			printf '%s\n' "$run"
			return 0
		fi
	done

	return 1
}

certification_state=$(
	gh api "repos/$repository/issues/$certification_issue" --jq '.state'
)
if [ "$certification_state" != closed ]; then
	echo "release blocked: certification issue #$certification_issue is $certification_state" >&2
	exit 1
fi

if ! gh api \
	"repos/$repository/issues/$certification_issue/comments" \
	--paginate \
	--jq '.[] |
		select(
			.author_association == "OWNER" or
			.author_association == "MEMBER" or
			.author_association == "COLLABORATOR"
		) |
		.body' |
	grep -Fqx "Certified commit: $commit"; then
	echo "release blocked: certification issue #$certification_issue does not certify $commit" >&2
	exit 1
fi

if ! browser_run=$(
	find_complete_run browsers.yml push \
		browser-chromium-current \
		browser-chromium-previous \
		browser-firefox-current \
		browser-firefox-previous
); then
	echo "release blocked: no complete Browser evidence for $commit" >&2
	exit 1
fi

if ! capacity_run=$(
	find_complete_run capacity.yml workflow_dispatch \
		capacity-rated \
		capacity-small-conference \
		capacity-small-demoparty \
		capacity-large-conference \
		capacity-large-demoparty \
		capacity-stress
); then
	echo "release blocked: no complete full Capacity evidence for $commit" >&2
	exit 1
fi

echo "Browser run $browser_run and Capacity run $capacity_run certify $commit"
