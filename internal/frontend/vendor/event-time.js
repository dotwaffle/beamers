"use strict";

document.querySelectorAll("time[data-event-time]").forEach((element) => {
  try {
    const formatter = new Intl.DateTimeFormat(element.dataset.locale, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
      timeZone: element.dataset.timezone,
      timeZoneName: "short",
    });
    element.textContent = formatter.format(new Date(element.dateTime));
  } catch {
    // Keep the unambiguous server-rendered fallback.
  }
});
