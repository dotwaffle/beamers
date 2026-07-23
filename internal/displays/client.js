const snapshotPath = "/beamers.display.v1.DisplayService/GetSnapshot";
const acknowledgePath = "/beamers.display.v1.DisplayService/Acknowledge";
const expectedProtocol = document.documentElement.dataset.protocolVersion;
const expectedAsset = document.documentElement.dataset.assetVersion;
const healthRefreshMilliseconds = 10000;
const maximumBackoffMilliseconds = 15000;
const reloadLoopWindowMilliseconds = 60000;

let appliedSnapshot;
let clockOffsetMilliseconds = 0;
let clockUncertaintyMilliseconds = 0;
let eventSource;
let healthTimer;
let recoveryAttempt = 0;
let recoveryGeneration = 0;
let recoveryTimer;
let rendererFailures = 0;

async function recoverDisplay(reason = "reconnecting") {
  const generation = ++recoveryGeneration;
  clearTimeout(recoveryTimer);
  clearTimeout(healthTimer);
  eventSource?.close();
  setConnection(reason, reason === "connecting" ? "Connecting…" : "Connection lost. Reconnecting…");

  try {
    const {snapshot, offset, uncertainty} = await fetchSnapshot();
    if (generation !== recoveryGeneration) {
      return;
    }
    if (!snapshotCompatible(snapshot)) {
      await controlledReload(snapshot?.assetVersion);
      return;
    }
    try {
      renderSnapshot(snapshot);
      rendererFailures = 0;
    } catch {
      rendererFailures++;
      void reportRendererFailure();
      if (rendererFailures >= 3) {
        await controlledReload(snapshot.assetVersion);
      } else {
        scheduleRecovery();
      }
      return;
    }
    appliedSnapshot = snapshot;
    clockOffsetMilliseconds = offset;
    clockUncertaintyMilliseconds = uncertainty;
    recoveryAttempt = 0;
    void acknowledgeSnapshot(snapshot, false);
    openEventStream(snapshot);
    scheduleHealthRefresh();
  } catch {
    if (generation === recoveryGeneration) {
      if (recoveryAttempt >= 3) {
        await controlledReload();
      } else {
        scheduleRecovery();
      }
    }
  }
}

async function fetchSnapshot() {
  const startedAt = Date.now();
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch(snapshotPath, {
      method: "POST",
      credentials: "same-origin",
      headers: {"Content-Type": "application/json"},
      body: "{}",
      cache: "no-store",
      signal: controller.signal,
    });
    if (!response.ok) {
      throw new Error(`snapshot request failed: ${response.status}`);
    }
    const {snapshot} = await response.json();
    const completedAt = Date.now();
    const serverTime = Date.parse(snapshot?.serverTime);
    if (!Number.isFinite(serverTime)) {
      throw new Error("snapshot server time is invalid");
    }
    return {
      snapshot,
      offset: Math.round(serverTime - ((startedAt + completedAt) / 2)),
      uncertainty: Math.round((completedAt - startedAt) / 2),
    };
  } finally {
    clearTimeout(timeout);
  }
}

function snapshotCompatible(snapshot) {
  return snapshot?.protocolVersion === expectedProtocol &&
    snapshot?.assetVersion === expectedAsset &&
    typeof snapshot.streamId === "string" &&
    snapshot.streamId !== "" &&
    snapshot.streamPosition !== undefined;
}

function openEventStream(snapshot) {
  const query = new URLSearchParams({
    stream_id: snapshot.streamId,
    after: snapshot.streamPosition,
  });
  eventSource = new EventSource(`/display/events?${query}`);
  eventSource.addEventListener("open", () => {
    recoveryAttempt = 0;
    setConnection("connected", "Connected");
  });
  eventSource.addEventListener("invalidate", (event) => {
    let invalidation;
    try {
      invalidation = JSON.parse(event.data);
    } catch {
      scheduleRecovery(0);
      return;
    }
    if (invalidation.protocol_version !== expectedProtocol ||
        invalidation.asset_version !== expectedAsset) {
      void controlledReload(invalidation.asset_version);
      return;
    }
    scheduleRecovery(0);
  });
  eventSource.addEventListener("error", () => {
    scheduleRecovery();
  });
}

function scheduleRecovery(delay) {
  eventSource?.close();
  clearTimeout(healthTimer);
  clearTimeout(recoveryTimer);
  setConnection("disconnected", "Connection lost. Reconnecting…");
  const wait = delay ?? recoveryBackoff();
  recoveryTimer = setTimeout(() => void recoverDisplay("reconnecting"), wait);
}

function recoveryBackoff() {
  const base = Math.min(500 * (2 ** recoveryAttempt), maximumBackoffMilliseconds);
  recoveryAttempt++;
  return Math.round(base * (0.75 + (Math.random() * 0.5)));
}

function scheduleHealthRefresh() {
  clearTimeout(healthTimer);
  healthTimer = setTimeout(() => void refreshHealth(), healthRefreshMilliseconds);
}

async function refreshHealth() {
  try {
    const {snapshot, offset, uncertainty} = await fetchSnapshot();
    if (!snapshotCompatible(snapshot)) {
      await controlledReload(snapshot?.assetVersion);
      return;
    }
    if (!appliedSnapshot || snapshot.streamId !== appliedSnapshot.streamId) {
      void recoverDisplay("reconnecting");
      return;
    }
    try {
      renderSnapshot(snapshot);
      rendererFailures = 0;
    } catch {
      rendererFailures++;
      void reportRendererFailure();
      if (rendererFailures >= 3) {
        await controlledReload(snapshot.assetVersion);
      } else {
        scheduleHealthRefresh();
      }
      return;
    }
    appliedSnapshot = snapshot;
    clockOffsetMilliseconds = offset;
    clockUncertaintyMilliseconds = uncertainty;
    void acknowledgeSnapshot(snapshot, false);
    scheduleHealthRefresh();
  } catch {
    scheduleRecovery();
  }
}

async function controlledReload(assetVersion) {
  setConnection("incompatible", "Updating Display…");
  try {
    const response = await fetch("/display", {
      credentials: "same-origin",
      cache: "no-store",
      headers: {"X-Beamers-Display-Preflight": "1"},
    });
    const availableAsset = response.headers.get("X-Beamers-Display-Asset");
    if (!response.ok || !availableAsset ||
        (assetVersion && availableAsset !== assetVersion)) {
      scheduleRecovery();
      return;
    }
    const guardKey = `beamers-display-reload:${availableAsset}`;
    const lastReload = Number(sessionStorage.getItem(guardKey) || "0");
    if (Date.now() - lastReload < reloadLoopWindowMilliseconds) {
      scheduleRecovery();
      return;
    }
    sessionStorage.setItem(guardKey, String(Date.now()));
    window.location.reload();
  } catch {
    scheduleRecovery();
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

async function acknowledgeSnapshot(snapshot, rendererUnstable) {
  try {
    await fetch(acknowledgePath, {
      method: "POST",
      credentials: "same-origin",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({
        protocolVersion: snapshot.protocolVersion,
        assetVersion: snapshot.assetVersion,
        streamId: snapshot.streamId,
        streamPosition: snapshot.streamPosition,
        activeEventId: snapshot.activeEventId,
        activationGeneration: snapshot.activationGeneration,
        publishedRevision: snapshot.publishedRevision,
        standby: snapshot.standby,
        clockOffsetMilliseconds,
        clockUncertaintyMilliseconds,
        rendererUnstable,
        snapshotToken: snapshot.snapshotToken,
      }),
    });
  } catch {
    // Applied-state reporting never replaces the last committed frame.
  }
}

async function reportRendererFailure() {
  if (appliedSnapshot) {
    await acknowledgeSnapshot(appliedSnapshot, true);
  }
}

function setConnection(state, message) {
  document.documentElement.dataset.connection = state;
  const indicator = document.querySelector("#display-connection");
  if (indicator) {
    indicator.dataset.connection = state;
    indicator.textContent = message;
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

void recoverDisplay("connecting");
