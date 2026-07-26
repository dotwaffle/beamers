#!/bin/sh
set -eu

repository=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
mkdir "$temporary/bin"

cat >"$temporary/bin/docker" <<'EOF'
#!/bin/sh
case "$*" in
	"buildx version")
		printf 'github.com/docker/buildx %s deadbeef\n' \
			"${FAKE_BUILDX_VERSION:-v0.35.0}"
		;;
	"buildx inspect --bootstrap")
		printf 'Driver: docker-container\n'
		printf 'Driver Options: image="%s"\n' \
			"${FAKE_BUILDKIT_IMAGE:-moby/buildkit:v0.31.2@sha256:2f5adac4ecd194d9f8c10b7b5d7bceb5186853db1b26e5abd3a657af0b7e26ec}"
		printf 'BuildKit version: %s\n' \
			"${FAKE_BUILDKIT_VERSION:-v0.31.2}"
		;;
	*)
		printf 'unexpected docker call: %s\n' "$*" >&2
		exit 1
		;;
esac
EOF
chmod +x "$temporary/bin/docker"

PATH="$temporary/bin:$PATH" \
	"$repository/scripts/check-build-toolchain.sh" >"$temporary/evidence"
grep -Fxq 'Docker Buildx: v0.35.0' "$temporary/evidence"
grep -Fxq 'Buildx driver: docker-container' "$temporary/evidence"
grep -Fxq 'Docker BuildKit: v0.31.2' "$temporary/evidence"
grep -Eq '^BuildKit image: moby/buildkit:v0\.31\.2@sha256:[0-9a-f]{64}$' \
	"$temporary/evidence"

if PATH="$temporary/bin:$PATH" FAKE_BUILDX_VERSION=v0.34.0 \
	"$repository/scripts/check-build-toolchain.sh" >/dev/null 2>&1
then
	echo "drifted Buildx version passed" >&2
	exit 1
fi
if PATH="$temporary/bin:$PATH" FAKE_BUILDKIT_VERSION=v0.31.1 \
	"$repository/scripts/check-build-toolchain.sh" >/dev/null 2>&1
then
	echo "drifted BuildKit version passed" >&2
	exit 1
fi
if PATH="$temporary/bin:$PATH" \
	FAKE_BUILDKIT_IMAGE=moby/buildkit:v0.31.2@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
	"$repository/scripts/check-build-toolchain.sh" >/dev/null 2>&1
then
	echo "drifted BuildKit image passed" >&2
	exit 1
fi
