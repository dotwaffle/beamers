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
  await browser.runTimer((delay) => delay === 60000);
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

  await browser.runTimer((delay) => delay === 30000);
  assert.equal(browser.document.main, committedFrame);
  assert.equal(committedFrame.children[0], header);
  assert.equal(committedFrame.children[2], clock);
  assert.equal(rotation.children[0].hidden, true);
  assert.equal(rotation.children[1].hidden, false);
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

test("Location Now Next excludes canceled Sessions without removing rotation content", async () => {
  const browser = await startBrowser({
    snapshot: displaySnapshot({
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
        displaySession("Current Session"),
        displaySession("Next Session"),
      ],
    }),
  });
  const nowNext = browser.document.main.children[0];
  const rotation = browser.document.main.children[1];
  assert.doesNotMatch(nodeText(nowNext), /Canceled Session/);
  assert.match(nodeText(nowNext), /Current Session/);
  assert.match(nodeText(nowNext), /Next Session/);
  assert.match(nodeText(rotation), /Canceled Session/);
});

test("display styles preserve content changes while reducing motion", () => {
  const template = fs.readFileSync(
    new URL("./page.templ", `file://${__filename}`),
    "utf8",
  );
  assert.match(template, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(template, /transition-duration: 0\.01ms/);
  assert.match(template, /@media \(min-aspect-ratio: 8\/5\)/);
});

async function startBrowser(options = {}) {
  const timers = new Map();
  let nextTimerID = 0;
  const acknowledgments = [];
  const eventSources = [];
  const storage = new Map();
  const initialMain = new FakeNode("main");
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
      return undefined;
    },
  };
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
      if (browser.snapshotRequests <= (options.snapshotFailures ?? 0)) {
        throw new Error("snapshot unavailable");
      }
      const snapshot = snapshots[
        Math.min(browser.snapshotRequests - 1, snapshots.length - 1)
      ];
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
      accentColor: "#1d4ed8",
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

function displaySession(title) {
  return {
    title,
    lifecycle: "Planned",
    forecastStart: "2099-08-21T08:00:00Z",
    forecastEnd: "2099-08-21T09:00:00Z",
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
    this.tagName = tagName;
    this.textContent = "";
  }

  append(...children) {
    this.children.push(...children);
  }

  replaceWith(replacement) {
    if (this.document?.main === this) {
      this.document.main = replacement;
    }
  }
}
