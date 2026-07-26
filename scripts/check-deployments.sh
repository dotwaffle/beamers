#!/bin/sh
set -eu

repository=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
project=
compose_env=
image=
cleanup() {
	if [ -n "$project" ] && [ -n "$compose_env" ]; then
		docker compose --env-file "$compose_env" -p "$project" \
			-f "$repository/compose.yaml" down --volumes --remove-orphans \
			>/dev/null 2>&1 || true
	fi
	if [ -n "$image" ]; then
		docker image rm "$image" >/dev/null 2>&1 || true
	fi
	rm -rf "$temporary"
}
trap cleanup EXIT

version=0.0.0-test-$$
if SOURCE_DATE_EPOCH=0 "$repository/scripts/build-release.sh" \
	"+invalid" "$temporary/invalid" --binary-only >/dev/null 2>&1
then
	echo "release builder accepted a non-OCI version" >&2
	exit 1
fi
build_release() {
	destination=$1
	if [ "${BEAMERS_FULL_DEPLOYMENT_CHECK:-0}" = "1" ]; then
		SOURCE_DATE_EPOCH=0 "$repository/scripts/build-release.sh" \
			"$version" "$destination"
	else
		SOURCE_DATE_EPOCH=0 "$repository/scripts/build-release.sh" \
			"$version" "$destination" --binary-only
	fi
}

build_release "$temporary/first"
build_release "$temporary/second"

first="$temporary/first/beamers-linux-amd64"
second="$temporary/second/beamers-linux-amd64"
cmp "$first" "$second"

go version -m "$first" | grep -F 'GOOS=linux'
go version -m "$first" | grep -F 'GOARCH=amd64'
go version -m "$first" | grep -F 'CGO_ENABLED=0'
strings "$first" | grep -m 1 -F 'Pacific/Auckland'
if [ "${BEAMERS_FULL_DEPLOYMENT_CHECK:-0}" = "1" ]; then
	cmp "$temporary/first/beamers-oci-linux-amd64.tar" \
		"$temporary/second/beamers-oci-linux-amd64.tar"
	cmp "$temporary/first/beamers-image-linux-amd64.tar" \
		"$temporary/second/beamers-image-linux-amd64.tar"
	cmp "$temporary/first/beamers-build-toolchain.txt" \
		"$temporary/second/beamers-build-toolchain.txt"
fi

grep -F 'ENTRYPOINT ["/usr/local/bin/beamers"]' "$repository/Dockerfile"
grep -F 'STOPSIGNAL SIGTERM' "$repository/Dockerfile"
grep -F 'VOLUME ["/var/lib/beamers"]' "$repository/Dockerfile"
grep -F 'User=beamers' "$repository/deploy/systemd/beamers.service"
grep -F 'TimeoutStopSec=10s' "$repository/deploy/systemd/beamers.service"
grep -F -- '--data-dir=/var/lib/beamers/data' \
	"$repository/deploy/systemd/beamers.service"

touch "$temporary/tls.crt" "$temporary/tls.key"
cat >"$temporary/compose.env" <<EOF
BEAMERS_IMAGE=beamers:$version
BEAMERS_TLS_CERT=$temporary/tls.crt
BEAMERS_TLS_KEY=$temporary/tls.key
EOF
docker compose --env-file "$temporary/compose.env" \
	-f "$repository/compose.yaml" config --quiet
docker compose --env-file "$temporary/compose.env" \
	-f "$repository/compose.yaml" config |
	grep -F 'stop_grace_period: 10s'

systemd_root="$temporary/systemd-root"
mkdir -p "$systemd_root/etc/systemd/system" "$systemd_root/usr/local/bin"
cp "$repository/deploy/systemd/beamers.service" \
	"$systemd_root/etc/systemd/system/beamers.service"
cp "$first" "$systemd_root/usr/local/bin/beamers"
cat >"$systemd_root/etc/systemd/system/sysinit.target" <<'EOF'
[Unit]
Description=System Initialization
EOF
cat >"$systemd_root/etc/passwd" <<'EOF'
beamers:x:65532:65532::/var/lib/beamers:/sbin/nologin
EOF
cat >"$systemd_root/etc/group" <<'EOF'
beamers:x:65532:
EOF
systemd-analyze verify --root="$systemd_root" beamers.service

if [ "${BEAMERS_FULL_DEPLOYMENT_CHECK:-0}" != "1" ]; then
	exit 0
fi
if [ "${BEAMERS_SYSTEMD_RUNTIME_CHECK:-0}" = "1" ]; then
	BEAMERS_EPHEMERAL_SYSTEMD_CHECK=1 \
		"$repository/scripts/check-systemd-runtime.sh" "$first"
fi

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
	-keyout "$temporary/tls.key" \
	-out "$temporary/tls.crt" \
	-subj /CN=localhost \
	-addext subjectAltName=DNS:localhost,IP:127.0.0.1 \
	>/dev/null 2>&1
chmod 0444 "$temporary/tls.crt" "$temporary/tls.key"
compose_env="$temporary/runtime.env"
cat >"$compose_env" <<EOF
BEAMERS_IMAGE=beamers:$version
BEAMERS_PORT=0
BEAMERS_TLS_CERT=$temporary/tls.crt
BEAMERS_TLS_KEY=$temporary/tls.key
EOF
image="beamers:$version"
docker load --input "$temporary/first/beamers-image-linux-amd64.tar"
project="beamers-check-$$"
compose() {
	docker compose --env-file "$compose_env" -p "$project" \
		-f "$repository/compose.yaml" "$@"
}

compose run --rm --no-deps beamers \
	init \
	--data-dir=/var/lib/beamers/data \
	--attachments-dir=/var/lib/beamers/attachments
compose up --detach --wait --wait-timeout 30
container=$(compose ps --quiet beamers)
test "$(docker exec "$container" cat /proc/1/comm)" = "beamers"
docker exec "$container" wget --no-check-certificate --quiet \
	--output-document=- https://127.0.0.1:8443/schedule |
	grep -F '/assets/schedule.css'
docker exec "$container" wget --no-check-certificate --quiet \
	--output-document=- https://127.0.0.1:8443/assets/schedule.css |
	grep -F 'grid-template-columns'
docker exec "$container" wget --no-check-certificate --quiet \
	--output-document=- https://127.0.0.1:8443/assets/frontend.css |
	grep -F 'font-family'
compose stop beamers
compose logs --no-color beamers | grep -F '"phase":"shutdown"'
compose run --rm --no-deps beamers \
	backup \
	--data-dir=/var/lib/beamers/data \
	--attachments-dir=/var/lib/beamers/attachments \
	--output=/var/lib/beamers/compose.beamers-backup
compose run --rm --no-deps beamers \
	backup verify \
	--input=/var/lib/beamers/compose.beamers-backup
compose run --rm --no-deps beamers \
	restore preview \
	--input=/var/lib/beamers/compose.beamers-backup \
	--data-dir=/var/lib/beamers/restored-data \
	--attachments-dir=/var/lib/beamers/restored-attachments \
	>"$temporary/compose-restore-plan.json"
grep -F '"listen_address":"0.0.0.0:8443"' \
	"$temporary/compose-restore-plan.json"
grep -F '"tls_private_key":"/run/secrets/tls.key"' \
	"$temporary/compose-restore-plan.json"
grep -F '"requires_configuration_acknowledgment":true' \
	"$temporary/compose-restore-plan.json"
grep -F '"resolution":"acknowledgment_required"' \
	"$temporary/compose-restore-plan.json"
compose run --rm --no-deps beamers \
	restore apply \
	--journal=/var/lib/beamers/restored-data.beamers-restore.json \
	--acknowledge-replacement \
	--acknowledge-configuration-differences
compose up --detach --wait --wait-timeout 30
