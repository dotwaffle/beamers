# Limit Version-One Localization

Version one ships English application chrome for Crew, public, Enrollment, and Display interfaces.
It accepts and preserves full Unicode Event content.
Each Event defines an Event Locale used for document language metadata and regional date, time, and number formatting, separately from its authoritative timezone.

Individual content may carry a Content Language tag when it differs from the Event Locale so assistive technology receives correct language metadata.
Version one does not provide translated application strings or parallel translated copies of Event fields.

Interface strings remain isolated from domain content so later translation does not require changing stored Event data or public identifiers.
The absence of translations is a scope boundary, not permission to omit language metadata.
