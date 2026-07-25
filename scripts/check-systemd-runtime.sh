#!/bin/sh
set -eu

if [ "${BEAMERS_EPHEMERAL_SYSTEMD_CHECK:-0}" != "1" ] || [ "$#" -ne 1 ]; then
	echo "systemd runtime check requires an explicit ephemeral-host opt-in and one binary" >&2
	exit 2
fi
for path in \
	/usr/local/bin/beamers \
	/etc/systemd/system/beamers.service \
	/etc/beamers \
	/var/lib/beamers
do
	if sudo test -e "$path"; then
		echo "refusing to replace existing path: $path" >&2
		exit 1
	fi
done
if id beamers >/dev/null 2>&1; then
	echo "refusing to replace existing beamers user" >&2
	exit 1
fi

repository=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
installed=0
cleanup() {
	if [ "$installed" = "1" ]; then
		sudo systemctl stop beamers.service >/dev/null 2>&1 || true
		sudo rm -f /etc/systemd/system/beamers.service /usr/local/bin/beamers
		sudo rm -rf /etc/beamers /var/lib/beamers
		sudo systemctl daemon-reload
		sudo userdel beamers >/dev/null 2>&1 || true
	fi
	rm -rf "$temporary"
}
trap cleanup EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
	-keyout "$temporary/tls.key" \
	-out "$temporary/tls.crt" \
	-subj /CN=localhost \
	-addext subjectAltName=DNS:localhost,IP:127.0.0.1 \
	>/dev/null 2>&1

sudo useradd --system --home-dir /var/lib/beamers \
	--shell /usr/sbin/nologin beamers
sudo install -o root -g root -m 0755 "$1" /usr/local/bin/beamers
sudo install -d -o beamers -g beamers -m 0700 /var/lib/beamers
sudo install -d -o root -g beamers -m 0750 /etc/beamers
sudo install -o root -g beamers -m 0644 "$temporary/tls.crt" \
	/etc/beamers/tls.crt
sudo install -o root -g beamers -m 0640 "$temporary/tls.key" \
	/etc/beamers/tls.key
sudo install -o root -g root -m 0644 \
	"$repository/deploy/systemd/beamers.service" \
	/etc/systemd/system/beamers.service
installed=1

sudo -u beamers /usr/local/bin/beamers init \
	--data-dir=/var/lib/beamers/data \
	--attachments-dir=/var/lib/beamers/attachments
sudo systemctl daemon-reload
sudo systemctl start beamers.service

wait_ready() {
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		if curl --fail --silent --insecure \
			https://127.0.0.1:8443/readyz >/dev/null; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.1
	done
	sudo systemctl status beamers.service --no-pager >&2 || true
	exit 1
}

wait_ready
pid=$(sudo systemctl show beamers.service --property=MainPID --value)
test "$(sudo cat "/proc/$pid/comm")" = "beamers"
sudo systemctl stop beamers.service
sudo journalctl --unit=beamers.service --no-pager |
	grep -F '"phase":"shutdown"'
sudo systemctl start beamers.service
wait_ready
