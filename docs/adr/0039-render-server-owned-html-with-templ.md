# Render server-owned HTML with templ

Version one renders public, enrollment, Crew, and control pages with templ.
Components produce both complete documents and reusable partial responses.
Handlers call application services directly and never expose Ent entities as view models.

Htmx provides modest hypermedia interactions such as forms, filtering, navigation, validation, and partial replacement.
Routes return useful complete pages for direct navigation where appropriate rather than trusting the `HX-Request` header as an authorization or correctness boundary.

The revisioned state stream in ADR 0038 remains independent of htmx.
Small, purpose-built JavaScript modules implement Display rendering, clocks, presentation effects, state-stream recovery, and control behavior that is not cleanly expressed as an HTML exchange.
Version one does not use htmx's SSE extension or Datastar's DOM-patch and signal protocol for authoritative state.

Pinned htmx and application assets are embedded in the Go binary; deployment does not depend on a CDN or Node build.
The Content Security Policy uses external scripts and forbids inline handlers or evaluated expressions.
Partial swaps preserve keyboard operation, focus, validation errors, loading state, and assistive-technology announcements required by ADR 0032.

Datastar and GraphQL remain candidates only if a later concrete client need justifies their additional reactive state or query surfaces.
