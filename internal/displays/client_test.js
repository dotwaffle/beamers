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
  assert.notEqual(browser.document.main, committedFrame);
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
  const browser = await startBrowser();
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
  const browser = await startBrowser();
  const committedFrame = browser.document.main;
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
      timer.callback();
      await this.flush();
    },
  };
  const snapshot = options.snapshot ?? displaySnapshot();
  async function fetch(url, request = {}) {
    if (url.endsWith("/GetSnapshot")) {
      browser.snapshotRequests++;
      if (browser.snapshotRequests <= (options.snapshotFailures ?? 0)) {
        throw new Error("snapshot unavailable");
      }
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
    Date,
    EventSource,
    Intl,
    JSON,
    Math,
    Number,
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

class FakeNode {
  constructor(tagName, document) {
    this.children = [];
    this.dataset = {};
    this.document = document;
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
