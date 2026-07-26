#!/bin/sh
set -eu

repository=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
# shellcheck disable=SC1091 # Repository-controlled data file.
. "$repository/build/toolchain.env"

case "$BUILDKIT_IMAGE" in
	"moby/buildkit:$BUILDKIT_VERSION@sha256:"*) ;;
	*)
		echo "BuildKit image does not match configured version" >&2
		exit 1
		;;
esac
buildkit_digest=${BUILDKIT_IMAGE##*@sha256:}
case "$buildkit_digest" in
	"" | *[!0-9a-f]*)
		echo "BuildKit image digest is invalid" >&2
		exit 1
		;;
esac
if [ "${#buildkit_digest}" -ne 64 ]; then
	echo "BuildKit image digest is invalid" >&2
	exit 1
fi
mise_buildx=$(
	sed -n 's/^"github:docker\/buildx" = { version = "\([^"]*\)",.*$/v\1/p' \
		"$repository/mise.toml"
)
if [ "$mise_buildx" != "$BUILDX_VERSION" ]; then
	echo "mise Buildx $mise_buildx does not match $BUILDX_VERSION" >&2
	exit 1
fi

actual_buildx=$(docker buildx version | awk 'NR == 1 { print $2 }')
if [ "$actual_buildx" != "$BUILDX_VERSION" ]; then
	echo "Docker Buildx $actual_buildx does not match $BUILDX_VERSION" >&2
	exit 1
fi
builder=$(docker buildx inspect --bootstrap)
actual_driver=$(
	printf '%s\n' "$builder" |
		sed -n 's/^Driver:[[:space:]]*//p'
)
if [ "$actual_driver" != "docker-container" ]; then
	echo "Docker Buildx driver $actual_driver is not docker-container" >&2
	exit 1
fi
actual_image=$(
	printf '%s\n' "$builder" |
		sed -n 's/^[[:space:]]*Driver Options:[[:space:]]*image="\([^"]*\)".*/\1/p'
)
if [ "$actual_image" != "$BUILDKIT_IMAGE" ]; then
	echo "Docker BuildKit image $actual_image does not match $BUILDKIT_IMAGE" >&2
	exit 1
fi
actual_buildkit=$(
	printf '%s\n' "$builder" |
		sed -n 's/^[[:space:]]*BuildKit version:[[:space:]]*//p'
)
if [ "$actual_buildkit" != "$BUILDKIT_VERSION" ]; then
	echo "Docker BuildKit $actual_buildkit does not match $BUILDKIT_VERSION" >&2
	exit 1
fi

printf 'Docker Buildx: %s\n' "$actual_buildx"
printf 'Buildx driver: %s\n' "$actual_driver"
printf 'Docker BuildKit: %s\n' "$actual_buildkit"
printf 'BuildKit image: %s\n' "$BUILDKIT_IMAGE"
