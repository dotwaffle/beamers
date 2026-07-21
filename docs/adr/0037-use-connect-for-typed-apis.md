# Use Connect for typed APIs

Version one exposes programmatic commands and queries through ConnectRPC using versioned Protocol Buffer contracts.
Browser callers use the Connect protocol's JSON encoding by default.
Server-rendered HTML handlers call the same application services directly rather than issuing loopback RPCs.

RPC messages are transport contracts, not Ent entities or the domain model.
Commands carry the idempotency and expected-revision values required by their semantics.
Connect error codes and typed details communicate stable machine failures without exposing internal errors.

Interceptors establish authenticated viewer context and handle request IDs, validation, tracing, and error translation.
Ent privacy remains the final authorization boundary.
Same-origin browser protections, request-size limits, deadlines, and abuse controls apply independently of Connect's protocol support.

Version one begins with unary RPCs.
Persistent Display updates use a separately chosen transport.
Server-rendered pages do not require generated browser clients, and GraphQL is deferred until an exploratory-query consumer justifies its larger schema and authorization surface.

Protocol generation is reproducible.
Protobuf sources and generated artifacts are committed, and CI runs Buf lint and breaking-change checks.
Packages use an explicit version namespace so later compatible evolution does not expose the persistence schema.
