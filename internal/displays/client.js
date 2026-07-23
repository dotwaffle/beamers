async function connectDisplay() {
  try {
    const response = await fetch(
      "/beamers.display.v1.DisplayService/GetSnapshot",
      {
        method: "POST",
        credentials: "same-origin",
        headers: {"Content-Type": "application/json"},
        body: "{}",
      },
    );
    if (!response.ok) {
      throw new Error(`snapshot request failed: ${response.status}`);
    }
    const {snapshot} = await response.json();
    renderSnapshot(snapshot);
    void acknowledgeSnapshot(snapshot);
    const query = new URLSearchParams({
      stream_id: snapshot.streamId,
      after: snapshot.streamPosition,
    });
    const events = new EventSource(`/display/events?${query}`);
    events.addEventListener("open", () => {
      document.documentElement.dataset.connection = "connected";
    });
    events.addEventListener("invalidate", () => {
      window.location.reload();
    });
    events.addEventListener("error", () => {
      document.documentElement.dataset.connection = "disconnected";
    });
  } catch {
    document.documentElement.dataset.connection = "disconnected";
  }
}

function renderSnapshot(snapshot) {
  const main = document.createElement("main");
  if (snapshot.standby) {
    appendHeading(main, "Standby");
    appendParagraph(main, `Display: ${snapshot.displayName}`);
    if (snapshot.eventName) {
      appendParagraph(main, `Active Event: ${snapshot.eventName}`);
    }
    appendParagraph(main, "This Display has no Assignment for the Active Event.");
    replaceMain(main);
    return;
  }
  if (snapshot.viewKey === "event-overview") {
    appendHeading(main, "Event Overview");
    appendParagraph(main, `Location: ${snapshot.locationName}`);
    appendParagraph(main, `View: ${snapshot.viewKey}`);
    for (const session of snapshot.sessions ?? []) {
      const article = document.createElement("article");
      appendHeading(article, session.title, 2);
      appendSessionSchedule(article, snapshot, session);
      appendOptionalParagraph(article, session.speaker);
      appendOptionalParagraph(article, session.publicDetails);
      main.append(article);
    }
    replaceMain(main);
    return;
  }
  if (snapshot.viewKey === "location-signage") {
    appendHeading(main, "Location Signage");
    appendParagraph(main, `Location: ${snapshot.locationName}`);
    for (const session of snapshot.sessions ?? []) {
      const article = document.createElement("article");
      if (session.unavailable) {
        appendParagraph(article, session.availabilityMessage);
      } else {
        appendHeading(article, session.title, 2);
        appendSessionSchedule(article, snapshot, session);
        appendOptionalParagraph(article, session.speaker);
        appendOptionalParagraph(article, session.publicDetails);
      }
      main.append(article);
    }
    replaceMain(main);
    return;
  }
  appendHeading(main, snapshot.eventName);
  appendParagraph(main, `Display: ${snapshot.displayName}`);
  appendParagraph(main, `Location: ${snapshot.locationName}`);
  appendParagraph(main, `View: ${snapshot.viewKey}`);
  replaceMain(main);
}

function appendSessionSchedule(parent, snapshot, session) {
  appendParagraph(parent, `Status: ${session.lifecycle}`);
  appendParagraph(
    parent,
    `Forecast Time: ${formatScheduleTime(snapshot, session.forecastStart)}–` +
      formatScheduleTime(snapshot, session.forecastEnd),
  );
}

function formatScheduleTime(snapshot, value) {
  return new Intl.DateTimeFormat("en", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: snapshot.eventTimezone || "UTC",
  }).format(new Date(value));
}

async function acknowledgeSnapshot(snapshot) {
  try {
    await fetch(
      "/beamers.display.v1.DisplayService/Acknowledge",
      {
        method: "POST",
        credentials: "same-origin",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({
          protocolVersion: snapshot.protocolVersion,
          streamId: snapshot.streamId,
          streamPosition: snapshot.streamPosition,
          activeEventId: snapshot.activeEventId,
          activationGeneration: snapshot.activationGeneration,
          publishedRevision: snapshot.publishedRevision,
          snapshotToken: snapshot.snapshotToken,
        }),
      },
    );
  } catch {
    // Applied-state reporting never replaces the last committed frame.
  }
}

function appendHeading(parent, text, level = 1) {
  const heading = document.createElement(`h${level}`);
  heading.textContent = text;
  parent.append(heading);
}

function appendParagraph(parent, text) {
  const paragraph = document.createElement("p");
  paragraph.textContent = text;
  parent.append(paragraph);
}

function appendOptionalParagraph(parent, text) {
  if (text) {
    appendParagraph(parent, text);
  }
}

function replaceMain(main) {
  document.querySelector("main")?.replaceWith(main);
}

void connectDisplay();
