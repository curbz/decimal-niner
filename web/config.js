const state = { config: {}, path: "", browserTarget: null, browserMode: "file", browserPath: "" };
const $ = (selector) => document.querySelector(selector);

const overrides = {
  "d9.logging_level": { type: "radio", options: ["debug", "info", "warn", "error"] },
  "d9.traffic_engine": { type: "radio", options: ["vat", "trafficglobal", "d9traffic"] },
  "atc.listen_all_frequencies": { type: "checkbox" },
  "atc.strict_flightplan_matching": { type: "checkbox" },
  "atc.airlines_file": { type: "file" },
  "atc.atc_data_file": { type: "file" },
  "atc.atc_regions_file": { type: "file" },
  "atc.atc_holds_file": { type: "file" },
  "atc.atc_nav_data_file": { type: "file" },
  "atc.atc_fixes_file": { type: "file" },
  "atc.airports_cifp_dir": { type: "directory" },
  "atc.airports_data_file": { type: "file" },
  "atc.voices.piper.application": { type: "file" },
  "atc.voices.piper.voice_directory": { type: "directory" },
  "atc.voices.sox.application": { type: "file" },
  "atc.message_buffer_size": { type: "number", step: 1, min: 1 },
  "atc.voices.handoff_valediction_factor": { type: "number", step: 1, min: 1 },
  "atc.voices.say_again_factor": { type: "number", step: 1, min: 1 },
  "vat.refresh_interval_seconds": { type: "number", step: 0.1, min: 0.1 },
  "trafficglobal.plugin_directory": { type: "directory" },
  "d9traffic.flight_plan_directory": { type: "directory" },
  "d9.resources_path": { type: "directory" }
};

function prettyName(key) {
  return key.replace(/_/g, " ").replace(/\b\w/g, c => c.toUpperCase());
}

function el(tag, attrs = {}, text = "") {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key === "class") node.className = value;
    else if (key === "for") node.htmlFor = value;
    else node.setAttribute(key, value);
  }
  if (text !== "") node.textContent = text;
  return node;
}

function createScalar(key, value, path) {
  const override = overrides[path] || {};
  const wrapper = el("div", { class: "field" });
  wrapper.appendChild(el("label", { for: `field-${path}` }, prettyName(key)));

  if (typeof value === "boolean" || override.type === "checkbox") {
    const input = el("input", { id: `field-${path}`, type: "checkbox" });
    input.checked = Boolean(value);
    input.dataset.path = path;
    input.dataset.type = "boolean";
    wrapper.appendChild(input);
  } else if (override.type === "radio") {
    const group = el("div");
    for (const option of override.options) {
      const row = el("label", { class: "radio-row" });
      const input = el("input", { type: "radio", name: `radio-${path}`, value: option });
      input.checked = value === option;
      input.dataset.path = path;
      input.dataset.type = "string";
      row.appendChild(input);
      row.appendChild(document.createTextNode(` ${option}`));
      group.appendChild(row);
    }
    wrapper.appendChild(group);
  } else if (override.type === "file" || override.type === "directory") {
    const control = el("div", { class: "path-control" });
    const input = el("input", { id: `field-${path}`, type: "text", value: value == null ? "" : String(value) });
    input.dataset.path = path;
    input.dataset.type = typeof value === "number" ? "number" : "string";
    const browse = el("button", { type: "button" }, "Browse");
    browse.addEventListener("click", () => openBrowser(input, override.type));
    control.appendChild(input); control.appendChild(browse); wrapper.appendChild(control);
  } else {
    const input = el("input", { id: `field-${path}`, type: typeof value === "number" ? "number" : "text", value: value == null ? "" : String(value) });
    if (override.step) input.step = String(override.step);
    if (override.min != null) input.min = String(override.min);
    input.dataset.path = path;
    input.dataset.type = typeof value === "number" ? (Number.isInteger(value) ? "integer" : "number") : "string";
    wrapper.appendChild(input);
  }
  return wrapper;
}

function setAtPath(config, path, value) {
  const parts = path.split(".");
  let cursor = config;
  for (let i = 0; i < parts.length - 1; i++) {
    const part = parts[i];
    const next = parts[i + 1];
    if (Array.isArray(cursor)) {
      const index = parseInt(part, 10);
      cursor = cursor[index];
    } else {
      if (cursor[part] == null) cursor[part] = /^\d+$/.test(next) ? [] : {};
      cursor = cursor[part];
    }
  }
  const finalPart = parts[parts.length - 1];
  if (Array.isArray(cursor)) cursor[parseInt(finalPart, 10)] = value;
  else cursor[finalPart] = value;
}

function readValue(input) {
  if (input.type === "radio") return input.checked ? input.value : undefined;
  if (input.dataset.type === "boolean") return input.checked;
  if (input.dataset.type === "integer") return parseInt(input.value, 10);
  if (input.dataset.type === "number") return parseFloat(input.value);
  return input.value;
}

function collect() {
  const copy = JSON.parse(JSON.stringify(state.config));
  for (const input of document.querySelectorAll("[data-path]")) {
    const value = readValue(input);
    if (value !== undefined) setAtPath(copy, input.dataset.path, value);
  }
  for (const row of document.querySelectorAll(".array[data-path]")) {
    const values = [...row.querySelectorAll(".array-input")].map(input => {
      if (row.dataset.itemType === "integer") return parseInt(input.value, 10);
      if (row.dataset.itemType === "number") return parseFloat(input.value);
      return input.value;
    });
    setAtPath(copy, row.dataset.path, values);
  }
  return copy;
}

function createArray(key, values, path) {
  const itemType = typeof values[0] === "number" ? (Number.isInteger(values[0]) ? "integer" : "number") : "string";
  const wrapper = el("div", { class: "array", path });
  wrapper.dataset.path = path;
  wrapper.dataset.itemType = itemType;
  const title = el("label", {}, prettyName(key));
  wrapper.appendChild(title);

  const addRow = (value = itemType === "integer" ? 0 : itemType === "number" ? 0 : "") => {
    const row = el("div", { class: "array-row" });
    const input = el("input", { class: "array-input", type: itemType === "string" ? "text" : "number", value: String(value) });
    const remove = el("button", { type: "button" }, "Remove");
    remove.addEventListener("click", () => row.remove());
    row.appendChild(input); row.appendChild(remove); wrapper.appendChild(row);
  };
  values.forEach(addRow);
  const add = el("button", { type: "button", class: "array-add" }, "+ Add");
  add.addEventListener("click", () => addRow());
  wrapper.appendChild(add);
  return wrapper;
}

function createObjectCard(key, value, path) {
  const card = el("div", { class: "object-card" });
  card.appendChild(el("strong", {}, prettyName(key)));
  for (const [childKey, childValue] of Object.entries(value)) {
    const childPath = `${path}.${childKey}`;
    card.appendChild(renderValue(childKey, childValue, childPath));
  }
  return card;
}

function renderValue(key, value, path) {
  if (Array.isArray(value)) {
    if (value.every(item => typeof item !== "object" || item === null)) return createArray(key, value, path);
    const card = el("div", { class: "object-card" });
    card.appendChild(el("strong", {}, prettyName(key)));
    value.forEach((item, index) => card.appendChild(createObjectCard(String(index + 1), item, `${path}.${index}`)));
    return card;
  }
  if (value !== null && typeof value === "object") {
    const wrapper = el("div");
    wrapper.appendChild(el("strong", {}, prettyName(key)));
    for (const [childKey, childValue] of Object.entries(value)) {
      wrapper.appendChild(renderValue(childKey, childValue, `${path}.${childKey}`));
    }
    return wrapper;
  }
  return createScalar(key, value, path);
}

function renderSections(config) {
  const sections = $("#sections");
  sections.replaceChildren();
  for (const [sectionName, value] of Object.entries(config)) {
    const details = el("details", { class: "section", open: "" });
    details.appendChild(el("summary", {}, prettyName(sectionName)));
    const body = el("div", { class: "section-body" });
    if (value !== null && typeof value === "object" && !Array.isArray(value)) {
      for (const [key, child] of Object.entries(value)) {
        body.appendChild(renderValue(key, child, `${sectionName}.${key}`));
      }
    } else {
      body.appendChild(renderValue(sectionName, value, sectionName));
    }
    details.appendChild(body);
    sections.appendChild(details);
  }
}

async function loadConfig() {
  const status = $("#status");
  try {
    const response = await fetch("/api/config");
    if (!response.ok) throw new Error((await response.json()).error || "Could not load configuration");
    const data = await response.json();
    state.config = data.config;
    state.path = data.path;
    $("#config-path").textContent = data.path;
    renderSections(state.config);
    status.textContent = "Configuration loaded";
  } catch (error) {
    status.className = "error";
    status.textContent = error.message;
  }
}

async function saveConfig() {
  const status = $("#status");
  const button = $("#save");
  button.disabled = true;
  try {
    const response = await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: collect() })
    });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || "Could not save configuration");
    status.className = "success";
    status.textContent = "Configuration saved. Restart decimal-niner for changes to take effect.";
  } catch (error) {
    status.className = "error";
    status.textContent = error.message;
  } finally {
    button.disabled = false;
  }
}

async function openBrowser(target, mode) {
  state.browserTarget = target;
  state.browserMode = mode;
  let initial = target.value;
  if (!initial) initial = state.path ? state.path.substring(0, state.path.lastIndexOf("/")) : "/";
  await browse(initial);
  $("#browser").showModal();
}

async function browse(path) {
  const response = await fetch(`/api/files?mode=${encodeURIComponent(state.browserMode)}&path=${encodeURIComponent(path)}`);
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || "Could not read directory");
  state.browserPath = data.path;
  $("#browser-path").textContent = data.path;
  const entries = $("#browser-entries");
  entries.replaceChildren();
  for (const entry of data.entries) {
    const button = el("button", { type: "button", class: "browser-entry" }, `${entry.dir ? "📁" : "📄"} ${entry.name}`);
    button.addEventListener("click", async () => {
      if (entry.dir) await browse(entry.path);
      else state.browserPath = entry.path;
    });
    entries.appendChild(button);
  }
}

$("#save").addEventListener("click", saveConfig);
$("#browser-select").addEventListener("click", () => {
  if (state.browserTarget) state.browserTarget.value = state.browserPath;
});

loadConfig();
