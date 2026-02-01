const $ = (id) => document.getElementById(id);

const player = $("player");
const audioToggle = $("audioToggle");
const histPerfToggle = $("histPerfToggle");
const histPerformanceWrap = $("histPerformanceWrap");
const noEntryTitle = $("noEntryTitle");
const noEntryHint = $("noEntryHint");

const nfInt = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

// Filters UI
const applyFiltersBtn = $("applyFiltersBtn");
const filtersStatus = $("filtersStatus");
const f_or_rng_min = $("f_or_rng_min");
const f_or_rng_max = $("f_or_rng_max");
const f_or_vol_min = $("f_or_vol_min");
const f_or_vol_max = $("f_or_vol_max");
const f_today_min = $("f_today_min");
const f_today_max = $("f_today_max");
const f_entry_min = $("f_entry_min");
const f_entry_max = $("f_entry_max");
const f_px_min = $("f_px_min");
const f_px_max = $("f_px_max");

// Historic controls (exist in DOM even if the card is hidden)
const historicDateInput = $("historicDate");
const histPrevBtn = $("histPrevBtn");
const histNextBtn = $("histNextBtn");
const histTodayBtn = $("histTodayBtn");
const histLoadBtn = $("histLoadBtn");
const historicSubtitle = $("historicSubtitle");
const historicNote = $("historicNote");

const seenEventIDs = new Set();
let currentSessionID = null;
let pendingHistoricRequest = false;
let histMinISO = "";
let histMaxISO = "";
let lastState = null;

// -----------------------------
// Filters: allow staged edits
// -----------------------------
const dirtyFilterIDs = new Set();
function markFilterDirty(el) {
  if (!el || !el.id) return;
  dirtyFilterIDs.add(el.id);
}
function clearFilterDirty() {
  dirtyFilterIDs.clear();
}
function isFilterDirty(el) {
  if (!el || !el.id) return false;
  return dirtyFilterIDs.has(el.id);
}

// -----------------------------
// Historic: performance toggle (persisted)
// -----------------------------
let showHistoricPerformance = true;
try {
  const saved = localStorage.getItem("orb_show_hist_perf");
  if (saved !== null) showHistoricPerformance = (saved === "1");
} catch (_) {}
if (histPerfToggle) {
  histPerfToggle.checked = showHistoricPerformance;
  histPerfToggle.addEventListener("change", () => {
    showHistoricPerformance = !!histPerfToggle.checked;
    try { localStorage.setItem("orb_show_hist_perf", showHistoricPerformance ? "1" : "0"); } catch (_) {}
    if (lastState) renderState(lastState);
  });
}

function fmt(n, digits=2) {
  if (n === null || n === undefined) return "—";
  if (typeof n !== "number") return String(n);
  if (!isFinite(n)) return "—";
  return n.toFixed(digits);
}

function fmtInt(n) {
  if (n === null || n === undefined) return "—";
  const x = (typeof n === "number") ? n : Number(n);
  if (!isFinite(x)) return "—";
  return nfInt.format(Math.round(x));
}

function fmtPct(x) {
  if (!isFinite(x)) return "—";
  return (x * 100).toFixed(2) + "%";
}
function fmtMoney(x) {
  if (!isFinite(x)) return "—";
  const sign = x < 0 ? "-" : "";
  const v = Math.abs(x);
  return `${sign}$${v.toFixed(2)}`;
}
function badge(status) {
  const s = (status || "").toUpperCase();
  let cls = "neutral";
  if (s === "LONG" || s === "PROFIT") cls = "good";
  else if (s === "STOP" || s === "STOP LOSS HIT") cls = "bad";
  else if (s === "TIME_EXIT") cls = "neutral";
  else if (s === "SELECTED" || s === "TRACKING") cls = "warn";
  return `<span class="badge ${cls}">${status || "—"}</span>`;
}

function setHistoricNote(text) {
  if (!historicNote) return;
  if (!text) {
    historicNote.style.display = "none";
    historicNote.textContent = "";
    return;
  }
  historicNote.style.display = "";
  historicNote.textContent = text;
}

function setHistoricControlsDisabled(disabled) {
  for (const el of [historicDateInput, histPrevBtn, histNextBtn, histTodayBtn, histLoadBtn]) {
    if (el) el.disabled = !!disabled;
  }
}

function setFiltersStatus(text) {
  if (!filtersStatus) return;
  if (!text) {
    filtersStatus.style.display = "none";
    filtersStatus.textContent = "";
    return;
  }
  filtersStatus.style.display = "";
  filtersStatus.textContent = text;
}

function numVal(el) {
  if (!el) return null;
  const s = String(el.value ?? "").trim();
  if (s === "") return null;
  const v = Number(s);
  return isFinite(v) ? v : null;
}
function intVal(el) {
  if (!el) return null;
  const s = String(el.value ?? "").trim();
  if (s === "") return null;
  const v = parseInt(s, 10);
  return Number.isFinite(v) ? v : null;
}

async function applyFilters() {
  const payload = {
    open_5m_range_pct_min: numVal(f_or_rng_min),
    open_5m_range_pct_max: numVal(f_or_rng_max),
    open_5m_vol_min: numVal(f_or_vol_min),
    open_5m_vol_max: numVal(f_or_vol_max),
    open_5m_today_pct_min: numVal(f_today_min),
    open_5m_today_pct_max: numVal(f_today_max),
    entry_minutes_after_open_min: intVal(f_entry_min),
    entry_minutes_after_open_max: intVal(f_entry_max),
    entry_price_min: numVal(f_px_min),
    entry_price_max: numVal(f_px_max),
  };

  setFiltersStatus("Saving…");
  try {
    const res = await fetch("/api/filters", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      cache: "no-store",
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body?.ok === false) {
      setFiltersStatus(body?.error || `Failed to update filters (${res.status})`);
      return;
    }
    setFiltersStatus("Saved.");
    clearFilterDirty();
    setTimeout(() => setFiltersStatus(""), 1200);

    // Historic UX: re-run the currently selected date so changes apply immediately
    if (lastState?.mode === "historic") {
      const fallbackISO =
        (lastState?.historic_resolved_date_ny || lastState?.historic_target_date_ny || (lastState?.now_ny || "").slice(0, 10));
      const iso = clampISO(historicDateInput?.value || fallbackISO || histMaxISO);
      if (iso) {
        setFiltersStatus("Saved. Replaying historic session…");
        requestHistoricRun(iso);
      }
    }
  } catch (_) {
    setFiltersStatus("Failed to update filters (network error).");
  }
}

function clampISO(iso) {
  if (!iso) return iso;
  if (histMinISO && iso < histMinISO) return histMinISO;
  if (histMaxISO && iso > histMaxISO) return histMaxISO;
  return iso;
}

function addDaysISO(iso, delta) {
  const [y, m, d] = iso.split("-").map(Number);
  const dt = new Date(Date.UTC(y, m - 1, d));
  dt.setUTCDate(dt.getUTCDate() + delta);
  const yy = dt.getUTCFullYear();
  const mm = String(dt.getUTCMonth() + 1).padStart(2, "0");
  const dd = String(dt.getUTCDate()).padStart(2, "0");
  return `${yy}-${mm}-${dd}`;
}

function stepWeekdayISO(iso, delta) {
  let cur = iso;
  for (let i = 0; i < 10; i++) {
    cur = addDaysISO(cur, delta);
    const dow = new Date(cur + "T00:00:00Z").getUTCDay(); // 0=Sun,6=Sat
    if (dow !== 0 && dow !== 6) return cur;
  }
  return cur;
}

async function requestHistoricRun(dateISO) {
  if (!dateISO) return;
  if (pendingHistoricRequest) return;

  pendingHistoricRequest = true;
  setHistoricControlsDisabled(true);
  if (histLoadBtn) histLoadBtn.textContent = "Loading…";
  setHistoricNote("Starting historic replay…");

  try {
    const res = await fetch(`/api/historic/run?date=${encodeURIComponent(dateISO)}`, {
      method: "POST",
      cache: "no-store",
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      setHistoricNote(body?.error || `Failed to start historic replay (${res.status})`);
    }
  } catch (_) {
    setHistoricNote("Failed to start historic replay (network error).");
  } finally {
    pendingHistoricRequest = false;
    if (histLoadBtn) histLoadBtn.textContent = "Load";
    // re-enable is controlled by renderState() based on phase
  }
}

async function fetchState() {
  const res = await fetch("/api/state", { cache: "no-store" });
  return await res.json();
}

function addEvent(ev, {silent=false} = {}) {
  const wrap = $("events");
  const row = document.createElement("div");
  row.className = "event";
  row.innerHTML = `
    <div>
      <div class="meta">${ev.time_ny} · ${ev.type}${ev.symbol ? " · " + ev.symbol : ""}</div>
      <div class="msg">${ev.message}</div>
    </div>
    <div>${badge(ev.type)}</div>
  `;
  wrap.prepend(row);

  while (wrap.children.length > 200) wrap.removeChild(wrap.lastChild);

  if (!silent && audioToggle.checked && ev.audio_id) {
    playAudio(ev.audio_id);
  }
}

function syncEvents(events) {
  if (!Array.isArray(events) || events.length === 0) return;

  // Add in chronological order (old → new) because addEvent() prepends.
  for (const ev of events) {
    if (!ev || !ev.id) continue;
    if (seenEventIDs.has(ev.id)) continue;
    seenEventIDs.add(ev.id);
    addEvent(ev, { silent: true });
  }

  // prevent unbounded growth
  if (seenEventIDs.size > 2000) {
    seenEventIDs.clear();
  }
}

async function playAudio(id) {
  try {
    const res = await fetch(`/api/audio/${id}.mp3`, { cache: "no-store" });
    if (!res.ok) return;
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    player.src = url;
    await player.play();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  } catch (_) {}
}

function connectEvents() {
  const es = new EventSource("/api/events");
  es.addEventListener("event", (msg) => {
    try {
      const ev = JSON.parse(msg.data);
      if (ev && ev.id && !seenEventIDs.has(ev.id)) {
        seenEventIDs.add(ev.id);
        addEvent(ev, { silent: false });
      }
    } catch (_) {}
  });
}

function renderHistoric(report, mode, st) {
  const card = $("historicCard");
  if (mode !== "historic") {
    card.style.display = "none";
    return;
  }
  card.style.display = "";

  // Show/hide performance tables
  if (histPerformanceWrap) {
    histPerformanceWrap.style.display = showHistoricPerformance ? "" : "none";
  }

  // date picker bounds & value
  histMinISO = st.historic_min_date_ny || "";
  histMaxISO = st.historic_max_date_ny || "";
  if (historicDateInput) {
    if (histMinISO) historicDateInput.min = histMinISO;
    if (histMaxISO) historicDateInput.max = histMaxISO;
  }

  const resolvedISO = st.historic_resolved_date_ny || report?.summary?.date_ny || "";
  const targetISO = st.historic_target_date_ny || "";
  const displayISO = clampISO(resolvedISO || targetISO || (st.now_ny || "").slice(0, 10));
  if (historicDateInput && document.activeElement !== historicDateInput) {
    if (historicDateInput.value !== displayISO) historicDateInput.value = displayISO;
  }

  const busy = pendingHistoricRequest || (st.phase && st.phase !== "closed");
  setHistoricControlsDisabled(busy);

  if (historicSubtitle) {
    const windowText = report?.summary ? `${report.summary.window_start_ny} → ${report.summary.window_end_ny}` : "";
    historicSubtitle.textContent = displayISO ? `Session: ${displayISO}${windowText ? " · " + windowText : ""}` : "Pick a date to replay the session.";
  }

  const note = st.historic_note || "";
  setHistoricNote(note);

  // If we don't have a ready report yet, clear performance tables so old data doesn't linger.
  // (If performance is OFF, we may still render the "Of interest" list from st.tickers.)
  if (!report || !report.summary) {
    const sumWrap = $("histSummary");
    const tb = $("tradesBody");
    if (sumWrap) sumWrap.innerHTML = "";
    if (tb) tb.innerHTML = "";
  }

  // -----------------------------------------
  // Performance OFF: show "Of interest" list
  // -----------------------------------------
  if (!showHistoricPerformance) {
    if (noEntryTitle) noEntryTitle.textContent = "Of interest (09:35 selected)";
    if (noEntryHint) noEntryHint.textContent = "All tickers that met the 09:30–09:35 selection filters (Open5m Range% + Open5m Vol).";

    const nb = $("noEntryBody");
    if (!nb) return;
    nb.innerHTML = "";

    // Build reason maps from the report if available
    const reasonBySym = new Map();
    const tradeBySym = new Map();
    if (report?.no_entries) {
      for (const n of report.no_entries) reasonBySym.set(n.symbol, n.reason || "");
    }
    if (report?.trades) {
      for (const t of report.trades) tradeBySym.set(t.symbol, t);
    }

    const tickers = Array.isArray(st.tickers) ? st.tickers.slice() : [];
    tickers.sort((a,b) => (a.symbol || "").localeCompare(b.symbol || ""));

    for (const t of tickers) {
      const sym = t.symbol || "";
      const tr = tradeBySym.get(sym);
      const reason =
        (reasonBySym.get(sym)) ||
        (tr ? (`Trade: ${tr.exit_reason || "—"}`) : "") ||
        "";

      const row = document.createElement("tr");
      row.innerHTML = `
        <td><strong>${sym}</strong></td>
        <td>${fmtInt(t.open_5m_vol)}</td>
        <td>${fmtPct(t.open_5m_range_pct)}</td>
        <td>${isFinite(t.open_5m_today_pct) ? t.open_5m_today_pct.toFixed(1) + "%" : "—"}</td>
        <td>${t.saw_cross_in_window ? "Yes" : "No"}</td>
        <td>${t.first_cross_time_ny || "—"}</td>
        <td>${(t.first_cross_price > 0) ? fmt(t.first_cross_price, 2) : "—"}</td>
        <td>${reason || "—"}</td>
      `;
      nb.appendChild(row);
    }
    return;
  }

  // -----------------------------------------
  // Performance ON: existing historic report
  // -----------------------------------------
  if (noEntryTitle) noEntryTitle.textContent = "Selected but no entry";
  if (noEntryHint) noEntryHint.textContent = "Useful for diagnosing false positives (no VWAP cross in the entry window, Today% filter failures, etc.).";

  if (!report || !report.summary) return;

  const s = report.summary;
  const metrics = [
    ["Date", s.date_ny],
    ["Window", `${s.window_start_ny} → ${s.window_end_ny}`],
    ["Candidates", s.candidates],
    ["Trades", s.trades_taken],
    ["No-entry", s.no_entry],
    ["Win rate", isFinite(s.win_rate) ? (s.win_rate * 100).toFixed(1) + "%" : "—"],
    ["Wins", s.wins],
    ["Losses", s.losses],
    ["TIME_EXIT", s.time_exits],
    ["Net P/L", fmtMoney(s.net_pnl)],
    ["Net return", fmtPct(s.net_return_pct)],
    ["Profit factor", isFinite(s.profit_factor) ? s.profit_factor.toFixed(2) : "—"],
    ["Avg trade", fmtPct(s.avg_return_pct)],
    ["Best trade", fmtPct(s.best_trade_pct)],
    ["Worst trade", fmtPct(s.worst_trade_pct)],
  ];

  const sumWrap = $("histSummary");
  sumWrap.innerHTML = metrics.map(([k,v]) => `
    <div class="metric">
      <span>${k}</span>
      <strong>${v ?? "—"}</strong>
    </div>
  `).join("");

  // Trades table
  const tb = $("tradesBody");
  tb.innerHTML = "";
  const trades = Array.isArray(report.trades) ? report.trades : [];
  for (const t of trades) {
    const cls =
      t.realized_pnl_pct > 0 ? "pos" :
      t.realized_pnl_pct < 0 ? "neg" : "flat";
    const tr = document.createElement("tr");
    tr.className = cls;
    tr.innerHTML = `
      <td><strong>${t.symbol}</strong></td>
      <td>${t.entry_time_ny}</td>
      <td>${fmt(t.entry_price, 4)}</td>
      <td>${fmt(t.take_profit_price, 4)}</td>
      <td>${fmt(t.stop_price, 4)}</td>
      <td>${t.exit_time_ny}</td>
      <td>${fmt(t.exit_price, 4)}</td>
      <td>${badge(t.exit_reason)}</td>
      <td>${fmtPct(t.realized_pnl_pct)}</td>
      <td>${fmtMoney(t.realized_pnl)}</td>
      <td>${fmtPct(t.mfe_pnl_pct)}</td>
      <td>${fmtMoney(t.mfe_pnl)}</td>
      <td>${fmtPct(t.mae_pnl_pct)}</td>
      <td>${fmtMoney(t.mae_pnl)}</td>
      <td>${fmtPct(t.hold_pnl_pct)}</td>
      <td>${fmtMoney(t.hold_pnl)}</td>
    `;
    tb.appendChild(tr);
  }

  // No-entry table
  const nb = $("noEntryBody");
  nb.innerHTML = "";
  const ne = Array.isArray(report.no_entries) ? report.no_entries : [];
  for (const n of ne) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><strong>${n.symbol}</strong></td>
      <td>${fmtInt(n.open_5m_vol)}</td>
      <td>${fmtPct(n.open_5m_range_pct)}</td>
      <td>${isFinite(n.open_5m_today_pct) ? n.open_5m_today_pct.toFixed(1) + "%" : "—"}</td>
      <td>${n.saw_cross_in_window ? "Yes" : "No"}</td>
      <td>${n.first_cross_time_ny || "—"}</td>
      <td>${fmt(n.first_cross_price, 2)}</td>
      <td>${n.reason || "—"}</td>
    `;
    nb.appendChild(tr);
  }
}

function renderState(st) {
  lastState = st;
  $("now").textContent = st.now_ny;
  $("phase").textContent = st.phase;
  $("watchCount").textContent = st.watchlist_count;
  $("trackedCount").textContent = st.tracked_count;
  $("openTime").textContent = st.open_time_ny;
  $("selTime").textContent = st.selection_time_ny;
  $("cutoffTime").textContent = st.vwap_cutoff_ny;
  $("exitTime").textContent = st.force_exit_ny;

  const mode = st.mode || "realtime";
  $("mode").textContent = mode;

  // Filters UI sync (don't overwrite the field being edited)
  const f = st.filters || {};
  const syncInput = (el, v) => {
    if (!el) return;
    if (document.activeElement === el) return;
    if (isFilterDirty(el)) return;
    if (v === null || v === undefined) return;
    const s = String(v);
    if (el.value !== s) el.value = s;
  };
  syncInput(f_or_rng_min, f.open_5m_range_pct_min);
  syncInput(f_or_rng_max, f.open_5m_range_pct_max);
  syncInput(f_or_vol_min, f.open_5m_vol_min);
  syncInput(f_or_vol_max, f.open_5m_vol_max);
  syncInput(f_today_min, f.open_5m_today_pct_min);
  syncInput(f_today_max, f.open_5m_today_pct_max);
  syncInput(f_entry_min, f.entry_minutes_after_open_min);
  syncInput(f_entry_max, f.entry_minutes_after_open_max);
  syncInput(f_px_min, f.entry_price_min);
  syncInput(f_px_max, f.entry_price_max);

  // New session boundary: clear UI caches so identical event IDs across replays don't get ignored
  if (st.session_id && st.session_id !== currentSessionID) {
    currentSessionID = st.session_id;
    seenEventIDs.clear();
    $("events").innerHTML = "";
  }

  // Audio UX: disabled in historic mode
  if (mode === "historic") {
    audioToggle.checked = false;
    audioToggle.disabled = true;
    $("testAudioBtn").disabled = true;
  } else {
    audioToggle.disabled = false;
    $("testAudioBtn").disabled = false;
  }

  $("status").textContent =
    st.phase === "collecting_open_5m" ? "Collecting 09:30-09:34 minute bars..." :
    st.phase === "selecting_0935" ? "Selecting candidates + computing open_5m_today_pct..." :
    st.phase === "tracking_ticks" ? "Tracking tick trades for filtered tickers (VWAP cross logic active)..." :
    st.phase === "waiting_open" ? "Waiting for 09:30 open..." :
    "Idle / closed";

  // Backfill events so historic runs still show a log even if the UI loads later
  syncEvents(st.events || []);

  // Historic report
  renderHistoric(st.historic_report, mode, st);

  // Existing tickers table (still useful)
  const body = $("tickersBody");
  body.innerHTML = "";

  const tickers = (st.tickers || []).slice().sort((a,b) => {
    const sa = (a.status||"").toUpperCase();
    const sb = (b.status||"").toUpperCase();
    const score = (s) => s==="LONG"?0 : s==="PROFIT"?1 : s==="STOP"?2 : 9;
    return score(sa) - score(sb);
  });

  for (const t of tickers) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><strong>${t.symbol}</strong></td>
      <td>${badge(t.status)}</td>
      <td>${fmt(t.last_price, 4)}</td>
      <td>${fmt(t.vwap, 4)}</td>
      <td>${fmt(t.minutes_after_open, 2)}</td>
      <td>${fmt(t.open_0930, 4)}</td>
      <td>${fmtInt(t.open_5m_vol)}</td>
      <td>${fmtPct(t.open_5m_range_pct)}</td>
      <td>${isFinite(t.open_5m_today_pct) ? t.open_5m_today_pct.toFixed(1) + "%" : "—"}</td>
      <td>${fmt(t.entry_price, 4)}</td>
      <td>${fmt(t.take_profit_price, 4)}</td>
      <td>${fmt(t.stop_price, 4)}</td>
    `;
    body.appendChild(tr);
  }
}

async function loop() {
  try {
    const st = await fetchState();
    renderState(st);
  } catch (_) {}
  setTimeout(loop, 1000);
}

$("testAudioBtn").addEventListener("click", () => {
  addEvent({ time_ny:"—", type:"TEST", message:"(Audio fires on BUY/PROFIT/STOP/11AM if OPENAI_API_KEY is set and mode is realtime.)" });
});

if (applyFiltersBtn) {
  applyFiltersBtn.addEventListener("click", applyFilters);
}

// Mark filter fields dirty on edit so the 1s poll doesn't overwrite staged changes
for (const el of [
  f_or_rng_min, f_or_rng_max,
  f_or_vol_min, f_or_vol_max,
  f_today_min, f_today_max,
  f_entry_min, f_entry_max,
  f_px_min, f_px_max,
]) {
  if (!el) continue;
  el.addEventListener("input", () => markFilterDirty(el));
}

// Historic control wiring
if (historicDateInput) {
  historicDateInput.addEventListener("change", () => {
    const iso = clampISO(historicDateInput.value);
    if (iso) requestHistoricRun(iso);
  });
}
if (histLoadBtn) {
  histLoadBtn.addEventListener("click", () => {
    const iso = clampISO(historicDateInput?.value);
    if (iso) requestHistoricRun(iso);
  });
}
if (histTodayBtn) {
  histTodayBtn.addEventListener("click", () => {
    const iso = clampISO(histMaxISO);
    if (historicDateInput && iso) {
      historicDateInput.value = iso;
      requestHistoricRun(iso);
    }
  });
}
if (histPrevBtn) {
  histPrevBtn.addEventListener("click", () => {
    const base = clampISO(historicDateInput?.value || histMaxISO);
    const iso = clampISO(stepWeekdayISO(base, -1));
    if (historicDateInput && iso) {
      historicDateInput.value = iso;
      requestHistoricRun(iso);
    }
  });
}
if (histNextBtn) {
  histNextBtn.addEventListener("click", () => {
    const base = clampISO(historicDateInput?.value || histMaxISO);
    const iso = clampISO(stepWeekdayISO(base, +1));
    if (historicDateInput && iso) {
      historicDateInput.value = iso;
      requestHistoricRun(iso);
    }
  });
}

connectEvents();
loop();
