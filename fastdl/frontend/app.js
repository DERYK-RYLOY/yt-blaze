/* YT Blaze frontend logic: wires UI to the Go backend via REST + SSE. */
(() => {
  const API = 'http://127.0.0.1:8080/api';

  // ---- selected options ----
  let format = 'mp4';
  let quality = '4k';
  let audio = 'mp3';

  // ---- element refs ----
  const $ = (id) => document.getElementById(id);
  const urlInput = $('url-input');
  const goBtn = $('go-btn');
  const statusLine = $('status-line');
  const speedNum = $('speed-num');
  const progressBar = $('progress-bar');
  const needle = $('needle');
  const logoWrap = $('logo-wrap');
  const historyList = $('history-list');
  const btnReset = $('btn-reset');

  // ================= chip selection =================
  function wireChips(containerId, attr, setter) {
    const el = $(containerId);
    el.addEventListener('click', (e) => {
      const btn = e.target.closest('button');
      if (!btn) return;
      el.querySelectorAll('button').forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');
      setter(btn.getAttribute(attr));
    });
  }
  wireChips('format-btns', 'data-format', (v) => (format = v));
  wireChips('video-quality', 'data-q', (v) => (quality = v));
  wireChips('audio-quality', 'data-a', (v) => (audio = v));

  // ================= gauge ticks =================
  (function drawTicks() {
    const g = document.getElementById('gauge-ticks');
    const cx = 100, cy = 100;
    for (let i = 0; i <= 20; i++) {
      const a = (-180 + (i * 180) / 20) * (Math.PI / 180);
      const major = i % 5 === 0;
      const r1 = major ? 64 : 70;
      const r2 = 78;
      const x1 = cx + Math.cos(a) * r1, y1 = cy + Math.sin(a) * r1;
      const x2 = cx + Math.cos(a) * r2, y2 = cy + Math.sin(a) * r2;
      const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
      line.setAttribute('x1', x1.toFixed(2));
      line.setAttribute('y1', y1.toFixed(2));
      line.setAttribute('x2', x2.toFixed(2));
      line.setAttribute('y2', y2.toFixed(2));
      if (major) line.setAttribute('stroke-width', '2.4');
      g.appendChild(line);
    }
  })();

  // needle rotation: -90deg (0 MB/s) .. +90deg (MAX)
  const SPEED_MAX = 50; // MB/s at full deflection
  function setNeedle(bps) {
    const mbps = bps / (1024 * 1024);
    const frac = Math.max(0, Math.min(1, mbps / SPEED_MAX));
    const deg = -90 + frac * 180;
    needle.setAttribute('transform', `rotate(${deg.toFixed(1)} 100 100)`);
  }

  // ================= SSE progress =================
  let busy = false;
  function connectSSE() {
    const es = new EventSource(API + '/progress/stream');
    es.onmessage = (e) => {
      let ev;
      try { ev = JSON.parse(e.data); } catch { return; }
      const active = !!ev.active;

      // shimmer ray on while any download active
      logoWrap.classList.toggle('active', active);

      if (active) {
        // live values while downloading
        busy = true;
        goBtn.classList.add('busy');
        speedNum.textContent = ev.speed || '0 B/s';
        setNeedle(ev.speed_bps || 0);
        progressBar.style.width = (ev.progress || 0).toFixed(1) + '%';
        const t = ev.title ? ' — ' + ev.title : '';
        statusLine.textContent = (ev.status || 'downloading') + t + '  ' + (ev.progress || 0).toFixed(0) + '%';
      } else {
        // idle: keep the mockup resting display (14.2 MB/s, needle, bar) on
        // first paint, but clear busy state and show done/idle status.
        if (busy) {
          // a download just finished — reflect completion briefly
          busy = false;
          goBtn.classList.remove('busy');
          if (ev.status === 'finished') {
            statusLine.textContent = 'Download finished ✓';
            progressBar.style.width = '100%';
            speedNum.textContent = '0 B/s';
            setNeedle(0);
            loadHistory();
          } else if (ev.status && ev.status !== 'idle') {
            statusLine.textContent = ev.status;
          } else {
            statusLine.textContent = '';
          }
        }
      }
    };
    es.onerror = () => { /* auto-reconnects */ };
  }
  connectSSE();

  // ================= start download =================
  goBtn.addEventListener('click', startDownload);
  urlInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') startDownload(); });

  function startDownload() {
    if (busy) return;
    const url = urlInput.value.trim();
    if (!url) {
      statusLine.textContent = 'Paste a URL first';
      urlInput.focus();
      return;
    }
    const body = {
      url,
      container: format,
      quality,
      audio: audio,
    };
    fetch(API + '/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
      .then(async (r) => {
        const data = await r.json();
        if (!r.ok) throw new Error(data.error || 'failed');
        statusLine.textContent = 'Download started…';
        urlInput.value = '';
        loadHistory();
      })
      .catch((err) => {
        statusLine.textContent = 'Error: ' + err.message;
      });
  }

  // ================= history =================
  const LINK_SVG = '<svg width="14" height="14" viewBox="0 0 16 16"><path d="M6.5 9.5l3-3M5.5 8L4 9.5a2.1 2.1 0 003 3L8.5 11M10.5 8L12 6.5a2.1 2.1 0 00-3-3L7.5 5" stroke="#555" stroke-width="1.3" fill="none" stroke-linecap="round"/></svg>';

  function loadHistory() {
    fetch(API + '/history')
      .then((r) => r.json())
      .then((items) => {
        historyList.innerHTML = '';
        if (!items || !items.length) {
          historyList.innerHTML = '<li class="history-empty">No downloads yet</li>';
          return;
        }
        // newest first (backend already returns newest first)
        items.slice(0, 20).forEach((h) => {
          const li = document.createElement('li');
          li.innerHTML = LINK_SVG + '<span>' + shortUrl(h.url) + '</span>';
          li.title = h.url;
          li.addEventListener('click', () => {
            urlInput.value = h.url;
            urlInput.focus();
          });
          historyList.appendChild(li);
        });
      })
      .catch(() => {});
  }
  loadHistory();

  function shortUrl(u) {
    try {
      const parsed = new URL(u);
      return parsed.host + parsed.pathname + (parsed.search ? parsed.search.slice(0, 12) + '…' : '');
    } catch {
      return u.length > 42 ? u.slice(0, 42) + '…' : u;
    }
  }

  btnReset.addEventListener('click', () => {
    fetch(API + '/history', { method: 'DELETE' })
      .then(() => loadHistory())
      .catch(() => {});
  });

  // ================= titlebar buttons =================
  $('btn-info').addEventListener('click', () => {
    statusLine.textContent = 'YT Blaze v1.3.0 — Built by DERYK RYLOY';
    setTimeout(() => { if (!busy) statusLine.textContent = ''; }, 4000);
  });
  $('btn-logs').addEventListener('click', () => {
    // future: log panel
  });
})();
