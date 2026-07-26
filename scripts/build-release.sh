#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 3 ]; then
	echo "usage: $0 VERSION [OUTPUT-DIR] [--binary-only]" >&2
	exit 2
fi

version=$1
output=${2:-dist}
mode=${3:-}
case "$version" in
	"" | *[!0-9A-Za-z._-]* | [.-]*)
		echo "version contains unsupported characters" >&2
		exit 2
		;;
esac
if [ "${#version}" -gt 128 ]; then
	echo "version exceeds the OCI tag limit" >&2
	exit 2
fi
if [ -n "$mode" ] && [ "$mode" != "--binary-only" ]; then
	echo "unknown build mode: $mode" >&2
	exit 2
fi

repository=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
source_date_epoch=${SOURCE_DATE_EPOCH:-$(git -C "$repository" log -1 --format=%ct)}
case "$source_date_epoch" in
	"" | *[!0-9]*)
		echo "SOURCE_DATE_EPOCH must be a nonnegative integer" >&2
		exit 2
		;;
esac
case "$output" in
	/*) ;;
	*) output="$repository/$output" ;;
esac
mkdir -p "$output"

binary="$output/beamers-linux-amd64"
(
	cd "$repository"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS='' GOWORK=off \
		go build -mod=readonly -trimpath -buildvcs=false \
		-ldflags="-buildid= -s -w -X github.com/dotwaffle/beamers/internal/buildinfo.version=$version" \
		-o "$binary" ./cmd/beamers
)
touch -d "@$source_date_epoch" "$binary"
(
	cd "$output"
	sha256sum beamers-linux-amd64 >beamers-linux-amd64.sha256
)

if [ "$mode" = "--binary-only" ]; then
	exit 0
fi

"$repository/scripts/check-build-toolchain.sh" \
	>"$output/beamers-build-toolchain.txt"
archive="$output/beamers-oci-linux-amd64.tar"
docker_archive="$output/beamers-image-linux-amd64.tar"
docker buildx build \
	--platform linux/amd64 \
	--build-arg "VERSION=$version" \
	--build-arg "SOURCE_DATE_EPOCH=$source_date_epoch" \
	--build-context "beamers-binary=$output" \
	--provenance=false \
	--sbom=false \
	--output "type=oci,name=beamers:$version,dest=$archive,rewrite-timestamp=true" \
	--output "type=docker,name=beamers:$version,dest=$docker_archive,rewrite-timestamp=true" \
	"$repository"
touch -d "@$source_date_epoch" "$archive" "$docker_archive"
(
	cd "$output"
	sha256sum beamers-oci-linux-amd64.tar >beamers-oci-linux-amd64.tar.sha256
	sha256sum beamers-image-linux-amd64.tar >beamers-image-linux-amd64.tar.sha256
)
