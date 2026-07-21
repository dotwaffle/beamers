# Preserve planned, forecast, and actual times

The application preserves Planned Time, calculates Forecast Time from live adjustments, and records Actual Time for each Session Run from crew-confirmed Session progression.
Extensions and Pull Forward recalculate forecasts without erasing the original plan, while End Now records reality without implicitly rewriting later Sessions.
Keeping all three layers makes audience updates, crew decisions, and later review explainable.

Public presentation suppresses insignificant timing noise without changing Actual Time.
For a Session planned to last more than ten minutes, an Actual Start or End within two minutes either side of the last publicly Communicated Time is shown as that communicated time.
Larger deviations show Actual Time, while crew history and audit always retain the exact value.

The public Schedule emphasizes the current operational truth.
Scheduled Sessions show Forecast Time and identify a changed Planned Time as "was ...".
Live Sessions show normalized Actual Start with Forecast End or remaining time.
Ended Sessions show normalized Actual Time.
Canceled Sessions show their last Forecast Time with the Public Cancellation Message.
