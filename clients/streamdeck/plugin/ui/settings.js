/* Stream Deck's property inspector entry point. */
let socket;
let context;
let saved = {};
const fields = ["provider", "cliPath"];
const meter = document.getElementById("bucket");
const custom = document.getElementById("customBucket");
const status = document.getElementById("status");
function show(settings) {
  saved = settings ?? {};
  for (const name of fields) {
    const fallback = { provider: "Claude", cliPath: "" }[name];
    document.getElementById(name).value = typeof saved[name] === "string" ? saved[name] : fallback;
  }
  const id = typeof saved.bucket === "string" ? saved.bucket : "first";
  const known = [...meter.options].some(option => option.value === id && id !== "custom");
  meter.value = known ? id : "custom";
  custom.value = known ? "" : id;
  document.getElementById("customMeter").hidden = known;
}
function save(patch) {
  if (socket?.readyState !== WebSocket.OPEN) {
    status.textContent = "Reopen these settings to reconnect.";
    return;
  }
  status.textContent = "";
  saved = { ...saved, ...patch };
  socket.send(JSON.stringify({ event: "setSettings", context, payload: saved }));
}
window.connectElgatoStreamDeckSocket = (port, uuid, registerEvent, info, actionInfo) => {
  context = uuid;
  const action = JSON.parse(actionInfo);
  document.getElementById("usage").hidden = action.action !== "com.aaronfeledy.headroom.usage";
  show(action.payload.settings);
  socket = new WebSocket("ws://127.0.0.1:" + port);
  socket.onopen = () => socket.send(JSON.stringify({ event: registerEvent, uuid }));
  socket.onmessage = ({ data }) => {
    const message = JSON.parse(data);
    if (message.event === "didReceiveSettings") show(message.payload.settings);
  };
  socket.onclose = () => { status.textContent = "Reopen these settings to reconnect."; };
};
for (const name of fields) {
  document.getElementById(name).addEventListener("change", () => {
    save({ [name]: document.getElementById(name).value.trim() });
  });
}
meter.addEventListener("change", () => {
  document.getElementById("customMeter").hidden = meter.value !== "custom";
  if (meter.value !== "custom") save({ bucket: meter.value });
  else custom.focus();
});
custom.addEventListener("change", () => {
  const id = custom.value.trim();
  if (/^[a-z0-9_]{1,64}$/.test(id) && id !== "first" && id !== "custom") save({ bucket: id });
  else status.textContent = "Enter a meter ID using lowercase letters, numbers, and underscores.";
});
