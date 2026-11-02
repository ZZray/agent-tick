const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(process.env.TICK_HTML || path.join(__dirname, 'index.html'), 'utf8');
const source = html.slice(html.indexOf('const SCR_PAGE_MS'), html.indexOf('// ---------- 全屏卡片'));
function screen(sessions = []) {
  const ctx = {
    document: { hidden: false, querySelector: () => null },
    getComputedStyle: () => ({ columnGap: '16', columnWidth: '336', fontSize: '16', paddingLeft: '24', paddingRight: '24' }),
    visible: () => sessions, now: () => 3600000, MIN: 60000,
    isWaiting: s => ['waiting', 'blocked'].includes(s.status),
    isEnded: s => ['done', 'failed', 'cancelled'].includes(s.status),
    isDormant: s => s.status === 'running' && s.updatedAt < 1800000,
    byOrder: (a, b) => b.createdAt - a.createdAt,
    floatData: () => ({ active: sessions.filter(s => !s.parentId && !['done', 'failed', 'cancelled'].includes(s.status) && s.updatedAt >= 1800000), stale: sessions.filter(s => !s.parentId && s.status === 'running' && s.updatedAt < 1800000), recent: [], nW: 1, nR: 0 }),
    itemsToday: () => 0, S: { conn: 'live', sessions: new Map(sessions.map(s => [s.id, s])) },
    root: { querySelector: () => ({}), querySelectorAll: () => [] },
    morph: (_, markup) => { ctx.markup = markup; return []; },
    settle: () => {}, requestAnimationFrame: () => {}, RM: { matches: true },
    esc: s => String(s).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    IC: { logo: '' }, EN: false, Date,
  };
  vm.createContext(ctx);
  vm.runInContext(source, ctx);
  return ctx;
}

test('a short final page has its own active dot and wraps to the first page', () => {
  const ctx = screen();
  let dots;
  const flow = { clientWidth: 1000, scrollWidth: 1250, scrollLeft: 250, scrollTo: o => { flow.scrollLeft = o.left; } };
  ctx.document.querySelector = selector => selector === '.cv-flow' ? flow : {
    querySelector: () => ({ dataset: {}, set innerHTML(value) { dots = value; } }),
  };
  ctx.scrDots();
  assert.equal(dots, '<i class=""></i><i class="on"></i>');
  ctx.scrAutoPage();
  assert.equal(flow.scrollLeft, 0);
});

test('automatic paging covers every column and reaches the final edge', () => {
  const ctx = screen();
  const flow = { clientWidth: 1000, scrollWidth: 3100, scrollLeft: 0, scrollTo: o => { flow.scrollLeft = o.left; } };
  ctx.document.querySelector = () => flow;
  const stops = [];
  for (let i = 0; i < 4; i++) { ctx.scrAutoPage(); stops.push(flow.scrollLeft); }
  assert.deepEqual(stops, [968, 1936, 2100, 0]);
  flow.clientWidth = 1300;
  ctx.scrAutoPage();
  assert.equal(flow.scrollLeft, 1268);
});

test('running and waiting child agents remain visible after their parent leaves', () => {
  const sessions = [
    { id: 'parent', status: 'done', endedAt: 1, createdAt: 1 },
    { id: 'child-run', parentId: 'parent', status: 'running', updatedAt: 3500000, createdAt: 2 },
    { id: 'child-wait', parentId: 'parent', status: 'waiting', waitSince: 3500000, createdAt: 3 },
    { id: 'old', status: 'running', updatedAt: 1, createdAt: 4 },
  ];
  const ctx = screen(sessions);
  ctx.scrCard = s => `<article>${s.id}</article>`;
  ctx.renderScreen();
  assert.ok(ctx.markup.includes('<article>child-run</article>'));
  assert.ok(ctx.markup.includes('<article>child-wait</article>'));
  assert.ok(ctx.markup.includes('<article>old</article>'));
  assert.ok(!ctx.markup.includes('<article>parent</article>'));
  assert.match(ctx.markup, /data-roll>1<\/b>在跑/);
  assert.match(ctx.markup, /data-roll>1<\/b>等你/);
});

test('manual interaction pauses paging and hidden pages do not advance', () => {
  const ctx = screen();
  const flow = { clientWidth: 1000, scrollWidth: 2000, scrollLeft: 0, scrollTo: o => { flow.scrollLeft = o.left; } };
  ctx.document.querySelector = () => flow;
  vm.runInContext('scrUserAt = Date.now()', ctx);
  ctx.scrAutoPage();
  assert.equal(flow.scrollLeft, 0);
  vm.runInContext('scrUserAt = Date.now() - 30001', ctx);
  ctx.scrAutoPage();
  assert.equal(flow.scrollLeft, 968);
  ctx.document.hidden = true;
  ctx.scrAutoPage();
  assert.equal(flow.scrollLeft, 968);
});

test('no visible sessions renders the idle clock', () => {
  const ctx = screen();
  ctx.renderScreen();
  assert.match(ctx.markup, /scr-idle/);
  assert.ok(!ctx.markup.includes('cv-flow'));
});

test('long checklists keep the current step and both folded ranges', () => {
  const ctx = screen();
  ctx.sicon = () => '';
  const items = Array.from({ length: 20 }, (_, i) => ({ id: `i${i}`, text: `step <${i}>`, status: i < 10 ? 'done' : i === 10 ? 'doing' : 'todo' }));
  const markup = ctx.scrSteps({ id: 's1', items });
  assert.match(markup, /前面 9 项/);
  assert.match(markup, /step &lt;10>/);
  assert.match(markup, /后面还有 6 项/);
});
