const $ = (id) => document.getElementById(id);
const state = { auto: true, timer: null };

function baseUrl() {
  return ($("base").value || "").replace(/\/$/, "");
}

function showError(msg) {
  const el = $("error");
  if (!msg) { el.hidden = true; el.textContent = ""; return; }
  el.hidden = false;
  el.textContent = msg;
}

function showActionResult(ok, text) {
  const el = $("action-result");
  el.hidden = !text;
  el.className = ok ? "okbox" : "err";
  el.textContent = text || "";
}

function persistCreds() {
  try {
    sessionStorage.setItem("autofix.cc.token", $("admin-token").value);
    sessionStorage.setItem("autofix.cc.actor", $("actor").value);
  } catch (_) {}
}

function restoreCreds() {
  try {
    const t = sessionStorage.getItem("autofix.cc.token");
    const a = sessionStorage.getItem("autofix.cc.actor");
    if (t) $("admin-token").value = t;
    if (a) $("actor").value = a;
  } catch (_) {}
}

function parseProm(text) {
  const metrics = {};
  for (const line of text.split("\n")) {
    if (!line || line.startsWith("#")) continue;
    const m = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+([0-9.eE+-]+)/);
    if (!m) continue;
    const name = m[1];
    const labels = {};
    if (m[2]) {
      for (const part of m[2].slice(1, -1).split(",")) {
        const kv = part.match(/([a-zA-Z_][a-zA-Z0-9_]*)="((?:\\.|[^"])*)"/);
        if (kv) labels[kv[1]] = kv[2];
      }
    }
    const value = parseFloat(m[3]);
    if (!metrics[name]) metrics[name] = [];
    metrics[name].push({ labels: labels, value: value });
  }
  return metrics;
}

function findMetric(metrics, name, labelFilter) {
  const rows = metrics[name] || [];
  if (!labelFilter) return rows;
  return rows.filter((r) => Object.entries(labelFilter).every(([k, v]) => r.labels[k] === v));
}

function sumMetric(metrics, name, labelFilter) {
  return findMetric(metrics, name, labelFilter).reduce((a, r) => a + r.value, 0);
}

function circuitLabel(code) {
  if (code === 0) return "closed";
  if (code === 1) return "half_open";
  if (code === 2) return "open";
  return "unknown";
}

async function fetchJson(path, opts) {
  const res = await fetch(baseUrl() + path, Object.assign({ cache: "no-store" }, opts || {}));
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch (_) { body = text; }
  if (!res.ok) {
    const detail = body && body.error ? body.error : text;
    const err = new Error(path + " → HTTP " + res.status + (detail ? " " + detail : ""));
    err.status = res.status;
    err.body = body;
    throw err;
  }
  return body;
}

async function fetchText(path) {
  const res = await fetch(baseUrl() + path, { cache: "no-store" });
  if (!res.ok) throw new Error(path + " → HTTP " + res.status);
  return res.text();
}

function renderHealth(health) {
  if (!health) {
    $("health-text").textContent = "unknown";
    $("health-dot").className = "dot unknown";
    return;
  }
  const status = health.status || (health.ok === true || health.ok === "ok" ? "ok" : (health.health || "unknown"));
  $("health-text").textContent = status;
  $("circuit-text").textContent = health.wayback_circuit || "—";
  if (health.paused === true || health.paused === "true" || health.discovery_paused === "true") {
    $("paused-text").textContent = "yes";
  } else if (health.paused === false || health.paused === "false" || health.discovery_paused === "false") {
    $("paused-text").textContent = "no";
  }
  $("health-dot").className = "dot " + (status === "ok" || status === true ? "ok" : status === "unknown" ? "unknown" : "warn");
}

function renderMetrics(metrics) {
  const q = sumMetric(metrics, "autofix_queue_depth");
  $("kpi-queue").textContent = Number.isFinite(q) ? String(Math.round(q)) : "—";
  const discHttp = sumMetric(metrics, "autofix_discover_requests_total", { source: "http" });
  $("kpi-discover-http").textContent = Number.isFinite(discHttp) ? String(Math.round(discHttp)) : "—";
  const trips = sumMetric(metrics, "autofix_circuit_trips_total");
  $("kpi-trips").textContent = Number.isFinite(trips) ? String(Math.round(trips)) : "—";
  const healSum = sumMetric(metrics, "autofix_heal_duration_seconds_sum");
  const healCount = sumMetric(metrics, "autofix_heal_duration_seconds_count");
  if (healCount > 0) {
    $("kpi-heal-avg").textContent = (healSum / healCount).toFixed(3);
    $("kpi-heal-n").textContent = "n=" + Math.round(healCount);
  } else {
    $("kpi-heal-avg").textContent = "—";
    $("kpi-heal-n").textContent = "";
  }
  const statuses = ["PENDING", "HEALED", "DEAD", "HEALTHY"];
  $("links-body").innerHTML = statuses.map((s) => {
    const n = sumMetric(metrics, "autofix_links_total", { status: s });
    return "<tr><td>" + s + "</td><td class=\"num\">" + (Number.isFinite(n) ? Math.round(n) : 0) + "</td></tr>";
  }).join("");
  const circuitRows = findMetric(metrics, "autofix_circuit_state");
  const tripsByName = {};
  for (const r of findMetric(metrics, "autofix_circuit_trips_total")) {
    tripsByName[r.labels.name || "?"] = r.value;
  }
  if (!circuitRows.length) {
    $("circuits-body").innerHTML = "<tr><td colspan=\"4\" style=\"color:var(--muted)\">No circuit metrics</td></tr>";
  } else {
    $("circuits-body").innerHTML = circuitRows.map((r) => {
      const name = r.labels.name || "?";
      return "<tr><td>" + name + "</td><td>" + circuitLabel(r.value) + "</td><td class=\"num\">" + r.value + "</td><td class=\"num\">" + (tripsByName[name] != null ? Math.round(tripsByName[name]) : "—") + "</td></tr>";
    }).join("");
  }
}

function renderAdminStats(stats) {
  if (!stats) return;
  $("kpi-queue").textContent = stats.queue_depth != null ? String(stats.queue_depth) : "—";
  const disc = (stats.discover_requests_total && stats.discover_requests_total.http) || 0;
  $("kpi-discover-http").textContent = String(disc);
  const circuits = stats.circuits || [];
  let trips = 0;
  for (const c of circuits) trips += c.trips_total || 0;
  $("kpi-trips").textContent = String(trips);
  const heal = stats.heal_duration_seconds;
  if (heal && heal.count > 0) {
    $("kpi-heal-avg").textContent = (heal.sum / heal.count).toFixed(3);
    $("kpi-heal-n").textContent = "n=" + Math.round(heal.count);
  } else {
    $("kpi-heal-avg").textContent = "—";
    $("kpi-heal-n").textContent = "";
  }
  const links = stats.links_written || {};
  const statuses = ["PENDING", "HEALED", "DEAD", "HEALTHY"];
  $("links-body").innerHTML = statuses.map((s) => "<tr><td>" + s + "</td><td class=\"num\">" + (links[s] || 0) + "</td></tr>").join("");
  if (!circuits.length) {
    $("circuits-body").innerHTML = "<tr><td colspan=\"4\" style=\"color:var(--muted)\">No circuits</td></tr>";
  } else {
    $("circuits-body").innerHTML = circuits.map((c) =>
      "<tr><td>" + (c.name || "?") + "</td><td>" + (c.state || "—") +
      "</td><td class=\"num\">" + (c.state_code != null ? c.state_code : "—") +
      "</td><td class=\"num\">" + (c.trips_total != null ? c.trips_total : "—") + "</td></tr>"
    ).join("");
  }
  if (circuits[0] && circuits[0].state) $("circuit-text").textContent = circuits[0].state;
  if (stats.health) {
    $("health-text").textContent = stats.health;
    $("health-dot").className = "dot " + (stats.health === "ok" ? "ok" : "warn");
  }
}

function parseUrls() {
  return $("urls").value.split(/\s+/).map((s) => s.trim()).filter(Boolean);
}

function authHeaders() {
  const token = ($("admin-token").value || "").trim();
  const headers = { "Content-Type": "application/json" };
  if (token) headers.Authorization = "Bearer " + token;
  return headers;
}

function renderAudit(events) {
  const body = $("audit-body");
  if (!events || !events.length) {
    body.innerHTML = "<tr><td colspan=\"5\" style=\"color:var(--muted)\">No audit events yet</td></tr>";
    return;
  }
  const rows = events.slice().reverse().slice(0, 40);
  body.innerHTML = rows.map((ev) => {
    const ok = ev.ok ? "<span class=\"ok-flag\">ok</span>" : "<span class=\"bad-flag\">fail</span>";
    const extra = ev.error || (ev.after ? JSON.stringify(ev.after) : "");
    const reason = (ev.reason || "") + (extra ? " — " + extra : "");
    return "<tr><td class=\"num\">" + (ev.ts || "—") + "</td><td>" + (ev.actor || "—") +
      "</td><td>" + (ev.action || "—") + "</td><td>" + ok + "</td><td>" + reason + "</td></tr>";
  }).join("");
}

async function loadAudit() {
  persistCreds();
  try {
    const data = await fetchJson("/v1/admin/audit", { headers: authHeaders() });
    renderAudit((data && data.events) || []);
  } catch (e) {
    $("audit-body").innerHTML = "<tr><td colspan=\"5\" style=\"color:var(--muted)\">" + (e && e.message ? e.message : e) + "</td></tr>";
  }
}

async function postAction(action, extra) {
  persistCreds();
  const actor = ($("actor").value || "").trim();
  const reason = ($("reason").value || "").trim();
  if (!actor || !reason) {
    showActionResult(false, "actor and reason are required");
    return;
  }
  const body = Object.assign({ action: action, actor: actor, reason: reason }, extra || {});
  try {
    const res = await fetchJson("/v1/admin/actions", {
      method: "POST",
      headers: authHeaders(),
      body: JSON.stringify(body),
    });
    showActionResult(true, JSON.stringify(res, null, 2));
    await Promise.all([refresh(), loadAudit()]);
  } catch (e) {
    const payload = e && e.body ? JSON.stringify(e.body, null, 2) : "";
    showActionResult(false, (e && e.message ? e.message : String(e)) + (payload ? "\n" + payload : ""));
  }
}

async function refresh() {
  const errors = [];
  let usedAdmin = false;
  try {
    const stats = await fetchJson("/v1/admin/stats");
    renderAdminStats(stats);
    usedAdmin = true;
  } catch (e) {
    errors.push("admin/stats: " + (e && e.message ? e.message : e));
  }
  if (!usedAdmin) {
    try {
      const health = await fetchJson("/healthz");
      renderHealth(health);
    } catch (e) {
      errors.push("healthz: " + (e && e.message ? e.message : e));
      renderHealth(null);
    }
    try {
      const text = await fetchText("/metrics");
      renderMetrics(parseProm(text));
      errors.length = 0;
    } catch (e) {
      errors.push("metrics: " + (e && e.message ? e.message : e) + "\n(If this is CORS, ensure healer withCORS is running.)");
    }
  } else {
    try {
      const health = await fetchJson("/healthz");
      renderHealth(health);
    } catch (_) {}
  }
  $("last-fetch").textContent = new Date().toLocaleTimeString();
  showError(errors.length ? errors.join("\n\n") : "");
}

function schedule() {
  if (state.timer) clearInterval(state.timer);
  if (!state.auto) return;
  const sec = Math.max(3, Number($("interval").value) || 8);
  state.timer = setInterval(refresh, sec * 1000);
}

$("refresh").addEventListener("click", () => refresh());
$("auto").addEventListener("click", () => {
  state.auto = !state.auto;
  $("auto").textContent = "Auto: " + (state.auto ? "on" : "off");
  schedule();
});
$("interval").addEventListener("change", schedule);
$("admin-token").addEventListener("change", persistCreds);
$("actor").addEventListener("change", persistCreds);
$("act-requeue").addEventListener("click", () => {
  const urls = parseUrls();
  if (!urls.length) { showActionResult(false, "urls required for link.requeue"); return; }
  postAction("link.requeue", { urls: urls });
});
$("act-override").addEventListener("click", () => {
  const urls = parseUrls();
  if (!urls.length) { showActionResult(false, "urls required for link.override"); return; }
  const extra = { urls: urls, status: $("status").value };
  const resolved = ($("resolved").value || "").trim();
  if (resolved) extra.resolved_url = resolved;
  postAction("link.override", extra);
});
$("act-reset").addEventListener("click", () => {
  postAction("circuit.reset", { circuit_name: "healer_wayback" });
});
$("act-pause").addEventListener("click", () => postAction("discovery.pause"));
$("act-resume").addEventListener("click", () => postAction("discovery.resume"));
$("act-audit").addEventListener("click", () => loadAudit());

restoreCreds();
refresh();
schedule();
