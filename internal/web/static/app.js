// PTP dashboard front-end. Pure vanilla JS: fetch the snapshot once and render
// all five panels. The curve selector switches between true offset, protocol
// measured offset, and servo frequency correction without a refetch.
let SNAP = null;

async function load() {
  const r = await fetch('/api/snapshot');
  SNAP = await r.json();
  render();
}

function el(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else e.setAttribute(k, v);
  }
  for (const k of kids) e.append(k instanceof Node ? k : document.createTextNode(k));
  return e;
}

function render() {
  const s = SNAP;
  document.getElementById('spec').textContent = s.specName;
  document.getElementById('seed').textContent = s.seed;
  document.getElementById('gm').textContent = s.finalGm || '（无）';
  document.getElementById('dur').textContent = s.durationSec.toFixed(1);
  renderTopology(s);
  renderTheory(s);
  renderElections(s);
  renderPorts(s);
  renderStats(s);
  renderCurve();
  renderTransitions(s);
  renderTimeline(s);
}

function renderTopology(s) {
  const svg = document.getElementById('topo');
  svg.innerHTML = '';
  const ns = 'http://www.w3.org/2000/svg';
  // Edges first.
  for (const edge of s.topologyView.edges) {
    const a = s.topologyView.nodes.find(n => n.id === edge.source);
    const b = s.topologyView.nodes.find(n => n.id === edge.target);
    if (!a || !b) continue;
    const line = document.createElementNS(ns, 'line');
    line.setAttribute('x1', a.x); line.setAttribute('y1', a.y);
    line.setAttribute('x2', b.x); line.setAttribute('y2', b.y);
    line.setAttribute('class', 'link');
    svg.appendChild(line);
    const lbl = document.createElementNS(ns, 'text');
    lbl.setAttribute('x', (a.x + b.x) / 2);
    lbl.setAttribute('y', (a.y + b.y) / 2 - 4);
    lbl.setAttribute('class', 'link-label');
    lbl.setAttribute('text-anchor', 'middle');
    lbl.textContent = `${(edge.delayABNs / 1000).toFixed(0)}μs / ${(edge.delayBANs / 1000).toFixed(0)}μs`;
    svg.appendChild(lbl);
  }
  // Nodes.
  for (const n of s.topologyView.nodes) {
    const g = document.createElementNS(ns, 'g');
    const rect = document.createElementNS(ns, 'rect');
    rect.setAttribute('x', n.x - 52); rect.setAttribute('y', n.y - 26);
    rect.setAttribute('width', 104); rect.setAttribute('height', 52);
    rect.setAttribute('rx', 8);
    rect.setAttribute('fill', '#1c2330');
    if (n.isGm) rect.setAttribute('class', 'node-gm');
    if (!n.powered) rect.setAttribute('stroke', 'var(--fault)');
    g.appendChild(rect);
    const name = document.createElementNS(ns, 'text');
    name.setAttribute('x', n.x); name.setAttribute('y', n.y - 4);
    name.setAttribute('text-anchor', 'middle');
    name.setAttribute('class', 'node-label');
    name.textContent = `${n.name} [${n.type}]`;
    g.appendChild(name);
    const sub = document.createElementNS(ns, 'text');
    sub.setAttribute('x', n.x); sub.setAttribute('y', n.y + 14);
    sub.setAttribute('text-anchor', 'middle');
    sub.setAttribute('class', 'link-label');
    sub.textContent = n.isGm ? 'GRANDMASTER'
      : (n.powered ? `prio1=${n.priority1} class=${n.clockClass}` : 'POWERED OFF');
    g.appendChild(sub);
    svg.appendChild(g);
  }
}

function renderTheory(s) {
  const box = document.getElementById('theory');
  box.innerHTML = '';
  const tc = s.theoryCheck;
  if (!tc) { box.textContent = '（无理论对照数据）'; return; }
  const ok = tc.gmMatches;
  box.append(
    el('div', {},
      el('span', { class: ok ? 'theory-ok' : 'theory-bad' },
        ok ? '✓ 手算与仿真主时钟一致：' : '✗ 主时钟不一致：'),
      ` 预测=${tc.predictedGM} 实际=${tc.actualGM}`),
    el('div', { style: 'margin-top:6px;color:var(--muted)' },
      `预测 Passive 端口: ${tc.predictedPassive || '—'} ｜ 残差底噪(1σ): ${tc.residualFloorNs.toFixed(2)} ns`)
  );
  const rows = tc.bmcaTrace.map(tr =>
    el('div', { style: 'color:var(--muted);margin-top:2px' },
      `· [${tr.step}] ${tr.winner}${tr.loser ? ' 胜 ' + tr.loser : ''} ${tr.field ? '(' + tr.field + ')' : ''} — ${tr.detail}`));
  box.append(...rows);
}

function renderElections(s) {
  const tb = document.querySelector('#electionTable tbody');
  tb.innerHTML = '';
  // Most recent 60 decisions, newest at bottom.
  const rows = s.elections.slice(-60);
  for (const e of rows) {
    tb.append(el('tr', {},
      el('td', {}, e.tSec.toFixed(3)),
      el('td', {}, e.device),
      el('td', { title: e.detail }, e.decision),
      el('td', {}, String(e.stepsRemoved))));
  }
  tb.parentElement.parentElement.scrollTop = tb.parentElement.parentElement.scrollHeight;
}

function renderPorts(s) {
  const tb = document.querySelector('#portTable tbody');
  tb.innerHTML = '';
  for (const p of s.ports) {
    tb.append(el('tr', {},
      el('td', {}, p.device),
      el('td', {}, 'p' + p.port),
      el('td', {}, el('span', { class: 'pill ' + p.role }, p.role)),
      el('td', {}, String(p.foreignMasters)),
      el('td', {}, p.peerDelayNs ? p.peerDelayNs.toFixed(1) : '—'),
      el('td', {}, String(p.syncRx)),
      el('td', {}, String(p.announceRx)),
      el('td', {}, String(p.delayReqTx))));
  }
}

function renderStats(s) {
  const box = document.getElementById('stats');
  box.innerHTML = '';
  const sum = s.summary;
  box.append(
    stat('平均 |偏移|', sum.meanAbsNs.toFixed(2) + ' ns'),
    stat('最大 |偏移|', sum.maxAbsNs.toFixed(2) + ' ns'),
    stat('RMS', sum.rmsNs.toFixed(2) + ' ns'),
    stat('Passive 端口', (sum.passivePorts || []).length)));
}

function stat(label, val) {
  return el('span', { class: 'stat' }, el('b', {}, val), label);
}

function renderCurve() {
  const mode = document.getElementById('curveSelect').value;
  const canvas = document.getElementById('curve');
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth, h = 240;
  canvas.width = w * dpr; canvas.height = h * dpr;
  const ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);

  const curves = SNAP.offsetCurves;
  if (!curves.length) {
    ctx.fillStyle = '#8b97a5'; ctx.fillText('无偏移样本', 20, 30); return;
  }
  let all = [];
  for (const c of curves) for (const p of c.points) all.push(p);
  const t0 = Math.min(...all.map(p => p.tSec));
  const t1 = Math.max(...all.map(p => p.tSec));
  const valOf = p => mode === 'true' ? p.true : mode === 'measured' ? p.measured : p.freqPpb;
  let vmin = Math.min(...all.map(valOf)), vmax = Math.max(...all.map(valOf));
  if (vmin === vmax) { vmin -= 1; vmax += 1; }
  const pad = (vmax - vmin) * 0.08; vmin -= pad; vmax += pad;

  // Axes + zero line.
  ctx.strokeStyle = '#2b333d'; ctx.beginPath();
  ctx.moveTo(50, 10); ctx.lineTo(50, h - 30); ctx.lineTo(w - 10, h - 30); ctx.stroke();
  const yOf = v => 10 + (h - 40) * (1 - (v - vmin) / (vmax - vmin));
  if (vmin < 0 && vmax > 0) {
    ctx.strokeStyle = '#444c56'; ctx.setLineDash([4, 4]);
    ctx.beginPath(); ctx.moveTo(50, yOf(0)); ctx.lineTo(w - 10, yOf(0)); ctx.stroke();
    ctx.setLineDash([]);
  }
  ctx.fillStyle = '#8b97a5';
  ctx.fillText(vmax.toFixed(0), 6, 16);
  ctx.fillText(vmin.toFixed(0), 6, h - 28);
  ctx.fillText(`${t0.toFixed(1)}s`, 50, h - 12);
  ctx.fillText(`${t1.toFixed(1)}s`, w - 50, h - 12);

  const colors = ['#4aa8ff', '#3fb950', '#d29922', '#a371f7', '#f85149', '#39c5cf'];
  curves.forEach((c, ci) => {
    ctx.strokeStyle = colors[ci % colors.length]; ctx.lineWidth = 1.5;
    ctx.beginPath();
    c.points.forEach((p, i) => {
      const x = 50 + (w - 60) * (p.tSec - t0) / Math.max(1e-9, t1 - t0);
      const y = yOf(valOf(p));
      i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
    });
    ctx.stroke();
  });
  // Legend.
  curves.forEach((c, ci) => {
    const x = 60 + ci * 130;
    ctx.fillStyle = colors[ci % colors.length];
    ctx.fillRect(x, 14, 10, 10);
    ctx.fillStyle = '#d7dde5'; ctx.fillText(c.device, x + 14, 23);
  });
}

function renderTransitions(s) {
  const tb = document.querySelector('#transTable tbody');
  tb.innerHTML = '';
  for (const e of s.stateTransitions.slice(-80)) {
    tb.append(el('tr', {},
      el('td', {}, e.tSec.toFixed(4)),
      el('td', {}, `${e.device}:p${e.port}`),
      el('td', {}, `${e.from} → `, el('span', { class: 'pill ' + e.to }, e.to)),
      el('td', { style: 'color:var(--muted)' }, e.reason)));
  }
}

function renderTimeline(s) {
  const tb = document.querySelector('#timelineTable tbody');
  tb.innerHTML = '';
  for (const m of s.timeline.slice(-120)) {
    tb.append(el('tr', { class: 'timeline-row ' + m.kind },
      el('td', {}, m.tSec.toFixed(5)),
      el('td', {}, m.kind),
      el('td', {}, `${m.device}:p${m.port}${m.peer ? '→' + m.peer : ''}`),
      el('td', {}, m.msgType),
      el('td', {}, String(m.seqId)),
      el('td', {}, m.correction ? m.correction.toFixed(1) :
        (m.residence ? '+' + m.residence.toFixed(1) : '—'))));
  }
}

document.getElementById('reload').addEventListener('click', load);
document.getElementById('curveSelect').addEventListener('change', renderCurve);
window.addEventListener('resize', () => SNAP && renderCurve());
load();
