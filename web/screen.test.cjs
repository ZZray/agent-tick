const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(process.env.TICK_HTML || path.join(__dirname, 'index.html'), 'utf8');
const source = html.slice(html.indexOf('const CV_GAP'), html.indexOf('// 面板设置留在侧栏 morph 之外'));
const MIN = 60000;
const NOW = 100 * MIN;
function screen(sessions = []) {
  const byId = new Map(sessions.map(s => [s.id, s]));
  const ctx = {
    document: { querySelector: () => null, querySelectorAll: () => [], documentElement: {} },
    innerWidth: 1920, innerHeight: 1200,
    store: { get: (_, d) => d, set: () => {} },
    visible: () => sessions.filter(s => !s.archived), now: () => NOW, MIN,
    isChild: s => !!s.parentId && byId.has(s.parentId) && !byId.get(s.parentId).archived,
    isWaiting: s => ['waiting', 'blocked'].includes(s.status),
    isEnded: s => ['done', 'failed', 'cancelled'].includes(s.status),
    isDormant: s => s.status === 'running' && s.updatedAt < NOW - 30 * MIN,
    byOrder: (a, b) => b.createdAt - a.createdAt,
    sessionRow: s => `<div class="s" data-sid="${s.id}">${s.title}</div>`, sweeper: () => () => false, short: () => '1m',
    itemsToday: () => 3, S: { conn: 'live', sessions: byId, open: new Set() },
    root: { querySelector: () => null }, RM: { matches: true }, EASE_OUT: 'ease-out', EN: false, Date,
    morph: (_, markup) => { ctx.markup = markup; return []; }, settle: () => {}, schedule: () => {},
    esc: s => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    IC: { logo: '' }, scrTimeOverride: null,
  };
  vm.createContext(ctx);
  vm.runInContext(source, ctx);
  ctx.run = code => vm.runInContext(code, ctx);
  return ctx;
}
const plain = v => JSON.parse(JSON.stringify(v)); // vm 里造出来的对象原型不同，比较前先转成本地对象
const overlap = (a, b) => a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
const apart = (a, b) => a.x + a.w + 24 <= b.x || b.x + b.w + 24 <= a.x || a.y + a.h + 24 <= b.y || b.y + b.h + 24 <= a.y; // 至少隔一个间距

test('new cards fill three columns left to right, then start the next row without overlapping', () => {
  const ctx = screen();
  const rects = [];
  for (const h of [260, 180, 320, 200, 240, 150, 300]) {
    const p = ctx.cvPlace(rects, 380, h);
    rects.push({ ...p, w: 380, h });
  }
  assert.deepEqual(rects.slice(0, 3).map(r => [r.x, r.y]), [[0, 0], [404, 0], [808, 0]]);
  assert.ok(rects.every(r => r.x <= 808), 'never more than three columns');
  for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) assert.equal(overlap(rects[i], rects[j]), false, `${i} overlaps ${j}`);
  assert.ok(rects[3].y > 0, 'fourth card starts a new row');
});

test('a card the user parked at the origin is not covered by the next one', () => {
  const ctx = screen();
  const p = ctx.cvPlace([{ x: 0, y: 0, w: 380, h: 300 }], 380, 200);
  assert.deepEqual(plain(p), { x: 404, y: 0 });
  const q = ctx.cvPlace([{ x: 0, y: 0, w: 380, h: 300 }, { x: 404, y: 0, w: 380, h: 300 }, { x: 808, y: 0, w: 380, h: 300 }], 380, 200);
  assert.deepEqual(plain(q), { x: 0, y: 324 });
});

test('fit centers the cards and never zooms in past the fit cap', () => {
  const ctx = screen();
  const v = ctx.cvFit([{ x: 0, y: 0, w: 380, h: 200 }], 1920, 1200, 56);
  assert.equal(v.z, 1.5);
  assert.equal(v.x, (1920 - 380 * 1.5) / 2);
  assert.equal(Math.round(v.y), Math.round(56 + (1200 - 56 - 200 * 1.5) / 2));
  const wide = ctx.cvFit([{ x: -2000, y: 0, w: 380, h: 200 }, { x: 4000, y: 3000, w: 380, h: 200 }], 1920, 1200, 56);
  assert.ok(wide.z >= 0.25 && wide.z < 0.4);
  const empty = ctx.cvFit([], 1920, 1200, 56);
  assert.equal(empty.z, 1);
});

test('zooming keeps the world point under the cursor fixed and respects the limits', () => {
  const ctx = screen();
  const v = { x: 100, y: 50, z: 1 };
  const w = { x: (700 - v.x) / v.z, y: (400 - v.y) / v.z };
  const n = ctx.cvZoomAt(v, 700, 400, 1.5);
  assert.equal(n.z, 1.5);
  assert.ok(Math.abs(n.x + w.x * n.z - 700) < 1e-9 && Math.abs(n.y + w.y * n.z - 400) < 1e-9);
  assert.equal(ctx.cvZoomAt(v, 0, 0, 100).z, 2.5);
  assert.equal(ctx.cvZoomAt(v, 0, 0, 0.0001).z, 0.25);
});

test('waiting cards come first, long-ended and archived sessions stay off the canvas', () => {
  const ctx = screen([
    { id: 'run', status: 'running', createdAt: 2, updatedAt: NOW - MIN, title: 'run' },
    { id: 'wait', status: 'waiting', createdAt: 1, waitSince: NOW - MIN, updatedAt: NOW, title: 'wait' },
    { id: 'old', status: 'done', createdAt: 0, endedAt: NOW - 40 * MIN, updatedAt: 0, title: 'old' },
    { id: 'fresh', status: 'done', createdAt: 3, endedAt: NOW - 5 * MIN, updatedAt: 0, title: 'fresh' },
    { id: 'gone', status: 'running', createdAt: 4, updatedAt: NOW, archived: true, title: 'gone' },
  ]);
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['wait', 'run', 'fresh']);
});

test('sub-agents are cards of their own; a card whose parent has long left simply has no parent to point to', () => {
  const ctx = screen([
    { id: 'p1', status: 'running', createdAt: 1, updatedAt: NOW, title: 'live parent' },
    { id: 'k1', parentId: 'p1', status: 'running', createdAt: 2, updatedAt: NOW, title: 'kid of live parent' },
    { id: 'p2', status: 'done', createdAt: 1, endedAt: NOW - 50 * MIN, updatedAt: 0, title: 'old parent' },
    { id: 'k2', parentId: 'p2', status: 'running', createdAt: 3, updatedAt: NOW, title: 'kid of old parent' },
    { id: 'k3', parentId: 'p2', status: 'waiting', createdAt: 4, waitSince: NOW, updatedAt: NOW, title: 'waiting kid' },
  ]);
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['k3', 'k2', 'k1', 'p1']);
});

test('a new sub-agent card lands to the right of its parent, below any card already there', () => {
  const ctx = screen();
  const parent = { x: 0, y: 0, w: 380, h: 300 };
  assert.deepEqual(plain(ctx.cvPlaceNear([parent], parent, 380, 100)), { x: 452, y: 0 });
  assert.deepEqual(plain(ctx.cvPlaceNear([parent, { x: 452, y: 0, w: 380, h: 100 }], parent, 380, 100)), { x: 452, y: 124 });
});

test('the edge from a parent to its sub-agent leaves from the side that faces the child', () => {
  const ctx = screen();
  const a = { x: 0, y: 0, w: 380, h: 100 };
  assert.equal(ctx.cvEdgePath(a, { x: 452, y: 0, w: 380, h: 100 }), 'M380 26 C420 26 412 26 452 26');
  assert.equal(ctx.cvEdgePath(a, { x: -452, y: 0, w: 380, h: 100 }), 'M0 26 C-40 26 -32 26 -72 26');
  assert.equal(ctx.cvEdgePath(a, { x: 0, y: 140, w: 380, h: 100 }), 'M190 100 C190 140 190 100 190 140');
  assert.equal(ctx.cvEdgePath(a, { x: 0, y: -140, w: 380, h: 100 }), 'M190 0 C190 -40 190 0 190 -40');
});

test('tidy layout keeps each parent with its sub-agents and never overlaps', () => {
  const ctx = screen();
  const nodes = [
    { id: 'p', parentId: '', w: 380, h: 300 }, { id: 'k1', parentId: 'p', w: 380, h: 100 }, { id: 'k2', parentId: 'p', w: 380, h: 120 },
    { id: 'q', parentId: '', w: 380, h: 200 }, { id: 'r', parentId: '', w: 380, h: 200 }, { id: 'lost', parentId: 'gone', w: 380, h: 90 },
  ];
  const pos = plain([...ctx.cvArrangeLayout(nodes, 1280)]);
  const at = Object.fromEntries(pos);
  assert.deepEqual(at.p, { x: 0, y: 0 });
  assert.deepEqual(at.k1, { x: 452, y: 0 });
  assert.deepEqual(at.k2, { x: 452, y: 124 });
  assert.equal(pos.length, nodes.length, 'every card gets a spot, including one whose parent is not on the canvas');
  const rects = nodes.map(n => ({ ...at[n.id], w: n.w, h: n.h }));
  for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) assert.equal(apart(rects[i], rects[j]), true, `${nodes[i].id} is too close to ${nodes[j].id}`);
  assert.ok(Math.max(...rects.map(r => r.x + r.w)) <= 1260, 'stays inside the window width');
});

test('the canvas renders one card per session, shows the question on waiting cards and leaves checklists folded like the float window', () => {
  const ctx = screen([
    { id: 'a', agent: 'codex', status: 'running', createdAt: 2, updatedAt: NOW, title: 'task a', items: [] },
    { id: 'w', agent: 'kiro', status: 'waiting', createdAt: 1, waitSince: NOW - 3 * MIN, updatedAt: NOW, title: 'task w', statusNote: 'delete the cache?', items: [] },
    { id: 'e', agent: 'kiro', status: 'done', createdAt: 0, endedAt: NOW - MIN, updatedAt: 0, title: 'task e', summary: 'all green', items: [] },
  ]);
  ctx.renderScreen();
  assert.equal((ctx.markup.match(/class="cv-card /g) || []).length, 3);
  assert.match(ctx.markup, /class="scr warn"/);
  assert.match(ctx.markup, /delete the cache\?/);
  assert.match(ctx.markup, /data-cv="wait"/);
  assert.match(ctx.markup, /visibility:hidden/, 'cards without a stored spot wait for the layout pass');
  assert.deepEqual([...ctx.S.open], [], 'cards start folded; opening one is the user\'s click, same as the float window');
});

test('stored card spots are used as they are and an empty board shows the clock', () => {
  const ctx = screen([{ id: 'a', agent: 'codex', status: 'running', createdAt: 2, updatedAt: NOW, title: 'task a', items: [] }]);
  ctx.run("CV.pos.set('a', { x: 40, y: 80 })");
  ctx.renderScreen();
  assert.match(ctx.markup, /left:40px;top:80px/);
  const idle = screen([]);
  idle.renderScreen();
  assert.match(idle.markup, /class="scr-idle"/);
  assert.doesNotMatch(idle.markup, /cv-world/);
});

test('task refresh preserves tidy animation and a repeated tidy replaces its cleanup timer', () => {
  const ctx = screen([{ id: 'a', agent: 'codex', status: 'running', createdAt: 2, updatedAt: NOW, title: 'task a', items: [] }]);
  const timers = new Map();
  let timerId = 0, clock = 0;
  const card = { dataset: { cvid: 'a' }, offsetWidth: 380, offsetHeight: 100, style: {} };
  const makeWorld = () => {
    const classes = new Set(['cv-world']);
    return { classes, classList: { add: c => classes.add(c), remove: c => classes.delete(c) }, querySelectorAll: () => [card] };
  };
  let world = makeWorld();
  ctx.document.querySelector = selector => selector === '.cv-world' ? world : null;
  ctx.performance = { now: () => clock };
  ctx.requestAnimationFrame = () => 1;
  ctx.setTimeout = (fn, delay) => { const id = ++timerId; timers.set(id, { fn, at: clock + delay }); return id; };
  ctx.clearTimeout = id => timers.delete(id);
  ctx.cvEdges = ctx.cvFitView = ctx.cvLayout = () => {};
  ctx.morph = (_, markup) => {
    ctx.markup = markup;
    // A task refresh applies the newly rendered class, as patchNode does.
    const renderedClass = markup.match(/class="(cv-world[^\"]*)"/)[1];
    world.classes.clear();
    renderedClass.split(' ').forEach(c => world.classes.add(c));
    return [];
  };
  ctx.cvArrange();
  ctx.renderScreen();
  assert.equal(world.classes.has('arranging'), true, 'a task refresh must not cancel the card transition');
  clock = 200;
  ctx.cvArrange();
  assert.equal(timers.size, 1, 'the previous cleanup must not stop a newer tidy');
  assert.equal([...timers.values()][0].at, 680);
  // If all cards leave and the canvas is rebuilt, cleanup targets the current world.
  world = makeWorld();
  ctx.renderScreen();
  assert.equal(world.classes.has('arranging'), true);
  [...timers.values()][0].fn();
  assert.equal(world.classes.has('arranging'), false, 'the current canvas is cleaned up');
  ctx.renderScreen();
  assert.doesNotMatch(ctx.markup, /cv-world[^\"]*arranging/, 'later task updates must not restore a finished animation');
});

function items(flags) {
  const ctx = {
    ...flags, ITEMS_LONG: undefined, S: { open: new Set(), justDone: new Set() },
    esc: s => String(s ?? ''), finished: it => ['done', 'skipped'].includes(it.status), sicon: () => '', modHead: () => '',
    itemMs: () => ({ ms: 0, approx: false }), dur: () => '0s', IC: { chev: '', copy: '', undoArrow: '', skip: '' },
  };
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('const ITEMS_LONG'), html.indexOf('function cardHtml')), ctx);
  return ctx;
}
const longSession = () => ({ id: 's', status: 'running', items: Array.from({ length: 63 }, (_, i) => ({ id: `i${i}`, text: `step ${i}`, status: i < 35 ? 'done' : i === 35 ? 'doing' : 'todo' })) });

test('the canvas folds a long checklist the same way the float window does', () => {
  for (const flags of [{ FLOAT: true, SCREEN: false }, { FLOAT: false, SCREEN: true }]) {
    const out = items(flags).itemsHtml(longSession());
    assert.match(out, /前面 34 项已完成/, JSON.stringify(flags));
    assert.match(out, /后面还有 \d+ 项/);
    assert.ok((out.match(/class="it-s /g) || []).length <= 6, 'only the rows around the current step stay open');
  }
  const panel = items({ FLOAT: false, SCREEN: false }).itemsHtml(longSession());
  assert.equal((panel.match(/class="it-s /g) || []).length, 63, 'the panel detail still lists every item');
});
