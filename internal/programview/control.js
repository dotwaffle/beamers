const eventId = Number(document.body.dataset.eventId);
const sessionId = Number(document.body.dataset.sessionId);
const build = document.querySelector('meta[name="beamers-build"]').content;
const statusNode = document.querySelector("#connection-status");
let channel;
let account;
const pendingCommands = new Map();

function commandId(prefix) {
  return `${prefix}-${crypto.randomUUID()}`;
}

async function call(service, method, body, options = {}) {
  const response = await fetch(`/beamers.${service}.v1.${method.service}/${method.name}`, {
    method: "POST",
    credentials: "same-origin",
    keepalive: options.keepalive ?? false,
    headers: {
      "Content-Type": "application/json",
      "Connect-Protocol-Version": "1",
      "X-Beamers-Build": build,
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({}));
    const failure = new Error(error.message ?? `${method.name} failed`);
    failure.responseReceived = true;
    throw failure;
  }
  return response.json();
}

async function commandCall(key, service, method, body, options = {}) {
  let request = pendingCommands.get(key);
  if (!request) {
    request = {...body, commandId: commandId(`program-${method.name.toLowerCase()}`)};
    pendingCommands.set(key, request);
  }
  try {
    const result = await call(service, method, request, options);
    pendingCommands.delete(key);
    return result;
  } catch (error) {
    if (error.responseReceived) pendingCommands.delete(key);
    throw error;
  }
}

const programMethod = (name) => ({
  service: "ProgramControlService",
  name,
});

async function refresh() {
  const response = await call("program", programMethod("GetProgramChannel"), {
    eventId,
    sessionId,
  });
  adopt(response.channel);
}

function adopt(next) {
  if (channel &&
      (Number(next.controlStateRevision) < Number(channel.controlStateRevision) ||
       Number(next.liveStateRevision) < Number(channel.liveStateRevision))) {
    return;
  }
  channel = next;
  render();
}

function itemLabel(item) {
  if (!item) return "—";
  return item.title || item.kind.replace("PROGRAM_ITEM_KIND_", "").toLowerCase();
}

function render() {
  for (const field of ["previous", "current", "next", "preview", "programOutput"]) {
    const node = document.querySelector(`[data-item="${field}"]`);
    node.textContent = itemLabel(channel[field]);
    if (field === "programOutput") {
      node.dataset.kind = channel[field]?.kind ?? "";
      node.dataset.entryId = String(channel[field]?.entryId ?? "");
      node.dataset.revision = String(channel.liveStateRevision);
    }
  }
  const owner = channel.controlOwner;
  document.querySelector("#owner").textContent = owner
    ? `${owner.name} · ${owner.connected ? "connected" : "disconnected"}`
    : "Unowned";
  const requester = channel.handoverRequester;
  document.querySelector("#handover-requester").textContent = requester
    ? `Handover requested by ${requester.name} · ${requester.connected ? "connected" : "disconnected"}`
    : "No handover request";
  const items = document.querySelector("#program-items");
  items.replaceChildren(...(channel.items ?? []).map((item) => {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = itemLabel(item);
    const replay = item.kind === "PROGRAM_ITEM_KIND_ENTRY" &&
      [channel.previous, channel.current, channel.programOutput].some(
        (shown) => shown?.kind === item.kind && shown?.entryId === item.entryId,
      );
    button.setAttribute("aria-label", `${replay ? "Replay" : "Preview"} ${itemLabel(item)}`);
    button.setAttribute("aria-pressed", String(
      item.kind === channel.preview?.kind && item.entryId === channel.preview?.entryId,
    ));
    button.addEventListener("click", () => selectPreview(item));
    return button;
  }));
  const displays = document.querySelector("#displays");
  displays.replaceChildren(...(channel.consumingDisplays ?? []).map((display) => {
    const item = document.createElement("li");
    item.dataset.displayId = String(display.displayId);
    item.dataset.delivery = display.deliveryState;
    item.textContent = `${display.name}: ${display.deliveryState}`;
    return item;
  }));
  if (!channel.consumingDisplays?.length) {
    const item = document.createElement("li");
    item.textContent = "No Displays consume this channel.";
    displays.append(item);
  }
  const deferButton = document.querySelector("#defer");
  const retryDefer = pendingCommands.has("program-defer");
  const deferred = retryDefer ||
    channel.next?.kind === "PROGRAM_ITEM_KIND_ENTRY" && !channel.next.retry;
  deferButton.hidden = !deferred;
  deferButton.textContent = retryDefer
    ? "Retry exact Defer command"
    : deferred
    ? `Defer ${itemLabel(channel.next)}`
    : "Defer current Entry";
  statusNode.textContent = `Live revision ${channel.liveStateRevision}; control revision ${channel.controlStateRevision}`;
}

async function changeControl(action, options = {}) {
  if (action === "CONTROL_ACTION_TAKEOVER" &&
      !confirm("Take control from the current owner?")) {
    return;
  }
  const response = await commandCall(`program-control-${action}`, "program", programMethod("ChangeControl"), {
    eventId,
    sessionId,
    action,
    confirmed: action === "CONTROL_ACTION_TAKEOVER",
    expectedControlStateRevision: channel.controlStateRevision,
  }, options);
  adopt(response.channel);
}

async function selectPreview(item) {
  const response = await commandCall(
    `program-preview-${JSON.stringify(item)}`,
    "program",
    programMethod("SelectPreview"),
    {
    eventId,
    sessionId,
    item,
    expectedControlStateRevision: channel.controlStateRevision,
    },
  );
  adopt(response.channel);
}

async function take() {
  let order = {entryOrder: {revision: 0}, fingerprint: ""};
  if (channel.preview?.kind === "PROGRAM_ITEM_KIND_ENTRY") {
    order = await call("competition", {
      service: "CompetitionService",
      name: "PreviewEntryOrder",
    }, {eventId, sessionId});
  }
  const response = await commandCall("program-take", "program", programMethod("Take"), {
    eventId,
    sessionId,
    expectedLiveStateRevision: channel.liveStateRevision,
    expectedControlStateRevision: channel.controlStateRevision,
    expectedEntryOrderRevision: order.entryOrder.revision,
    entryOrderFingerprint: order.fingerprint,
    preview: channel.preview,
  });
  adopt(response.channel);
}

async function deferEntry() {
  if (pendingCommands.has("program-defer")) {
    const response = await commandCall(
      "program-defer",
      "program",
      programMethod("DeferEntry"),
      {},
    );
    adopt(response.channel);
    return;
  }
  const next = channel.next;
  if (next?.kind !== "PROGRAM_ITEM_KIND_ENTRY" || next.retry) return;
  const competition = await call("competition", {
    service: "CompetitionService",
    name: "GetCompetition",
  }, {eventId, sessionId});
  const entry = competition.entries.find(
    (candidate) => Number(candidate.id) === Number(next.entryId),
  );
  if (!entry) throw new Error("Current Entry changed; refresh and try again");
  const response = await commandCall("program-defer", "program", programMethod("DeferEntry"), {
    eventId,
    sessionId,
    entryId: next.entryId,
    expectedEntryRevision: entry.revision,
    expectedProgramRevision: channel.liveStateRevision,
    expectedControlStateRevision: channel.controlStateRevision,
  });
  adopt(response.channel);
}

async function start() {
  account = await fetch("/auth/session", {credentials: "same-origin"}).then((response) => response.json());
  await refresh();
  const events = new EventSource(`/crew/program/${sessionId}/events?event_id=${eventId}`);
  events.addEventListener("invalidate", () => refresh().catch(showError));
  events.addEventListener("error", () => {
    statusNode.textContent = "Program stream reconnecting…";
  });
}

function showError(error) {
  statusNode.textContent = error.responseReceived
    ? error.message
    : `${error.message}; retry the same action to replay its exact command`;
}

for (const button of document.querySelectorAll("[data-control-action]")) {
  button.addEventListener("click", () => changeControl(button.dataset.controlAction).catch(showError));
}
document.querySelector("#take").addEventListener("click", () => take().catch(showError));
document.querySelector("#defer").addEventListener("click", () => deferEntry().catch(showError));

window.addEventListener("beforeunload", (event) => {
  if (channel?.controlOwner?.name === account?.name && channel.controlOwner.connected) {
    event.preventDefault();
  }
});
start().catch(showError);
