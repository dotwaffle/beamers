# Require Secure Crew Origins

Version one supports two HTTPS deployments: the Go service may terminate TLS with an operator-supplied certificate and private key, or it may serve HTTP behind an explicitly configured trusted TLS reverse proxy.
Supporting both fits small self-contained venues and installations with existing infrastructure.

Crew login and authenticated control are refused over plain HTTP on non-loopback interfaces by default.
An Administrator may enable a conspicuously labeled insecure-LAN mode for an isolated deployment, accepting that network peers could observe credentials and sessions.
Enabling or disabling that mode is audited and the crew dashboard continuously warns while it is active.

Display Enrollment and authenticated Display connections also require HTTPS by default.
A separate insecure-Display mode may be enabled for constrained venue devices.
It does not permit Crew login or control over HTTP and carries its own persistent dashboard warning because a network peer could intercept Display credentials or content.
Its credentials retain only the existing least-privilege Display access.

Proxy-derived scheme and client information are trusted only from configured proxy addresses.
Certificate private keys follow the existing backup rule for private authentication material and are excluded by default.

Credential-bearing and recovery endpoints have conservative built-in abuse limits, including Crew login, Display Enrollment, Upload Links when introduced, and Administrator recovery.
These protections do not depend on a reverse proxy.
Public read-only routes favor conditional responses and caching; deployments may add broader perimeter limits without changing application semantics.
