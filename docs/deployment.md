# Deploy Beamers at a venue

Beamers supports Linux AMD64 as a direct CGo-free binary, a systemd service, and Docker Compose.
All three profiles use:

- database state under `/var/lib/beamers/data`;
- the Attachment Store under `/var/lib/beamers/attachments`;
- direct TLS on port 8443; and
- a ten-second graceful-stop budget.

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
  --shutdown-timeout=10s
```

Send `SIGINT` or `SIGTERM` directly to this process.
Do not place it behind a shell wrapper that remains PID 1.

## Run with systemd

Install the supplied unit after the binary and persistent state are ready:

```sh
install -o root -g root -m 0644 deploy/systemd/beamers.service \
  /etc/systemd/system/beamers.service
systemctl daemon-reload
systemctl enable --now beamers.service
```

The unit starts only when `/var/lib/beamers/data/beamers.db` exists.
It sends `SIGTERM` to the Beamers process and gives it the same ten-second budget configured with `--shutdown-timeout`.

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

Compose uses an exec-form entrypoint, leaves Beamers as PID 1, sends `SIGTERM`, and enforces the same ten-second budget as the process.

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
Restore does not rewrite native arguments, systemd units, Compose files, certificates, private keys, or replication destinations.

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

## Reproduce release artifacts

With the repository's pinned Go toolchain and Docker Buildx:

```sh
SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" \
  scripts/build-release.sh VERSION
```

The builder fixes Linux AMD64, disables CGo, removes local paths and volatile Go build metadata, pins container inputs by digest, and emits checksums for the binary, OCI archive, and loadable Docker archive.
