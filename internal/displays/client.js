const snapshotPath = "/beamers.display.v1.DisplayService/GetSnapshot";
const acknowledgePath = "/beamers.display.v1.DisplayService/Acknowledge";
const expectedProtocol = document.documentElement.dataset.protocolVersion;
const expectedAsset = document.documentElement.dataset.assetVersion;
const healthRefreshMilliseconds = 10000;
const maximumBackoffMilliseconds = 15000;
const reloadLoopWindowMilliseconds = 60000;

let appliedSnapshot;
let clockReference = {
  serverMilliseconds: Date.now(),
  monotonicMilliseconds: performance.now(),
};
let clockOffsetMilliseconds = 0;
let clockUncertaintyMilliseconds = 0;
let clockTimer;
let eventSource;
let healthTimer;
let recoveryAttempt = 0;
let recoveryGeneration = 0;
let recoveryTimer;
let rendererFailures = 0;
let rotationTimer;

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
      renderSnapshot(snapshot, offset);
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
      renderSnapshot(snapshot, offset);
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

function renderSnapshot(snapshot, offset) {
  const composition = snapshot.composition;
  if (!composition?.layout?.key || !Array.isArray(composition.layout.regions) ||
      !composition.theme) {
    throw new Error("snapshot composition is invalid");
  }
  const renderKey = JSON.stringify({
    activeEventId: snapshot.activeEventId,
    publishedRevision: snapshot.publishedRevision,
    locationId: snapshot.locationId,
    viewKey: snapshot.viewKey,
    standby: snapshot.standby,
    sessions: snapshot.sessions,
    stageTimer: snapshot.stageTimer,
    composition,
  });
  const candidateClockReference = {
    serverMilliseconds: Date.now() + offset,
    monotonicMilliseconds: performance.now(),
  };
  if (document.querySelector("main")?.dataset.renderKey === renderKey) {
    clockReference = candidateClockReference;
    return;
  }
  const main = document.createElement("main");
  let startClockUpdates;
  main.dataset.renderKey = renderKey;
  main.dataset.layout = composition.layout.key;
  main.className = [
    "display-view",
    `display-layout-${controlledToken(composition.layout.key, [
      "standby", "event-overview", "location-signage", "stage-timer", "competition-output",
    ])}`,
    `display-font-${controlledToken(composition.theme.font, ["sans", "serif", "mono"])}`,
    `display-background-${controlledToken(composition.theme.background, ["solid", "variable-media"])}`,
    `display-transition-${controlledToken(composition.theme.transition, ["none", "fade"])}`,
  ].join(" ");
  main.style.setProperty("--display-foreground", composition.theme.foregroundColor);
  main.style.setProperty("--display-background", composition.theme.backgroundColor);
  main.style.setProperty("--display-accent", composition.theme.accentColor);
  main.style.setProperty("--display-scrim", composition.theme.scrimColor);
  main.style.setProperty("--display-scrim-opacity", composition.theme.scrimOpacity / 100);
  const alpha = Math.round((composition.theme.scrimOpacity / 100) * 255)
    .toString(16)
    .padStart(2, "0");
  main.style.setProperty(
    "--display-scrim-layer",
    `${composition.theme.scrimColor}${alpha}`,
  );

  for (const configuredRegion of composition.layout.regions) {
    const region = document.createElement("section");
    region.dataset.region = configuredRegion.name;
    region.dataset.widget = configuredRegion.widget;
    region.dataset.persistent = String(Boolean(configuredRegion.persistent));
    region.className = "display-region";
    const startWidgetUpdates = renderWidget(
      region,
      configuredRegion.widget,
      snapshot,
      composition.theme,
      candidateClockReference,
    );
    if (startWidgetUpdates) {
      startClockUpdates = startWidgetUpdates;
    }
    main.append(region);
  }
  replaceMain(main);
  clockReference = candidateClockReference;
  clearTimeout(clockTimer);
  startClockUpdates?.();
  startRotation(main, composition.layout.rotationSeconds);
}

function controlledToken(value, allowed) {
  if (!allowed.includes(value)) {
    throw new Error("snapshot composition token is invalid");
  }
  return value;
}

function renderWidget(region, widget, snapshot, theme, candidateClockReference) {
  switch (widget) {
  case "branding":
    if (snapshot.standby) {
      appendParagraph(region, theme.branding || snapshot.eventName || "Beamers");
    } else {
      appendHeading(region, theme.branding || snapshot.eventName || "Beamers");
      if (snapshot.viewKey === "event-overview") {
        appendParagraph(region, "Event Overview");
        appendParagraph(region, `Location: ${snapshot.locationName}`);
      }
    }
    return;
  case "standby":
    appendHeading(region, "Standby");
    appendParagraph(region, `Display: ${snapshot.displayName}`);
    if (snapshot.eventName) {
      appendParagraph(region, `Active Event: ${snapshot.eventName}`);
    }
    appendParagraph(region, "This Display has no Assignment for the Active Event.");
    return;
  case "location":
    appendHeading(region, snapshot.locationName);
    appendParagraph(region, "Location Signage");
    return;
  case "now-next":
    appendHeading(region, "Now / Next", 2);
    for (const session of (snapshot.sessions ?? [])
      .filter((candidate) => candidate.lifecycle !== "Canceled")
      .slice(0, 2)) {
      const article = document.createElement("article");
      renderSession(article, snapshot, session);
      region.append(article);
    }
    return;
  case "rotation":
    for (const session of snapshot.sessions ?? []) {
      const article = document.createElement("article");
      article.dataset.rotationPage = "true";
      article.hidden = region.children.length > 0;
      renderSession(article, snapshot, session);
      region.append(article);
    }
    if (region.children.length === 0) {
      appendParagraph(region, "No public Event information is currently scheduled.");
    }
    return;
  case "clock": {
    const clock = document.createElement("time");
    clock.dataset.displayClock = "true";
    const startUpdates = prepareClock(clock, snapshot, candidateClockReference);
    region.append(clock);
    return startUpdates;
  }
  case "stage-timer":
    return prepareStageTimer(region, snapshot, candidateClockReference);
  case "program-output":
    appendHeading(region, "Program Output");
    return;
  default:
    throw new Error("snapshot widget is invalid");
  }
}

function prepareClock(clock, snapshot, reference) {
  const update = () => {
    const current = new Date(estimatedServerNow());
    clock.dateTime = current.toISOString();
    clock.textContent = new Intl.DateTimeFormat("en", {
      hour: "2-digit", minute: "2-digit", timeZone: snapshot.eventTimezone || "UTC",
    }).format(current);
    clockTimer = setTimeout(update, 60000);
  };
  const current = new Date(estimatedServerNow(reference));
  clock.dateTime = current.toISOString();
  clock.textContent = new Intl.DateTimeFormat("en", {
    hour: "2-digit", minute: "2-digit", timeZone: snapshot.eventTimezone || "UTC",
  }).format(current);
  return () => {
    clockTimer = setTimeout(update, 60000);
  };
}

function prepareStageTimer(region, snapshot, reference) {
  const timer = snapshot.stageTimer;
  if (!timer) {
    appendHeading(region, "Stage Timer");
    appendParagraph(region, "No Session is live at this Location.");
    return;
  }
  appendHeading(region, timer.title || "Live Session");
  const direction = document.createElement("p");
  const clock = document.createElement("time");
  const emphasis = document.createElement("p");
  const adjustmentNotice = prepareTimerAdjustmentNotice(timer, reference);
  clock.dataset.stageTimerClock = "true";
  emphasis.dataset.timerEmphasisLabel = "true";
  region.append(direction, clock, emphasis);
  if (adjustmentNotice) {
    region.append(adjustmentNotice);
  }
  if (timer.mode === "STAGE_TIMER_MODE_ELAPSED" && timer.forecastEnd) {
    const forecastEnd = new Date(timer.forecastEnd);
    if (!Number.isFinite(forecastEnd.getTime())) {
      throw new Error("Stage Timer Forecast End is invalid");
    }
    appendParagraph(region, `Forecast End: ${new Intl.DateTimeFormat("en", {
      hour: "2-digit",
      minute: "2-digit",
      timeZone: snapshot.eventTimezone || "UTC",
    }).format(forecastEnd)}`);
  }

  const update = (currentReference) => {
    const now = estimatedServerNow(currentReference);
    const anchor = Date.parse(timer.anchor);
    if (!Number.isFinite(anchor)) {
      throw new Error("Stage Timer anchor is invalid");
    }
    const countdown = timer.mode === "STAGE_TIMER_MODE_COUNTDOWN";
    const elapsed = timer.mode === "STAGE_TIMER_MODE_ELAPSED";
    if (!countdown && !elapsed) {
      throw new Error("Stage Timer mode is invalid");
    }
    const delta = countdown ? anchor - now : now - anchor;
    const overtime = countdown && delta < 0;
    const duration = Math.max(Math.abs(delta), 0);
    clock.dateTime = new Date(anchor).toISOString();
    clock.textContent = `${overtime ? "+" : ""}${formatTimerDuration(
      duration,
      countdown && !overtime,
    )}`;
    direction.textContent = overtime ? "Overtime" : (elapsed ? "Elapsed" : "Remaining");
    const level = timerEmphasis(timer.thresholds ?? [], countdown ? delta : Infinity);
    region.dataset.timerEmphasis = level;
    region.dataset.timerOvertime = String(overtime);
    emphasis.textContent = level === "normal"
      ? ""
      : level[0].toUpperCase() + level.slice(1);
  };
  update(reference);
  return () => {
    const tick = () => {
      update();
      clockTimer = setTimeout(tick, 250);
    };
    clockTimer = setTimeout(tick, 250);
  };
}

function prepareTimerAdjustmentNotice(timer, reference) {
  const seconds = Number(timer.adjustmentSeconds);
  if (!Number.isSafeInteger(seconds) || seconds === 0 || !timer.adjustmentNoticeExpiresAt) {
    return null;
  }
  const expiresAt = Date.parse(timer.adjustmentNoticeExpiresAt);
  if (!Number.isFinite(expiresAt) || estimatedServerNow(reference) >= expiresAt) {
    return null;
  }
  const absolute = Math.abs(seconds);
  const notice = document.createElement("p");
  notice.dataset.timerAdjustmentNotice = "true";
  notice.textContent = `Time adjusted: ${seconds > 0 ? "+" : "-"}${Math.floor(absolute / 60)}:` +
    String(absolute % 60).padStart(2, "0");
  const hide = () => {
    if (estimatedServerNow() >= expiresAt) {
      notice.hidden = true;
      return;
    }
    setTimeout(hide, Math.min(expiresAt - estimatedServerNow(), 250));
  };
  setTimeout(hide, Math.min(expiresAt - estimatedServerNow(reference), 250));
  return notice;
}

function estimatedServerNow(reference = clockReference) {
  return reference.serverMilliseconds +
    (performance.now() - reference.monotonicMilliseconds);
}

function formatTimerDuration(milliseconds, roundUp) {
  const seconds = roundUp
    ? Math.ceil(milliseconds / 1000)
    : Math.floor(milliseconds / 1000);
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:` +
    String(seconds % 60).padStart(2, "0");
}

function timerEmphasis(thresholds, remainingMilliseconds) {
  let result = "normal";
  for (const threshold of thresholds) {
    const remainingSeconds = Number(threshold.remainingSeconds);
    if (!Number.isFinite(remainingSeconds) || remainingSeconds <= 0) {
      throw new Error("Stage Timer threshold is invalid");
    }
    if (remainingMilliseconds <= remainingSeconds * 1000) {
      switch (threshold.emphasis) {
      case "TIMER_EMPHASIS_ATTENTION":
        result = "attention";
        break;
      case "TIMER_EMPHASIS_URGENT":
        result = "urgent";
        break;
      default:
        throw new Error("Stage Timer emphasis is invalid");
      }
    }
  }
  return result;
}

function renderSession(parent, snapshot, session) {
  if (session.unavailable) {
    appendParagraph(parent, session.availabilityMessage);
    return;
  }
  appendHeading(parent, session.title, 3);
  appendSessionSchedule(parent, snapshot, session);
  appendOptionalParagraph(parent, session.speaker);
  appendOptionalParagraph(parent, session.publicDetails);
}

function startRotation(main, seconds) {
  clearTimeout(rotationTimer);
  const pages = findRotationPages(main);
  if (pages.length < 2 || !Number.isInteger(seconds) || seconds < 5 || seconds > 300) {
    return;
  }
  let active = 0;
  const advance = () => {
    pages[active].hidden = true;
    active = (active + 1) % pages.length;
    pages[active].hidden = false;
    rotationTimer = setTimeout(advance, seconds * 1000);
  };
  rotationTimer = setTimeout(advance, seconds * 1000);
}

function findRotationPages(main) {
  const pages = [];
  for (const region of main.children) {
    if (region.dataset.widget === "rotation") {
      pages.push(
        ...Array.from(region.children).filter(
          (child) => child.dataset.rotationPage === "true",
        ),
      );
    }
  }
  return pages;
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
