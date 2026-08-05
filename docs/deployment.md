# Deploy Beamers at a venue

Beamers supports Linux AMD64 as a direct CGo-free binary, a systemd service, and Docker Compose.
All three profiles use:

- database state under `/var/lib/beamers/data`;
- the Attachment Store under `/var/lib/beamers/attachments`;
- direct TLS on port 8443; and
- a 30-second graceful-stop budget with 35 seconds of platform kill grace.

The binary embeds its web assets and IANA timezone database.
Serving and upgrading require no CDN, Node runtime, update service, or internet connection.

## Certify a release commit

Before tagging, wait for the commit's `Browsers` workflow and run the complete Capacity certification against that same commit:

```sh
gh workflow run Capacity --ref main -f profile=all
```

Attach the completed manual browser, kiosk, and accessibility evidence to issue \#51 and close it only when every check passes.
A repository owner, member, or collaborator must end the evidence comment with the exact release candidate commit:

```text
Certified commit: FULL_40_CHARACTER_COMMIT_SHA
```

The tag workflow rejects missing, failed, partial, expired, or different-commit Browser and Capacity evidence before building release artifacts.

## Verify release files

Obtain release files and their checksum files outside the venue network.
Verify them before copying them into place:

```sh
sha256sum --check beamers-linux-amd64.sha256
sha256sum --check beamers-image-linux-amd64.tar.sha256
sha256sum --check beamers-oci-linux-amd64.tar.sha256
```

`beamers-image-linux-amd64.tar` is the loadable Docker archive.
`beamers-oci-linux-amd64.tar` is the equivalent OCI-layout archive for OCI-native tooling.

## Install the binary

Direct and systemd deployments keep versioned binaries so rollback never depends on another download:

```sh
install -d -o root -g root -m 0755 /usr/local/lib/beamers
install -o root -g root -m 0755 beamers-linux-amd64 \
  /usr/local/lib/beamers/beamers-VERSION
ln -sfn /usr/local/lib/beamers/beamers-VERSION /usr/local/bin/beamers
```

## Prepare persistent state

Create one unprivileged service identity and persistent parent directory:

```sh
useradd --system --home-dir /var/lib/beamers --shell /usr/sbin/nologin beamers
install -d -o beamers -g beamers -m 0700 /var/lib/beamers
install -d -o root -g beamers -m 0750 /etc/beamers
install -o root -g beamers -m 0644 venue.crt /etc/beamers/tls.crt
install -o root -g beamers -m 0640 venue.key /etc/beamers/tls.key
```

The certificate must cover the host name used by Crew Members and Displays.
Keep the database, Attachment Store, and their adjacent Beamers access locks inside the persistent `/var/lib/beamers` parent.

Initialize once before starting any service:

```sh
sudo -u beamers /usr/local/bin/beamers init \
  --data-dir=/var/lib/beamers/data \
  --attachments-dir=/var/lib/beamers/attachments
```

Then issue the one-time Administrator bootstrap credential while the server is stopped:

```sh
sudo -u beamers /usr/local/bin/beamers bootstrap \
  --data-dir=/var/lib/beamers/data
```

## Run the binary directly

```sh
sudo -u beamers /usr/local/bin/beamers serve \
  --data-dir=/var/lib/beamers/data \
  --attachments-dir=/var/lib/beamers/attachments \
  --listen=0.0.0.0:8443 \
  --tls-cert=/etc/beamers/tls.crt \
  --tls-key=/etc/beamers/tls.key \
  --shutdown-timeout=30s
```

Send `SIGINT` or `SIGTERM` directly to this process.
Do not place it behind a shell wrapper that remains PID 1.
A direct invocation has no platform kill deadline of its own; give the process at least 35 seconds before forcing termination, matching the margin the shipped systemd and Compose profiles give it.

## Configure SceneID

Create a SceneID OAuth client with this callback URL:

```text
https://BEAMERS-HOST/auth/federation/sceneid/callback
```

Store its secret in a host-readable file, then add:

```sh
--sceneid-client-id=CLIENT-ID \
--sceneid-client-secret-file=/etc/beamers/sceneid-secret \
--sceneid-callback-url=https://BEAMERS-HOST/auth/federation/sceneid/callback
```

Add `--sceneid-allow-account-creation` to let new SceneID identities create Accounts while the installation Registration Policy is open.
Without it, previously linked identities may sign in and authenticated Accounts may link SceneID.
SceneID requires HTTPS except on a loopback listener.
The client secret is not stored in browser configuration or Backups.

## Configure continuous replication

Add `--replica-url=LITESTREAM-URL` to continuously replicate the authoritative SQLite database to a second local or mounted-remote path:

```sh
--replica-url=file:///mnt/offsite/beamers.db
```

Version one accepts only an absolute, credential-free `file:` URL.
The URL must not contain a host, user info, query, or fragment.
Mount the destination at that path.

This covers **the database only**.
It does not replicate the Attachment Store; a Litestream replica alone cannot restore uploaded files.
Protect Attachments with a separate mechanism sized to the venue's tolerance for loss, for example a periodic Backup (`beamers backup`, which archives both the database and Attachments together) or a scheduled `rsync` of `/var/lib/beamers/attachments` to off-host storage.
A restore from the database replica without a corresponding Attachment copy leaves every uploaded file permanently missing.

## Run with systemd

Install the supplied unit after the binary and persistent state are ready:

```sh
install -o root -g root -m 0644 deploy/systemd/beamers.service \
  /etc/systemd/system/beamers.service
systemctl daemon-reload
systemctl enable --now beamers.service
```

The unit always attempts to start; it does not gate on the database file existing.
A missing or damaged database makes `serve` enter its documented local recovery mode instead of the unit silently staying inactive, so the problem is visible at `/readyz` and in diagnostics rather than presenting as a service that never came up.
It sends `SIGTERM` to the Beamers process and gives it the 30-second budget configured with `--shutdown-timeout`, with `TimeoutStopSec=35s` as the platform kill deadline that budget must fit inside.
`MemoryMax=4G` leaves half of the [reference hardware](capacity.md)'s 8 GB for the OS, Litestream, and other host processes while still covering the rated capacity envelope; a unit that exceeds it is killed rather than pushing the host into swap or OOM-killing something else.

Because the unit carries no readiness awareness of its own, add an external watchdog that polls `/readyz` on a companion timer unit and pages an Administrator on sustained failure, rather than restarting blindly:

```sh
#!/bin/sh
# beamers-watchdog.sh, invoked by a companion .timer unit.
systemctl is-active --quiet beamers.service || exit 0
curl --fail --silent --show-error --insecure --max-time 2 \
  https://127.0.0.1:8443/readyz && exit 0
STATE=/run/beamers-watchdog.count
COUNT=$(( $(cat "$STATE" 2>/dev/null || echo 0) + 1 ))
echo "$COUNT" >"$STATE"
[ "$COUNT" -ge 3 ] || exit 0
echo 0 >"$STATE"
logger -t beamers-watchdog "readiness failed 3 consecutive checks; check for recovery mode before restarting"
```

The `systemctl is-active` guard keeps the watchdog from fighting an Administrator's intentional `systemctl stop beamers.service`, for example during the offline restore below.
The consecutive-failure counter keeps one slow probe from restarting a healthy process.
Recovery mode fails `/readyz` by design.
Restarting does not repair a missing or damaged database.
Alert an Administrator to diagnose and restore it.
Beamers does not implement `sd_notify` watchdog pings; systemd's own `Restart=on-failure` only reacts to the process exiting, not to a running-but-unready process.

## Run with Docker Compose

Load the externally obtained image without contacting a registry:

```sh
docker load --input beamers-image-linux-amd64.tar
cp .env.example .env
```

Edit `.env` with the loaded image tag and absolute certificate paths.
Compose runs as UID and GID 65532, so give that identity read access to the dedicated certificate copies without making the private key generally readable.

Initialize the named volume once:

```sh
docker compose run --rm --no-deps beamers \
  init \
  --data-dir=/var/lib/beamers/data \
  --attachments-dir=/var/lib/beamers/attachments
docker compose run --rm --no-deps beamers \
  bootstrap --data-dir=/var/lib/beamers/data
```

Start only the already loaded image and wait for readiness:

```sh
docker compose up --detach --pull never --wait
```

The named `beamers-data` volume is authoritative installation storage.
Do not run `docker compose down --volumes` unless intentionally destroying the installation.
The image declares the same volume so an ad hoc container does not write authoritative state into its disposable layer.

Compose uses an exec-form entrypoint, leaves Beamers as PID 1, sends `SIGTERM`, and enforces `stop_grace_period: 35s` as the platform kill deadline around the process's own 30-second `--shutdown-timeout`.
`mem_limit: 4G` matches the systemd profile's bound: half of the [reference hardware](capacity.md)'s 8 GB, leaving headroom for the host while covering the rated capacity envelope.

Compose's `healthcheck` only affects `docker compose up --wait` and `docker ps` status; Compose does not restart a container that is running but reports unhealthy.
An installation that stops passing `/readyz` while the process keeps running needs an external watchdog (a host-level `docker inspect --format '{{.State.Health.Status}}'` poll, or a systemd unit wrapping Compose) to notice and act; do not assume `restart: unless-stopped` alone covers this case, since it only restarts a container that has exited.
Apply the same care as the systemd watchdog below: require several consecutive unhealthy checks before restarting, and recognize that recovery mode reports unhealthy by design and needs an Administrator to restore, not a repeated `docker compose restart`.

### Recover a Docker deployment locally

A storage failure inside the container does not present as a normal HTTP error; the port stays open but `/readyz` fails and `beamers serve` reports recovery mode in its logs.
Use `docker compose exec beamers sh` to inspect logs or run read-only diagnostics while the container keeps running.
`serve` holds an exclusive lock on the installation for as long as the container runs, including while it is in recovery mode, so `restore preview` and `restore apply` cannot run inside that same running container: both need the same lock and fail with an in-use error.

Stop the service before cutover, then run the restore as a one-off container against the named volume, the same pattern used for `init` and `bootstrap`:

```sh
docker compose stop beamers
docker compose run --rm --no-deps beamers \
  restore preview \
  --input=/var/lib/beamers/BACKUP.zip \
  --data-dir=/var/lib/beamers/data
# review the printed plan, then apply the journal path it reports
docker compose run --rm --no-deps beamers \
  restore apply \
  --journal=/var/lib/beamers/data.beamers-restore.json \
  --acknowledge-replacement
docker compose up --detach --pull never --wait
```

The container image is read-only outside the mounted `beamers-data` volume and `/tmp`, so recovery commands can only write inside `/var/lib/beamers`, matching what a restore or export needs.

## Back up and restore configuration

Each successful service start records an allowlisted non-secret receipt of its effective listener, security, replication, storage-path, and shutdown configuration.
Native, systemd, and Compose Backups include that receipt in the versioned archive manifest and verify its SHA-256 digest.
The receipt contains configured TLS file paths but never certificate or private-key contents.
It contains only credential-free Litestream file URLs and never credentials or tokens.

An offline Backup uses the configuration from the last successful service start.
Restart Beamers after changing host deployment configuration so the receipt reflects the effective settings before creating a Backup.

Restore preview reports differences between the archived configuration and the target host configuration.
The supplied data and Attachment Store destinations are explicit path mappings.
Other unavailable paths or behavior differences require `--acknowledge-configuration-differences` when applying the prepared Restore.
Before cutover, an Administrator may explicitly cancel an intact prepared Restore through `restore cancel` or `/admin/restores/cancel`.
Restore does not rewrite native arguments, systemd units, Compose files, certificates, private keys, or replication destinations.

Restore enforces these fixed version-one archive limits before decompression:

- The compressed archive must not exceed 65 GiB.
- The archive may contain one manifest, one database, and at most 32,768 Attachment entries.
- The manifest and each Attachment must not exceed 64 MiB.
- The database must not exceed 64 GiB.
- All expanded entries together must not exceed 128 GiB.

Verification and extraction honor cancellation and deadlines, and a rejected Restore removes its private staging data.

## Readiness and shutdown

- `/livez` proves the process is alive.
- `/readyz` proves the installation can accept normal work.
- Startup is complete only after `/readyz` succeeds.
- Readiness becomes unavailable immediately when shutdown begins.

A custom service manager must pass its actual graceful-stop budget through `--shutdown-timeout`.
Do not add a sleeping pre-stop hook: it consumes the platform budget before Beamers receives the signal.

## Upgrade offline

Beamers never discovers, downloads, installs, or restarts for an upgrade.
The operator owns each step:

1. Verify the new artifact and preserve the prior binary or image.
2. Install the new binary at a distinct versioned path, or load the new image
   and select its tag in `.env`.
3. Stop Beamers and confirm the old process exited.
4. Point `/usr/local/bin/beamers` at the new versioned binary when using direct
   or systemd deployment.
5. Start the new artifact.
   A known safe migration creates and verifies its Backup, stages and validates the new state, and completes automatically.
6. If the new artifact requires approval, use its authenticated Administrator
   Maintenance Mode at `/admin/upgrade`.
7. Require `/readyz` to succeed before returning the installation to service.

The `beamers upgrade preview` and `beamers upgrade apply` subcommands use host operating-system authority.
Reserve them for recovery when the installation cannot authenticate an Administrator safely.
Run these commands from the verified new binary, or through Compose after selecting the verified new image, so the command knows the target migration.

If validation fails and the migration is not reader-compatible with the prior version, restore the verified pre-upgrade Backup with the prior binary or image.
Version one has no down migrations.

## Build release artifacts

Create a `docker-container` builder, then run the release build:

```sh
docker buildx create \
  --name beamers-release \
  --driver docker-container \
  --use
SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" \
  scripts/build-release.sh VERSION
docker buildx use default
docker buildx rm beamers-release
```

The builder fixes Linux AMD64, disables CGo, removes local paths and volatile Go build metadata, pins container inputs by digest, and emits checksums for the binary, OCI archive, and loadable Docker archive.
