/* YT Blaze frontend logic - wires the UI to the Go backend.
   API base comes from the Wails binding (dynamic free port) with a
   fallback to 127.0.0.1:8080 for plain-browser dev. */
(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);

  const state = {
    apiBase: 'http://127.0.0.1:8080/api',
    format: 'mp4',
    quality: '4k',
    audio: 'mp3',
    mode: 'video', // last quality column clicked: 'video' | 'audio'
    busy: false,
    activeId: null,
    backendUp: false,
    everReady: false,
    sse: null,
    est: null,        // EstimateResponse for the current URL
    estUrl: '',       // URL the est map belongs to
    estCooldown: 0,   // ms retry cooldown while backend is warm
  };

  // ---------- element refs ----------
  const urlInput = $('url-input');
  const goBtn = $('go-btn');
  const goText = $('go-text');
  const statusLine = $('status-line');
  const logoWrap = $('logo-wrap');
  const progress = $('progress');
  const progressFill = $('progress-fill');
  const progressCaption = $('progress-caption');
  const historyList = $('history-list');
  const btnReset = $('btn-reset');
  const sizeHint = $('size-hint');
  const audioOnlyBtn = $('audio-only');

  // ============ Wails bindings ============
  async function bindAPIBase() {
    try {
      const base = await window.go?.main?.App?.APIBase();
      if (typeof base === 'string' && base.trim()) {
        state.apiBase = base.trim().replace(/\/+$/, '');
      }
    } catch { /* plain browser: keep fallback */ }
  }

  // ============ chip columns ============
  function wireChips(containerId, attr, onPick) {
    const el = $(containerId);
    el.addEventListener('click', (e) => {
      // Only actual selection chips (attribute carriers) — the AUDIO ONLY
      // toggle may live in the same row but has its own handler.
      const btn = e.target.closest(`button[${attr}]`);
      if (!btn || !el.contains(btn)) return;
      el.querySelectorAll(`button[${attr}]`).forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');
      onPick(btn.getAttribute(attr));
    });
  }
  wireChips('format-btns', 'data-format', (v) => {
    state.format = v;
    if (state.mode === 'audio') setMode('video');
    else updateSizeHint();
  });
  wireChips('video-quality', 'data-q', (v) => { state.quality = v; setMode('video'); });
  wireChips('audio-quality', 'data-a', (v) => { state.audio = v; setMode('audio'); });

  // AUDIO ONLY toggle: flips between video and audio-only downloads.
  audioOnlyBtn.addEventListener('click', () => {
    setMode(state.mode === 'audio' ? 'video' : 'audio');
  });

  function setMode(mode) {
    state.mode = mode;
    audioOnlyBtn.classList.toggle('active', mode === 'audio');
    const cols = document.querySelector('.quality-cols');
    if (cols) cols.classList.toggle('audio', mode === 'audio');
    hint(
      mode === 'audio'
        ? `Audio only: ${state.audio.toUpperCase()}`
        : `Video mode: ${label(state.quality)} ${state.format.toUpperCase()}`,
      ''
    );
    updateSizeHint();
  }

  function label(v) { return v.toUpperCase() === '4K' ? '4K' : v.toUpperCase(); }

  let hintTimer = null;
  function hint(msg, tail) {
    if (state.busy) return; // never override live progress text
    statusLine.textContent = tail ? `${msg} · ${tail}` : msg;
    clearTimeout(hintTimer);
    hintTimer = setTimeout(() => {
      if (!state.busy && statusLine.textContent.startsWith(msg)) statusLine.textContent = '';
    }, 2200);
  }

  function setBusy(on) {
    state.busy = on;
    goBtn.classList.toggle('stop', on);
    goText.textContent = on ? 'STOP' : 'GO';
    logoWrap.classList.toggle('active', on);
    progress.classList.toggle('show', on);
    updateSizeHint(); // hide the badge while the real progress runs
  }

  // ============ size estimates (MB badge) ============
  let estTimer = null;
  urlInput.addEventListener('input', () => {
    clearTimeout(estTimer);
    estTimer = setTimeout(refreshEstimate, 700);
  });
  urlInput.addEventListener('change', refreshEstimate);

  async function refreshEstimate() {
    const url = urlInput.value.trim();
    if (!url || url.length < 8 || !url.includes('://') || !state.backendUp) {
      state.est = null;
      updateSizeHint();
      return;
    }
    if (state.est && state.estUrl === url) { updateSizeHint(); return; }
    try {
      const r = await fetch(`${state.apiBase}/estimate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url }),
        signal: AbortSignal.timeout(50000),
      });
      const data = await r.json().catch(() => ({}));
      if (!r.ok || !state.estUrlGuard(url)) return; // stale answer for an older input
      state.est = data;
      state.estUrl = url;
      updateSizeHint();
    } catch {
      state.est = null;
      updateSizeHint();
    }
  }

  // guard: only keep a response if the user hasn't changed the URL meanwhile
  state.estUrlGuard = (url) => urlInput.value.trim() === url;
  let lastHintText = '';
  function updateSizeHint() {
    const bytes = estimatedBytes();
    if (state.busy || !bytes || bytes <= 0) {
      sizeHint.classList.remove('show');
      lastHintText = '';
      return;
    }
    const sel = state.mode === 'audio'
      ? `${state.audio.toUpperCase()} AUDIO ONLY`
      : `${state.quality.toUpperCase()} ${state.format.toUpperCase()}`;
    const html = `<span class="size-dot"></span>≈ <b>${humanSize(bytes)}</b><span class="dot">·</span>${sel.toLowerCase()}`;
    if (html !== lastHintText) {
      sizeHint.innerHTML = html;
      lastHintText = html;
    }
    sizeHint.classList.add('show');
  }

  function estimatedBytes() {
    if (!state.est || state.estUrl !== urlInput.value.trim()) return 0;
    if (state.mode === 'audio') {
      return state.est.audio?.[state.audio] || 0;
    }
    const byCont = state.est.video || {};
    const cont = state.format === 'raw' ? 'raw' : state.format;
    return byCont[cont]?.[state.quality] || byCont.mp4?.[state.quality] || 0;
  }

  function humanSize(bytes) {
    const mb = bytes / (1024 * 1024);
    if (mb >= 1024) return `${(mb / 1024).toFixed(2)} GB`;
    if (mb >= 100) return `${Math.round(mb)} MB`;
    return `${mb.toFixed(1)} MB`;
  }

  // ============ SSE progress ============
  function connectSSE() {
    if (state.sse) return;
    try {
      state.sse = new EventSource(`${state.apiBase}/progress/stream`);
    } catch { return; }
    state.sse.onmessage = (e) => {
      let ev;
      try { ev = JSON.parse(e.data); } catch { return; }
      applyProgress(ev);
    };
    state.sse.onerror = () => { /* EventSource reconnects automatically */ };
  }

  function applyProgress(ev) {
    const active = !!ev.active;
    state.activeId = ev.download?.id || state.activeId;

    if (active) {
      if (!state.busy) setBusy(true);
      const pct = Math.max(0, Math.min(100, ev.progress || 0));
      progressFill.style.width = pct.toFixed(1) + '%';
      const d = ev.download;
      if (d?.file_path) {
        const name = d.file_path.split(/[\\/]/).pop();
        progressCaption.textContent = `${(ev.speed || '')} · ETA ${(ev.eta ?? '—')} · ${name}`;
      } else {
        progressCaption.textContent = `${(ev.speed || '')} · ETA ${(ev.eta ?? '—')}`;
      }
      statusLine.textContent = '';
    } else if (state.busy) {
      setBusy(false);
      const d = ev.download;
      const status = d ? d.status : ev.status;
      if (status === 'finished') {
        progressFill.style.width = '100%';
        statusLine.textContent = 'Download finished ✓';
      } else if (status === 'cancelled') {
        statusLine.textContent = 'Cancelled.';
      } else if (status === 'failed') {
        statusLine.textContent = 'Error: ' + (d?.error || 'download failed');
      } else {
        statusLine.textContent = status && status !== 'idle' ? status : '';
      }
      // log the finished download into history
      if (status === 'finished') loadHistory();
    }
  }

  // ============ start / cancel ============
  goBtn.addEventListener('click', () => {
    if (state.busy) {
      if (!state.activeId) return;
      fetch(`${state.apiBase}/downloads/${encodeURIComponent(state.activeId)}`, { method: 'DELETE' })
        .catch(() => {});
      statusLine.textContent = 'Cancelling…';
      return;
    }
    startDownload();
  });
  urlInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') startDownload(); });

  function startDownload() {
    const url = urlInput.value.trim();
    if (!url) {
      statusLine.textContent = 'Paste a URL first';
      urlInput.focus();
      return;
    }
    if (!state.backendUp) {
      statusLine.textContent = 'Engines warming up — one sec…';
      return;
    }

    const body = {
      url,
      container: state.format,
      quality: state.quality,
      audio_only: state.mode === 'audio',
      audio: state.audio,
    };
    fetch(`${state.apiBase}/download`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
      .then(async (r) => {
        const data = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(data.error || `http ${r.status}`);
        statusLine.textContent = 'Download started…';
        urlInput.value = '';
        state.est = null;
        state.estUrl = '';
        updateSizeHint();
      })
      .catch((err) => { statusLine.textContent = 'Error: ' + err.message; });
  }

  // ============ history ============
  const LINK_SVG =
    '<svg width="12" height="12" viewBox="0 0 16 16"><path d="M6.5 9.5l3-3M5.5 8L4 9.5a2.1 2.1 0 003 3L8.5 11M10.5 8L12 6.5a2.1 2.1 0 00-3-3L7.5 5" stroke="currentColor" stroke-width="1.3" fill="none" stroke-linecap="round"/></svg>';

  function loadHistory() {
    fetch(`${state.apiBase}/history`, { signal: AbortSignal.timeout(1200) })
      .then((r) => r.json())
      .then((items) => {
        historyList.innerHTML = '';
        if (!Array.isArray(items) || !items.length) {
          historyList.innerHTML = '<li class="history-empty">No downloads yet</li>';
          return;
        }
        items.slice(0, 12).forEach((h) => {
          const li = document.createElement('li');
          li.className = 'history-item';
          li.title = h.url;
          const span = document.createElement('span');
          span.textContent = shortUrl(h.url);
          li.innerHTML = LINK_SVG;
          li.appendChild(span);
          li.addEventListener('click', () => { urlInput.value = h.url; refreshEstimate(); urlInput.focus(); });
          historyList.appendChild(li);
        });
      })
      .catch(() => {});
  }

  function shortUrl(u) {
    try {
      const p = new URL(u);
      return (p.host + p.pathname + p.search).replace(/^https?:\/\//, '');
    } catch {
      return u.length > 40 ? u.slice(0, 40) + '…' : u;
    }
  }

  btnReset.addEventListener('click', () => {
    fetch(`${state.apiBase}/history`, { method: 'DELETE' }).then(loadHistory).catch(() => {});
  });

  // ============ titlebar tool buttons ============
  $('btn-info').addEventListener('click', async () => {
    if (!state.busy) statusLine.textContent = 'YT Blaze v1.3.0 — aria2 + yt-dlp, 16-connection downloads';
    clearTimeout(hintTimer);
    hintTimer = setTimeout(() => { if (!state.busy) statusLine.textContent = ''; }, 4000);
  });
  $('btn-logs').addEventListener('click', async () => {
    try { await window.go?.main?.App?.ShowOutputDir(); }
    catch { /* browser dev */ }
  });

  // ============ boot: warm-up polling until the backend answers ============
  async function healthLoop() {
    for (;;) {
      let ok = false;
      try {
        const r = await fetch(`${state.apiBase}/health`, { signal: AbortSignal.timeout(900) });
        ok = r.ok;
      } catch { ok = false; }

      if (ok !== state.backendUp) {
        state.backendUp = ok;
        if (ok && !state.everReady) {
          state.everReady = true;
          connectSSE();
          loadHistory();
          refreshEstimate();
          if (!state.busy) statusLine.textContent = '';
        } else if (!ok) {
          if (state.sse) { state.sse.close(); state.sse = null; }
        }
      }
      await new Promise((res) => setTimeout(res, ok ? 5000 : 700));
    }
  }

  (async () => {
    await bindAPIBase();
    loadHistory(); // best effort against the fallback base too
    initErrorProbe();
    healthLoop();
  })();

  function initErrorProbe() {
    try { window.go?.main?.App?.InitError?.().then((e) => {
      if (e) statusLine.textContent = 'Backend error: ' + e;
    }); } catch { /* ignore */ }
  }
})();
