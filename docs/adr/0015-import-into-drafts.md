# Import schedules into Drafts

A one-way schedule import produces Draft changes and never mutates live state.
Before Publish, the Crew Member sees validation failures, additions, updates, conflicts, ignored fields, and unresolved Lane or Location mappings.
A later re-import may propose updates but cannot silently synchronize or overwrite authoritative data.
Import/export is domain interchange and remains separate from exact backup and restore.

CSV keys and iCalendar UIDs are retained as Import References.
A later import may use them to detect likely duplicates and offer an explicit mapping, but an Import Reference never becomes canonical Session identity and never authorizes an automatic overwrite.
A match produces field-level Draft proposals; absent source fields retain their existing values, and no deletion is inferred.
Crew Notes, Attachments, lifecycle, and Session Runs are never import targets.
Duplicate Import References in one source are blocking conflicts.
A Crew Member may accept proposals individually or in a reviewed bulk selection before Publish.
Once a Session has a Session Run, a repeat import may propose only descriptive Public Details.
Imported changes to timing, Session type, Lane/Location placement, or Competition Entry ordering are conflicts.
Crew Members may make deliberate audited corrections or reschedule outside the import workflow.

Supported external import formats are CSV and iCalendar.
CSV templates cover Sessions and Competition Entries with explicit field mapping. iCalendar offers a convenient but intentionally lossy Session import; unsupported timing, Competition, and live-control fields receive defaults or require mapping during preview. pretalx, frab, Partymeister, and Wuhu integrations are out of scope for the foreseeable future.
