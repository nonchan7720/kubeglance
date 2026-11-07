// kubeglance UI: four levels, one screen each, refreshed every 15 s.
// No framework, no build step. Talks to /api/v1/*.
(() => {
  'use strict';

  const $ = (sel) => document.querySelector(sel);
  const main = $('#main');
  const crumb = $('#crumb');
  const state = { level: 'cluster', window: '30m', filter: null, timer: null, interval: 15000, lang: 'en' };

  // ---------- i18n ----------
  // Two languages, one dictionary. The language is ?lang=, else what the
  // device remembers, else the browser's; the EN/JA button switches it.
  const DICT = {
    en: {
      'tab.cluster': 'Cluster', 'tab.nodes': 'Nodes', 'tab.workloads': 'Workloads', 'tab.pods': 'Pods',
      loading: 'loading…', back: '‹ back', footer: 'now · <b>avg</b> · <i>median</i> · p95 over the window',
      'nodes.ready': 'nodes ready', workloads: 'workloads', pods: 'pods', pod: 'pod',
      'usage.vs.alloc': 'Usage vs allocatable', 'req.vs.alloc': 'Requested vs allocatable', headroom: 'headroom for new pods',
      'cluster.note': (lim) => `Usage is the sum of node usage. Requests are the sum of running pods' requests: when requested is near allocatable, the scheduler is out of room even if usage is low. Limits total ${lim}.`,
      'of.limit': 'of limit', 'of.request': 'of request', gaps: 'gaps', avg: 'avg', med: 'med', p95: 'p95',
      'spread.cpu': 'CPU spread', 'spread.mem': 'MEM spread', 'spread.def': 'Spread = busiest − idlest node.',
      'balanced': 'balanced', 'uneven.some': 'somewhat uneven', 'uneven': 'uneven — consider rebalancing',
      notready: 'NotReady', req: 'req', 'per.pod': 'per pod', 'cpu.of.req': 'cpu', 'no.cpu.req': 'no cpu request',
      gone: 'gone', 'no.workloads': 'no workloads', 'no.pods': 'no pods', 'load.fail': 'could not load',
      'node.filter': (n) => `node ${n}`, 'workload.filter': (w) => `workload ${w}`,
      collecting: (s, v) => `collecting every ${s}s · ${v}`, 'collector.bad': (e) => `collector: ${e}`, 'no.data': 'no data yet',
    },
    ja: {
      'tab.cluster': 'クラスタ', 'tab.nodes': 'ノード', 'tab.workloads': 'ワークロード', 'tab.pods': 'Pod',
      loading: '読み込み中…', back: '‹ 戻る', footer: '現在 · <b>平均</b> · <i>中央値</i> · p95（ウィンドウ内）',
      'nodes.ready': 'ノード Ready', workloads: 'ワークロード', pods: 'Pod', pod: 'Pod',
      'usage.vs.alloc': '使用量 / allocatable', 'req.vs.alloc': 'requests / allocatable', headroom: '新しい Pod の余地',
      'cluster.note': (lim) => `使用量はノード使用量の合計。requests は稼働中 Pod の requests の合計で、allocatable に近いと使用量が低くてもスケジューラに空きがありません。limits 合計 ${lim}。`,
      'of.limit': '/ limit', 'of.request': '/ request', gaps: '欠損', avg: '平均', med: '中央', p95: 'p95',
      'spread.cpu': 'CPU の偏り', 'spread.mem': 'MEM の偏り', 'spread.def': '偏り = 最も忙しいノード − 最も暇なノード。',
      'balanced': '均等', 'uneven.some': 'やや偏り', 'uneven': '偏りあり — リバランス検討',
      notready: 'NotReady', req: 'req', 'per.pod': 'Pod あたり', 'cpu.of.req': 'cpu', 'no.cpu.req': 'cpu request なし',
      gone: '消滅', 'no.workloads': 'ワークロードなし', 'no.pods': 'Pod なし', 'load.fail': '読み込めません',
      'node.filter': (n) => `ノード ${n}`, 'workload.filter': (w) => `ワークロード ${w}`,
      collecting: (s, v) => `${s}秒ごとに収集中 · ${v}`, 'collector.bad': (e) => `コレクタ: ${e}`, 'no.data': 'まだデータなし',
    },
  };
  const t = (key, ...args) => {
    const v = (DICT[state.lang] || DICT.en)[key] ?? DICT.en[key] ?? key;
    return typeof v === 'function' ? v(...args) : v;
  };
  function detectLang() {
    const q = new URLSearchParams(location.search).get('lang');
    let saved = null;
    try { saved = localStorage.getItem('kubeglance.lang'); } catch { /* storage may be unavailable */ }
    const pick = q || saved || (navigator.language || 'en');
    return pick.toLowerCase().startsWith('ja') ? 'ja' : 'en';
  }
  function setLang(lang) {
    state.lang = lang;
    document.documentElement.lang = lang;
    try { localStorage.setItem('kubeglance.lang', lang); } catch { /* ignore */ }
    $('#lang').textContent = lang === 'ja' ? 'EN' : 'JA'; // the button shows what you switch to
    $('#lang').title = lang === 'ja' ? 'Switch to English' : '日本語に切り替え';
    document.querySelectorAll('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
    document.querySelectorAll('[data-i18n-html]').forEach((el) => { el.innerHTML = t(el.dataset.i18nHtml); });
  }

  // ---------- formatting ----------
  const cpu = (m) => (m >= 1000 ? (m / 1000).toFixed(m >= 10000 ? 0 : 2) + ' cores' : m + 'm');
  const mem = (b) => {
    const u = ['B', 'Ki', 'Mi', 'Gi', 'Ti'];
    let i = 0;
    let v = b;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v : v.toFixed(v >= 100 ? 0 : 1)) + u[i];
  };
  const pct = (r) => (r > 0 ? Math.round(r * 100) + '%' : '—');
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  // ---------- bar ----------
  // scale: the value that fills the bar. Prefer the limit, else allocatable,
  // else 2× the request, else 1.5× the max seen in the window.
  function bar(kind, st, { req = 0, lim = 0, cap = 0 }, fmt) {
    const now = st.now[kind]; const avg = st.avg[kind]; const med = st.median[kind]; const p95 = st.p95[kind]; const max = st.max[kind];
    const scale = lim || cap || (req ? req * 2 : 0) || Math.max(1, max * 1.5);
    const w = (v) => Math.min(100, (v / scale) * 100).toFixed(1) + '%';
    const ratio = lim ? now / lim : cap ? now / cap : 0;
    const cls = ratio >= 0.9 ? 'hot' : ratio >= 0.7 ? 'warn' : kind === 'cpu' ? 'cpu' : 'mem';
    const marks = (req && req < scale ? `<span class="mark" style="left:${w(req)}" title="request"></span>` : '') +
      (lim && lim <= scale ? `<span class="mark lim" style="left:${w(lim)}" title="limit"></span>` : '');
    const gap = st.coverage < 0.95 && st.expected > 0 ? `<span class="gap">${t('gaps')} ${Math.round((1 - st.coverage) * 100)}%</span>` : '';
    return `<div class="metric"><div class="lbl">${kind === 'cpu' ? 'CPU' : 'MEM'}</div>
      <div>
        <div class="bar"><span class="fill ${cls}" style="width:${w(now)}"></span><span class="avg" style="left:${w(avg)}" title="avg"></span>${marks}</div>
        <div class="nums"><span class="now">${fmt(now)}</span><span>${lim ? pct(now / lim) + ' ' + t('of.limit') : cap ? pct(now / cap) : req ? pct(now / req) + ' ' + t('of.request') : ''} ${gap}</span></div>
        <div class="nums stats"><span>${t('avg')} <b>${fmt(avg)}</b></span><span>${t('med')} <i>${fmt(med)}</i></span><span>${t('p95')} ${fmt(p95)}</span></div>
      </div></div>`;
  }

  const bars = (st, res) => bar('cpu', st, { req: res.req?.cpu, lim: res.lim?.cpu, cap: res.cap?.cpu }, cpu) +
    bar('memory', st, { req: res.req?.memory, lim: res.lim?.memory, cap: res.cap?.memory }, mem);

  // ---------- views ----------
  function renderCluster(v) {
    const u = v.usage;
    main.innerHTML = `
      <div class="kpis">
        <div class="kpi"><div class="v">${v.readyNodes}/${v.nodes}</div><div class="k">${t('nodes.ready')}</div></div>
        <div class="kpi"><div class="v">${v.workloads}</div><div class="k">${t('workloads')}</div></div>
        <div class="kpi"><div class="v">${v.pods}</div><div class="k">${t('pods')}</div></div>
      </div>
      <div class="card">
        <div class="head"><span class="name">${t('usage.vs.alloc')}</span><span class="sub">${cpu(v.allocatable.cpu)} · ${mem(v.allocatable.memory)}</span></div>
        ${bars(u, { cap: v.allocatable })}
      </div>
      <div class="card">
        <div class="head"><span class="name">${t('req.vs.alloc')}</span><span class="sub">${t('headroom')}</span></div>
        ${kv('CPU', cpu(v.requests.cpu), cpu(v.allocatable.cpu), v.requests.cpu / (v.allocatable.cpu || 1))}
        ${kv('MEM', mem(v.requests.memory), mem(v.allocatable.memory), v.requests.memory / (v.allocatable.memory || 1))}
      </div>
      <p class="note">${t('cluster.note', cpu(v.limits.cpu) + ' / ' + mem(v.limits.memory))}</p>`;
  }

  function kv(lbl, used, total, r) {
    const cls = r >= 0.9 ? 'hot' : r >= 0.7 ? 'warn' : lbl === 'CPU' ? 'cpu' : 'mem';
    return `<div class="metric"><div class="lbl">${lbl}</div><div>
      <div class="bar"><span class="fill ${cls}" style="width:${Math.min(100, r * 100).toFixed(1)}%"></span></div>
      <div class="nums"><span>${used}</span><span>${pct(r)}</span><span>${total}</span></div></div></div>`;
  }

  function renderNodes(v) {
    const im = v.imbalance;
    const verdict = (s) => t(s.spread >= 0.4 ? 'uneven' : s.spread >= 0.2 ? 'uneven.some' : 'balanced');
    main.innerHTML = `
      <p class="note">${t('spread.cpu')} <b>${pct(im.cpu.spread)}</b> (${verdict(im.cpu)}) · ${t('spread.mem')} <b>${pct(im.memory.spread)}</b> (${verdict(im.memory)}). ${t('spread.def')}</p>` +
      v.nodes.map((n) => `
      <div class="card tap" data-node="${esc(n.name)}">
        <div class="head"><span class="name">${esc(n.name)}${n.ready ? '' : `<span class="badge">${t('notready')}</span>`}${(n.roles || []).map((r) => `<span class="badge">${esc(r)}</span>`).join('')}</span>
          <span class="sub">${n.pods} ${t('pods')} · ${t('req')} ${cpu(n.requests.cpu)} / ${mem(n.requests.memory)}</span></div>
        ${bars(n.usage, { cap: n.allocatable })}
      </div>`).join('');
    main.querySelectorAll('[data-node]').forEach((el) => el.addEventListener('click', () => go('pods', { node: el.dataset.node })));
  }

  function renderWorkloads(v) {
    if (!v.workloads.length) { main.innerHTML = `<p class="muted">${t('no.workloads')}</p>`; return; }
    main.innerHTML = v.workloads.map((w) => `
      <div class="card tap" data-wl="${esc(w.namespace)}/${esc(w.kind)}/${esc(w.name)}">
        <div class="head"><span class="name">${esc(w.name)}<span class="badge">${esc(w.kind)}</span></span>
          <span class="sub">${esc(w.namespace)} · ${w.pods} ${t(w.pods === 1 ? 'pod' : 'pods')}</span></div>
        ${bars(w.usage, { req: w.requests, lim: w.limits })}
        <div class="nums"><span>${t('per.pod')}: ${cpu(w.perPod.cpu)} · ${mem(w.perPod.memory)}</span><span>${w.requestRatio.cpu ? t('cpu.of.req') + ' ' + pct(w.requestRatio.cpu) + ' ' + t('of.request') : t('no.cpu.req')}</span></div>
      </div>`).join('');
    main.querySelectorAll('[data-wl]').forEach((el) => el.addEventListener('click', () => go('pods', { workload: el.dataset.wl })));
  }

  function renderPods(v) {
    if (!v.pods.length) { main.innerHTML = `<p class="muted">${t('no.pods')}</p>`; return; }
    main.innerHTML = v.pods.map((p) => `
      <div class="card ${p.deleted ? 'deleted' : ''}">
        <div class="head"><span class="name">${esc(p.key.name)}${p.deleted ? `<span class="badge">${t('gone')}</span>` : ''}${p.phase && p.phase !== 'Running' ? `<span class="badge">${esc(p.phase)}</span>` : ''}</span>
          <span class="sub">${esc(p.key.namespace)} · ${esc(p.node || '—')}</span></div>
        ${bars(p.usage, { req: p.requests, lim: p.limits })}
      </div>`).join('');
  }

  // ---------- navigation ----------
  function go(level, filter) {
    state.level = level;
    state.filter = filter || null;
    document.querySelectorAll('#tabs button').forEach((b) => b.classList.toggle('active', b.dataset.level === level));
    if (filter) {
      const label = filter.node ? t('node.filter', filter.node) : t('workload.filter', filter.workload);
      crumb.innerHTML = `<button id="back">${t('back')}</button><span>${esc(label)}</span>`;
      crumb.hidden = false;
      $('#back').addEventListener('click', () => go(filter.node ? 'nodes' : 'workloads'));
    } else {
      crumb.hidden = true;
    }
    refresh();
  }

  async function refresh() {
    const q = new URLSearchParams({ window: state.window });
    if (state.filter) Object.entries(state.filter).forEach(([k, v]) => q.set(k, v));
    try {
      const [res, st] = await Promise.all([fetch(`api/v1/${state.level}?${q}`), fetch('api/v1/status')]);
      if (!res.ok) throw new Error(res.statusText);
      const data = await res.json();
      ({ cluster: renderCluster, nodes: renderNodes, workloads: renderWorkloads, pods: renderPods })[state.level](data);
      if (st.ok) {
        const s = await st.json();
        const dot = $('#status');
        dot.className = 'status ' + (s.collector.healthy ? 'ok' : 'bad');
        dot.title = s.collector.healthy ? t('collecting', s.interval / 1e9, s.version) : t('collector.bad', s.collector.lastError || t('no.data'));
        state.interval = Math.max(5000, s.interval / 1e6);
      }
      $('#updated').textContent = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch (e) {
      main.innerHTML = `<p class="muted">${t('load.fail')}: ${esc(e.message)}</p>`;
    }
    clearTimeout(state.timer);
    state.timer = setTimeout(refresh, state.interval);
  }

  document.querySelectorAll('#tabs button').forEach((b) => b.addEventListener('click', () => go(b.dataset.level)));
  $('#window').addEventListener('change', (e) => { state.window = e.target.value; refresh(); });
  $('#lang').addEventListener('click', () => { setLang(state.lang === 'ja' ? 'en' : 'ja'); go(state.level, state.filter); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
  setLang(detectLang());
  go('cluster');
})();
