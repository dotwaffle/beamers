const eventId = Number(document.body.dataset.eventId);
const sessionId = Number(document.body.dataset.sessionId);
const build = document.querySelector('meta[name="beamers-build"]').content;
const statusNode = document.querySelector("#connection-status");
let channel;
let account;

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
    throw new Error(error.message ?? `${method.name} failed`);
  }
  return response.json();
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
    document.querySelector(`[data-item="${field}"]`).textContent = itemLabel(channel[field]);
  }
  const owner = channel.controlOwner;
  document.querySelector("#owner").textContent = owner
    ? `${owner.name} · ${owner.connected ? "connected" : "disconnected"}`
    : "Unowned";
  const items = document.querySelector("#program-items");
  items.replaceChildren(...(channel.items ?? []).map((item) => {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = itemLabel(item);
    button.setAttribute("aria-pressed", String(
      item.kind === channel.preview?.kind && item.entryId === channel.preview?.entryId,
    ));
    button.addEventListener("click", () => selectPreview(item));
    return button;
  }));
  const displays = document.querySelector("#displays");
  displays.replaceChildren(...(channel.consumingDisplays ?? []).map((display) => {
    const item = document.createElement("li");
    item.dataset.delivery = display.deliveryState;
    item.textContent = `${display.name}: ${display.deliveryState}`;
    return item;
  }));
  if (!channel.consumingDisplays?.length) {
    const item = document.createElement("li");
    item.textContent = "No Displays consume this channel.";
    displays.append(item);
  }
  statusNode.textContent = `Live revision ${channel.liveStateRevision}; control revision ${channel.controlStateRevision}`;
}

async function changeControl(action, options = {}) {
  if (action === "CONTROL_ACTION_TAKEOVER" &&
      !confirm("Take control from the current owner?")) {
    return;
  }
  const response = await call("program", programMethod("ChangeControl"), {
    eventId,
    sessionId,
    action,
    confirmed: action === "CONTROL_ACTION_TAKEOVER",
    commandId: commandId("program-control"),
    expectedControlStateRevision: channel.controlStateRevision,
  }, options);
  adopt(response.channel);
}

async function selectPreview(item) {
  const response = await call("program", programMethod("SelectPreview"), {
    eventId,
    sessionId,
    item,
    commandId: commandId("program-preview"),
    expectedControlStateRevision: channel.controlStateRevision,
  });
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
  const response = await call("program", programMethod("Take"), {
    eventId,
    sessionId,
    commandId: commandId("program-take"),
    expectedLiveStateRevision: channel.liveStateRevision,
    expectedControlStateRevision: channel.controlStateRevision,
    expectedEntryOrderRevision: order.entryOrder.revision,
    entryOrderFingerprint: order.fingerprint,
    preview: channel.preview,
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
  statusNode.textContent = error.message;
}

for (const button of document.querySelectorAll("[data-control-action]")) {
  button.addEventListener("click", () => changeControl(button.dataset.controlAction).catch(showError));
}
document.querySelector("#take").addEventListener("click", () => take().catch(showError));

window.addEventListener("beforeunload", (event) => {
  if (channel?.controlOwner?.name === account?.name && channel.controlOwner.connected) {
    event.preventDefault();
  }
});
start().catch(showError);
