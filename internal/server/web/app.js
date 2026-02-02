const $ = (id) => document.getElementById(id);

const player = $("player");
const audioToggle = $("audioToggle");
const histPerfToggle = $("histPerfToggle");
const histPerformanceWrap = $("histPerformanceWrap");
const noEntryTitle = $("noEntryTitle");
const noEntryHint = $("noEntryHint");

// Charts (Of interest slideshow)
const chartsToolbar = $("chartsToolbar");
const showChartsBtn = $("showChartsBtn");
const hideChartsBtn = $("hideChartsBtn");
const chartPrevBtn = $("chartPrevBtn");
const chartNextBtn = $("chartNextBtn");
const chartTickerSelect = $("chartTickerSelect");
const chartListSelect = $("chartListSelect");
const chartCounter = $("chartCounter");
const chartPanel = $("chartPanel");
const chartContainer = $("chartContainer");
const chartSym = $("chartSym");
const chartMeta = $("chartMeta");
const chartRevealBtn = $("chartRevealBtn");
const chartNextBarBtn = $("chartNextBarBtn");

// Sold-off section
const soldOffTitle = $("soldOffTitle");
const soldOffHint = $("soldOffHint");
const soldOffBody = $("soldOffBody");

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

// Sold-off scan filters
const f_sold_pct_min = $("f_sold_pct_min");
const f_sold_rng_min = $("f_sold_rng_min");
const f_sold_today_min = $("f_sold_today_min");

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
// Charts state (Of interest slideshow)
// -----------------------------
let chartsDateISO = "";
let chartsOpen = false;
let chartIndex = 0;
let interestTickers = [];
const chartDataCache = new Map(); // sym -> payload from /api/chart/bars

let soldOffTickers = [];

let chartListMode = "interest"; // "interest" | "soldoff"
try {
  const saved = localStorage.getItem("orb_chart_list_mode");
  if (saved === "interest" || saved === "soldoff") chartListMode = saved;
} catch (_) {}

function getActiveChartTickers() {
  return (chartListMode === "soldoff") ? soldOffTickers : interestTickers;
}

// lightweight-charts instances
let lwChart = null;
let lwCandle = null;
let lwSMA = null;
let lwVWAP = null;
let lwVolume = null;
let lwOnResize = null;
let chartsTimeZone = "America/New_York";

// -----------------------------
// "Guess then reveal" state
// -----------------------------
// Initial window:
// - optional 09:29 prev-close (if present)
// - 09:30–09:35 inclusive (6 bars)
const INITIAL_MINUTES_AFTER_OPEN = 6; // 09:30..09:35 inclusive

// Key of the currently loaded chart. Used to prevent the 1s poll render loop
// from re-calling showChartAt() and resetting the "Show next" progress.
let currentChartKey = ""; // `${chartsDateISO}:${symbol}`
let currentChartPayload = null;
let currentChartBars = null;     // raw bars from API (includes optional prev-close at 09:29)
let currentChartOpenUnix = 0;    // 09:30 NY in unix seconds
let currentChartShownN = 0;      // current shown bar count (step mode)
let currentChartFull = null;     // { candles, volumes, sma9, vwap, barsLen } for FULL bars
let currentChartSep = null;      // { t0, t1 } times for boundary (set on "Show Rest")

// Separator overlay (vertical line)
let boundaryCanvas = null;
let boundaryT0 = null; // unix sec time of last shown bar
let boundaryT1 = null; // unix sec time of first newly revealed bar
let boundaryHandler = null;

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

    sold_off_from_open_pct_min: numVal(f_sold_pct_min),
    sold_off_open5m_range_pct_min: numVal(f_sold_rng_min),
    sold_off_open5m_today_pct_min: numVal(f_sold_today_min),
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

function resetChartsState() {
  chartsDateISO = "";
  chartsOpen = false;
  chartIndex = 0;
  interestTickers = [];
  soldOffTickers = [];
  chartDataCache.clear();
  currentChartKey = "";
  currentChartPayload = null;
  currentChartBars = null;
  currentChartOpenUnix = 0;
  currentChartShownN = 0;
  currentChartFull = null;
  currentChartSep = null;
  boundaryT0 = null;
  boundaryT1 = null;
  hideChartsUI(true);
  setChartsToolbarVisible(false);
}

function setChartsToolbarVisible(visible) {
  if (!chartsToolbar) return;
  chartsToolbar.style.display = visible ? "" : "none";
}

function hideChartsUI(destroy = false) {
  if (chartPanel) chartPanel.style.display = "none";
  if (chartMeta) chartMeta.textContent = "";
  if (chartSym) chartSym.textContent = "—";
  if (destroy) destroyLWChart();
}

function destroyLWChart() {
  if (lwOnResize) {
    window.removeEventListener("resize", lwOnResize);
    lwOnResize = null;
  }
  // unsubscribe separator redraw
  if (lwChart && boundaryHandler) {
    try {
      const ts = lwChart.timeScale && lwChart.timeScale();
      if (ts && ts.unsubscribeVisibleLogicalRangeChange) {
        ts.unsubscribeVisibleLogicalRangeChange(boundaryHandler);
      }
    } catch (_) {}
  }
  boundaryHandler = null;
  boundaryT0 = null;
  boundaryT1 = null;
  boundaryCanvas = null;

  if (lwChart) {
    lwChart.remove();
    lwChart = null;
  }
  lwCandle = lwSMA = lwVWAP = lwVolume = null;
  if (chartContainer) chartContainer.innerHTML = "";
}

function ensureBoundaryCanvas() {
  if (!chartContainer) return null;
  if (boundaryCanvas && boundaryCanvas.isConnected) return boundaryCanvas;

  // chartContainer is position:relative in CSS; create an absolute overlay.
  boundaryCanvas = document.createElement("canvas");
  boundaryCanvas.style.position = "absolute";
  boundaryCanvas.style.inset = "0";
  boundaryCanvas.style.pointerEvents = "none";
  // Make sure it sits above lightweight-charts canvases
  boundaryCanvas.style.zIndex = "999";
  chartContainer.appendChild(boundaryCanvas);
  return boundaryCanvas;
}

function clearBoundaryLine() {
  boundaryT0 = null;
  boundaryT1 = null;
  if (!boundaryCanvas) return;
  const c = boundaryCanvas.getContext("2d");
  if (!c) return;
  c.clearRect(0, 0, boundaryCanvas.width, boundaryCanvas.height);
}

function redrawBoundaryLine() {
  if (!lwChart || !chartContainer) return;
  if (!boundaryT0 || !boundaryT1) return;

  const canvas = ensureBoundaryCanvas();
  if (!canvas) return;

  const rect = chartContainer.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  const w = Math.max(1, Math.floor(rect.width));
  const h = Math.max(1, Math.floor(rect.height));

  canvas.style.width = w + "px";
  canvas.style.height = h + "px";
  canvas.width = Math.floor(w * dpr);
  canvas.height = Math.floor(h * dpr);

  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  let x0 = null;
  let x1 = null;
  try {
    // IMPORTANT: timeToCoordinate works reliably only for times that exist on the scale.
    // We store the two real bar times and draw halfway between their coordinates.
    x0 = lwChart.timeScale().timeToCoordinate(boundaryT0);
    x1 = lwChart.timeScale().timeToCoordinate(boundaryT1);
  } catch (_) {}
  if (x0 === null || x0 === undefined) return;
  if (x1 === null || x1 === undefined) return;

  const x = (x0 + x1) / 2;

  ctx.save();
  // Magenta dotted line
  ctx.strokeStyle = "rgba(255, 0, 255, 0.95)";
  ctx.lineWidth = 2;
  ctx.lineCap = "round";      // makes short dashes look like dots
  ctx.setLineDash([1, 7]);    // dotted pattern (tweak if desired)
  ctx.beginPath();
  ctx.moveTo(x, 0);
  ctx.lineTo(x, h);
  ctx.stroke();
  ctx.restore();
}

function setBoundaryLineBetween(t0, t1) {
  boundaryT0 = t0 || null;
  boundaryT1 = t1 || null;
  if (!boundaryT0 || !boundaryT1) {
    clearBoundaryLine();
    return;
  }
  redrawBoundaryLine();
}

function ensureLWChart() {
  if (!chartContainer) return false;
  if (!window.LightweightCharts) {
    if (chartMeta) chartMeta.textContent = "Lightweight Charts failed to load (CDN blocked?).";
    return false;
  }
  if (lwChart) return true;

  chartContainer.innerHTML = "";
  boundaryCanvas = null;
  boundaryT0 = null;
  boundaryT1 = null;

  const width = chartContainer.clientWidth || 900;
  const height = chartContainer.clientHeight || 520;

  // --- TWEAK THESE TWO NUMBERS TO TASTE ---
  const VOLUME_HEIGHT = 0.25;     // 25% of chart height for volume (like your screenshot)
  const BAR_SPACING_PX = 10;      // narrower bars (try 8..14)

  const timeFormatter = (unixSec) => {
    try {
      const dt = new Date(unixSec * 1000);
      return new Intl.DateTimeFormat("en-US", {
        timeZone: chartsTimeZone || "America/New_York",
        hour12: false,
        hour: "2-digit",
        minute: "2-digit",
      }).format(dt);
    } catch (_) {
      return "";
    }
  };

  lwChart = LightweightCharts.createChart(chartContainer, {
    width,
    height,
    layout: {
      background: { type: "solid", color: "#0b1020" },
      textColor: "#e2e2e2",
    },
    grid: {
      vertLines: { color: "rgba(255,255,255,0.06)" },
      horzLines: { color: "rgba(255,255,255,0.06)" },
    },

    // IMPORTANT: this reserves the bottom band so candles don't draw there
    rightPriceScale: {
      borderColor: "rgba(255,255,255,0.10)",
      scaleMargins: { top: 0.06, bottom: VOLUME_HEIGHT },
    },

    timeScale: {
      timeVisible: true,
      secondsVisible: false,
      borderColor: "rgba(255,255,255,0.10)",
      barSpacing: BAR_SPACING_PX, // narrower candles/volume bars
      rightOffset: 0,
    },

    localization: { timeFormatter },
  });

  // ✅ Volume FIRST so it's drawn behind candles/lines
  lwVolume = lwChart.addHistogramSeries({
    priceFormat: { type: "volume" },
    priceScaleId: "",              // overlay scale (keeps volume axis hidden)
    lastValueVisible: false,
    priceLineVisible: false,
    scaleMargins: { top: 1 - VOLUME_HEIGHT, bottom: 0 },
  });

  // Candles
  lwCandle = lwChart.addCandlestickSeries({
    upColor: "rgba(32,201,151,1)",
    downColor: "rgba(255,77,109,1)",
    borderUpColor: "rgba(32,201,151,1)",
    borderDownColor: "rgba(255,77,109,1)",
    wickUpColor: "rgba(32,201,151,1)",
    wickDownColor: "rgba(255,77,109,1)",
  });

  // 9 SMA (red)
  lwSMA = lwChart.addLineSeries({
    color: "red",
    lineWidth: 2,
  });

  // VWAP since 09:30 (yellow)
  lwVWAP = lwChart.addLineSeries({
    color: "yellow",
    lineWidth: 2,
  });

  lwOnResize = () => {
    if (!lwChart || !chartContainer) return;
    lwChart.resize(chartContainer.clientWidth || width, chartContainer.clientHeight || height);
    redrawBoundaryLine();
  };
  window.addEventListener("resize", lwOnResize);

  // keep separator positioned correctly on scroll/zoom
  boundaryHandler = () => redrawBoundaryLine();
  try {
    const ts = lwChart.timeScale && lwChart.timeScale();
    if (ts && ts.subscribeVisibleLogicalRangeChange) ts.subscribeVisibleLogicalRangeChange(boundaryHandler);
  } catch (_) {}

  return true;
}

function computeSMA9(bars, openUnix) {
  const period = 9;
  const win = [];
  const out = [];
  for (const b of bars) {
    if (b.time < openUnix) continue; // start SMA at 09:30
    win.push(b.close);
    if (win.length > period) win.shift();
    if (win.length === period) {
      const sum = win.reduce((a, x) => a + x, 0);
      out.push({ time: b.time, value: sum / period });
    }
  }
  return out;
}

function computeVWAP(bars, openUnix) {
  let cumPV = 0;
  let cumV = 0;
  const out = [];
  for (const b of bars) {
    if (b.time < openUnix) continue; // anchor at 09:30
    const v = b.volume;
    if (!isFinite(v) || v <= 0) continue;
    const typical = (b.high + b.low + b.close) / 3;
    cumPV += typical * v;
    cumV += v;
    if (cumV > 0) out.push({ time: b.time, value: cumPV / cumV });
  }
  return out;
}

async function fetchChartBars(sym) {
  if (!chartsDateISO) throw new Error("missing chart date");
  const key = `${chartsDateISO}:${sym}`;
  if (chartDataCache.has(key)) return chartDataCache.get(key);

  const res = await fetch(
    `/api/chart/bars?symbol=${encodeURIComponent(sym)}&date=${encodeURIComponent(chartsDateISO)}`,
    { cache: "no-store" }
  );
  const body = await res.json().catch(() => null);
  if (!res.ok || !body?.ok) {
    throw new Error(body?.error || `HTTP ${res.status}`);
  }

  chartDataCache.set(key, body);
  return body;
}

function buildSeries(bars, openUnix) {
  const candles = bars.map(b => ({
    time: b.time,
    open: b.open,
    high: b.high,
    low: b.low,
    close: b.close,
  }));

  const volumes = bars.map(b => ({
    time: b.time,
    value: b.volume,
    color: (b.close >= b.open) ? "rgba(32,201,151,0.55)" : "rgba(255,77,109,0.55)",
  }));

  const sma9 = computeSMA9(bars, openUnix);
  const vwap = computeVWAP(bars, openUnix);

  return { candles, volumes, sma9, vwap, barsLen: bars.length };
}

function paintCurrentChart() {
  if (!lwChart || !lwCandle || !lwVolume || !lwSMA || !lwVWAP) return;
  if (!currentChartPayload || !currentChartBars || !currentChartFull) return;

  const total = currentChartFull.candles.length;
  const shownNRaw = Math.max(0, Math.min(currentChartShownN, total));
  const isFull = shownNRaw >= total;
  const shown = isFull
    ? currentChartFull
    : buildSeries(currentChartBars.slice(0, shownNRaw), currentChartOpenUnix);
  const shownN = shown.candles.length;

  lwCandle.setData(shown.candles);
  lwVolume.setData(shown.volumes);
  lwSMA.setData(shown.sma9);
  lwVWAP.setData(shown.vwap);

  // ✅ Left-align: no symmetric padding, no "centered" display.
  // Give a little extra space to the right (more when hidden, minimal when revealed).
  const rightPad = isFull ? 2 : 14;
  try {
    lwChart.timeScale().setVisibleLogicalRange({
      from: 0,
      to: Math.max(0, (shownN - 1) + rightPad),
    });
  } catch (_) {
    // fallback
    try { lwChart.timeScale().fitContent(); } catch (_) {}
  }

  // Separator line persists after "Show Rest" (permanent reveal)
  if (currentChartSep && currentChartSep.t0 && currentChartSep.t1) {
    setBoundaryLineBetween(currentChartSep.t0, currentChartSep.t1);
  }
  else clearBoundaryLine();

  // Button state
  if (chartNextBarBtn) {
    const hasNext = currentChartBars && currentChartShownN < currentChartBars.length;
    chartNextBarBtn.disabled = !hasNext;
  }
  if (chartRevealBtn) {
    const hasRest = currentChartBars && currentChartShownN < currentChartBars.length;
    chartRevealBtn.disabled = !hasRest;
    chartRevealBtn.textContent = hasRest ? "Show Rest" : "Rest Shown";
  }

  // Meta
  const prev = currentChartPayload.prev_close_date_ny ? `PrevClose: ${currentChartPayload.prev_close_date_ny}` : "PrevClose: —";
  const exitUnix = currentChartPayload.exit_unix || 0;
  const exitHM = exitUnix ? new Intl.DateTimeFormat("en-US", {
    timeZone: chartsTimeZone || "America/New_York", hour12:false, hour:"2-digit", minute:"2-digit"
  }).format(new Date(exitUnix * 1000)) : "—";
  if (chartMeta) {
    const hint = (currentChartBars && currentChartShownN < currentChartBars.length)
      ? `Click “Show next” (1 bar) or “Show Rest” (to ${exitHM})`
      : `All bars shown → ${exitHM}`;
    chartMeta.textContent = `${currentChartPayload.date_ny} · ${prev} · Showing ${shownN}/${total} · ${hint}`;
  }
}

async function showChartAt(idx) {
  const list = getActiveChartTickers();
  if (!list.length) return;
  if (!chartsDateISO) return;

  chartsOpen = true;

  // clamp index
  if (idx < 0) idx = 0;
  if (idx >= list.length) idx = list.length - 1;
  chartIndex = idx;

  const sym = list[chartIndex];
  currentChartKey = `${chartsDateISO}:${sym}`;

  if (chartTickerSelect) chartTickerSelect.value = sym;
  if (chartCounter) chartCounter.textContent = `${chartIndex + 1} / ${list.length}`;
  if (chartPrevBtn) chartPrevBtn.disabled = (chartIndex <= 0);
  if (chartNextBtn) chartNextBtn.disabled = (chartIndex >= list.length - 1);

  if (chartPanel) chartPanel.style.display = "";
  if (chartSym) chartSym.textContent = sym;
  if (chartMeta) chartMeta.textContent = "Loading…";

  currentChartSep = null;
  clearBoundaryLine();

  try {
    const payload = await fetchChartBars(sym);

    chartsTimeZone = payload.timezone || chartsTimeZone || "America/New_York";
    const openUnix = payload.open_unix || 0;

    const bars = Array.isArray(payload.bars) ? payload.bars : [];
    if (!bars.length) {
      if (chartMeta) chartMeta.textContent = "No bars returned for this symbol/time window.";
      destroyLWChart();
      return;
    }

    if (!ensureLWChart()) return;

    // Store payload for paintCurrentChart()
    currentChartPayload = payload;
    currentChartBars = bars;
    currentChartOpenUnix = openUnix;

    // Full series (used when "Show Rest" is on)
    currentChartFull = buildSeries(bars, openUnix);

    // Initial window count:
    // - include any prev-close bar(s) with time < openUnix
    // - include 09:30..09:35 inclusive => openUnix + (6-1)*60
    const lastInitialUnix = openUnix ? (openUnix + (INITIAL_MINUTES_AFTER_OPEN - 1) * 60) : 0;
    let initN = 0;
    if (openUnix) {
      for (const b of bars) {
        if (b.time < openUnix) { initN++; continue; }
        if (b.time <= lastInitialUnix) { initN++; continue; }
        break; // bars are chronological
      }
    } else {
      // fallback (shouldn't happen): show a small prefix
      initN = Math.min(bars.length, 1 + INITIAL_MINUTES_AFTER_OPEN);
    }
    currentChartShownN = initN;

    // Paint initial (guess) view
    paintCurrentChart();

  } catch (err) {
    if (chartMeta) chartMeta.textContent = `Chart load failed: ${err?.message || err}`;
    destroyLWChart();
  }
}

function renderSoldOff(report, st) {
  if (!soldOffTitle || !soldOffHint || !soldOffBody) return;

  soldOffTitle.textContent = "Sold off by 10:30";

  const f = st?.filters || {};
  const downMin = (typeof f.sold_off_from_open_pct_min === "number") ? f.sold_off_from_open_pct_min : null;
  const rngMin  = (typeof f.sold_off_open5m_range_pct_min === "number") ? f.sold_off_open5m_range_pct_min : null;
  const todayMin = (typeof f.sold_off_open5m_today_pct_min === "number") ? f.sold_off_open5m_today_pct_min : null;

  let hint = "Historic scan for tickers that sold off hard from the 09:30 open by 10:30.";
  if (downMin !== null && rngMin !== null && todayMin !== null) {
    hint = `Down ≥ ${fmtPct(downMin)} from the 09:30 open by 10:30, with Open5m Range% ≥ ${fmtPct(rngMin)} and Open5m Today% ≥ ${todayMin}%.`;
  }

  const end = report?.summary?.window_end_ny || "";
  if (end && end < "10:30:00") {
    hint += ` (Data ends at ${end}, so this scan may be incomplete.)`;
  }
  soldOffHint.textContent = hint;

  const rows = Array.isArray(report?.sold_off) ? report.sold_off : [];
  soldOffTickers = rows.map(r => r.symbol).filter(Boolean);

  soldOffBody.innerHTML = "";
  for (const s of rows) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><strong>${s.symbol}</strong></td>
      <td>${fmtPct(s.drop_pct)}</td>
      <td>${fmt(s.open_0930, 4)}</td>
      <td>${fmt(s.low_price, 4)}</td>
      <td>${s.low_time_ny || "—"}</td>
      <td>${fmt(s.price_at_scan_time, 4)}</td>
      <td>${fmtPct(s.open_5m_range_pct)}</td>
      <td>${fmtInt(s.open_5m_vol)}</td>
      <td>${isFinite(s.open_5m_today_pct) ? s.open_5m_today_pct.toFixed(1) + "%" : "—"}</td>
    `;
    soldOffBody.appendChild(tr);
  }
}

function renderHistoric(report, mode, st) {
  const card = $("historicCard");
  if (mode !== "historic") {
    card.style.display = "none";
    // NEW
    resetChartsState();
    return;
  }
  card.style.display = "";

  // If performance tables are shown, we do NOT show the Of-interest chart UI.
  if (showHistoricPerformance) {
    setChartsToolbarVisible(false);
    hideChartsUI(true);
  }

  // Show/hide performance tables
  if (histPerformanceWrap) {
    histPerformanceWrap.style.display = showHistoricPerformance ? "" : "none";
  }

  // date picker bounds & value
  histMinISO = st.historic_min_date_ny || "";
  histMaxISO = st.historic_max_date_ny || "";
  if (historicDateInput) {
    if (histMinISO) historicDateInput.min = histMinISO;
    else historicDateInput.removeAttribute("min");

    if (histMaxISO) historicDateInput.max = histMaxISO;
    else historicDateInput.removeAttribute("max");
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
    if (!nb) {
      renderSoldOff(report, st);
      return;
    }
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

    // NEW: update slideshow list + date
    const syms = tickers.map(t => t.symbol).filter(Boolean);
    chartsDateISO =
      (st.historic_resolved_date_ny || st.historic_target_date_ny || (st.now_ny || "").slice(0,10) || "");
    interestTickers = syms;

    // Sold-off list comes from the report (if ready)
    const sold = Array.isArray(report?.sold_off) ? report.sold_off : [];
    soldOffTickers = sold.map(x => x.symbol).filter(Boolean);

    // Build the chart list selector
    if (chartListSelect) {
      // If the current mode has no items but the other does, auto-switch.
      if (chartListMode === "interest" && !interestTickers.length && soldOffTickers.length) chartListMode = "soldoff";
      if (chartListMode === "soldoff" && !soldOffTickers.length && interestTickers.length) chartListMode = "interest";

      chartListSelect.innerHTML = `
        <option value="interest">Of interest (${interestTickers.length})</option>
        <option value="soldoff">Sold off (${soldOffTickers.length})</option>
      `;
      chartListSelect.value = chartListMode;
    }

    const active = getActiveChartTickers();

    if (active.length) {
      setChartsToolbarVisible(true);
      if (showChartsBtn) showChartsBtn.disabled = false;

      // Keep chartIndex stable by symbol when possible (prevents jumping if list changes)
      if (chartsOpen && currentChartPayload?.symbol) {
        const idx = active.indexOf(currentChartPayload.symbol);
        if (idx >= 0) chartIndex = idx;
      }
      if (chartIndex >= active.length) chartIndex = 0;

      const selectedSym = active[chartIndex] || "";

      // Rebuild dropdown, preserve selection
      if (chartTickerSelect) {
        const prevVal = chartTickerSelect.value;
        chartTickerSelect.innerHTML = active.map(s => `<option value="${s}">${s}</option>`).join("");
        const nextVal = chartsOpen ? selectedSym : (active.includes(prevVal) ? prevVal : selectedSym);
        if (nextVal) chartTickerSelect.value = nextVal;
      }

      // Toolbar state: don't stomp it to "—" while chart is open
      if (chartCounter) {
        chartCounter.textContent = chartsOpen
          ? `${Math.min(chartIndex + 1, active.length)} / ${active.length}`
          : `— / ${active.length}`;
      }
      if (chartPrevBtn) chartPrevBtn.disabled = chartsOpen ? (chartIndex <= 0) : true;
      if (chartNextBtn) chartNextBtn.disabled = chartsOpen ? (chartIndex >= active.length - 1) : (active.length <= 1);

      // CRITICAL FIX:
      // Do NOT call showChartAt() every 1s poll. That resets currentChartShownN and makes bars "disappear".
      if (chartsOpen) {
        const desiredKey = selectedSym ? `${chartsDateISO}:${selectedSym}` : "";
        const needsLoad = !desiredKey || !lwChart || !currentChartBars || (currentChartKey !== desiredKey);
        if (needsLoad) showChartAt(chartIndex).catch(() => {});
      } else {
        hideChartsUI(true);
      }
    } else {
      setChartsToolbarVisible(false);
      hideChartsUI(true);
    }

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

    // Always render sold-off section (doesn't interfere with existing tables)
    renderSoldOff(report, st);
    return;
  }

  // -----------------------------------------
  // Performance ON: existing historic report
  // -----------------------------------------
  if (noEntryTitle) noEntryTitle.textContent = "Selected but no entry";
  if (noEntryHint) noEntryHint.textContent = "Useful for diagnosing false positives (no VWAP cross in the entry window, Today% filter failures, etc.).";

  if (!report || !report.summary) {
    renderSoldOff(report, st);
    return;
  }

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

  // Always render sold-off section (doesn't interfere with existing tables)
  renderSoldOff(report, st);
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

  syncInput(f_sold_pct_min, f.sold_off_from_open_pct_min);
  syncInput(f_sold_rng_min, f.sold_off_open5m_range_pct_min);
  syncInput(f_sold_today_min, f.sold_off_open5m_today_pct_min);

  // New session boundary: clear UI caches so identical event IDs across replays don't get ignored
  if (st.session_id && st.session_id !== currentSessionID) {
    currentSessionID = st.session_id;
    seenEventIDs.clear();
    $("events").innerHTML = "";

    // NEW
    resetChartsState();
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
  f_sold_pct_min, f_sold_rng_min, f_sold_today_min,
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

// Charts UI wiring
if (showChartsBtn) {
  showChartsBtn.addEventListener("click", () => {
    if (!getActiveChartTickers().length) return;
    chartsOpen = true;
    chartIndex = 0; // start with the first chart in the Of-interest list
    showChartAt(chartIndex).catch(() => {});
  });
}
if (hideChartsBtn) {
  hideChartsBtn.addEventListener("click", () => {
    chartsOpen = false;
    hideChartsUI(true);
  });
}
if (chartPrevBtn) {
  chartPrevBtn.addEventListener("click", () => {
    showChartAt(chartIndex - 1).catch(() => {});
  });
}
if (chartNextBtn) {
  chartNextBtn.addEventListener("click", () => {
    showChartAt(chartIndex + 1).catch(() => {});
  });
}
if (chartTickerSelect) {
  chartTickerSelect.addEventListener("change", () => {
    const sym = chartTickerSelect.value;
    const idx = getActiveChartTickers().indexOf(sym);
    if (idx >= 0) showChartAt(idx).catch(() => {});
  });
}

if (chartListSelect) {
  chartListSelect.addEventListener("change", () => {
    const v = chartListSelect.value;
    chartListMode = (v === "soldoff") ? "soldoff" : "interest";
    try { localStorage.setItem("orb_chart_list_mode", chartListMode); } catch (_) {}
    chartIndex = 0;
    if (chartsOpen) showChartAt(0).catch(() => {});
  });
}

if (chartNextBarBtn) {
  chartNextBarBtn.addEventListener("click", () => {
    if (!currentChartPayload || !currentChartBars || !currentChartFull) return;
    if (currentChartShownN >= currentChartBars.length) return;
    currentChartShownN++;
    paintCurrentChart();
  });
}

if (chartRevealBtn) {
  chartRevealBtn.addEventListener("click", () => {
    if (!currentChartPayload || !currentChartBars || !currentChartFull) return;
    // One-way reveal: permanently add remaining bars
    if (currentChartShownN >= currentChartBars.length) return;

    // Separator EXACTLY between last shown and first newly revealed
    currentChartSep = null;
    const n = currentChartShownN;
    if (n > 0 && currentChartBars.length > n) {
      const t0 = currentChartBars[n - 1]?.time;
      const t1 = currentChartBars[n]?.time;
      if (t0 && t1 && t1 > t0) currentChartSep = { t0, t1 };
    }

    currentChartShownN = currentChartBars.length;
    paintCurrentChart();
  });
}

connectEvents();
loop();
