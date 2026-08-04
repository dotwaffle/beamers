const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const clientSource = fs.readFileSync(
  new URL("./client.js", `file://${__filename}`),
  "utf8",
);

test("transport failure retains the frame and resnapshots before reconnecting", async () => {
  const browser = await startBrowser();
  const committedFrame = browser.document.main;
  assert.equal(browser.eventSources.length, 1);

  browser.eventSources[0].emit("error");
  assert.equal(browser.document.main, committedFrame);
  assert.equal(browser.indicator.dataset.connection, "disconnected");

  await browser.runTimer((delay) => delay < 10000);
  assert.equal(browser.snapshotRequests, 2);
  assert.equal(browser.eventSources.length, 2);
  assert.equal(browser.document.main, committedFrame);
});

test("snapshot failure retains the frame and retries with bounded backoff", async () => {
  const browser = await startBrowser({snapshotFailures: 1});
  assert.equal(browser.document.main, browser.initialMain);
  assert.equal(browser.eventSources.length, 0);
  assert.equal(browser.indicator.dataset.connection, "disconnected");
  assert.ok(browser.timerDelays().some((delay) => delay >= 375 && delay <= 625));

  await browser.runTimer((delay) => delay >= 375 && delay <= 625);
  assert.equal(browser.snapshotRequests, 2);
  assert.equal(browser.eventSources.length, 1);
  assert.notEqual(browser.document.main, browser.initialMain);
});

test("maintenance retains the frame and reports Stale instead of Disconnected", async () => {
  const browser = await startBrowser({snapshotMaintenance: 1});
  assert.equal(browser.document.main, browser.initialMain);
  assert.equal(browser.eventSources.length, 0);
  assert.equal(browser.indicator.dataset.connection, "stale");
  assert.equal(browser.indicator.textContent, "Stale during maintenance. Retrying…");

  await browser.runTimer((delay) => delay >= 375 && delay <= 625);
  assert.equal(browser.snapshotRequests, 2);
  assert.equal(browser.eventSources.length, 1);
});

test("invalidation resnapshots instead of treating SSE as authority", async () => {
  const browser = await startBrowser({
    snapshots: [
      displaySnapshot(),
      displaySnapshot({publishedRevision: "2"}),
    ],
  });
  const committedFrame = browser.document.main;

  browser.eventSources[0].emit("invalidate", {
    data: JSON.stringify({
      protocol_version: "beamers.display.v1",
      asset_version: "asset-current",
      stream_position: 9,
    }),
  });
  assert.equal(browser.document.main, committedFrame);

  await browser.runTimer((delay) => delay === 0);
  assert.equal(browser.snapshotRequests, 2);
  assert.notEqual(browser.document.main, committedFrame);
});

test("renderer failure retains the frame and reports instability", async () => {
  const clockComposition = displayComposition({
    regions: [
      {name: "branding", widget: "branding", persistent: true},
      {name: "message", widget: "standby", persistent: true},
      {name: "clock", widget: "clock", persistent: true},
    ],
  });
  const browser = await startBrowser({
    snapshots: [
      displaySnapshot({composition: clockComposition}),
      displaySnapshot({composition: clockComposition, publishedRevision: "2"}),
      displaySnapshot({composition: clockComposition, publishedRevision: "2"}),
    ],
  });
  const committedFrame = browser.document.main;
  const clock = committedFrame.children.find(
    (region) => region.dataset.widget === "clock",
  ).children[0];
  const clockBeforeFailure = clock.textContent;
  browser.failRendering = true;

  browser.eventSources[0].emit("invalidate", {
    data: JSON.stringify({
      protocol_version: "beamers.display.v1",
      asset_version: "asset-current",
      stream_position: 2,
    }),
  });
  await browser.runTimer((delay) => delay === 0);
  assert.equal(browser.document.main, committedFrame);
  assert.equal(browser.acknowledgments.at(-1).rendererUnstable, true);
  // The clock re-renders on the true minute boundary rather than a flat 60s
  // interval, so its next tick lands somewhere in (0, 60000].
  await browser.runTimer((delay) => delay > 0 && delay <= 60000);
  assert.notEqual(clock.textContent, clockBeforeFailure);

  browser.failRendering = false;
  await browser.runTimer((delay) => delay > 0 && delay < 10000);
  assert.notEqual(browser.document.main, committedFrame);
});

test("controlled reload verifies the entry document and prevents reload loops", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({assetVersion: "asset-next"}),
    availableAsset: "asset-next",
  });
  assert.equal(browser.reloads, 1);
  assert.equal(browser.entryRequests, 1);

  await vm.runInContext('controlledReload("asset-next")', browser.context);
  await browser.flush();
  assert.equal(browser.reloads, 1);
  assert.equal(browser.entryRequests, 2);
});

test("rotation advances configured pages without replacing persistent regions", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      standby: false,
      viewKey: "event-overview",
      composition: displayComposition({
        key: "event-overview",
        rotationSeconds: 30,
        regions: [
          {name: "header", widget: "branding", persistent: true},
          {name: "schedule", widget: "rotation", persistent: false},
          {name: "clock", widget: "clock", persistent: true},
        ],
      }),
      sessions: [
        displaySession("Opening Keynote"),
        displaySession("Closing Keynote"),
      ],
    }),
  });
  const committedFrame = browser.document.main;
  const header = committedFrame.children[0];
  const rotation = committedFrame.children[1];
  const clock = committedFrame.children[2];
  assert.equal(rotation.children[0].hidden, false);
  assert.equal(rotation.children[1].hidden, true);
  // The server document marks rotation pages the same way, so a reconnect
  // repaints the Region without shifting its presentation.
  assert.equal(rotation.children[0].dataset.slot, "rotation");
  assert.equal(rotation.children[1].dataset.slot, "rotation");

  await browser.runTimer((delay) => delay === 30000);
  assert.equal(browser.document.main, committedFrame);
  assert.equal(committedFrame.children[0], header);
  assert.equal(committedFrame.children[2], clock);
  assert.equal(rotation.children[0].hidden, true);
  assert.equal(rotation.children[1].hidden, false);
});

test("rotation position survives a snapshot re-render, carried from the outgoing DOM", async () => {
  const composition = displayComposition({
    key: "event-overview",
    rotationSeconds: 30,
    regions: [
      {name: "schedule", widget: "rotation", persistent: false},
    ],
  });
  const firstSnapshot = displaySnapshot({
    standby: false,
    viewKey: "event-overview",
    composition,
    sessions: [
      displaySession("Opening Keynote", {id: "1"}),
      displaySession("Closing Keynote", {id: "2"}),
    ],
  });
  // A forecast nudge changes Session content (and so the render key) without
  // changing which Sessions exist or their order.
  const nudgedSnapshot = displaySnapshot({
    standby: false,
    viewKey: "event-overview",
    composition,
    publishedRevision: "2",
    sessions: [
      displaySession("Opening Keynote", {id: "1"}),
      displaySession("Closing Keynote", {id: "2", forecastEnd: "2099-08-21T09:15:00Z"}),
    ],
  });
  const browser = await startBrowser({snapshots: [firstSnapshot, nudgedSnapshot]});

  // Advance rotation to the second page before the nudge lands.
  await browser.runTimer((delay) => delay === 30000);
  const rotatedFrame = browser.document.main;
  assert.equal(rotatedFrame.children[0].children[0].hidden, true);
  assert.equal(rotatedFrame.children[0].children[1].hidden, false);

  browser.eventSources[0].emit("invalidate", {
    data: JSON.stringify({
      protocol_version: "beamers.display.v1",
      asset_version: "asset-current",
      stream_position: 2,
    }),
  });
  await browser.runTimer((delay) => delay === 0);

  const renderedFrame = browser.document.main;
  assert.notEqual(renderedFrame, rotatedFrame);
  const rotation = renderedFrame.children[0];
  assert.equal(rotation.children[0].dataset.sessionId, "1");
  assert.equal(rotation.children[1].dataset.sessionId, "2");
  assert.equal(rotation.children[0].hidden, true);
  assert.equal(rotation.children[1].hidden, false);
});

test("Override firing hard-cuts rather than crossfading the incoming frame", () => {
  const stylesheet = fs.readFileSync(
    new URL("./display.css", `file://${__filename}`),
    "utf8",
  );
  // The incoming-frame crossfade excludes any Override via
  // [data-override-kind], and every Override branch in client.js sets that
  // dataset attribute on main, so an Emergency Alert, Urgent Notice, or
  // Technical Difficulties firing is always a hard cut regardless of the
  // Theme's transition choice.
  assert.match(
    stylesheet,
    /\.display-transition-fade:not\(\[data-override-kind\]\)\s*\{\s*animation: display-fade/,
  );
});

test("timeline renderer uses the projected Event-day geometry", async () => {
  const suppressed = {
    unavailable: true,
    availabilityMessage: "Location unavailable until Aug 21, 2099 10:15 UTC",
    forecastStart: "2099-08-21T09:45:00Z",
    forecastEnd: "2099-08-21T10:15:00Z",
    timelineDay: "2099-08-21",
    timelineOffset: 7292,
    timelineWidth: 2083,
    timelineLane: 0,
    timelineLaneCount: 1,
  };
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      standby: false,
      viewKey: "timeline",
      composition: displayComposition({
        key: "timeline",
        regions: [
          {name: "timeline", widget: "timeline", persistent: true},
        ],
      }),
      sessions: [
        {
          ...displaySession("Opening Keynote"),
          timelineDay: "2099-08-21",
          timelineOffset: 1042,
          timelineWidth: 6250,
          timelineLane: 0,
          timelineLaneCount: 1,
        },
        suppressed,
      ],
    }),
  });

  const day = browser.document.main.children[0].children[0].children[0];
  assert.match(nodeText(day), /2099-08-21/);
  const timeline = day.children[1];
  assert.equal(timeline.className, "display-timeline");
  assert.equal(timeline.children[0].style.properties.get("--display-offset"), "1042");
  assert.equal(timeline.children[0].style.properties.get("--display-width"), "6250");
  assert.equal(timeline.children[1].style.properties.get("--display-offset"), "7292");
  assert.equal(timeline.children[1].style.properties.get("--display-width"), "2083");
  // A suppressed span reports that it is taken, never the Session in it.
  assert.equal(timeline.children[1].dataset.unavailable, "true");
  assert.match(nodeText(timeline.children[1]), /Location unavailable until/);
  assert.doesNotMatch(nodeText(timeline), /Private/);
});

test("live renderer uses server-selected public time labels", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      standby: false,
      viewKey: "event-overview",
      composition: displayComposition({
        key: "event-overview",
        regions: [
          {name: "schedule", widget: "rotation", persistent: false},
        ],
      }),
      sessions: [{
        ...displaySession("Opening Keynote"),
        lifecycle: "Live",
        presentedStart: "2099-08-21T08:04:00Z",
        presentedStartLabel: "Actual Start",
      }],
    }),
  });

  const schedule = nodeText(browser.document.main.children[0]);
  assert.match(schedule, /Actual Start/);
  assert.match(schedule, /Forecast End/);
  assert.doesNotMatch(schedule, /Forecast Time/);
});

test("health refresh preserves an unchanged composition and its rotation", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      standby: false,
      viewKey: "event-overview",
      composition: displayComposition({
        key: "event-overview",
        rotationSeconds: 15,
        regions: [
          {name: "schedule", widget: "rotation", persistent: false},
        ],
      }),
      sessions: [
        displaySession("First"),
        displaySession("Second"),
      ],
    }),
  });
  const committedFrame = browser.document.main;

  await browser.runTimer((delay) => delay === 10000);
  assert.equal(browser.document.main, committedFrame);
  assert.ok(browser.timerDelays().includes(15000));
});

test("Competition Output renders committed Program Output", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      standby: false,
      viewKey: "competition-output",
      composition: displayComposition({
        key: "competition-output",
        regions: [
          {name: "program", widget: "program-output", persistent: true},
        ],
      }),
      programOutput: {
        kind: "PROGRAM_ITEM_KIND_ENTRY",
        entryId: "42",
        title: "Aurora",
      },
    }),
  });
  const program = browser.document.main.children[0];
  assert.match(nodeText(program), /Aurora/);
  assert.match(nodeText(program), /Entry/);
});

test("Competition Output rerenders after a durable Take", async () => {
  const snapshot = {
    standby: false,
    viewKey: "competition-output",
    composition: displayComposition({
      key: "competition-output",
      regions: [
        {name: "program", widget: "program-output", persistent: true},
      ],
    }),
  };
  const browser = await startBrowser({
    snapshots: [
      displaySnapshot({
        ...snapshot,
        programOutputRevision: "1",
        programOutput: {kind: "PROGRAM_ITEM_KIND_STARTING", title: "Starting"},
      }),
      displaySnapshot({
        ...snapshot,
        programOutputRevision: "2",
        streamPosition: "2",
        programOutput: {kind: "PROGRAM_ITEM_KIND_UPCOMING", title: "Upcoming"},
      }),
    ],
  });

  browser.eventSources[0].emit("invalidate", {
    data: JSON.stringify({
      protocol_version: "beamers.display.v1",
      asset_version: "asset-current",
      stream_position: 2,
    }),
  });
  await browser.runTimer((delay) => delay === 0);

  assert.match(nodeText(browser.document.main.children[0]), /Upcoming/);
  assert.equal(browser.document.main.dataset.programOutputKind, "PROGRAM_ITEM_KIND_UPCOMING");
  assert.equal(browser.document.main.dataset.programOutputRevision, "2");
  assert.equal(browser.document.main.dataset.streamPosition, "2");
  assert.equal(browser.acknowledgments.at(-1).streamPosition, "2");
});

test("persistent clock advances without replacing the committed frame", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      serverTime: "2099-08-21T08:00:00Z",
      eventTimezone: "UTC",
      composition: displayComposition({
        regions: [
          {name: "clock", widget: "clock", persistent: true},
        ],
      }),
    }),
  });
  const committedFrame = browser.document.main;
  const clock = committedFrame.children.find(
    (region) => region.dataset.widget === "clock",
  );
  assert.ok(clock);
  const time = clock.children[0];
  const before = time.textContent;

  await browser.runTimer((delay) => delay === 60000);
  assert.equal(browser.document.main, committedFrame);
  assert.notEqual(time.textContent, before);
});

test("persistent clock re-renders aligned to the true minute boundary", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      serverTime: "2099-08-21T08:00:47Z",
      eventTimezone: "UTC",
      composition: displayComposition({
        regions: [
          {name: "clock", widget: "clock", persistent: true},
        ],
      }),
    }),
  });
  // The clock synchronized 47 seconds into the minute, so the next re-render
  // has to land on the minute boundary (13s away), never a flat 60s later --
  // otherwise the digits sit up to 59s stale between paints.
  assert.ok(browser.timerDelays().some((delay) => delay === 13000));
  assert.ok(!browser.timerDelays().includes(60000));

  await browser.runTimer((delay) => delay === 13000);
  // Once aligned, the following re-render is a full minute later.
  assert.ok(browser.timerDelays().some((delay) => delay === 60000));
});

test("progress bar carries its initial fraction before insertion into the frame", async () => {
  const capturedAtInsertion = [];
  const originalAppend = FakeNode.prototype.append;
  FakeNode.prototype.append = function append(...children) {
    for (const child of children) {
      if (child.dataset?.progressStart) {
        capturedAtInsertion.push(child.style.properties.get("--display-progress"));
      }
    }
    return originalAppend.apply(this, children);
  };
  try {
    await startBrowser({
      snapshot: displaySnapshot({
        serverTime: "2099-08-21T08:30:00Z",
        standby: false,
        viewKey: "location-signage",
        composition: displayComposition({
          key: "location-signage",
          regions: [
            {name: "now-next", widget: "now-next", persistent: true},
          ],
        }),
        sessions: [{
          ...displaySession("Current Session"),
          lifecycle: "Live",
          presentedStart: "2099-08-21T08:00:00Z",
          presentedEnd: "2099-08-21T09:00:00Z",
        }],
      }),
    });
  } finally {
    FakeNode.prototype.append = originalAppend;
  }
  assert.ok(capturedAtInsertion.length > 0, "expected a progress bar to be inserted");
  // Set before parent.append(bar) runs, or a re-render's bar starts painted at
  // zero and animates up to its true value instead of showing it immediately.
  assert.equal(capturedAtInsertion[0], "0.5");
});

test("Now / Next kicker reads NOW only when Live, otherwise UP NEXT with the start time", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      serverTime: "2099-08-21T08:00:00Z",
      eventTimezone: "UTC",
      standby: false,
      viewKey: "location-signage",
      composition: displayComposition({
        key: "location-signage",
        regions: [
          {name: "now-next", widget: "now-next", persistent: true},
        ],
      }),
      sessions: [
        {...displaySession("Current Session"), lifecycle: "Live"},
        {
          ...displaySession("Next Session"),
          forecastStart: "2099-08-21T10:00:00Z",
          forecastEnd: "2099-08-21T11:00:00Z",
          presentedStart: "2099-08-21T10:00:00Z",
        },
      ],
    }),
  });
  const nowNext = browser.document.main.children[0];
  const nowCard = nowNext.children.find((child) => child.dataset.slot === "now");
  const nextCard = nowNext.children.find((child) => child.dataset.slot === "next");
  const kicker = (card) => card.children.find(
    (child) => child.className === "display-kicker",
  );
  assert.equal(kicker(nowCard).textContent, "NOW");
  assert.equal(kicker(nextCard).textContent, "UP NEXT · 2099-08-21 10:00");
});

test("Stage Timer advances from the synchronized monotonic clock into overtime", async () => {
  const browser = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:00:00Z",
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: "2099-08-21T08:00:02Z",
        thresholds: [],
      },
    }),
  });
  const region = browser.document.main.children[1];
  assert.match(nodeText(region), /Closing Keynote/);
  assert.match(nodeText(region), /00:02/);

  for (let tick = 0; tick < 12; tick++) {
    await browser.runTimer((delay) => delay === 250);
  }
  assert.match(nodeText(region), /\+00:01/);
  assert.equal(browser.document.main.children[1], region);
  assert.equal(
    vm.runInContext("updateTimers.size", browser.context),
    1,
    "completed timer handles are not retained",
  );
});

test("Stage Timer shows manual elapsed time and accessible threshold emphasis", async () => {
  const browser = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:01:05Z",
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_ELAPSED",
        anchor: "2099-08-21T08:00:00Z",
        forecastEnd: "2099-08-21T09:00:00Z",
        thresholds: [],
      },
    }),
  });
  const elapsedRegion = browser.document.main.children[1];
  assert.match(nodeText(elapsedRegion), /Elapsed/);
  assert.match(nodeText(elapsedRegion), /01:05/);
  assert.match(nodeText(elapsedRegion), /Forecast End/);
  assert.match(nodeText(elapsedRegion), /09:00/);

  const urgent = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:00:30Z",
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: "2099-08-21T08:01:00Z",
        thresholds: [{
          remainingSeconds: "60",
          emphasis: "TIMER_EMPHASIS_URGENT",
        }],
      },
    }),
  });
  const urgentRegion = urgent.document.main.children[1];
  assert.equal(urgentRegion.dataset.timerEmphasis, "urgent");
  assert.match(nodeText(urgentRegion), /Urgent/);
});

test("Stage Timer draws the elapsed bar from the projected span only", async () => {
  const projected = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:30:00Z",
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: "2099-08-21T09:00:00Z",
        thresholds: [],
        spanStart: "2099-08-21T08:00:00Z",
        spanEnd: "2099-08-21T09:00:00Z",
      },
    }),
  });
  const region = projected.document.main.children[1];
  const bar = region.children.find((node) => node.dataset?.progressStart);
  assert.ok(bar);
  assert.equal(bar["aria-valuenow"], "50");

  // Without the projected span the renderer derives nothing itself, even with
  // the counted Session in the payload: the projection is the single source.
  const underived = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:30:00Z",
      sessions: [{...displaySession("Closing Keynote"), id: "42", lifecycle: "Live"}],
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: "2099-08-21T09:00:00Z",
        thresholds: [],
      },
    }),
  });
  const bare = underived.document.main.children[1];
  assert.equal(bare.children.find((node) => node.dataset?.progressStart), undefined);
});

test("Stage Timer shows and expires a distinct adjustment notice", async () => {
  const browser = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:00:00Z",
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: "2099-08-21T08:10:00Z",
        thresholds: [],
        adjustmentSeconds: "300",
        adjustmentNoticeExpiresAt: "2099-08-21T08:00:01Z",
      },
    }),
  });
  const region = browser.document.main.children[1];
  const notice = region.children.find((node) => node.dataset.timerAdjustmentNotice);
  assert.ok(notice);
  assert.equal(notice.textContent, "Time adjusted: +5:00");
  assert.doesNotMatch(notice.textContent, /Stage Message/);

  for (let tick = 0; tick < 8 && !notice.hidden; tick++) {
    await browser.runTimer((delay) => delay === 250);
  }
  assert.equal(notice.hidden, true);
});

test("Stage Timer ignores browser wall-clock jumps after synchronization", async () => {
  const browser = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: "2099-08-21T08:00:00Z",
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: "2099-08-21T08:01:00Z",
        thresholds: [],
      },
    }),
  });
  const region = browser.document.main.children[1];
  assert.match(nodeText(region), /01:00/);
  browser.now += 60 * 60 * 1000;

  await browser.runTimer((delay) => delay === 250);
  assert.match(nodeText(region), /01:00/);
});

test("initial clock synchronization retries a noisy sample", async () => {
  const started = Date.now();
  const browser = await startBrowser({
    snapshots: [
      displaySnapshot({serverTime: new Date(started).toISOString()}),
      displaySnapshot({serverTime: new Date(started + 600).toISOString()}),
    ],
    snapshotDelays: [600, 0],
  });
  assert.equal(browser.snapshotRequests, 2);
  assert.equal(browser.acknowledgments.at(-1).clockUncertaintyMilliseconds, 0);
});

test("initial clock synchronization survives a wall-clock step between retries", async () => {
  const started = Date.now();
  const browser = await startBrowser({
    snapshots: [
      displaySnapshot({serverTime: new Date(started + 300).toISOString()}),
      displaySnapshot({serverTime: new Date(started + 950).toISOString()}),
      displaySnapshot({serverTime: new Date(started + 1700).toISOString()}),
    ],
    snapshotDelays: [600, 700, 800],
    snapshotWallJumps: [0, 60 * 60 * 1000, 0],
  });
  const acknowledgment = browser.acknowledgments.at(-1);
  assert.equal(browser.snapshotRequests, 3);
  assert.equal(acknowledgment.clockUncertaintyMilliseconds, 300);
  assert.ok(acknowledgment.clockOffsetMilliseconds < -(59 * 60 * 1000));
});

test("clock synchronization survives a backward wall-clock step during fetch", async () => {
  const started = Date.now();
  const browser = await startBrowser({
    snapshot: stageTimerSnapshot({
      serverTime: new Date(started + 50).toISOString(),
      stageTimer: {
        sessionId: "42",
        title: "Closing Keynote",
        mode: "STAGE_TIMER_MODE_COUNTDOWN",
        anchor: new Date(started + 60_000).toISOString(),
        thresholds: [],
      },
    }),
    snapshotDelays: [100],
    snapshotWallJumps: [-(60 * 60 * 1000)],
  });
  const acknowledgment = browser.acknowledgments.at(-1);
  assert.equal(acknowledgment.clockUncertaintyMilliseconds, 50);
  assert.ok(acknowledgment.clockOffsetMilliseconds > (59 * 60 * 1000));
  assert.match(nodeText(browser.document.main.children[1]), /01:00/);
});

test("clock synchronization retains a good sample after a noisy refresh", async () => {
  const started = Date.now();
  const timer = {
    sessionId: "42",
    title: "Closing Keynote",
    mode: "STAGE_TIMER_MODE_COUNTDOWN",
    anchor: new Date(started + 60_000).toISOString(),
    thresholds: [],
  };
  const browser = await startBrowser({
    snapshots: [
      stageTimerSnapshot({
        serverTime: new Date(started).toISOString(),
        stageTimer: timer,
      }),
      stageTimerSnapshot({
        serverTime: new Date(started).toISOString(),
        publishedRevision: "2",
        stageTimer: timer,
      }),
    ],
    snapshotDelays: [0, 600],
  });
  const initial = browser.acknowledgments.at(-1);
  const before = nodeText(browser.document.main.children[1]);
  browser.now += 60 * 60 * 1000;
  browser.eventSources[0].emit("invalidate", {
    data: JSON.stringify({
      protocol_version: "beamers.display.v1",
      asset_version: "asset-current",
      stream_position: 2,
    }),
  });
  await browser.runTimer((delay) => delay === 0);
  const refreshed = browser.acknowledgments.at(-1);
  assert.equal(nodeText(browser.document.main.children[1]), before);
  assert.equal(
    refreshed.clockOffsetMilliseconds,
    initial.clockOffsetMilliseconds - (60 * 60 * 1000),
  );
  assert.equal(
    refreshed.clockUncertaintyMilliseconds,
    initial.clockUncertaintyMilliseconds,
  );
});

test("Location Now Next excludes inactive Sessions without removing rotation content", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
      serverTime: "2099-08-21T09:00:00Z",
      standby: false,
      viewKey: "location-signage",
      composition: displayComposition({
        key: "location-signage",
        regions: [
          {name: "now-next", widget: "now-next", persistent: true},
          {name: "event-content", widget: "rotation", persistent: false},
        ],
      }),
      sessions: [
        {...displaySession("Canceled Session"), lifecycle: "Canceled"},
        {
          ...displaySession("Ended Session"),
          lifecycle: "Ended",
          forecastEnd: "2099-08-21T08:00:00Z",
        },
        {
          ...displaySession("Past Session"),
          forecastEnd: "2099-08-21T08:59:00Z",
        },
        {...displaySession("Current Session"), lifecycle: "Live"},
        {
          ...displaySession("Next Session"),
          forecastStart: "2099-08-21T10:00:00Z",
          forecastEnd: "2099-08-21T11:00:00Z",
        },
      ],
    }),
  });
  const nowNext = browser.document.main.children[0];
  const rotation = browser.document.main.children[1];
  assert.doesNotMatch(nodeText(nowNext), /Canceled Session/);
  assert.doesNotMatch(nodeText(nowNext), /Ended Session/);
  assert.doesNotMatch(nodeText(nowNext), /Past Session/);
  assert.match(nodeText(nowNext), /Current Session/);
  assert.match(nodeText(nowNext), /Next Session/);
  assert.match(nodeText(rotation), /Canceled Session/);
  assert.match(nodeText(rotation), /Ended Session/);
  assert.match(nodeText(rotation), /Past Session/);
});

test("Display Overrides replace content, overlay Stage Messages, and acknowledge identities", async () => {
  const browser = await startBrowser({
    snapshot: stageTimerSnapshot({
      stageMessage: {
        id: "41",
        revision: "2",
        kind: "StageMessage",
        text: "Stop now",
        emphasis: "Urgent",
        untilCleared: true,
      },
      technicalDifficulties: {
        id: "42",
        revision: "1",
        kind: "TechnicalDifficulties",
        text: "Please wait",
        emphasis: "Normal",
        untilCleared: true,
      },
    }),
  });
  assert.equal(browser.document.main.dataset.overrideKind, "TechnicalDifficulties");
  assert.match(nodeText(browser.document.main), /Please wait/);
  assert.equal(browser.document.aside.dataset.emphasis, "Urgent");
  assert.match(nodeText(browser.document.aside), /Urgent:.*Stop now/);
  assert.deepEqual(
    {
      stageMessageId: browser.acknowledgments.at(-1).stageMessageId,
      stageMessageRevision: browser.acknowledgments.at(-1).stageMessageRevision,
      technicalDifficultiesId: browser.acknowledgments.at(-1).technicalDifficultiesId,
      technicalDifficultiesRevision:
        browser.acknowledgments.at(-1).technicalDifficultiesRevision,
    },
    {
      stageMessageId: "41",
      stageMessageRevision: "2",
      technicalDifficultiesId: "42",
      technicalDifficultiesRevision: "1",
    },
  );
});

test("Display refreshes and acknowledges an Override at its expiry", async () => {
  const initial = displaySnapshot({
    serverTime: "2099-08-21T08:00:00Z",
    stageMessage: {
      id: "41",
      revision: "1",
      kind: "StageMessage",
      text: "One second",
      emphasis: "Normal",
      expiresAt: "2099-08-21T08:00:01Z",
    },
  });
  const browser = await startBrowser({
    snapshots: [
      initial,
      displaySnapshot({
        serverTime: "2099-08-21T08:00:01Z",
        streamPosition: initial.streamPosition,
      }),
    ],
  });
  assert.ok(browser.document.aside);
  await browser.runTimer((delay) => delay >= 25 && delay <= 1000);
  assert.equal(browser.document.aside, undefined);
  assert.equal(browser.acknowledgments.at(-1).stageMessageId, 0);
  assert.equal(browser.acknowledgments.at(-1).streamPosition, initial.streamPosition);
});

test("Emergency and Urgent priority compose above lower Overrides", async () => {
  const lower = {
    stageMessage: {
      id: "10", revision: "1", kind: "StageMessage",
      text: "Stage", emphasis: "Normal", untilCleared: true,
    },
    technicalDifficulties: {
      id: "11", revision: "1", kind: "TechnicalDifficulties",
      text: "Technical", presentation: "Replace", untilCleared: true,
    },
  };
  const overlay = await startBrowser({
    snapshot: stageTimerSnapshot({
      ...lower,
      urgentNotice: {
        id: "12", revision: "1", kind: "UrgentNotice",
        text: "Urgent overlay", presentation: "Overlay", untilCleared: true,
      },
    }),
  });
  assert.equal(overlay.document.main.dataset.overrideKind, "TechnicalDifficulties");
  assert.match(nodeText(overlay.document.aside), /Stage/);
  assert.match(nodeText(overlay.document.urgentAside), /Urgent overlay/);

  const replaced = await startBrowser({
    snapshot: stageTimerSnapshot({
      ...lower,
      urgentNotice: {
        id: "13", revision: "1", kind: "UrgentNotice",
        text: "Urgent replace", presentation: "Replace", untilCleared: true,
      },
    }),
  });
  assert.equal(replaced.document.main.dataset.overrideKind, "UrgentNotice");
  assert.equal(replaced.document.aside, undefined);
  assert.equal(replaced.document.urgentAside, undefined);

  const emergency = await startBrowser({
    snapshot: stageTimerSnapshot({
      ...lower,
      urgentNotice: {
        id: "13", revision: "1", kind: "UrgentNotice",
        text: "Urgent replace", presentation: "Replace", untilCleared: true,
      },
      emergencyAlert: {
        id: "14", revision: "1", kind: "EmergencyAlert",
        text: "Evacuate", presentation: "Replace", untilCleared: true,
      },
    }),
  });
  assert.equal(emergency.document.main.dataset.overrideKind, "EmergencyAlert");
  assert.match(nodeText(emergency.document.main), /Evacuate/);
  assert.equal(emergency.document.aside, undefined);
  assert.equal(emergency.document.urgentAside, undefined);
  assert.equal(emergency.acknowledgments.at(-1).emergencyAlertId, "14");
});

test("display styles preserve content changes while reducing motion", () => {
  const stylesheet = fs.readFileSync(
    new URL("./display.css", `file://${__filename}`),
    "utf8",
  );
  assert.match(stylesheet, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(stylesheet, /transition-duration: 0\.01ms/);
  assert.match(stylesheet, /@media \(min-aspect-ratio: 8\/5\)/);
  // The ranking the client applies to Now / Next has to be styled, or the
  // dataset marker is inert and both Sessions read as equally current.
  assert.match(stylesheet, /\[data-slot="now"\]/);
  assert.match(stylesheet, /\[data-slot="next"\]/);
});

test("display styles cover the type scale, lifecycle badges, and connection states", () => {
  const stylesheet = fs.readFileSync(
    new URL("./display.css", `file://${__filename}`),
    "utf8",
  );
  // The type scale has to reach the Stage Message and Urgent Notice overlay,
  // which both mount outside .display-view -- declaring it there alone would
  // leave those surfaces at the browser default.
  assert.match(stylesheet, /:root\s*\{[^}]*--display-text-body/);
  assert.match(stylesheet, /\.stage-message\s*\{[^}]*var\(--display-text-body\)/);
  assert.match(
    stylesheet,
    /\.urgent-notice-overlay\s*\{[^}]*var\(--display-text-lead\)/,
  );
  // Lifecycle badges use the theme's validated Live and Danger inks, and a
  // Canceled title is struck through wherever the badge appears.
  assert.match(stylesheet, /\.display-badge\[data-lifecycle="Live"\]/);
  assert.match(stylesheet, /\.display-badge\[data-lifecycle="Canceled"\]/);
  assert.match(stylesheet, /\[data-lifecycle="Canceled"\] h3/);
  // Stale and Disconnected cannot share a look, or a frozen panel passes as
  // live; a prolonged Disconnected outage also dims the frame. Reconnecting
  // -- the state a retry attempt occupies before recovery is confirmed --
  // carries the same outage treatment as Disconnected, or the frame would
  // brighten and un-badge on every retry even though nothing has recovered.
  assert.match(stylesheet, /html\[data-connection="stale"\] #display-connection/);
  assert.match(stylesheet, /html\[data-connection="disconnected"\][\s\S]{0,80}#display-connection/);
  assert.match(stylesheet, /html\[data-connection="reconnecting"\][\s\S]{0,80}#display-connection/);
  assert.match(stylesheet, /html\[data-connection="disconnected"\][\s\S]{0,80}\.display-view/);
  assert.match(stylesheet, /html\[data-connection="reconnecting"\][\s\S]{0,80}\.display-view/);
  // The connection pulse animates only the badge's outline, not its own
  // opacity: fading the fill would fade its text along with it and drop
  // below the Display contrast requirement for part of every cycle.
  assert.doesNotMatch(stylesheet, /@keyframes display-connection-pulse\s*\{\s*50%\s*\{\s*opacity/);
  // Lifecycle badges keep a system-color border under forced-colors, or the
  // authored fill/ink collapse leaves an unbounded, unreadable badge shape.
  assert.match(
    stylesheet,
    /@media \(forced-colors: active\)\s*\{\s*\.display-badge\s*\{[^}]*border/,
  );
  // Urgent gets a fill in addition to Attention's outline, so the two levels
  // do not rely on line style alone, but the outline stays on both as the
  // non-color cue.
  assert.match(stylesheet, /\[data-timer-emphasis="urgent"\][^}]*background/);
  assert.match(stylesheet, /\[data-timer-emphasis="attention"\][^}]*outline/);
  assert.match(stylesheet, /\[data-timer-emphasis="urgent"\][^}]*outline/);
});

test("Event Theme variants survive Display snapshot rendering", async () => {
  const composition = displayComposition();
  composition.theme = {
    ...composition.theme,
    branding: "Revision",
    brandAsset: "signal",
    signalColor: "#ffdf6e",
    background: "nebula",
    font: "demoscene",
  };
  const browser = await startBrowser({
    snapshot: displaySnapshot({composition}),
  });
  assert.match(browser.document.main.className, /display-background-nebula/);
  assert.match(browser.document.main.className, /display-font-demoscene/);
  assert.match(nodeText(browser.document.main), /◆ Revision/);
  assert.equal(
    browser.document.main.style.properties.get("--display-signal"),
    "#ffdf6e",
  );
  // The display variable that carries the Theme's surface color is named
  // for what it carries, and the Nebula backdrop mixes it in rather than a
  // hardcoded navy, so a bright Theme Preset still tints the backdrop.
  assert.equal(
    browser.document.main.style.properties.get("--display-surface"),
    "#1d4ed8",
  );
});

test("Nebula backdrop mixes the Theme's surface color instead of a fixed navy", () => {
  const stylesheet = fs.readFileSync(
    new URL("./display.css", `file://${__filename}`),
    "utf8",
  );
  assert.match(
    stylesheet,
    /\.display-background-nebula\s*\{[^}]*color-mix\(in srgb, var\(--display-surface/,
  );
  assert.doesNotMatch(stylesheet, /#172755/);
});

async function startBrowser(options = {}) {
  const timers = new Map();
  let nextTimerID = 0;
  const acknowledgments = [];
  const eventSources = [];
  const storage = new Map();
  const initialMain = new FakeNode("main");
  const body = new FakeNode("body");
  const indicator = new FakeNode("p");
  indicator.dataset.connection = "connecting";
  const document = {
    documentElement: {
      dataset: {
        protocolVersion: "beamers.display.v1",
        assetVersion: "asset-current",
      },
    },
    main: initialMain,
    aside: undefined,
    urgentAside: undefined,
    body,
    createElement(tagName) {
      if (browser.failRendering) {
        throw new Error("renderer unavailable");
      }
      return new FakeNode(tagName, document);
    },
    querySelector(selector) {
      if (selector === "main") {
        return document.main;
      }
      if (selector === "#display-connection") {
        return indicator;
      }
      if (selector === "aside[data-stage-message]") {
        return document.aside;
      }
      if (selector === "aside[data-urgent-notice]") {
        return document.urgentAside;
      }
      return undefined;
    },
  };
  body.append = (...children) => {
    for (const child of children) {
      if (child.tagName === "aside") {
        if (child.dataset.urgentNotice) {
          document.urgentAside = child;
        } else {
          document.aside = child;
        }
      }
      body.children.push(child);
    }
  };
  body.document = document;
  initialMain.document = document;
  class EventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      this.closed = false;
      eventSources.push(this);
    }

    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }

    close() {
      this.closed = true;
    }

    emit(name, event = {}) {
      this.listeners.get(name)?.(event);
    }
  }
  const browser = {
    acknowledgments,
    context: undefined,
    document,
    entryRequests: 0,
    eventSources,
    failRendering: false,
    initialMain,
    indicator,
    now: Date.now(),
    monotonicNow: 0,
    reloads: 0,
    snapshotRequests: 0,
    timerDelays() {
      return [...timers.values()].map((timer) => timer.delay);
    },
    async flush() {
      for (let iteration = 0; iteration < 5; iteration++) {
        await new Promise((resolve) => setImmediate(resolve));
      }
    },
    async runTimer(select) {
      const found = [...timers].find(([, timer]) => select(timer.delay));
      assert.ok(found, "expected a matching recovery timer");
      const [id, timer] = found;
      timers.delete(id);
      this.now += timer.delay;
      this.monotonicNow += timer.delay;
      timer.callback();
      await this.flush();
    },
  };
  const NativeDate = Date;
  class BrowserDate extends NativeDate {
    constructor(value) {
      super(value === undefined ? browser.now : value);
    }

    static now() {
      return browser.now;
    }

    static parse(value) {
      return NativeDate.parse(value);
    }
  }
  const snapshots = options.snapshots ?? [options.snapshot ?? displaySnapshot()];
  async function fetch(url, request = {}) {
    if (url.endsWith("/GetSnapshot")) {
      browser.snapshotRequests++;
      const snapshotIndex = browser.snapshotRequests - 1;
      if (browser.snapshotRequests <= (options.snapshotMaintenance ?? 0)) {
        return {
          ok: false,
          status: 503,
          headers: {
            get(name) {
              return name === "X-Beamers-Maintenance" ? "upgrade" : null;
            },
          },
        };
      }
      if (browser.snapshotRequests <= (options.snapshotFailures ?? 0)) {
        throw new Error("snapshot unavailable");
      }
      const snapshot = snapshots[
        Math.min(snapshotIndex, snapshots.length - 1)
      ];
      const delay = options.snapshotDelays?.[snapshotIndex] ?? 0;
      browser.now += delay;
      browser.monotonicNow += delay;
      browser.now += options.snapshotWallJumps?.[snapshotIndex] ?? 0;
      return jsonResponse({snapshot});
    }
    if (url.endsWith("/Acknowledge")) {
      acknowledgments.push(JSON.parse(request.body));
      return jsonResponse({acknowledgment: {}});
    }
    if (url === "/display") {
      browser.entryRequests++;
      return {
        ok: true,
        headers: {
          get(name) {
            return name === "X-Beamers-Display-Asset"
              ? (options.availableAsset ?? "asset-current")
              : null;
          },
        },
      };
    }
    throw new Error(`unexpected fetch ${url}`);
  }
  const context = {
    AbortController,
    Date: BrowserDate,
    EventSource,
    Intl,
    JSON,
    Math,
    Number,
    performance: {
      now() {
        return browser.monotonicNow;
      },
    },
    URLSearchParams,
    clearTimeout(id) {
      timers.delete(id);
    },
    document,
    fetch,
    sessionStorage: {
      getItem(key) {
        return storage.get(key) ?? null;
      },
      setItem(key, value) {
        storage.set(key, value);
      },
    },
    setTimeout(callback, delay) {
      const id = ++nextTimerID;
      timers.set(id, {callback, delay});
      return id;
    },
    window: {
      location: {
        reload() {
          browser.reloads++;
        },
      },
    },
  };
  browser.context = vm.createContext(context);
  vm.runInContext(clientSource, browser.context, {filename: "client.js"});
  await browser.flush();
  return browser;
}

function displaySnapshot(overrides = {}) {
  return {
    protocolVersion: "beamers.display.v1",
    assetVersion: "asset-current",
    serverTime: new Date().toISOString(),
    displayName: "Test Display",
    activeEventId: "1",
    activationGeneration: "1",
    publishedRevision: "1",
    standby: true,
    streamId: "stream-1",
    streamPosition: "1",
    snapshotToken: "snapshot-token",
    sessions: [],
    composition: displayComposition(),
    ...overrides,
  };
}

function displayComposition(overrides = {}) {
  return {
    layout: {
      key: "standby",
      rotationSeconds: 15,
      regions: [
        {name: "branding", widget: "branding", persistent: true},
        {name: "message", widget: "standby", persistent: true},
      ],
      ...overrides,
    },
    theme: {
      branding: "",
      foregroundColor: "#ffffff",
      backgroundColor: "#101828",
      surfaceColor: "#1d4ed8",
      signalColor: "#62ebcb",
      liveColor: "#ff7b72",
      dangerColor: "#ff8fa3",
      background: "solid",
      scrimColor: "#000000",
      scrimOpacity: 85,
      font: "sans",
      transition: "fade",
    },
  };
}

function stageTimerSnapshot(overrides = {}) {
  return displaySnapshot({
    standby: false,
    viewKey: "stage-timer",
    composition: displayComposition({
      key: "stage-timer",
      regions: [
        {name: "header", widget: "branding", persistent: true},
        {name: "timer", widget: "stage-timer", persistent: true},
      ],
    }),
    ...overrides,
  });
}

function displaySession(title, overrides = {}) {
  return {
    title,
    lifecycle: "Scheduled",
    forecastStart: "2099-08-21T08:00:00Z",
    forecastEnd: "2099-08-21T09:00:00Z",
    presentedStart: "2099-08-21T08:00:00Z",
    presentedEnd: "2099-08-21T09:00:00Z",
    presentedStartLabel: "Forecast Start",
    presentedEndLabel: "Forecast End",
    ...overrides,
  };
}

function jsonResponse(body) {
  return {
    ok: true,
    async json() {
      return body;
    },
  };
}

function nodeText(node) {
  return [node.textContent, ...node.children.map(nodeText)].join(" ");
}

class FakeNode {
  constructor(tagName, document) {
    this.children = [];
    this.dataset = {};
    this.document = document;
    this.hidden = false;
    this.style = {
      properties: new Map(),
      setProperty(name, value) {
        this.properties.set(name, value);
      },
    };
    this.classList = {
      add: (...names) => {
        this.className = [this.className, ...names].filter(Boolean).join(" ");
      },
    };
    this.tagName = tagName;
    this.textContent = "";
  }

  append(...children) {
    this.children.push(...children);
  }

  setAttribute(name, value) {
    this[name] = value;
  }

  replaceWith(replacement) {
    if (this.document?.main === this) {
      this.document.main = replacement;
    }
  }

  remove() {
    if (this.document?.aside === this) {
      this.document.aside = undefined;
    }
    if (this.document?.urgentAside === this) {
      this.document.urgentAside = undefined;
    }
  }
}
