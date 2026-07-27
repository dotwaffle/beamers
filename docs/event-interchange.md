# Event Interchange Format

Beamers Event interchange is a vendor-neutral authoring format for moving one Event between installations.
It is separate from Backup and Restore and does not preserve operational history.

The current format identifier is `beamers.event`.
The current version is `2`.
An importer accepts versions `1` and `2` and rejects any other format or version without creating an Event.

## Workflow

Export requires Producer authority for the source Event.
Export is a commanded operation with a Command Receipt and Audit Entry.
It reads the current Published Rundown because Published structure is the authoritative portable snapshot.

Import review requires Administrator authority and has no side effects.
Review validates the entire document and returns a SHA-256 fingerprint plus Event and structure counts.

Import requires the exact reviewed bytes, fingerprint, and a Command ID.
It creates a new inactive Event, grants the importing Administrator the Producer role, and creates one complete Rundown Draft.
The Event configuration, Producer Grant, Draft changes, Command Receipt, and Audit Entry commit in one transaction.
The imported Rundown remains unpublished until the normal Draft review and Publish workflow completes.

## Version 2 document

Version 2 is strict UTF-8 JSON with this shape: One document must not exceed 64 MiB.

```json
{
  "format": "beamers.event",
  "version": 2,
  "event": {
    "name": "Revision 2026",
    "planned_start_date": "2026-08-21",
    "planned_end_date": "2026-08-23",
    "timezone": "Europe/Berlin",
    "event_locale": "de-DE",
    "content_language": "en",
    "event_day_boundary": "06:00",
    "entry_default_disposition": "Pending",
    "submission_eligibility": "AllAccounts",
    "target_adjustment_presets_seconds": [-300, 300, 600]
  },
  "locations": [
    {"ref": "location-1", "name": "Main Hall"}
  ],
  "lanes": [
    {
      "ref": "lane-1",
      "name": "Main Lane",
      "location_ref": "location-1"
    }
  ],
  "tracks": [
    {"ref": "track-1", "name": "Systems"}
  ],
  "sessions": [
    {
      "ref": "session-1",
      "title": "Opening Session",
      "type": "Ceremony",
      "audience_visibility": "Public",
      "planned_start": "2026-08-21T09:00:00+02:00",
      "planned_end": "2026-08-21T10:00:00+02:00",
      "timing_policy": "FixedEnd",
      "minimum_duration_seconds": 1800,
      "start_boundary": "Hard",
      "end_boundary": "Soft",
      "lane_refs": ["lane-1"],
      "location_refs": ["location-1"],
      "track_refs": ["track-1"]
    }
  ]
}
```

The Event object contains every value accepted by Event creation.
Locations contain `ref` and `name`.
Lanes contain `ref`, `name`, and `location_ref`.
Tracks contain `ref` and `name`.
Sessions contain `ref`, title, optional speaker, type, Audience Visibility, optional Public Details, Planned Start and End, Timing Policy, minimum duration in seconds, boundaries, optional Upload or Submission Deadline, optional Competition entry default, and relationship refs.

Refs are deterministic document-local identities and never expose database IDs.
Every ref must be non-empty and unique across the document.
Refs use the command identifier character rules: at most 200 Unicode code points with no whitespace or control characters.
Every relationship ref must resolve to an object of the required kind in the same document.
Refs do not establish lineage or authorize later updates to another Event.

Arrays use source identity order.
Relationship arrays represent sets and must not contain duplicates.
Export orders relationship refs deterministically.
An exported document imported into a new Event and Published re-exports as the same version 2 bytes.

Version 1 documents remain importable.
They predate Submission Eligibility and import with the `AllAccounts` default.

## Validation and preservation

The decoder rejects malformed or invalid UTF-8, unknown fields, duplicate identities, missing or wrong-kind relationship refs, invalid Event configuration, and invalid Rundown structure.
It validates every object and relationship before starting Event mutation.
Rejected import commands record rejection evidence but leave no partial Event, Grant, or Draft state.

Text remains the same Unicode code-point sequence and is not normalized.
Documents that would require trimming or language-tag canonicalization are rejected instead of silently changing text.
Text length and control-character rules are the same rules used by Event and Rundown commands.

Dates use `YYYY-MM-DD`.
Event Day Boundary uses local `HH:MM`.
Planned times and deadlines use RFC 3339 with an explicit numeric offset.
The offset must match the Event's IANA timezone at that instant.
This preserves the selected occurrence of a repeated local time and rejects fabricated offsets.

## Deliberate exclusions

Version 2 excludes Crew Notes, matching the external-import boundary in ADR 0015.
It also excludes Draft and Published history, lifecycle and live timing state, Session Runs, Competition Entries, Results, Awards, Prizegiving state, Attachments and bytes, Accounts and Event Grants, Displays, Command Receipts, Audit Entries, credentials, generated publications, and Backup or Restore metadata.

Add a new format version when portable authoring needs content that version 2 cannot represent as a reviewable Rundown Draft.
