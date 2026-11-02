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
  ctx.flowing = node => { ctx.flowNode = node; return ctx.run('cvFlowing(flowNode)'); };
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

test('modules appear only after execution reaches that branch, with its ancestors and stable IDs', () => {
  const owner = { id: 'p', agent: 'codex', status: 'running', updatedAt: NOW, items: [
    { id: 'i1', group: '后端 / 接口 / 鉴权', text: 'verify', status: 'todo' },
    { id: 'i2', group: '后端 / 存储', text: 'persist', status: 'todo' },
    { id: 'i3', group: '部署', text: 'publish', status: 'skipped' },
  ] };
  const ctx = screen([owner]);
  assert.equal(ctx.cvFlowNodes().length, 1, 'future and skipped-only modules stay in the checklist');
  owner.items[0].status = 'doing';
  const shown = ctx.cvFlowNodes().filter(n => n.ownerId);
  assert.deepEqual(plain(shown.map(n => n.modulePath)), ['后端', '后端 / 接口', '后端 / 接口 / 鉴权']);
  assert.equal(shown[0].parentId, owner.id);
  assert.equal(shown[1].parentId, shown[0].id);
  assert.equal(shown[2].parentId, shown[1].id);
  assert.equal(shown[0].items.length, 0, 'ancestors aggregate descendants without duplicating their checklist rows');
  const id = shown[2].id;
  owner.items.unshift({ id: 'i0', group: '准备', text: 'prepare', status: 'done' });
  assert.equal(ctx.cvFlowNodes().find(n => n.modulePath === '后端 / 接口 / 鉴权').id, id, 'item insertion does not change module identity');
  const second = { ...owner, id: 'q' };
  assert.notEqual(ctx.cvFlowNodes([second]).find(n => n.modulePath === '后端 / 接口 / 鉴权').id, id, 'same labels in another conversation have another ID');
});

test('module progress uses every descendant and flow stops when its owner is not running', () => {
  const owner = { id: 'p', status: 'running', updatedAt: NOW, endedAt: NOW, items: [
    { id: 'a', group: '开发', text: 'parent work', status: 'done' },
    { id: 'b', group: '开发 / 测试', text: 'future test', status: 'todo' },
    { id: 'c', group: '开发 / 接口', text: 'active work', status: 'doing' },
  ] };
  const ctx = screen([owner]);
  const parent = () => ctx.cvFlowNodes().find(n => n.modulePath === '开发');
  assert.equal(parent().done, 1);
  assert.equal(parent().total, 3);
  assert.equal(parent().status, 'running');
  assert.equal(ctx.flowing(parent()), true);
  for (const status of ['waiting', 'blocked', 'planning', 'done', 'cancelled']) {
    owner.status = status;
    assert.equal(ctx.flowing(parent()), false, `${status} owner must not leave stale doing items flowing`);
  }
  owner.status = 'running';
  owner.items[2].status = 'blocked';
  assert.equal(parent().status, 'blocked');
  assert.equal(ctx.flowing(parent()), false);
  owner.items[1].status = 'skipped'; owner.items[2].status = 'done';
  assert.equal(parent().status, 'done');
  assert.equal(parent().done, 3);
});

test('historically executed todo groups stay visible without marking them running', () => {
  const ctx = screen([{ id: 'p', status: 'running', updatedAt: NOW, items: [
    { id: 'i', group: '已进入模块', status: 'todo', text: 'retry later', spentMs: 1200 },
  ] }]);
  const module = ctx.cvFlowNodes().find(n => n.ownerId);
  assert.equal(module.status, 'planning');
  assert.equal(ctx.flowing(module), false);
});

test('an active branch flows through a mixed blocked parent without animating its blocked sibling', () => {
  const owner = { id: 'p', status: 'running', updatedAt: NOW, items: [
    { id: 'active', group: '开发 / 接口', status: 'doing', text: 'implement' },
    { id: 'blocked', group: '开发 / 测试', status: 'blocked', text: 'waiting for fixture' },
  ] };
  const ctx = screen([owner]);
  const nodes = ctx.cvFlowNodes();
  const parent = nodes.find(n => n.modulePath === '开发');
  assert.equal(parent.status, 'blocked', 'blocked work remains visible in the aggregate status');
  assert.equal(ctx.flowing(parent), true, 'running work still has a continuous line from the root');
  assert.equal(ctx.flowing(nodes.find(n => n.modulePath === '开发 / 接口')), true);
  assert.equal(ctx.flowing(nodes.find(n => n.modulePath === '开发 / 测试')), false);
  owner.status = 'waiting';
  assert.equal(ctx.flowing(parent), false, 'pausing the owner pauses every branch');
});

test('module projection does not inflate Agent counts, uses safe markup and never creates backend session actions', () => {
  const ctx = screen([{ id: 'p', agent: 'codex', status: 'running', updatedAt: NOW, title: 'task', items: [
    { id: 'i', group: '<module> / "child"', status: 'doing', text: '<unsafe>' },
  ] }]);
  ctx.renderScreen();
  assert.equal(ctx.cvCards().length, 1);
  assert.equal((ctx.markup.match(/class="cv-card /g) || []).length, 3);
  assert.match(ctx.markup, />1<\/b>在跑/);
  const module = ctx.cvFlowNodes().find(n => n.modulePath === '<module> / "child"');
  ctx.run(`CV.openModules.add(${JSON.stringify(module.id)})`);
  const markup = ctx.cvModuleCard(module);
  assert.match(markup, /&lt;unsafe>/);
  assert.match(markup, /&quot;child&quot;/);
  assert.doesNotMatch(markup, /data-sid=|data-act=|<unsafe>|<module>/);
});

test('screen primary running count matches root tasks and lists child agents separately', () => {
  const ctx = screen([
    { id: 'p', status: 'running', updatedAt: NOW, title: 'parent', items: [] },
    { id: 'k1', parentId: 'p', status: 'running', updatedAt: NOW, title: 'child one', items: [] },
    { id: 'k2', parentId: 'p', status: 'running', updatedAt: NOW, title: 'child two', items: [] },
    { id: 'orphan', parentId: 'missing', status: 'running', updatedAt: NOW, title: 'orphan', items: [] },
    { id: 'wait-parent', status: 'running', updatedAt: NOW, title: 'parent of waiting child', items: [] },
    { id: 'wait-child', parentId: 'wait-parent', status: 'waiting', waitSince: NOW, updatedAt: NOW, title: 'waiting child', items: [] },
  ]);
  ctx.renderScreen();
  assert.match(ctx.markup, />2<\/b>在跑/, 'only parent and orphan count as running roots');
  assert.match(ctx.markup, />2<\/b>子 agent/, 'child agents have a separate count');
  assert.match(ctx.markup, />1<\/b>项等你/, 'waiting jump includes actual waiting child sessions');
  assert.equal((ctx.markup.match(/class="cv-card /g) || []).length, 6, 'count grouping never removes a child card');
});

test('module checklist follows its active row and module coordinates survive save and reload', () => {
  const owner = { id: 'p', status: 'running', updatedAt: NOW, items: Array.from({ length: 30 }, (_, i) => ({ id: `i${i}`, group: '开发', text: `step ${i}`, status: i === 20 ? 'doing' : i === 0 ? 'blocked' : 'todo' })) };
  const ctx = screen([owner]);
  const module = ctx.cvFlowNodes().find(n => n.ownerId);
  ctx.run(`CV.openModules.add(${JSON.stringify(module.id)});CV.pos.set(${JSON.stringify(module.id)}, {x:123,y:456});CV.pos.set('deleted', {x:0,y:0})`);
  const markup = ctx.cvModuleCard(module);
  assert.match(markup, /step 20/);
  assert.doesNotMatch(markup, /step 0</);
  assert.equal((markup.match(/class="cv-mod-item /g) || []).length, 6);
  let saved;
  ctx.setTimeout = fn => { fn(); return 1; };
  ctx.clearTimeout = () => {};
  ctx.store.set = (_, value) => { saved = value; };
  ctx.cvSave();
  assert.deepEqual(plain(saved.pos[module.id]), { x: 123, y: 456 });
  assert.equal(saved.pos.deleted, undefined);
  const restored = screen([owner]);
  restored.store.get = () => saved;
  restored.cvLoad();
  assert.match(restored.cvModuleCard(module), /left:123px;top:456px/);
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

test('tidy lays out full recursive branches with spacing and keeps even a cyclic component visible', () => {
  const ctx = screen();
  const nodes = [
    { id: 'leaf', parentId: 'child', w: 320, h: 180 }, { id: 'child', parentId: 'p', w: 320, h: 140 },
    { id: 'sibling', parentId: 'p', w: 320, h: 100 }, { id: 'p', w: 380, h: 80 }, { id: 'q', w: 380, h: 130 },
    { id: 'cycle-a', parentId: 'cycle-b', w: 320, h: 100 }, { id: 'cycle-b', parentId: 'cycle-a', w: 320, h: 100 },
  ];
  const pos = Object.fromEntries(plain([...ctx.cvArrangeLayout(nodes, 1280)]));
  assert.equal(Object.keys(pos).length, nodes.length);
  assert.ok(pos.child.x >= pos.p.x + 380 + 72);
  assert.ok(pos.leaf.x >= pos.child.x + 320 + 72);
  const rects = nodes.map(n => ({ ...pos[n.id], w: n.w, h: n.h }));
  for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) assert.equal(apart(rects[i], rects[j]), true, `${nodes[i].id} overlaps ${nodes[j].id}`);
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

function cameraScreen() {
  const ctx = screen([
    { id: 'a', status: 'running', updatedAt: NOW, items: [] },
    { id: 'b', status: 'running', updatedAt: NOW, items: [] },
  ]);
  const els = ['a', 'b'].map(id => ({ dataset: { cvid: id }, offsetWidth: 380, offsetHeight: 180 }));
  ctx.document.querySelectorAll = () => els;
  ctx.document.querySelector = selector => selector === '.scr-h' ? { offsetHeight: 56 } : null;
  let clock = 10000, nextId = 0;
  const timers = new Map();
  ctx.Date = { now: () => clock };
  ctx.setTimeout = (fn, delay) => { const id = ++nextId; timers.set(id, { fn, at: clock + delay }); return id; };
  ctx.clearTimeout = id => timers.delete(id);
  ctx.cvApply = ctx.cvSave = () => {};
  ctx.run("CV.nodes=new Map(cvFlowNodes().map(n=>[n.id,n]));CV.pos.set('a',{x:0,y:0});CV.pos.set('b',{x:2200,y:0});CV.view={x:100,y:100,z:1}");
  return {
    ctx, els, timers,
    time: value => { clock = value; },
    fire: () => { const [id, t] = [...timers].sort((a, b) => a[1].at - b[1].at)[0]; timers.delete(id); clock = t.at; t.fn(); },
  };
}

test('new off-screen tasks adjust only the camera without requiring tidy', () => {
  const { ctx, els, fire } = cameraScreen();
  const before = plain(ctx.run('[...CV.pos]'));
  assert.equal(ctx.cvHeaderVisible(els[1]), false);
  ctx.cvNewContent(new Set(['b']), false);
  fire();
  assert.equal(ctx.cvHeaderVisible(els[1]), true, 'the new task title is visible after camera fit');
  assert.deepEqual(plain(ctx.run('[...CV.pos]')), before, 'all hand-placed cards keep their exact positions');
});

test('very distant new cards keep their title visible at minimum zoom, including a very tall card', () => {
  for (const height of [180, 20000]) {
    const { ctx, els, fire } = cameraScreen();
    els[1].offsetHeight = height;
    ctx.run("CV.pos.set('b',{x:20000,y:20000})");
    const before = plain(ctx.run('[...CV.pos]'));
    ctx.cvNewContent(new Set(['b']), false);
    fire();
    assert.equal(ctx.run('CV.view.z'), .25, 'minimum zoom remains unchanged');
    assert.equal(ctx.cvHeaderVisible(els[1]), true, `the ${height}px card is framed by its header`);
    assert.deepEqual(plain(ctx.run('[...CV.pos]')), before, 'fallback never rearranges user positions');
  }
});

test('new content waits for manual pan/drag/zoom to stop and coalesces incoming updates', () => {
  const { ctx, els, timers, fire, time } = cameraScreen();
  ctx.run('CV.interacting=true;CV.userAt=10000');
  ctx.cvNewContent(new Set(['b']), false);
  ctx.cvNewContent(new Set(['b']), false);
  assert.equal(timers.size, 1, 'one pending timer handles repeated data updates');
  fire();
  assert.equal(ctx.cvHeaderVisible(els[1]), false, 'a held drag must not be interrupted');
  time(10500); ctx.run('CV.interacting=false;CV.userAt=10500');
  fire();
  assert.equal(ctx.cvHeaderVisible(els[1]), false, 'recent pointer/scroll activity keeps its view for 1.2 seconds');
  fire();
  assert.equal(ctx.cvHeaderVisible(els[1]), true);
});

test('restored empty views recover while an existing useful view is retained and stale pending cards are ignored', () => {
  const { ctx, els, timers, fire } = cameraScreen();
  const useful = plain(ctx.run('CV.view'));
  ctx.cvNewContent(new Set(['a', 'b']), true);
  assert.equal(timers.size, 0, 'a restored view with a visible task is not replaced');
  assert.deepEqual(plain(ctx.run('CV.view')), useful);
  ctx.run('CV.view={x:-5000,y:-5000,z:2.5}');
  ctx.cvNewContent(new Set(), true);
  fire();
  assert.equal(els.some(ctx.cvHeaderVisible), true, 'a blank restored camera recovers without tidy');
  ctx.run('CV.view={x:100,y:100,z:1}');
  ctx.cvNewContent(new Set(['b']), false);
  ctx.run("CV.nodes.delete('b')");
  fire();
  assert.deepEqual(plain(ctx.run('CV.view')), useful, 'a task removed before the deferred fit does not move the camera');
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
