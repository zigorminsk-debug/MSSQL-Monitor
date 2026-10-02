// MS SQL Monitor — логика интерфейса
'use strict';

const $ = (id) => document.getElementById(id);
const state = { data: null, tab: 'recs' };

// ------------------------------------------------------------------ утилиты
function toast(msg, cls) {
  const t = $('toast');
  t.textContent = msg;
  t.className = 'toast ' + (cls || '');
  clearTimeout(t._tm);
  t._tm = setTimeout(() => t.classList.add('hidden'), 4500);
}

async function api(path, body) {
  const opt = body !== undefined
    ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
    : {};
  try {
    const r = await fetch(path, opt);
    return await r.json();
  } catch (e) {
    return { ok: false, error: 'Сервер интерфейса недоступен: ' + e };
  }
}

function fmtNum(v) {
  if (v === null || v === undefined || v === '' || v === '—') return '—';
  const n = parseFloat(String(v).replace(',', '.'));
  if (isNaN(n)) return String(v);
  if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(1) + ' млрд';
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1) + ' млн';
  if (Math.abs(n) >= 1e3) return Math.round(n).toLocaleString('ru-RU');
  if (Number.isInteger(n)) return String(n);
  return n.toFixed(1);
}

// ------------------------------------------------------------------ канва
function drawChart(series) {
  const cv = $('chart');
  const dpr = window.devicePixelRatio || 1;
  const w = cv.clientWidth || 900, h = 230;
  cv.width = w * dpr; cv.height = h * dpr;
  const c = cv.getContext('2d');
  c.setTransform(dpr, 0, 0, dpr, 0, 0);
  c.clearRect(0, 0, w, h);
  const pl = 44, pr = 56, pt = 12, pb = 22;
  const gw = w - pl - pr, gh = h - pt - pb;

  c.font = '11px Segoe UI, Arial';
  c.textAlign = 'right'; c.fillStyle = '#94a3b8';
  for (let i = 0; i <= 4; i++) {
    const v = i * 25, y = pt + gh * (1 - v / 100);
    c.strokeStyle = '#e2e8f0'; c.beginPath(); c.moveTo(pl, y); c.lineTo(w - pr, y); c.stroke();
    c.fillText(String(v), pl - 6, y + 4);
  }
  if (!series || series.length < 2) {
    c.fillStyle = '#94a3b8'; c.textAlign = 'center';
    c.fillText('ожидание данных… (подключитесь и нажмите «Начать суточный мониторинг»)', w / 2, h / 2);
    return;
  }
  let maxBatch = 1;
  for (const p of series) if (p.batch > maxBatch) maxBatch = p.batch;
  c.textAlign = 'left'; c.fillStyle = '#2563eb';
  c.fillText(fmtNum(maxBatch), w - pr + 6, pt + 10);

  const n = series.length;
  const xy = (i, v, vmax) => [pl + gw * i / (n - 1), pt + gh * (1 - Math.min(v, vmax) / vmax)];
  // batch
  c.strokeStyle = '#2563eb'; c.lineWidth = 1.5; c.beginPath();
  series.forEach((p, i) => { const [x, y] = xy(i, p.batch, maxBatch); i ? c.lineTo(x, y) : c.moveTo(x, y); });
  c.stroke();
  // cpu
  c.strokeStyle = '#dc2626'; c.lineWidth = 2; c.beginPath();
  series.forEach((p, i) => { const [x, y] = xy(i, p.cpu, 100); i ? c.lineTo(x, y) : c.moveTo(x, y); });
  c.stroke();
  const [lx, ly] = xy(n - 1, series[n - 1].cpu, 100);
  c.fillStyle = '#dc2626'; c.beginPath(); c.arc(lx, ly, 3, 0, 7); c.fill();

  c.fillStyle = '#94a3b8'; c.textAlign = 'left';
  c.fillText(new Date(series[0].ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }), pl, h - 6);
  c.textAlign = 'right';
  c.fillText(new Date(series[n - 1].ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }), w - pr, h - 6);
}

// ------------------------------------------------------------------ отрисовка
const SEV_RU = { critical: 'Критично', warning: 'Важно', info: 'Рекомендуется', ok: 'Норма' };
const SEV_CLS = { critical: 'c-crit', warning: 'c-warn', info: 'c-info', ok: 'c-ok' };

function render(d) {
  state.data = d;

  // чипы
  const cc = $('chip-conn'), cm = $('chip-mon');
  if (d.connected) {
    cc.textContent = '● подключено: ' + d.conn_info;
    cc.className = 'chip on';
  } else {
    cc.textContent = '● не подключено'; cc.className = 'chip off';
  }
  if (d.monitoring) {
    cm.textContent = '● СУТОЧНЫЙ МОНИТОРИНГ: каждые ' + d.interval + ' сек';
    cm.className = 'chip busy';
  } else {
    cm.textContent = '○ мониторинг остановлен'; cm.className = 'chip off';
  }

  // карточки
  const set = (id, v, warn) => { $(id).textContent = v; $(id).className = 'v' + (warn ? ' warn' : ''); };
  set('k-cpu', d.cards.cpu || '—', !!d.cards.cpu_warn);
  set('k-batch', d.cards.batch || '—', false);
  set('k-conn', d.cards.conn || '—', false);
  set('k-ple', d.cards.ple || '—', !!d.cards.ple_warn);
  set('k-blocked', d.cards.blocked || '0', !!d.cards.blocked_warn);
  set('k-health', d.findings.length || d.health ? d.health : '—', d.health && d.health < 70);

  drawChart(d.series);

  // кнопки
  $('btn-start').disabled = !d.connected || d.monitoring;
  $('btn-stop').disabled = !d.monitoring;
  $('btn-snapshot').disabled = !d.connected || d.generating;
  $('btn-daily').disabled = !d.connected || d.generating;
  if (!d.interval_set) { $('interval').value = d.interval; d.interval_set = true; }

  // рекомендации
  const tb = document.querySelector('#findings tbody');
  if (d.findings && d.findings.length) {
    const cnt = { critical: 0, warning: 0, info: 0 };
    d.findings.forEach(f => { if (cnt[f.severity] !== undefined) cnt[f.severity]++; });
    $('recs-summary').innerHTML =
      `<b>Оценка: ${d.health}/100.</b> ${d.summary || ''}` +
      ` &nbsp;<span class="badge c-crit">критично: ${cnt.critical}</span>` +
      ` <span class="badge c-warn">предупреждений: ${cnt.warning}</span>` +
      ` <span class="badge c-info">рекомендаций: ${cnt.info}</span>`;
    if (!tb._filled || tb._stamped !== d.findings.length + '' + d.health) {
      tb.innerHTML = d.findings.map((f, i) =>
        `<tr data-i="${i}" class="${f.severity}"><td><span class="badge ${SEV_CLS[f.severity]}">${SEV_RU[f.severity]}</span></td>` +
        `<td>${f.area}</td><td>${f.title}</td></tr>`).join('');
      tb._stamped = d.findings.length + '' + d.health;
      tb._filled = true;
      tb._items = d.findings;
      document.querySelectorAll('#findings tbody tr').forEach(tr => tr.onclick = () => {
        document.querySelectorAll('#findings tbody tr').forEach(x => x.classList.remove('selected'));
        tr.classList.add('selected');
        const f = d.findings[+tr.dataset.i];
        $('finding-detail').textContent =
          (f.problem ? 'ПРОБЛЕМА\n' + f.problem + '\n\n' : '') +
          (f.recommendation ? 'РЕКОМЕНДАЦИЯ\n' + f.recommendation + '\n\n' : '') +
          (f.script ? 'SQL-СКРИПТ\n' + f.script : '');
      });
    }
  } else if (!tb._filled) {
    tb.innerHTML = '';
  }

  // причины
  if (d.causes && d.causes.length) {
    $('causes-list').innerHTML = d.causes.map(c => `
      <div class="cause ${c.severity}">
        <h4><span class="badge ${SEV_CLS[c.severity]}">${SEV_RU[c.severity]}</span> ${c.title}</h4>
        <div class="ev"><b>Наблюдения:</b><ul>${c.evidence.map(e => `<li>${e}</li>`).join('')}</ul></div>
        <p>${c.explanation}</p>
        <b>Что делать:</b><ul>${c.fixes.map(f => `<li>${f}</li>`).join('')}</ul>
      </div>`).join('');
  } else {
    $('causes-list').innerHTML = '<div class="card hint">Причины появятся после первого анализа (кнопка «Полный снимок + отчёт»).</div>';
  }

  // отчёты
  const rtb = document.querySelector('#reports tbody');
  if (d.reports && d.reports.length) {
    rtb.innerHTML = d.reports.map(r =>
      `<tr><td class="mono">${r}</td><td><a href="/reports/${encodeURIComponent(r)}" target="_blank"><button>Открыть</button></a></td></tr>`).join('');
  } else {
    rtb.innerHTML = '<tr><td colspan="2" class="hint">Отчётов пока нет — нажмите «Полный снимок + отчёт».</td></tr>';
  }

  // журнал
  if (state.tab === 'log') $('log').textContent = d.log || '(пусто)';

  // настройки (однократно при загрузке)
  if (!state.settingsLoaded && d.settings) {
    state.settingsLoaded = true;
    $('s-slowms').value = d.settings.slow_query_ms;
    $('s-retention').value = d.settings.retention_days;
    $('s-dailytime').value = d.settings.daily_report_time;
    $('s-dailyon').checked = d.settings.daily_report_enabled;
    $('s-deep').checked = d.settings.deep_index_scan;
    $('s-autostart').checked = d.settings.autostart_monitoring;
    $('s-encrypt').checked = d.settings.encrypt;
    $('s-trust').checked = d.settings.trust_cert;
    $('interval').value = d.settings.sample_interval;
  }
}

// ------------------------------------------------------------------ действия
async function refresh() {
  try {
    const r = await fetch('/api/state');
    if (r.ok) render(await r.json());
  } catch (e) { /* сервер перезапускается */ }
}

function connPayload() {
  return {
    server: $('server').value, port: 0, auth: $('auth').value,
    login: $('login').value, password: $('password').value,
  };
}

function bind() {
  $('auth').onchange = () =>
    document.querySelector('.connbar').classList.toggle('sqlauth', $('auth').value === 'sql');

  $('btn-connect').onclick = async () => {
    toast('Подключение…');
    const r = await api('/api/connect', connPayload());
    if (r.ok) {
      toast('Подключено: ' + r.info, 'ok');
      if (state.data && state.data.settings && state.data.settings.autostart_monitoring) {
        await api('/api/monitor/start', {});
      }
    } else toast(r.error || 'Ошибка подключения', 'err');
    refresh();
  };

  $('btn-test').onclick = async () => {
    toast('Проверка подключения…');
    const r = await api('/api/test', connPayload());
    if (r.ok) toast('Успешно: ' + r.info, 'ok');
    else toast(r.error || 'Ошибка', 'err');
  };

  $('btn-demo').onclick = async () => {
    const r = await api('/api/demo', {});
    if (r.ok) toast('Демо-режим включён: данные имитируются. Теперь можно сформировать отчёт.', 'ok');
    refresh();
  };

  $('btn-start').onclick = async () => {
    const r = await api('/api/monitor/start', { interval: parseInt($('interval').value, 10) || 30 });
    if (!r.ok) toast(r.error, 'err');
    refresh();
  };
  $('btn-stop').onclick = async () => { await api('/api/monitor/stop', {}); refresh(); };

  $('btn-snapshot').onclick = () => {
    toast('Формирование отчёта (полный снимок — это до 1–2 минут)…');
    api('/api/report', { daily: false }).then(r => { if (!r.ok) toast(r.error, 'err'); });
  };
  $('btn-daily').onclick = () => {
    toast('Формирование суточного отчёта…');
    api('/api/report', { daily: true }).then(r => { if (!r.ok) toast(r.error, 'err'); });
  };

  $('btn-savesettings').onclick = async () => {
    const s = state.data && state.data.settings ? state.data.settings : {};
    const body = {
      slow_query_ms: parseInt($('s-slowms').value, 10) || 10000,
      retention_days: parseInt($('s-retention').value, 10) || 7,
      daily_report_time: $('s-dailytime').value || '08:00',
      daily_report_enabled: $('s-dailyon').checked,
      deep_index_scan: $('s-deep').checked,
      autostart_monitoring: $('s-autostart').checked,
      encrypt: $('s-encrypt').checked,
      trust_cert: $('s-trust').checked,
      sample_interval: parseInt($('interval').value, 10) || 30,
    };
    Object.assign(s, body);
    const r = await api('/api/settings', s);
    if (r.ok) { $('settings-status').textContent = '✓ Сохранено'; setTimeout(() => $('settings-status').textContent = '', 2500); }
    else toast(r.error || 'Ошибка', 'err');
  };

  // вкладки
  document.querySelectorAll('.tab').forEach(t => t.onclick = () => {
    document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
    document.querySelectorAll('.tabbody').forEach(x => x.classList.add('hidden'));
    t.classList.add('active');
    state.tab = t.dataset.tab;
    $('tab-' + state.tab).classList.remove('hidden');
    if (state.tab === 'log' && state.data) $('log').textContent = state.data.log || '';
    if (state.tab === 'recs') drawChart(state.data && state.data.series);
  });

  window.addEventListener('resize', () => drawChart(state.data && state.data.series));
}

bind();
refresh();
setInterval(refresh, 2000);
