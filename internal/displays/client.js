const snapshotPath = "/beamers.display.v1.DisplayService/GetSnapshot";
const acknowledgePath = "/beamers.display.v1.DisplayService/Acknowledge";
const expectedProtocol = document.documentElement.dataset.protocolVersion;
const expectedAsset = document.documentElement.dataset.assetVersion;
const healthRefreshMilliseconds = 10000;
const maximumBackoffMilliseconds = 15000;
const maximumClockUncertaintyMilliseconds = 250;
const reloadLoopWindowMilliseconds = 60000;

let appliedSnapshot;
let clockReference = {
  serverMilliseconds: Date.now(),
  monotonicMilliseconds: performance.now(),
};
let clockOffsetMilliseconds = 0;
let clockSynchronized = false;
let clockUncertaintyMilliseconds = 0;
let updateTimers = new Set();
let eventSource;
let healthTimer;
let recoveryAttempt = 0;
let recoveryGeneration = 0;
let recoveryTimer;
let rendererFailures = 0;
let rotationTimer;

class MaintenanceError extends Error {}

async function recoverDisplay(reason = "reconnecting") {
  const generation = ++recoveryGeneration;
  clearTimeout(recoveryTimer);
  clearTimeout(healthTimer);
  eventSource?.close();
  setConnection(reason, reason === "connecting" ? "Connecting…" : "Connection lost. Reconnecting…");

  try {
    const fetched = appliedSnapshot ? await fetchSnapshot() : await fetchInitialSnapshot();
    const {snapshot} = fetched;
    const clock = selectClockSample(fetched);
    if (generation !== recoveryGeneration) {
      return;
    }
    if (!snapshotCompatible(snapshot)) {
      await controlledReload(snapshot?.assetVersion);
      return;
    }
    try {
      renderSnapshot(snapshot, clock.offset);
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
    clockOffsetMilliseconds = clock.offset;
    clockSynchronized = true;
    clockUncertaintyMilliseconds = clock.uncertainty;
    recoveryAttempt = 0;
    void acknowledgeSnapshot(snapshot, false);
    openEventStream(snapshot);
    scheduleHealthRefresh(snapshot);
  } catch (error) {
    if (generation === recoveryGeneration) {
      if (error instanceof MaintenanceError) {
        scheduleRecovery(undefined, "stale");
      } else if (recoveryAttempt >= 3) {
        await controlledReload();
      } else {
        scheduleRecovery();
      }
    }
  }
}

async function fetchSnapshot() {
  const startedAt = performance.now();
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
      if (response.headers.get("X-Beamers-Maintenance") === "upgrade") {
        throw new MaintenanceError();
      }
      throw new Error(`snapshot request failed: ${response.status}`);
    }
    const {snapshot} = await response.json();
    const completedAt = performance.now();
    const elapsed = completedAt - startedAt;
    const serverTime = Date.parse(snapshot?.serverTime);
    if (!Number.isFinite(serverTime)) {
      throw new Error("snapshot server time is invalid");
    }
    return {
      snapshot,
      offset: Math.round(serverTime - (Date.now() - (elapsed / 2))),
      uncertainty: Math.round(elapsed / 2),
      serverReference: serverTime + (elapsed / 2),
      monotonicReference: completedAt,
    };
  } finally {
    clearTimeout(timeout);
  }
}

async function fetchInitialSnapshot() {
  let latest = await fetchSnapshot();
  let best = latest;
  for (let attempt = 0;
    attempt < 2 && best.uncertainty > maximumClockUncertaintyMilliseconds;
    attempt++) {
    latest = await fetchSnapshot();
    if (latest.uncertainty < best.uncertainty) {
      best = latest;
    }
  }
  return {
    ...best,
    snapshot: latest.snapshot,
  };
}

function selectClockSample(sample) {
  const offset = Math.round(
    sample.serverReference +
    (performance.now() - sample.monotonicReference) -
    Date.now(),
  );
  const {uncertainty} = sample;
  if (!clockSynchronized ||
      uncertainty <= maximumClockUncertaintyMilliseconds ||
      uncertainty < clockUncertaintyMilliseconds) {
    return {offset, uncertainty};
  }
  return {
    offset: Math.round(estimatedServerNow() - Date.now()),
    uncertainty: clockUncertaintyMilliseconds,
  };
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

function scheduleRecovery(delay, state = "disconnected") {
  eventSource?.close();
  clearTimeout(healthTimer);
  clearTimeout(recoveryTimer);
  setConnection(
    state,
    state === "stale"
      ? "Stale during maintenance. Retrying…"
      : "Connection lost. Reconnecting…",
  );
  const wait = delay ?? recoveryBackoff();
  recoveryTimer = setTimeout(() => void recoverDisplay("reconnecting"), wait);
}

function recoveryBackoff() {
  const base = Math.min(500 * (2 ** recoveryAttempt), maximumBackoffMilliseconds);
  recoveryAttempt++;
  return Math.round(base * (0.75 + (Math.random() * 0.5)));
}

function scheduleHealthRefresh(snapshot) {
  clearTimeout(healthTimer);
  healthTimer = setTimeout(
    () => void refreshHealth(),
    nextSnapshotRefreshMilliseconds(snapshot),
  );
}

function nextSnapshotRefreshMilliseconds(snapshot) {
  let delay = healthRefreshMilliseconds;
  const estimatedNow = Date.now() + clockOffsetMilliseconds;
  for (const override of [
    snapshot?.stageMessage,
    snapshot?.technicalDifficulties,
    snapshot?.urgentNotice,
    snapshot?.emergencyAlert,
  ]) {
    if (!override?.expiresAt) {
      continue;
    }
    const expiry = Date.parse(override.expiresAt);
    if (Number.isFinite(expiry)) {
      delay = Math.min(delay, Math.max(25, expiry - estimatedNow));
    }
  }
  return delay;
}

async function refreshHealth() {
  try {
    const fetched = await fetchSnapshot();
    const {snapshot} = fetched;
    const clock = selectClockSample(fetched);
    if (!snapshotCompatible(snapshot)) {
      await controlledReload(snapshot?.assetVersion);
      return;
    }
    if (!appliedSnapshot || snapshot.streamId !== appliedSnapshot.streamId) {
      void recoverDisplay("reconnecting");
      return;
    }
    try {
      renderSnapshot(snapshot, clock.offset);
      rendererFailures = 0;
    } catch {
      rendererFailures++;
      void reportRendererFailure();
      if (rendererFailures >= 3) {
        await controlledReload(snapshot.assetVersion);
      } else {
        scheduleHealthRefresh(appliedSnapshot);
      }
      return;
    }
    appliedSnapshot = snapshot;
    clockOffsetMilliseconds = clock.offset;
    clockSynchronized = true;
    clockUncertaintyMilliseconds = clock.uncertainty;
    void acknowledgeSnapshot(snapshot, false);
    scheduleHealthRefresh(snapshot);
  } catch (error) {
    scheduleRecovery(undefined, error instanceof MaintenanceError ? "stale" : "disconnected");
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
    programOutput: snapshot.programOutput,
    stageMessage: snapshot.stageMessage,
    technicalDifficulties: snapshot.technicalDifficulties,
    urgentNotice: snapshot.urgentNotice,
    emergencyAlert: snapshot.emergencyAlert,
    composition,
  });
  const candidateClockReference = {
    serverMilliseconds: Date.now() + offset,
    monotonicMilliseconds: performance.now(),
  };
  const outgoingMain = document.querySelector("main");
  if (outgoingMain?.dataset.renderKey === renderKey) {
    clockReference = candidateClockReference;
    return;
  }
  const survivingRotationSessionId = activeRotationSessionId(outgoingMain);
  const main = document.createElement("main");
  const startRegionUpdates = [];
  main.dataset.renderKey = renderKey;
  main.dataset.layout = composition.layout.key;
  main.dataset.displayId = String(snapshot.displayId);
  main.dataset.programOutputKind = snapshot.programOutput?.kind || "";
  main.dataset.programOutputEntryId = String(snapshot.programOutput?.entryId ?? "");
  main.dataset.programOutputRevision = String(snapshot.programOutputRevision ?? "");
  main.dataset.streamId = snapshot.streamId;
  main.dataset.streamPosition = String(snapshot.streamPosition);
  main.className = [
    "display-view",
    // Every built-in Layout key in internal/displayviews must appear here, or
    // the client rejects a Composition the server happily served and the Display
    // stops repainting. TestClientAcceptsEveryBuiltInLayoutKey pins the pairing.
    `display-layout-${controlledToken(composition.layout.key, [
      "standby", "event-overview", "location-signage", "stage-timer", "competition-output",
      "timeline", "crew-overview",
    ])}`,
    `display-font-${controlledToken(composition.theme.font, ["sans", "serif", "mono", "demoscene"])}`,
    `display-background-${controlledToken(composition.theme.background, ["solid", "variable-media", "nebula"])}`,
    `display-transition-${controlledToken(composition.theme.transition, ["none", "fade"])}`,
  ].join(" ");
  main.style.setProperty("--display-foreground", composition.theme.foregroundColor);
  main.style.setProperty("--display-background", composition.theme.backgroundColor);
  // The wire field stays accentColor (see display.proto); only the CSS
  // custom property is renamed to say what it carries.
  main.style.setProperty("--display-surface", composition.theme.accentColor);
  main.style.setProperty(
    "--display-signal",
    composition.theme.signalColor || composition.theme.accentColor,
  );
  main.style.setProperty("--display-live", composition.theme.liveColor);
  main.style.setProperty("--display-danger", composition.theme.dangerColor);
  main.style.setProperty("--display-scrim", composition.theme.scrimColor);
  main.style.setProperty("--display-scrim-opacity", composition.theme.scrimOpacity / 100);
  const alpha = Math.round((composition.theme.scrimOpacity / 100) * 255)
    .toString(16)
    .padStart(2, "0");
  main.style.setProperty(
    "--display-scrim-layer",
    `${composition.theme.scrimColor}${alpha}`,
  );

  if (snapshot.emergencyAlert) {
    main.dataset.overrideKind = "EmergencyAlert";
    main.className = "display-view emergency-alert display-override-replace";
    const region = document.createElement("section");
    region.setAttribute("role", "alert");
    region.setAttribute("aria-live", "assertive");
    appendHeading(region, "Emergency Alert");
    appendParagraph(region, snapshot.emergencyAlert.text);
    main.append(region);
  } else if (snapshot.urgentNotice?.presentation === "Replace") {
    main.dataset.overrideKind = "UrgentNotice";
    main.classList.add("display-override-replace", "urgent-notice-replace");
    const region = document.createElement("section");
    region.setAttribute("role", "alert");
    region.setAttribute("aria-live", "assertive");
    appendHeading(region, "Urgent Notice");
    appendParagraph(region, snapshot.urgentNotice.text);
    main.append(region);
  } else if (snapshot.technicalDifficulties) {
    main.dataset.overrideKind = "TechnicalDifficulties";
    main.classList.add("display-override-replace");
    const region = document.createElement("section");
    appendHeading(region, "Technical Difficulties");
    appendParagraph(region, snapshot.technicalDifficulties.text);
    main.append(region);
  } else {
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
        startRegionUpdates.push(startWidgetUpdates);
      }
      main.append(region);
    }
  }
  replaceMain(main);
  const suppressLower = snapshot.emergencyAlert ||
    snapshot.urgentNotice?.presentation === "Replace";
  renderStageMessage(suppressLower ? undefined : snapshot.stageMessage);
  renderUrgentNotice(snapshot.emergencyAlert ? undefined : snapshot.urgentNotice);
  clockReference = candidateClockReference;
  clearTrackedTimers();
  for (const startUpdates of startRegionUpdates) {
    startUpdates();
  }
  startProgressUpdates(main);
  startRotation(main, composition.layout.rotationSeconds, survivingRotationSessionId);
}

function scheduleTrackedUpdate(callback, delay) {
  let handle;
  handle = setTimeout(() => {
    updateTimers.delete(handle);
    callback();
  }, delay);
  updateTimers.add(handle);
  return handle;
}

function clearTrackedTimers() {
  for (const handle of updateTimers) {
    clearTimeout(handle);
  }
  updateTimers.clear();
}

// startProgressUpdates drives every elapsed bar in the frame.
//
// Each bar carries its own span, so this needs to know nothing about which
// Region drew it and works identically on a bar the server rendered into the
// entry document.
function startProgressUpdates(main) {
  const bars = collectProgressBars(main, []);
  if (bars.length === 0) {
    return;
  }
  const paint = () => {
    for (const bar of bars) {
      const fraction = elapsedFraction(
        Date.parse(bar.dataset.progressStart),
        Date.parse(bar.dataset.progressEnd),
        estimatedServerNow(),
      );
      if (fraction === null) {
        bar.hidden = true;
        continue;
      }
      bar.hidden = false;
      bar.style.setProperty("--display-progress", String(fraction));
      bar.setAttribute("aria-valuenow", String(Math.round(fraction * 100)));
    }
    scheduleTrackedUpdate(paint, 1000);
  };
  paint();
}

// collectProgressBars walks the frame rather than running a selector, because a
// bar may sit at any depth inside a Region.
function collectProgressBars(node, found) {
  for (const child of node.children ?? []) {
    if (child.dataset?.progressStart && child.dataset?.progressEnd) {
      found.push(child);
    }
    collectProgressBars(child, found);
  }
  return found;
}

// elapsedFraction is how far now sits between two instants, clamped to the span.
// It returns null for a span that cannot be measured, so a bar with unusable
// edges hides rather than sitting empty and implying no progress.
function elapsedFraction(start, end, now) {
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) {
    return null;
  }
  return Math.min(1, Math.max(0, (now - start) / (end - start)));
}

function renderUrgentNotice(message) {
  document.querySelector("aside[data-urgent-notice]")?.remove();
  if (!message || message.presentation !== "Overlay") {
    return;
  }
  const aside = document.createElement("aside");
  aside.dataset.urgentNotice = "true";
  aside.className = "urgent-notice-overlay";
  aside.setAttribute("role", "alert");
  aside.setAttribute("aria-live", "assertive");
  const label = document.createElement("strong");
  label.textContent = "Urgent Notice:";
  aside.append(label);
  appendParagraph(aside, message.text);
  document.body.append(aside);
}

function renderStageMessage(message) {
  document.querySelector("aside[data-stage-message]")?.remove();
  if (!message) {
    return;
  }
  const aside = document.createElement("aside");
  aside.dataset.stageMessage = "true";
  aside.dataset.emphasis = message.emphasis;
  aside.className = "stage-message";
  aside.setAttribute("role", "status");
  aside.setAttribute("aria-live", "assertive");
  const label = document.createElement("strong");
  label.textContent = {
    Normal: "Stage message:",
    Attention: "Attention:",
    Urgent: "Urgent:",
  }[message.emphasis] || "Stage message:";
  aside.append(label);
  appendParagraph(aside, message.text);
  document.body.append(aside);
}

function controlledToken(value, allowed) {
  if (!allowed.includes(value)) {
    throw new Error("snapshot composition token is invalid");
  }
  return value;
}

function renderWidget(region, widget, snapshot, theme, candidateClockReference) {
  switch (widget) {
  case "branding": {
    const branding = `${theme.brandAsset === "signal" ? "◆ " : ""}` +
      (theme.branding || snapshot.eventName || "Beamers");
    if (snapshot.standby) {
      appendParagraph(region, branding);
    } else {
      appendHeading(region, branding);
      if (snapshot.viewKey === "event-overview") {
        appendParagraph(region, "Event Overview");
        appendParagraph(region, `Location: ${snapshot.locationName}`);
      }
    }
    return;
  }
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
  case "now-next": {
    appendHeading(region, "Now / Next", 2);
    const snapshotTime = Date.parse(snapshot.serverTime);
    if (!Number.isFinite(snapshotTime)) {
      throw new Error("snapshot server time is invalid");
    }
    const upcoming = (snapshot.sessions ?? [])
      .filter((candidate) => {
        if (candidate.lifecycle === "Canceled" || candidate.lifecycle === "Ended") {
          return false;
        }
        if (candidate.lifecycle === "Live" || !candidate.forecastEnd) {
          return true;
        }
        const forecastEnd = Date.parse(candidate.forecastEnd);
        return Number.isFinite(forecastEnd) && forecastEnd > snapshotTime;
      })
      .slice(0, 2);
    for (const [index, session] of upcoming.entries()) {
      const article = document.createElement("article");
      article.dataset.slot = index === 0 ? "now" : "next";
      renderSession(article, snapshot, session);
      if (index === 0 && session.lifecycle === "Live") {
        appendProgressBar(article, session.presentedStart, session.presentedEnd, candidateClockReference);
      }
      region.append(article);
    }
    return;
  }
  case "rotation":
    for (const session of snapshot.sessions ?? []) {
      const article = document.createElement("article");
      article.dataset.rotationPage = "true";
      article.dataset.slot = "rotation";
      // rotationKey, not the redacted id, is the stable per-Session identity:
      // a suppressed Session sends id as 0 like every other suppressed
      // Session on the same View, which would collapse them all to one
      // rotation slot.
      article.dataset.sessionId = String(session.rotationKey ?? "");
      // Left hidden here: startRotation chooses which page opens, carrying
      // the outgoing frame's rotation position forward when it can.
      article.hidden = true;
      renderSession(article, snapshot, session);
      region.append(article);
    }
    if (region.children.length === 0) {
      appendParagraph(region, "No public Event information is currently scheduled.");
    }
    return;
  case "timeline": {
    const days = document.createElement("div");
    days.className = "display-timeline-days";
    const timelines = new Map();
    for (const session of snapshot.sessions ?? []) {
      let timeline = timelines.get(session.timelineDay);
      if (!timeline) {
        const day = document.createElement("section");
        day.className = "display-timeline-day";
        appendHeading(day, session.timelineDay, 3);
        timeline = document.createElement("ol");
        timeline.className = "display-timeline";
        timeline.style.setProperty("--display-lanes", String(session.timelineLaneCount));
        appendTimelineLaneLabels(timeline, session.timelineLaneCount);
        appendTimelineGridlines(timeline, session.timelineGridlines);
        appendTimelineNow(
          timeline,
          session.timelineNowOffset,
          session.timelineDayStart,
          session.timelineDayEnd,
        );
        day.append(timeline);
        days.append(day);
        timelines.set(session.timelineDay, timeline);
      }
      timeline.append(renderTimelineBlock(snapshot, session));
    }
    region.append(days);
    if ((snapshot.sessions ?? []).length === 0) {
      appendParagraph(region, "No public Event information is currently scheduled.");
    }
    return startTimelineNowUpdates(region, candidateClockReference);
  }
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
    if (snapshot.programOutput) {
      appendHeading(region, snapshot.programOutput.title || "Program Output");
      const kind = String(snapshot.programOutput.kind || "Standby")
        .replace("PROGRAM_ITEM_KIND_", "")
        .toLowerCase();
      appendParagraph(region, kind.charAt(0).toUpperCase() + kind.slice(1));
    } else {
      appendHeading(region, "Standby");
    }
    return;
  default:
    throw new Error("snapshot widget is invalid");
  }
}

// minuteAlignmentDelay is how long until the next minute boundary of the
// estimated server clock. Scheduling from a fixed 60000ms interval instead
// would let the displayed minute sit up to 59s stale, since a paint that
// starts partway through a minute never catches back up to the boundary.
function minuteAlignmentDelay(now) {
  return 60000 - (now % 60000);
}

function prepareClock(clock, snapshot, reference) {
  const update = () => {
    const current = new Date(estimatedServerNow());
    clock.dateTime = current.toISOString();
    clock.textContent = formatClockTime(snapshot, current);
    scheduleTrackedUpdate(update, minuteAlignmentDelay(estimatedServerNow()));
  };
  const current = new Date(estimatedServerNow(reference));
  clock.dateTime = current.toISOString();
  clock.textContent = formatClockTime(snapshot, current);
  return () => {
    scheduleTrackedUpdate(update, minuteAlignmentDelay(estimatedServerNow(reference)));
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
  direction.dataset.timerDirection = "true";
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
    appendParagraph(region, `Forecast End: ${formatClockTime(snapshot, forecastEnd)}`);
  }

  // The span is projected once server-side, so the entry document and this
  // renderer draw the same bar. A missing edge means no bar at all.
  if (timer.spanStart && timer.spanEnd) {
    appendProgressBar(region, timer.spanStart, timer.spanEnd, reference);
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
      scheduleTrackedUpdate(tick, 250);
    };
    scheduleTrackedUpdate(tick, 250);
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

// renderTimelineBlock consumes server-projected Event-day geometry so the entry
// document and browser renderer cannot disagree.
// appendTimelineLaneLabels adds one small chip per visual overlap lane. It
// is purely decorative -- the lane number is not domain identity -- so
// every chip is aria-hidden.
function appendTimelineLaneLabels(timeline, laneCount) {
  const lanes = Math.max(1, Number(laneCount) || 1);
  for (let lane = 0; lane < lanes; lane++) {
    const label = document.createElement("li");
    label.className = "display-timeline-lane-label";
    label.style.setProperty("--display-lane", String(lane));
    label.setAttribute("aria-hidden", "true");
    label.textContent = `Lane ${lane + 1}`;
    timeline.append(label);
  }
}

// appendTimelineGridlines draws the server-projected faint hour marks for
// one Event day. Offsets already share the block's coordinate space, so no
// further computation happens client-side.
function appendTimelineGridlines(timeline, gridlines) {
  for (const gridline of gridlines ?? []) {
    const mark = document.createElement("li");
    mark.className = "display-timeline-gridline";
    mark.style.setProperty("--display-offset", String(gridline.offset));
    mark.setAttribute("aria-hidden", "true");
    const label = document.createElement("span");
    label.textContent = gridline.label;
    mark.append(label);
    timeline.append(mark);
  }
}

// appendTimelineNow draws the now-line only when the server projected one
// for this Event day; nowOffset is absent (not zero) when the server time
// falls outside it, so an absent value must never draw a stray line at 0%.
// The line's position is then kept live from dayStart/dayEnd and the
// synchronized clock (see updateTimelineNowLines) rather than left frozen
// at the offset this one snapshot happened to compute, so it never visibly
// drifts from the persistent clock and Stage Timer on the same Display.
function appendTimelineNow(timeline, nowOffset, dayStart, dayEnd) {
  if (nowOffset === undefined || nowOffset === null) {
    return;
  }
  const now = document.createElement("li");
  now.className = "display-timeline-now";
  now.style.setProperty("--display-offset", String(nowOffset));
  now.setAttribute("aria-hidden", "true");
  const start = Date.parse(dayStart ?? "");
  const end = Date.parse(dayEnd ?? "");
  if (Number.isFinite(start) && Number.isFinite(end)) {
    now.dataset.dayStart = String(start);
    now.dataset.dayEnd = String(end);
  }
  timeline.append(now);
}

// updateTimelineNowLines recomputes every now-line's offset from its Event
// day bounds and the synchronized clock, so the Timeline keeps advancing
// between snapshots and while disconnected instead of freezing.
function updateTimelineNowLines(main, reference) {
  const now = estimatedServerNow(reference);
  for (const line of main.querySelectorAll(".display-timeline-now[data-day-start]")) {
    const fraction = elapsedFraction(
      Number(line.dataset.dayStart),
      Number(line.dataset.dayEnd),
      now,
    );
    if (fraction !== null) {
      line.style.setProperty("--display-offset", String(Math.round(fraction * 10000)));
    }
  }
}

function startTimelineNowUpdates(main, reference) {
  if (main.querySelectorAll(".display-timeline-now[data-day-start]").length === 0) {
    return undefined;
  }
  const update = () => {
    updateTimelineNowLines(main, reference);
    scheduleTrackedUpdate(update, minuteAlignmentDelay(estimatedServerNow(reference)));
  };
  return update;
}

function renderTimelineBlock(snapshot, session) {
  const block = document.createElement("li");
  block.className = "display-timeline-block";
  block.style.setProperty("--display-offset", String(session.timelineOffset));
  block.style.setProperty("--display-width", String(session.timelineWidth));
  block.style.setProperty("--display-lane", String(session.timelineLane));
  block.style.setProperty("--display-lanes", String(session.timelineLaneCount));
  block.dataset.lifecycle = session.lifecycle ?? "";
  if (session.unavailable) {
    block.dataset.unavailable = "true";
    appendParagraph(block, session.availabilityMessage);
    return block;
  }
  appendHeading(block, session.title, 3);
  // A Live block's signal-color fill is a color-only cue, and forced-colors
  // mode strips it entirely; the badge repeats the same proven-contrast
  // text and border treatment the Now/Next and rotation views already use.
  appendLifecycleBadge(block, session);
  const start = document.createElement("time");
  start.dateTime = String(session.presentedStart ?? "");
  start.textContent = formatScheduleTime(snapshot, session.presentedStart);
  const line = document.createElement("p");
  line.append(start);
  block.append(line);
  return block;
}

function renderSession(parent, snapshot, session) {
  if (session.unavailable) {
    appendParagraph(parent, session.availabilityMessage);
    return;
  }
  parent.dataset.lifecycle = session.lifecycle;
  if (parent.dataset.slot === "now" || parent.dataset.slot === "next") {
    appendKicker(parent, snapshot, session);
  }
  appendHeading(parent, session.title, 3);
  appendLifecycleBadge(parent, session);
  appendSessionSchedule(parent, snapshot, session);
  appendOptionalParagraph(parent, session.speaker);
  appendOptionalParagraph(parent, session.publicDetails);
}

// appendKicker labels a Now / Next card. NOW only claims a Session that is
// actually Live; a card can hold the next upcoming Session before it starts,
// and showing NOW there would claim it was running when it is not.
function appendKicker(parent, snapshot, session) {
  const kicker = document.createElement("p");
  kicker.className = "display-kicker";
  kicker.textContent = session.lifecycle === "Live"
    ? "NOW"
    : `UP NEXT · ${formatScheduleTime(snapshot, session.presentedStart)}`;
  parent.append(kicker);
}

// appendLifecycleBadge marks Live and Canceled explicitly. Scheduled and
// Ended are the unmarked default state and would only add noise.
function appendLifecycleBadge(parent, session) {
  if (session.lifecycle !== "Live" && session.lifecycle !== "Canceled") {
    return;
  }
  const badge = document.createElement("span");
  badge.className = "display-badge";
  badge.dataset.lifecycle = session.lifecycle;
  badge.textContent = session.lifecycle;
  parent.append(badge);
}

// startRotation opens the page identified by preferredSessionId when one of
// the freshly rendered pages carries that Session, so a snapshot re-render
// (a forecast nudge, a lifecycle change) does not yank the Display back to
// the first page. Absent or unmatched, it falls back to the first page,
// matching an intentional hard cut such as an Override clearing.
function startRotation(main, seconds, preferredSessionId) {
  clearTimeout(rotationTimer);
  const pages = findRotationPages(main);
  if (pages.length === 0) {
    return;
  }
  let active = preferredSessionId === undefined
    ? -1
    : pages.findIndex((page) => page.dataset.sessionId === preferredSessionId);
  if (active < 0) {
    active = 0;
  }
  pages[active].hidden = false;
  if (pages.length < 2 || !Number.isInteger(seconds) || seconds < 5 || seconds > 300) {
    return;
  }
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

// activeRotationSessionId reads the outgoing frame's visible rotation page,
// before it is replaced, so its Session identity can be carried into the
// incoming frame's rotation.
function activeRotationSessionId(main) {
  if (!main) {
    return undefined;
  }
  const active = findRotationPages(main).find((page) => !page.hidden);
  return active?.dataset.sessionId;
}

function appendSessionSchedule(parent, snapshot, session) {
  appendParagraph(parent, `Status: ${session.lifecycle}`);
  // Start and end each take their own row so the two instants line up in a
  // column. Run together on one line they wrapped mid-timestamp.
  const times = document.createElement("dl");
  times.className = "display-times";
  appendTimeRow(
    times,
    `${requiredText(session.presentedStartLabel, "presented start label")}:`,
    session.presentedStart,
    formatScheduleTime(snapshot, session.presentedStart),
  );
  appendTimeRow(
    times,
    `${requiredText(session.presentedEndLabel, "presented end label")}:`,
    session.presentedEnd,
    formatScheduleTime(snapshot, session.presentedEnd),
  );
  parent.append(times);
}

function appendTimeRow(parent, label, instant, text) {
  const term = document.createElement("dt");
  term.textContent = label;
  const value = document.createElement("dd");
  const stamp = document.createElement("time");
  stamp.dateTime = String(instant ?? "");
  stamp.textContent = text;
  value.append(stamp);
  parent.append(term, value);
}

// appendProgressBar draws the elapsed bar for a span, carrying its starting
// fraction before it ever joins the frame. Set only after insertion, a
// re-render's bar paints at zero and then animates up to its true value on
// the very next tick; the server-rendered entry document never has that
// problem because its style attribute is part of the same markup as the
// element, so this mirrors that by writing the property before parent.append.
function appendProgressBar(parent, start, end, reference) {
  const fraction = elapsedFraction(Date.parse(start), Date.parse(end), estimatedServerNow(reference));
  if (fraction === null) {
    return;
  }
  const bar = document.createElement("div");
  bar.className = "display-progress";
  bar.dataset.progressStart = String(start);
  bar.dataset.progressEnd = String(end);
  bar.style.setProperty("--display-progress", String(fraction));
  bar.setAttribute("role", "progressbar");
  bar.setAttribute("aria-label", "Elapsed");
  bar.setAttribute("aria-valuemin", "0");
  bar.setAttribute("aria-valuemax", "100");
  bar.setAttribute("aria-valuenow", String(Math.round(fraction * 100)));
  parent.append(bar);
  return bar;
}

// formatScheduleTime renders an instant the way publictime.DisplayDateTimeLayout
// does on the server: an ISO 8601 date and a 24-hour clock, no zone
// abbreviation. The parts are assembled explicitly rather than trusting a locale
// to order them, because the server document and this renderer draw the same
// Display and any difference shows up as text that shifts on reconnect.
function formatScheduleTime(snapshot, value) {
  const parts = eventTimeParts(snapshot, value, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}`;
}

// formatClockTime renders a 24-hour wall clock, matching publictime.TimeLayout.
function formatClockTime(snapshot, value) {
  const parts = eventTimeParts(snapshot, value, {hour: "2-digit", minute: "2-digit"});
  return `${parts.hour}:${parts.minute}`;
}

function eventTimeParts(snapshot, value, options) {
  const instant = new Date(value);
  if (!Number.isFinite(instant.getTime())) {
    throw new Error("Presented Session time is invalid");
  }
  const formatter = new Intl.DateTimeFormat("en", {
    ...options,
    // h23 keeps midnight at 00 and noon at 12 without an AM or PM suffix.
    hourCycle: "h23",
    timeZone: snapshot.eventTimezone || "UTC",
  });
  const parts = {};
  for (const part of formatter.formatToParts(instant)) {
    parts[part.type] = part.value;
  }
  return parts;
}

function requiredText(value, name) {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`Session ${name} is invalid`);
  }
  return value;
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
        stageMessageId: snapshot.stageMessage?.id || 0,
        stageMessageRevision: snapshot.stageMessage?.revision || 0,
        technicalDifficultiesId: snapshot.technicalDifficulties?.id || 0,
        technicalDifficultiesRevision: snapshot.technicalDifficulties?.revision || 0,
        urgentNoticeId: snapshot.urgentNotice?.id || 0,
        urgentNoticeRevision: snapshot.urgentNotice?.revision || 0,
        emergencyAlertId: snapshot.emergencyAlert?.id || 0,
        emergencyAlertRevision: snapshot.emergencyAlert?.revision || 0,
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
