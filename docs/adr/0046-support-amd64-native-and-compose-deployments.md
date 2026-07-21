# Support AMD64 native and Compose deployments

Version one officially supports the server on Linux AMD64, distributed as a standalone CGo-free binary and an OCI image.
The tested operating modes are a direct process, a supplied systemd service example, and a supplied Docker Compose example with explicit persistent storage.
They run the same server and use the same configuration and data layout.

Linux ARM64 server builds are deferred until the project has suitable test infrastructure or hardware.
Raspberry Pi remains a Display or operator-console target rather than a server target.
Kubernetes may be documented for informed operators but is not a tested version-one deployment profile.

Fly.io is a possible experimental hosted profile after version one, reusing the same OCI image.
It is not equivalent to the supported local Compose profile: it requires one authoritative Machine, persistent Volumes for database and attachments, external full-fidelity replication, and disabled autostop during live operation.
It does not replace the venue-local default in ADR 0010.
