import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

function loadCommandCall(fetch) {
  let nonce = 0;
  const control = {addEventListener() {}};
  const context = {
    crypto: {randomUUID: () => `uuid-${++nonce}`},
    document: {
      body: {dataset: {eventId: "1", sessionId: "2"}},
      querySelector(selector) {
        if (selector === 'meta[name="beamers-build"]') return {content: "test"};
        if (selector === "#connection-status") return {textContent: ""};
        return control;
      },
      querySelectorAll: () => [],
    },
    fetch,
    window: {addEventListener() {}},
  };
  vm.createContext(context);
  const source = fs.readFileSync(
    new URL("control.js", import.meta.url),
    "utf8",
  ).replace("start().catch(showError);", "");
  vm.runInContext(`${source}\nglobalThis.commandCallForTest = commandCall;`, context);
  return context.commandCallForTest;
}

test("ambiguous failure retries the exact command and definitive failure releases it", async () => {
  const requests = [];
  const outcomes = [
    new Error("connection reset"),
    {ok: true, json: async () => ({})},
    {ok: false, json: async () => ({message: "stale"})},
    {ok: true, json: async () => ({})},
  ];
  const commandCall = loadCommandCall(async (_url, options) => {
    requests.push(JSON.parse(options.body));
    const outcome = outcomes.shift();
    if (outcome instanceof Error) throw outcome;
    return outcome;
  });
  const method = {service: "ProgramControlService", name: "Take"};

  await assert.rejects(
    commandCall("take", "program", method, {expectedLiveStateRevision: 1}),
    /connection reset/,
  );
  await commandCall("take", "program", method, {expectedLiveStateRevision: 2});
  assert.deepEqual(requests[1], requests[0]);

  await assert.rejects(
    commandCall("take", "program", method, {expectedLiveStateRevision: 3}),
    /stale/,
  );
  await commandCall("take", "program", method, {expectedLiveStateRevision: 4});
  assert.notEqual(requests[3].commandId, requests[2].commandId);
  assert.equal(requests[3].expectedLiveStateRevision, 4);
});
