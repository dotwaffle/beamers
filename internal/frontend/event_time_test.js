import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

test("Event time uses the Event locale and timezone", () => {
  const elements = [
    { dateTime: "2099-08-21T11:30:00Z", dataset: { locale: "en-US", timezone: "Europe/Paris" } },
    { dateTime: "2099-08-21T11:30:00Z", dataset: { locale: "de-DE", timezone: "Europe/Paris" } },
  ];
  vm.runInNewContext(
    readFileSync(new URL("vendor/event-time.js", import.meta.url), "utf8"),
    { document: { querySelectorAll: () => elements }, Intl, Date },
  );

  assert.match(elements[0].textContent, /Aug/);
  assert.match(elements[1].textContent, /Aug/);
  assert.notEqual(elements[0].textContent, elements[1].textContent);
  assert.match(elements[0].textContent, /1:30/);
});
