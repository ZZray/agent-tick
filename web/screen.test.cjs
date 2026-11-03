const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(process.env.TICK_HTML || path.join(__dirname, 'index.html'), 'utf8');
const source = html.slice(html.indexOf('// 会话树与归档：'), html.indexOf('// 共享归档结束。')) + html.slice(html.indexOf('const CV_GAP'), html.indexOf('// 面板设置留在侧栏 morph 之外'));
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
    childrenOf: s => sessions.filter(c => c.parentId === s.id && !c.archived), SCREEN: true,
    isWaiting: s => ['waiting', 'blocked'].includes(s.status),
    waitingForUser: s => s.status === 'waiting',
    firstLine: s => String(s || '').split('\n')[0],
    progress: s => ({done:(s.items || []).filter(it=>it.status==='done'||it.status==='skipped').length,total:(s.items || []).length}),
    STATUS: {running:'进行中',planning:'写清单',waiting:'等你',blocked:'等依赖',paused:'待续',done:'完成'},
    isEnded: s => ['done', 'failed', 'cancelled'].includes(s.status),
    isDormant: s => s.status === 'running' && s.updatedAt < NOW - 30 * MIN,
    lastSign: s => s.updatedAt,
    byOrder: (a, b) => b.createdAt - a.createdAt,
    sessionRow: s => `<div class="s" data-sid="${s.id}">${s.title}</div>`, sweeper: () => () => false, short: () => '1m',
    itemsToday: () => 3, S: { conn: 'live', sessions: byId, open: new Set() },
    root: { querySelector: () => null }, RM: { matches: true }, EASE_OUT: 'ease-out', EN: false, Date,
    morph: (_, markup) => { ctx.markup = markup; return []; }, settle: () => {}, schedule: () => {},
    esc: s => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    IC: { logo: '' }, scrTimeOverride: null,
    setTimeout: () => 0, clearTimeout: () => {},
  };
  vm.createContext(ctx);
  vm.runInContext(source, ctx);
  vm.runInContext('CV.overview=false', ctx);
  ctx.run = code => vm.runInContext(code, ctx);
  ctx.flowing = node => { ctx.flowNode = node; return ctx.run('cvFlowing(flowNode)'); };
  return ctx;
}
const plain = v => JSON.parse(JSON.stringify(v)); // vm 里造出来的对象原型不同，比较前先转成本地对象
const overlap = (a, b) => a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
const apart = (a, b) => a.x + a.w + 24 <= b.x || b.x + b.w + 24 <= a.x || a.y + a.h + 24 <= b.y || b.y + b.h + 24 <= a.y; // 至少隔一个间距

function screenEvents(ctx) {
  const listeners = new Map(), intervals = [];
  ctx.document.documentElement = { dataset: {}, classList: { add() {}, remove() {} }, setPointerCapture() {} };
  ctx.document.addEventListener = (name, fn) => { const callbacks = listeners.get(name) || []; callbacks.push(fn); listeners.set(name, callbacks); };
  ctx.addEventListener = () => {};
  ctx.setInterval = (fn, ms) => { intervals.push({ fn, ms }); };
  ctx.requestAnimationFrame = fn => fn();
  ctx.window = { tickScreenClose: () => { ctx.closed = true; } };
  ctx.cvApply = () => {};
  ctx.startScreen();
  return { intervals, fire(name, extra = {}) { const event = { target: { closest: () => null }, preventDefault() {}, stopPropagation() {}, detail: 0, ...extra }; for (const fn of listeners.get(name) || []) fn(event); return event; } };
}

test('canvas HUD keeps common controls below the canvas and moves occasional actions to an upward menu', () => {
  const task = { id: 'a', title: '<Task> ' + 'long title '.repeat(30), status: 'running', updatedAt: NOW, items: [{ id: 'i', text: 'now', status: 'doing' }] };
  const ctx = screen([task]); ctx.renderScreen();
  const header = ctx.markup.match(/<header class="scr-h">([\s\S]*?)<\/header>/)[1];
  assert.doesNotMatch(header, /scr-title/, 'the expanded rail carries the selected title, so the header keeps only global numbers');
  assert.match(ctx.markup, /cv-rail-task[^>]*aria-pressed="true"[^>]*data-tip="&lt;Task>/);
  assert.match(header, /<b class="num">0%<\/b>总进度/);
  assert.match(header, /scr-kpi zero[^>]*data-tip="没有要你处理的"><b class="num">0<\/b>等你/, 'zero waits keep the same number rhythm and explain themselves in the tip');
  assert.match(header, /scr-clk[\s\S]*data-scr-clk[\s\S]*data-scr-date/, 'time and date stay in the top-right header');
  assert.match(ctx.markup, /<aside class="scr-side">[\s\S]*<\/aside>\s*<main class="scr-main">\s*<header class="scr-h">[\s\S]*<div class="cv-stage">/, 'inverted-L shell: side column, then an inset panel whose header sits above the canvas stage');
  assert.doesNotMatch(header, /cv-tools|data-cv="(out|in|arrange|overview|direction)"/);
  assert.match(ctx.markup, /class="cv-hud"/);
  for (const action of ['out', 'reset-zoom', 'in', 'fit', 'follow', 'minimap', 'more']) assert.match(ctx.markup, new RegExp(`data-cv="${action}"`));
  assert.doesNotMatch(ctx.markup, /class="cv-more"/);
  ctx.run('CV.more=true'); ctx.renderScreen();
  assert.match(ctx.markup, /class="cv-more"/);
  for (const action of ['overview', 'direction', 'arrange']) assert.match(ctx.markup, new RegExp(`data-cv="${action}"`));
});

test('mini-map projects the readable viewport through the exact camera inverse and includes off-screen nodes', () => {
  const ctx = screen(), view = { x: 350, y: -70, z: 1.25 }, area = { x: 116, y: 96, w: 1250, h: 700 };
  const node = { x: -4000, y: 2800, w: 380, h: 220 };
  const { viewport, bounds } = plain(ctx.cvMapGeometry([node], view, area));
  assert.deepEqual(viewport, { x: (116 - 350) / 1.25, y: (96 + 70) / 1.25, w: 1000, h: 560 });
  for (const r of [node, viewport]) assert.ok(r.x >= bounds.x + 40 && r.y >= bounds.y + 40 && r.x + r.w <= bounds.x + bounds.w - 40 && r.y + r.h <= bounds.y + bounds.h - 40);
  for (const b of [{ x: -10, y: 30, w: 1000, h: 100 }, { x: -10, y: 30, w: 100, h: 1000 }]) {
    const rect = { left: 70, top: 80, width: 200, height: 120 }, scale = Math.min(200 / b.w, 120 / b.h);
    const point = { x: b.x + b.w * .3, y: b.y + b.h * .65 };
    const clientX = rect.left + (rect.width - b.w * scale) / 2 + (point.x - b.x) * scale;
    const clientY = rect.top + (rect.height - b.h * scale) / 2 + (point.y - b.y) * scale;
    const mapped = plain(ctx.cvMapPoint(clientX, clientY, rect, b));
    assert.ok(Math.abs(mapped.x - point.x) < 1e-9 && Math.abs(mapped.y - point.y) < 1e-9, 'both horizontal and vertical SVG letterboxing are removed');
  }
});

test('safe viewport avoids the HUD and details while narrow layouts keep a scrollable toolbar available', () => {
  const ctx = screen();
  const flags = new Set(), hud = { offsetHeight: 170, offsetWidth: 282, style: {}, toggleAttribute: (name, on) => on ? flags.add(name) : flags.delete(name) };
  const detail = { offsetWidth: 470, style: {} }, stage = { getBoundingClientRect: () => ({ left: 240, top: 66, width: 1672, height: 1126 }) };
  ctx.document.querySelector = s => ({ '.cv-hud': hud, '.cv-detail': detail, '.cv-stage': stage })[s] || null;
  ctx.document.querySelectorAll = () => [detail]; ctx.cvHudLayout();
  assert.equal(hud.style.right, '494px', 'the toolbar sits inside the canvas panel, left of the details pane');
  assert.deepEqual(plain(ctx.cvViewport()), { x: 256, y: 82, w: 1672 - 16 - 498, h: 1126 - 16 - 198 }, 'the readable area is the canvas panel minus details and toolbar');
  assert.deepEqual([ctx.run('CV.sx'), ctx.run('CV.sy')], [240, 66], 'the world layer is offset by the panel origin');
  ctx.run('CV.view={x:300,y:200,z:1}'); assert.equal(ctx.run('cvTransform()'), 'translate(60px,134px) scale(1)', 'CV.view stays in window coordinates');
  const narrow = w => { stage.getBoundingClientRect = () => ({ left: 60, top: 66, width: w, height: 600 }); ctx.cvHudLayout(); };
  narrow(684); assert.equal(hud.style.maxWidth, '178px'); assert.equal(flags.has('data-compact'), true);
  narrow(560); assert.equal(hud.style.maxWidth, '72px'); assert.equal(flags.has('data-obscured'), false);
  assert.match(html, /\.cv-tool-row\{[^}]*overflow-x:auto/);
  assert.doesNotMatch(html, /\.cv-hud\[data-obscured\]/);
});

test('task rail collapses to rings, remembers the choice, and [ ] cycle roots without moving nodes', () => {
  const tasks = ['a', 'b', 'c'].map((id, i) => ({ id, status: 'running', title: `${id} root`, updatedAt: NOW, createdAt: i, items: [{ id: 'x', text: `${id} now`, status: 'doing' }] }));
  const ctx = screen(tasks), events = screenEvents(ctx); ctx.run("CV.focusSid='a'"); ctx.renderScreen();
  assert.doesNotMatch(ctx.markup, /class="cv-rail" data-compact/);
  assert.match(ctx.markup, /<span class="cv-rail-meta"><span>a now<\/span><\/span>/, 'expanded rows show the current step in grey without repeating the running status');
  events.fire('click', { target: { closest: s => s === '[data-cv]' ? { dataset: { cv: 'rail' } } : null } });
  assert.equal(ctx.run('CV.railCompact'), true); ctx.renderScreen();
  assert.match(ctx.markup, /class="cv-rail" data-compact/);
  assert.match(ctx.markup.match(/<header class="scr-h">([\s\S]*?)<\/header>/)[1], /scr-title[^>]*data-tip="a root"/, 'a collapsed rail moves the selected title into the header');
  let saved; ctx.store.set = (_, v) => { saved = v; };
  const timer = ctx.setTimeout; ctx.setTimeout = fn => { fn(); return 0; }; ctx.cvSave(); ctx.setTimeout = timer;
  assert.equal(saved.railCompact, true);
  ctx.store.get = () => ({ railCompact: true }); ctx.cvLoad(); assert.equal(ctx.run('CV.railCompact'), true);
  ctx.store.get = () => ({}); ctx.cvLoad(); assert.equal(ctx.run('CV.railCompact'), false);
  ctx.innerWidth = 900; ctx.renderScreen();
  assert.match(ctx.markup, /class="cv-rail" data-compact/);
  assert.match(ctx.markup, /data-cv="rail"/, 'narrow windows retain a touch-accessible temporary expansion button');
  ctx.innerWidth = 1920; ctx.renderScreen();
  ctx.run(`CV.pos=new Map([['a',{x:0,y:0}],['b',{x:0,y:400}],['c',{x:0,y:800}]])`);
  const positions = plain(ctx.run('[...CV.pos]'));
  ctx.document.querySelectorAll = s => s === '.cv-rail-task' ? ['a', 'b', 'c'].map(id => ({ dataset: { ownerId: id } })) : [];
  ctx.document.querySelector = s => s === '.cv-rail-task[aria-pressed="true"]' ? { dataset: { ownerId: ctx.run('CV.focusSid') } } : null;
  ctx.run("CV.focusSid='a'");
  const order = [']', ']', ']', '['].map(key => { events.fire('keydown', { key }); return ctx.run('CV.focusSid'); });
  assert.deepEqual(order, ['b', 'c', 'a', 'c']);
  assert.deepEqual(plain(ctx.run('[...CV.pos]')), positions);
});

test('temporary rail expansion preserves the camera and preference, supports keyboard and narrow touch', () => {
  const ctx = screen(), events = screenEvents(ctx);
  const attributes = new Set(), side = { toggleAttribute: (key, on) => on ? attributes.add(key) : attributes.delete(key), matches: () => false };
  const button = { setAttribute() {}, focus() {} };
  const target = { closest: selector => selector === '.scr-side' ? side : null, matches: selector => selector === ':focus-visible' };
  ctx.document.querySelector = selector => selector === '.scr-side' ? side : selector === '[data-cv="rail"]' ? button : null;
  ctx.run('CV.railCompact=true; CV.view={x:211,y:137,z:1.2}');
  const before = plain(ctx.run('CV.view'));
  let saved = 0; ctx.store.set = () => { saved++; };
  events.fire('pointerover', { target, relatedTarget: null, pointerType: 'mouse' });
  assert.equal(ctx.run('CV.railPeek'), true); assert.ok(attributes.has('data-peek'));
  assert.equal(ctx.run('CV.railCompact'), true); assert.equal(saved, 0); assert.deepEqual(plain(ctx.run('CV.view')), before);
  events.fire('pointerout', { target, relatedTarget: null });
  assert.equal(ctx.run('CV.railPeek'), false);
  events.fire('focusin', { target }); assert.equal(ctx.run('CV.railPeek'), true);
  events.fire('keydown', { key: 'Escape', target });
  assert.equal(ctx.run('CV.railPeek'), false); assert.equal(ctx.closed, undefined, 'Escape closes the floating rail before the screen');
  events.fire('focusin', { target }); assert.equal(ctx.run('CV.railPeek'), false, 'restoring toggle focus after Escape does not reopen it');
  events.fire('focusout', { target, relatedTarget: null });
  events.fire('focusin', { target }); assert.equal(ctx.run('CV.railPeek'), true);
  events.fire('focusout', { target, relatedTarget: null });
  ctx.innerWidth = 900;
  const clickTarget = { closest: selector => selector === '[data-cv]' ? { dataset: { cv: 'rail' } } : null };
  events.fire('click', { target: clickTarget }); assert.equal(ctx.run('CV.railPeek'), true);
  events.fire('click', { target: clickTarget }); assert.equal(ctx.run('CV.railPeek'), false);
  assert.equal(ctx.run('CV.railCompact'), true); assert.equal(saved, 0); assert.deepEqual(plain(ctx.run('CV.view')), before);
  assert.match(html, /\.scr-side\[data-compact\]\[data-peek\] \.scr-side-inner\{width:/, 'only the inner floating surface grows');
  assert.doesNotMatch(html, /\.scr-side\[data-compact\]\[data-peek\]\{[^}]*width:/, 'the column that owns the canvas geometry stays fixed');
});

test('ancestor and child module cards describe the actual current descendant step', () => {
  const owner = { id: 'p', title: 'API验收', status: 'running', updatedAt: NOW, items: [
    { id: 'old', group: '验证', text: '环境检查', status: 'done' },
    { id: 'current', group: '验证 / 二维处理', text: '调用FFT并核对输出维度', status: 'doing' },
    { id: 'future', group: '验证 / 谱图分析', text: '未来分析', status: 'todo' },
  ] };
  const ctx = screen([owner]); ctx.renderScreen();
  const modules = ctx.cvFlowNodes().filter(node => node.ownerId);
  assert.deepEqual(plain(modules.map(node => node.modulePath)), ['验证', '验证 / 二维处理']);
  for (const node of modules) {
    const card = ctx.cvModuleCard(node);
    assert.match(card, /step-dot[^>]*aria-hidden="true">●<\/span><span class="step-text">调用FFT并核对输出维度/);
    assert.doesNotMatch(card, /当前步骤/);
    assert.match(card, /验证 \/ 二维处理 · 调用FFT并核对输出维度/);
  }
  assert.equal(modules[0].done, 1); assert.equal(modules[0].total, 3, 'parent counts are still derived once from real items');
});

test('menu dismissal, mini-map preference and 100-percent reset preserve node coordinates and camera contracts', () => {
  const ctx = screen(), events = screenEvents(ctx);
  ctx.run("CV.view={x:200,y:150,z:1.6};CV.pos.set('hand',{x:90,y:120})");
  const click = action => events.fire('click', { target: { closest: selector => selector === '[data-cv]' ? { dataset: { cv: action } } : selector === '.cv-tools' ? {} : null } });
  click('more'); assert.equal(ctx.run('CV.more'), true);
  events.fire('keydown', { key: 'Escape' }); assert.equal(ctx.run('CV.more'), false); assert.equal(ctx.closed, undefined);
  click('more'); events.fire('click'); assert.equal(ctx.run('CV.more'), false);
  const view = plain(ctx.run('CV.view')), positions = plain(ctx.run('[...CV.pos]'));
  click('minimap'); assert.equal(ctx.run('CV.minimap'), false); assert.deepEqual(plain(ctx.run('CV.view')), view);
  const a = ctx.cvViewport(), center = { x: a.x + a.w / 2, y: a.y + a.h / 2 };
  click('reset-zoom'); const reset = plain(ctx.run('CV.view')); assert.equal(reset.z, 1);
  assert.equal((center.x - reset.x) / reset.z, (center.x - view.x) / view.z);
  assert.equal((center.y - reset.y) / reset.z, (center.y - view.y) / view.z);
  assert.deepEqual(plain(ctx.run('[...CV.pos]')), positions); assert.equal(ctx.run('CV.follow'), true); assert.equal(ctx.run('cvFollowing()'), false, 'manual zoom pauses following without turning the switch off');
  ctx.store.get = () => ({ minimap: false }); ctx.cvLoad(); assert.equal(ctx.run('CV.minimap'), false);
  ctx.store.get = () => ({}); ctx.cvLoad(); assert.equal(ctx.run('CV.minimap'), true);
});

test('mini-map click, drag and arrow navigation change only the camera at its existing zoom', () => {
  const ctx = screen(), events = screenEvents(ctx), attrs = new Set();
  const rect = { left: 40, top: 60, width: 200, height: 120 };
  const map = { querySelector: () => ({ getBoundingClientRect: () => rect }), setAttribute: n => attrs.add(n), removeAttribute: n => attrs.delete(n), focus() {} };
  ctx.document.querySelector = s => s === '.cv-minimap' ? map : null;
  ctx.cvMapUpdate = () => {};
  ctx.run("CV.view={x:200,y:100,z:1.5};CV.mapBounds={x:-100,y:50,w:1000,h:100};CV.pos.set('a',{x:400,y:500})");
  const pos = plain(ctx.run('[...CV.pos]')), area = ctx.cvViewport();
  const target = { closest: selector => selector === '.cv-minimap' ? map : null };
  events.fire('pointerdown', { target, button: 0, pointerId: 4, clientX: 140, clientY: 120 });
  const first = plain(ctx.run('CV.view'));
  assert.equal(first.z, 1.5); assert.equal((area.x + area.w / 2 - first.x) / first.z, 400); assert.equal((area.y + area.h / 2 - first.y) / first.z, 100);
  events.fire('pointermove', { pointerId: 4, clientX: 160, clientY: 120 });
  assert.equal(ctx.run('CV.view.x'), first.x - 150, '20 map pixels at .2 scale move the viewport 100 world units');
  assert.deepEqual(plain(ctx.run('CV.mapDrag.bounds')), { x: -100, y: 50, w: 1000, h: 100 });
  events.fire('pointerup'); assert.equal(ctx.run('CV.mapDrag'), null); assert.equal(ctx.run('CV.interacting'), false);
  events.fire('pointerdown', { target, button: 0, pointerId: 5, clientX: 140, clientY: 120 });
  events.fire('keydown', { key: 'Escape' });
  assert.equal(ctx.run('CV.mapDrag'), null); assert.equal(ctx.run('CV.interacting'), false); assert.equal(ctx.closed, undefined, 'Escape ends map dragging before closing the screen');
  const before = ctx.run('CV.view.x'); events.fire('keydown', { target, key: 'ArrowRight' });
  assert.equal(ctx.run('CV.view.x'), before - 96); assert.equal(ctx.run('CV.view.z'), 1.5); assert.equal(ctx.run('CV.follow'), true); assert.ok(ctx.run('CV.pauseUntil') > Date.now(), 'map navigation pauses following');
  assert.deepEqual(plain(ctx.run('[...CV.pos]')), pos);
});

test('screen clock updates the date after midnight without session events or page rendering', () => {
  const ctx = screen(), { intervals } = screenEvents(ctx), time = { textContent: '23:59' }, date = { textContent: 'old day' };
  let renders = 0; ctx.schedule = () => renders++;
  ctx.Date = class extends Date { toTimeString() { return '00:00'; } toLocaleDateString() { return 'new day'; } };
  ctx.document.querySelectorAll = s => s === '[data-scr-clk]' ? [time] : s === '[data-scr-date]' ? [date] : [];
  intervals.find(i => i.ms === 1000).fn();
  assert.equal(time.textContent, '00:00'); assert.equal(date.textContent, 'new day'); assert.equal(renders, 0);
});

test('camera actions after closing More measure the restored map height before their single camera move', () => {
  const ctx = screen(), events = screenEvents(ctx), measured = [];
  let menuVisible = false;
  const hud = { get offsetHeight() { return menuVisible ? 42 : 170; }, style: {}, toggleAttribute() {} };
  const menu = { remove: () => { menuVisible = false; } };
  ctx.document.querySelector = s => s === '.cv-hud' ? hud : s === '.cv-more' && menuVisible ? menu : null;
  ctx.cvMapUpdate = () => {};
  ctx.cvFitView = () => measured.push(ctx.cvViewport().h);
  ctx.cvFocusRoot = () => ctx.cvFitView();
  ctx.cvArrange = () => ctx.cvFitView();
  ctx.cvApply = () => measured.push(ctx.cvViewport().h);
  for (const action of ['overview', 'direction', 'arrange', 'root', 'fit', 'in', 'out', 'reset-zoom']) {
    ctx.run('CV.more=true;CV.view={x:50,y:60,z:1}'); menuVisible = true; measured.length = 0;
    events.fire('click', { target: { closest: selector => selector === '[data-cv]' ? { dataset: { cv: action, ownerId: 'a' } } : selector === '.cv-tools' && action !== 'root' ? {} : null } });
    assert.equal(menuVisible, false, action);
    assert.deepEqual(measured, [1200 - 16 - 198], `${action} reads the full map+toolbar height once, not the shorter open-menu HUD`);
  }
  for (const key of ['f', '0', '+', '-']) {
    ctx.run('CV.more=true;CV.view={x:50,y:60,z:1}'); menuVisible = true; measured.length = 0;
    events.fire('keydown', { key });
    assert.deepEqual(measured, [986], key);
  }
});

test('mini-map camera updates retain node DOM geometry until positions, sizes or selected root change', () => {
  const ctx = screen(), attributes = new Map(), frameAttrs = new Map(); let writes = 0;
  const group = { getAttribute: key => attributes.get(key), setAttribute: (key, value) => attributes.set(key, value), set innerHTML(value) { writes++; } };
  const svg = { setAttribute() {} }, frame = { setAttribute: (key, value) => frameAttrs.set(key, value) };
  const map = { querySelector: s => s === 'svg' ? svg : s === '.cv-map-nodes' ? group : frame };
  ctx.document.querySelector = s => s === '.cv-minimap' ? map : null;
  ctx.document.querySelectorAll = () => [{ dataset: { cvid: 'a' }, offsetWidth: 380, offsetHeight: 200 }];
  ctx.run("CV.nodes.set('a',{id:'a'});CV.pos.set('a',{x:20,y:40});CV.focusSid='a';CV.view={x:50,y:60,z:1}");
  ctx.cvMapUpdate(); assert.equal(writes, 1); const x = frameAttrs.get('x');
  ctx.run('CV.view.x+=100'); ctx.cvMapUpdate(); assert.equal(writes, 1); assert.equal(frameAttrs.get('x'), x - 100);
  ctx.run("CV.focusSid='other'"); ctx.cvMapUpdate(); assert.equal(writes, 2);
  ctx.run("CV.pos.set('a',{x:60,y:40})"); ctx.cvMapUpdate(); assert.equal(writes, 3);
});

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
  assert.equal(v.z, 1.25, 'auto-fit stops at 125% so cards stay in proportion with the shell');
  assert.equal(v.x, (1920 - 380 * 1.25) / 2);
  assert.equal(Math.round(v.y), Math.round(56 + (1200 - 56 - 200 * 1.25) / 2));
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

test('waiting cards come first and completed tasks leave the canvas immediately', () => {
  const ctx = screen([
    { id: 'run', status: 'running', createdAt: 2, updatedAt: NOW - MIN, title: 'run' },
    { id: 'wait', status: 'waiting', createdAt: 1, waitSince: NOW - MIN, updatedAt: NOW, title: 'wait' },
    { id: 'old', status: 'done', createdAt: 0, endedAt: NOW - 40 * MIN, updatedAt: 0, title: 'old' },
    { id: 'fresh', status: 'done', createdAt: 3, endedAt: NOW - 5 * MIN, updatedAt: 0, title: 'fresh' },
    { id: 'gone', status: 'running', createdAt: 4, updatedAt: NOW, archived: true, title: 'gone' },
  ]);
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['wait', 'run']);
});

test('shared canvas keeps every root and only current real child branches at 219-step 11-group scale', () => {
  const items = Array.from({ length: 219 }, (_, i) => ({ id: `i${i}`, group: `API / 模块${Math.floor(i / 20)}`, text: `API ${i}`, status: i < 101 ? 'done' : i === 101 ? 'doing' : 'todo' }));
  const owner = { id: 'p', title: 'API root', status: 'running', updatedAt: NOW, createdAt: 1, items };
  const children = Array.from({ length: 11 }, (_, i) => ({ id: `c${i}`, parentId: 'p', title: `child${i}`, status: 'blocked', updatedAt: NOW, createdAt: i + 2, items: [{ id: 'step', text: 'dependency work', status: 'blocked' }] }));
  children[7].status = 'running'; children[7].items[0].status = 'doing';
  const other = { id: 'q', title: 'other root', status: 'running', updatedAt: NOW, createdAt: 20, items: [] };
  const ctx = screen([owner, ...children, other]); ctx.run('CV.overview=false');
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['q', 'c7', 'p']);
  assert.equal(ctx.run('CV.focusSid'), 'p');
  const nodes = ctx.cvFlowNodes();
  assert.ok(nodes.length <= 5, 'each root retains its bounded current agent/module projection');
  assert.deepEqual(plain(nodes.filter(n => n.ownerId === 'p').map(n => n.modulePath)), ['API', 'API / 模块5']);
  ctx.renderScreen();
  assert.match(ctx.markup, /另有 10 个子任务/);
  assert.match(ctx.markup, /data-tip="画布上主任务清单的合计：101\/219"><b class="num">46%<\/b>总进度/);
  assert.match(ctx.markup, />10<\/b>等依赖/);
  assert.doesNotMatch(ctx.markup, /项等你/);
  const fresh = { id: 'new', title: 'new root', status: 'running', createdAt: 99, updatedAt: NOW, items: [] };
  ctx.S.sessions.set(fresh.id, fresh);
  ctx.visible = () => [...ctx.S.sessions.values()].filter(s => !s.archived);
  ctx.cvCards(); assert.equal(ctx.run('CV.focusSid'), 'p', 'new roots do not steal current selection');
  const before = plain(ctx.cvCards().map(s => s.id));
  ctx.run("CV.focusSid='q'");
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), before, 'selecting another root only changes the camera target');
});

test('root navigation and overview preserve every projected ID and world coordinate while changing only the camera', () => {
  const tasks = ['a','b','c'].map((id,i)=>({id,status:'running',title:`${id} ${'long title '.repeat(20)}`,updatedAt:NOW,createdAt:i,items:[{id:'current',group:'API / 当前模块',text:'current API',status:'doing'}]}));
  const ctx=screen(tasks);ctx.renderScreen();
  const ids=plain([...ctx.run('CV.nodes.keys()')]);
  const elements=ids.map((id,i)=>({dataset:{cvid:id},offsetWidth:320,offsetHeight:120,style:{},id}));
  ctx.document.querySelectorAll=()=>elements;
  const stage={getBoundingClientRect:()=>({left:240,top:66,width:1672,height:1126})},detail={offsetWidth:420};
  ctx.document.querySelector=s=>s==='.cv-stage'?stage:s==='.cv-detail'?detail:null;
  ctx.cvApply=()=>{};ctx.run(`CV.pos=new Map(${JSON.stringify(ids.map((id,i)=>[id,{x:(i%3)*420,y:Math.floor(i/3)*600}]))})`);
  const coordinates=plain(ctx.run('[...CV.pos]'));
  ctx.run("CV.pendingReveal.add('a');CV.needsFocus=true;CV.needsArrange=true");
  const beforeView=plain(ctx.run('CV.view'));
  ctx.cvFocusRoot('b',false);ctx.renderScreen();
  assert.equal(ctx.run('CV.pendingReveal.size'),0);assert.equal(ctx.run('CV.needsArrange'),false);
  assert.equal(ctx.run('CV.needsFocus'),false,'root selection does not turn into a topology-driven rearrange');
  assert.deepEqual(plain([...ctx.run('CV.nodes.keys()')]),ids);
  assert.deepEqual(plain(ctx.run('[...CV.pos]')),coordinates);
  assert.notDeepEqual(plain(ctx.run('CV.view')),beforeView);
  assert.ok(ctx.run('CV.view.z')>=1,'current work stays readable with a rail and a details pane');
  ctx.run('CV.overview=true');ctx.cvFitView(false);ctx.renderScreen();
  assert.deepEqual(plain([...ctx.run('CV.nodes.keys()')]),ids,'overview changes camera input, not the projection');
  assert.deepEqual(plain(ctx.run('[...CV.pos]')),coordinates);
  ctx.cvFocusRoot('a',false);ctx.renderScreen();
  assert.deepEqual(plain([...ctx.run('CV.nodes.keys()')]),ids);
  assert.deepEqual(plain(ctx.run('[...CV.pos]')),coordinates);
  assert.match(ctx.markup, /class="cv-rail" aria-label="任务进度导航"/);
  assert.doesNotMatch(ctx.markup,/cv-root-switch/);
  assert.match(ctx.markup,/data-tip="a long title/,'long root labels keep a complete hover title');
  assert.deepEqual(plain(ctx.cvViewport()),{x:256,y:82,w:1672-16-448,h:1126-32});
});

test('with following switched off, non-selected root state or module updates do not schedule camera focus or change selection', () => {
  const tasks=['a','b'].map((id,i)=>({id,status:'running',createdAt:i,updatedAt:NOW,title:id,items:[]}));
  const ctx=screen(tasks);ctx.run('CV.follow=false');ctx.renderScreen();ctx.run("CV.focusSid='a';CV.focusTopology=null");ctx.renderScreen();
  ctx.run('CV.needsFocus=false;CV.needsArrange=false;CV.pendingReveal.clear()');
  tasks[1].status='waiting';tasks[1].statusNote='new background question';ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'a');assert.equal(ctx.run('CV.needsFocus'),false);assert.equal(ctx.run('CV.needsArrange'),false);
  tasks[1].items=[{id:'new',group:'后台 / 新模块',text:'background API',status:'doing'}];ctx.renderScreen();
  assert.equal(ctx.run('CV.needsFocus'),false);
});

test('auto-follow jumps to a new waiting notice first, follows updates in the selected root at once, and waits out the dwell before switching roots', () => {
  const tasks=['a','b','c'].map((id,i)=>({id,status:'running',createdAt:i,updatedAt:NOW+i,title:id,items:[{id:'x',text:`${id} step`,status:'todo'}]}));
  const ctx=screen(tasks);let clock=50000;ctx.Date=class extends Date{static now(){return clock;}};
  ctx.run("CV.focusSid='a'");ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'a','the first render records signatures without treating existing work as an update');
  tasks[1].status='waiting';tasks[1].waitSince=NOW;ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'b','a new waiting notice wins');assert.equal(ctx.run('CV.needsFocus'),true);
  assert.equal(ctx.run('CV.followAt'),50000);
  // Use real geometry to initiate the requested camera; scheduling alone is not a viewed wait.
  ctx.document.querySelectorAll = selector => selector === '.cv-world>.cv-card' ? tasks.map(s => ({dataset:{cvid:s.id},offsetWidth:380,offsetHeight:180})) : [];
  ctx.run("CV.pos=new Map([['a',{x:0,y:0}],['b',{x:450,y:0}],['c',{x:900,y:0}]])");
  ctx.cvFitView(false);

  ctx.run('CV.needsFocus=false');tasks[1].statusNote='reworded question';ctx.renderScreen();
  assert.equal(ctx.run('CV.needsFocus'),false,'rewording an already seen question does not move the camera');
  clock+=1000;tasks[2].items[0].status='doing';ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'b','another root waits until the current target has been shown for 8 seconds');
  assert.equal(ctx.run('CV.followQueued'),'c');
  clock+=8000;ctx.cvFollowGo([ctx.run('CV.followQueued')]);
  assert.equal(ctx.run('CV.focusSid'),'c');assert.equal(ctx.run('CV.followQueued'),'');
  ctx.run('CV.needsFocus=false');tasks[2].items[0].status='done';ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'c');assert.equal(ctx.run('CV.needsFocus'),true,'progress inside the selected root refocuses immediately');
  ctx.run('CV.needsFocus=false');ctx.cvPauseFollow();tasks[0].items[0].status='doing';ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'c','a manual pause keeps the camera where the user put it');assert.equal(ctx.run('CV.lastActive'),'a');
  clock+=15100;ctx.cvResumeFollow();
  assert.equal(ctx.run('CV.focusSid'),'a','resuming after the idle period goes to the latest activity');assert.equal(ctx.run('CV.pauseUntil'),0);
  assert.equal(plain(ctx.cvFollowState()).state,'on');
  ctx.run('CV.follow=false');assert.equal(plain(ctx.cvFollowState()).state,'off');
});

test('waiting backlog is consumed only by a real camera move and respects dwell across repeated renders', () => {
  const tasks = [
    {id:'older',title:'older question',status:'waiting',waitSince:100,updatedAt:NOW,items:[]},
    {id:'newer',title:'newer question',status:'waiting',waitSince:200,updatedAt:NOW,items:[]},
    {id:'live',title:'working',status:'running',updatedAt:NOW,items:[{id:'i',text:'step',status:'todo'}]},
  ];
  const ctx=screen(tasks);let clock=50000;ctx.Date=class extends Date{static now(){return clock;}};
  ctx.renderScreen();ctx.renderScreen();ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'newer');
  assert.equal(ctx.run('CV.seenWaits.size'),0,'three renders do not pretend a pending camera reached a question');
  ctx.cvFitView(false);assert.equal(ctx.run('CV.seenWaits.size'),0,'missing DOM geometry cannot consume the question');
  ctx.document.querySelectorAll=selector=>selector==='.cv-world>.cv-card'?tasks.map(s=>({dataset:{cvid:s.id},offsetWidth:380,offsetHeight:180})):[];
  ctx.run("CV.pos=new Map([['older',{x:0,y:0}],['newer',{x:450,y:0}],['live',{x:900,y:0}]])");
  ctx.cvFitView(false);
  assert.equal(ctx.run("CV.seenWaits.has('newer:200')"),true);assert.equal(ctx.run('CV.pendingWait'),null);
  ctx.renderScreen();clock+=7999;ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'newer');assert.equal(ctx.run('CV.followQueued'),'older');
  clock+=2;ctx.cvFollowGo([ctx.run('CV.followQueued')]);
  assert.equal(ctx.run('CV.focusSid'),'older');assert.equal(ctx.run("CV.seenWaits.has('older:100')"),false);
  // A genuinely newer question can preempt the old backlog before its camera executes.
  tasks[2].status='waiting';tasks[2].waitSince=300;ctx.renderScreen();
  assert.equal(ctx.run('CV.focusSid'),'live');assert.equal(ctx.run("CV.seenWaits.has('older:100')"),false);
  ctx.cvFitView(false);assert.equal(ctx.run("CV.seenWaits.has('live:300')"),true);
  clock+=8001;ctx.renderScreen();assert.equal(ctx.run('CV.pendingWait.id'),'older');
  tasks[0].status='done';tasks[0].endedAt=NOW;ctx.renderScreen();
  assert.equal(ctx.run('CV.pendingWait'),null,'resolved pending questions cannot block normal follow');
});

function layoutWorld(ctx, heights, positions, selected='a', migrating=false) {
  const els=Object.entries(heights).map(([id,h])=>({dataset:{cvid:id},offsetWidth:380,offsetHeight:h,style:{}}));
  const world={querySelectorAll:()=>els};
  ctx.document.querySelector=s=>s==='.cv-world'?world:null;
  ctx.document.querySelectorAll=()=>els;
  ctx.cvWatch=ctx.cvEdges=()=>{};
  let fits=0;ctx.cvFitView=()=>{fits++;};
  ctx.setupPositions=positions;ctx.setupHeights=heights;ctx.selected=selected;ctx.migrating=migrating;
  ctx.run("CV.nodes=new Map([...S.sessions]);CV.pos=new Map(Object.entries(setupPositions));CV.sizes=new Map(Object.keys(setupHeights).map(id=>[id,{w:380,h:120}]));CV.focusSid=selected;CV.migrateLayout=migrating;CV.follow=false;CV.view={x:99,y:77,z:1.1}");
  return {els,fits:()=>fits};
}

test('same-ID height growth locally avoids collisions and preserves selected-root position and manual camera', () => {
  const tasks=['a','b','c'].map(id=>({id,title:id,status:'running',updatedAt:NOW,items:[]}));
  const ctx=screen(tasks);const initial={a:{x:0,y:0},b:{x:0,y:168},c:{x:0,y:1000}};
  const {fits}=layoutWorld(ctx,{a:204,b:120,c:120},initial,'b');ctx.cvLayout();
  assert.deepEqual(plain(ctx.run("CV.pos.get('b')")),initial.b,'background growth moves the background tree, not the selected root');
  assert.deepEqual(plain(ctx.run("CV.pos.get('c')")),initial.c,'an unrelated root is not globally rearranged');
  assert.notDeepEqual(plain(ctx.run("CV.pos.get('a')")),initial.a);
  assert.deepEqual(plain(ctx.run('CV.view')),{x:99,y:77,z:1.1});assert.equal(fits(),0);
  const rects=plain(ctx.run('[...CV.pos].map(([id,p])=>({...p,w:380,h:setupHeights[id]}))'));
  for(let i=0;i<rects.length;i++)for(let j=i+1;j<rects.length;j++)assert.equal(overlap(rects[i],rects[j]),false);
});

test('v2 layout migration preserves clean hand positions and resolves only actual old overlaps', () => {
  const tasks=['a','b'].map(id=>({id,title:id,status:'running',updatedAt:NOW,items:[]}));
  for(const collide of [false,true]){
    const ctx=screen(tasks),positions={a:{x:123,y:456},b:{x:collide?123:900,y:456}};
    const {fits}=layoutWorld(ctx,{a:120,b:120},positions,'a',true);ctx.cvLayout();
    assert.deepEqual(plain(ctx.run("CV.pos.get('a')")),positions.a);
    if(collide)assert.notDeepEqual(plain(ctx.run("CV.pos.get('b')")),positions.b);
    else assert.deepEqual(plain(ctx.run('[...CV.pos]')),Object.entries(positions));
    assert.equal(ctx.run('CV.migrateLayout'),false);
    assert.equal(fits(),0,'a v2 manual camera is not refocused during position migration');
    assert.deepEqual(plain(ctx.run('CV.view')),{x:99,y:77,z:1.1});
    const settled=plain(ctx.run('[...CV.pos]'));ctx.cvLayout();assert.deepEqual(plain(ctx.run('[...CV.pos]')),settled,'migration does not repeat');
  }
});

test('current fit shortcuts share camera-only semantics and keyboard navigation is immediate', () => {
  const ctx=screen([{id:'a',status:'running',updatedAt:NOW,items:[]}]);ctx.renderScreen();
  let animation;ctx.cvFitView=anim=>{animation=anim;};
  ctx.run("CV.overview=true;CV.pendingReveal.add('a');CV.needsFocus=true");ctx.cvFitCurrent(false);
  assert.equal(animation,false);assert.equal(ctx.run('CV.overview'),false);assert.equal(ctx.run('CV.pendingReveal.size'),0);
  assert.match(html,/cvFocusRoot\(b.dataset.ownerId, e.detail !== 0\)/);
  assert.match(html,/e.key === '0'[\s\S]*?cvFitCurrent\(false\)/);
  assert.match(html,/'dblclick'[\s\S]*?cvFitCurrent\(true\)/);
});

test('readable vertical fallback places the current leaf near the bottom so ancestors remain above it', () => {
  const task={id:'p',status:'running',title:'root',updatedAt:NOW,items:[{id:'api',group:'API / 当前',text:'current API',status:'doing'}]};
  const ctx=screen([task]);ctx.innerWidth=1280;ctx.innerHeight=800;ctx.renderScreen();
  const nodes=plain([...ctx.run('CV.nodes.values()')]);
  const els=nodes.map((n,i)=>({dataset:{cvid:n.id},offsetWidth:n.ownerId?320:380,offsetHeight:i===0?320:120}));
  ctx.document.querySelectorAll=()=>els;ctx.document.querySelector=s=>s==='.scr-h'?{offsetHeight:70}:s==='.cv-rail'?{offsetWidth:96}:null;
  ctx.cvApply=()=>{};
  ctx.nodesFixture=nodes;ctx.run("CV.direction='vertical';CV.focusSid='p';CV.pos=new Map(nodesFixture.map((n,i)=>[n.id,{x:0,y:i*400}]))");
  const before=plain(ctx.run('[...CV.pos]'));ctx.cvFitView(false);
  const leaf=nodes.find(n=>n.focusLeaf),p=ctx.run(`CV.pos.get(${JSON.stringify(leaf.id)})`),view=ctx.run('CV.view'),area=ctx.cvViewport();
  assert.equal(view.z,1);assert.equal(p.y+view.y+120,area.y+area.h-24);
  assert.ok(p.y+view.y>area.y+24,'the current leaf no longer sits at the top above an empty lower viewport');
  assert.deepEqual(plain(ctx.run('[...CV.pos]')),before,'viewport refinement never moves nodes');
});

test('follow off suppresses topology camera movement and direction/follow settings round-trip without discarding hand positions', () => {
  const { ctx, fire } = cameraScreen();
  const before = plain(ctx.run('[...CV.pos]')), view = plain(ctx.run('CV.view'));
  ctx.run('CV.follow=false;CV.needsArrange=true');
  let arranged=0;ctx.cvArrange=()=>{arranged++;};
  ctx.cvNewContent(new Set(['b']),false); fire();
  assert.equal(arranged,0);assert.deepEqual(plain(ctx.run('CV.view')),view);
  assert.deepEqual(plain(ctx.run('[...CV.pos]')),before);
  const saved={layoutVersion:3,direction:'vertical',follow:false,focusSid:'a',topology:'same',pos:{a:{x:444,y:333}},view:{x:3,y:7,z:1.25}};
  ctx.store.get=()=>saved;ctx.cvLoad();
  assert.equal(ctx.run('CV.direction'),'vertical');assert.equal(ctx.run('CV.follow'),false);
  assert.equal(ctx.run('CV.focusSid'),'a');assert.deepEqual(plain(ctx.run("CV.pos.get('a')")),{x:444,y:333});
});

test('vertical layout shares top origin and places descendants downward without overlap', () => {
  const ctx = screen();
  const nodes = [{ id:'p',w:380,h:140 },{ id:'a',parentId:'p',w:320,h:180 },{ id:'g',parentId:'a',w:320,h:230 },{ id:'b',parentId:'p',w:320,h:120 },{ id:'q',w:380,h:240 }];
  const vertical = ctx.cvArrangeLayout(nodes, 1280, 'vertical');
  assert.equal(vertical.get('p').y, 0); assert.equal(vertical.get('q').y, 0);
  assert.ok(vertical.get('a').y >= 140 + 72);
  assert.ok(vertical.get('g').y >= vertical.get('a').y + 180 + 72);
  const rects = nodes.map(n => ({...n,...vertical.get(n.id)}));
  for(let i=0;i<rects.length;i++)for(let j=i+1;j<rects.length;j++)assert.equal(overlap(rects[i],rects[j]),false);
  ctx.run("CV.direction='vertical'");
  const curve=ctx.cvEdgePath({x:0,y:0,w:380,h:140},{x:900,y:212,w:320,h:180});
  assert.match(curve,/^M190 140 C190 /,'vertical branches leave the parent bottom even when the child is far right');
  assert.match(curve,/1060 212$/,'vertical branches reach the child top center');
});

test('result summaries count unique item IDs and keep completed execution separate from validation', () => {
  const ctx = screen();
  const items = [{id:'a',status:'done'},{id:'b',status:'done',result:'failed',note:'actual failure',evidence:'reply <unsafe>'},{id:'c',status:'done',result:'unknown'},{id:'d',status:'skipped',result:'unverified'},{id:'e',status:'done',result:'passed'}];
  assert.equal(ctx.cvResultSummary([...items,items[1]]), '执行 5/5 · 通过 1 · 失败 1 · 未知 1 · 未验证 1 · 已排除 1');
  const owner = {id:'r',title:'API suite',status:'paused',updatedAt:NOW,items};
  ctx.S.sessions.set('r',owner);ctx.STATUS={paused:'待续'};
  ctx.cvOpenDetails('r');
  let markup=ctx.cvDetailHtml();
  assert.match(markup,/class="cv-result failed"/);assert.match(markup,/reply &lt;unsafe>/);
  assert.match(markup,/未填写结果的步骤不计为通过/);
  ctx.run("CV.detailFilter='result'");markup=ctx.cvDetailHtml();
  assert.doesNotMatch(markup,/data-item-id="a"/);assert.match(markup,/data-item-id="b"/);
  assert.equal(ctx.run('cvFollowing()'),false,'reading details pauses camera following');assert.equal(ctx.run('CV.follow'),true,'the user switch stays on');
  items[4].status='skipped';
  assert.match(ctx.cvResultSummary(items), /通过 0/,'a skipped step is never counted as passed');
  items[1].resultHistory=[{at:NOW-MIN,result:'unknown',note:'before retry',evidence:'receipt A'},{at:NOW,result:'failed',note:'recheck',evidence:'receipt B'}];
  markup=ctx.cvDetailHtml();
  assert.match(markup,/结果记录 2/);assert.match(markup,/receipt A/);assert.match(markup,/receipt B/);
});

test('validation totals include unreported items and exclude skipped items from every result category', () => {
  const ctx = screen();
  const cases = [
    {id:'old-done',status:'done'}, {id:'future',status:'todo'},
    ...['passed','failed','unknown','unverified'].map(result => ({id:result,status:'done',result})),
    ...['passed','failed','unknown','unverified'].map(result => ({id:'excluded-'+result,status:'skipped',result})),
  ];
  assert.equal(ctx.cvResultSummary([...cases,cases[0]]), '执行 9/10 · 通过 1 · 失败 1 · 未知 1 · 未验证 3 · 已排除 4');
});

test('active sub-agents keep only a compact finished parent context', () => {
  const ctx = screen([
    { id: 'p1', status: 'running', createdAt: 1, updatedAt: NOW, title: 'live parent' },
    { id: 'k1', parentId: 'p1', status: 'running', createdAt: 2, updatedAt: NOW, title: 'kid of live parent' },
    { id: 'p2', status: 'done', createdAt: 1, endedAt: NOW - 50 * MIN, updatedAt: 0, title: 'old parent' },
    { id: 'k2', parentId: 'p2', status: 'running', createdAt: 3, updatedAt: NOW, title: 'kid of old parent' },
    { id: 'k3', parentId: 'p2', status: 'waiting', createdAt: 4, waitSince: NOW, updatedAt: NOW, title: 'waiting kid' },
  ]);
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['k3', 'k2', 'k1', 'p1', 'p2']);
  const context = ctx.cvCards().find(s => s.id === 'p2');
  assert.equal(context.canvasContext, true);
  assert.doesNotMatch(ctx.cvCard(context, () => false), /data-sid=|cv-scope|s-items/);
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
  for (const status of ['waiting', 'blocked', 'planning']) {
    owner.status = status;
    assert.equal(ctx.flowing(parent()), false, `${status} owner must not leave stale doing items flowing`);
  }
  for (const status of ['done', 'cancelled']) {
    owner.status = status;
    assert.equal(parent(), undefined, `${status} owner no longer projects its historical modules`);
  }
  owner.status = 'running';
  owner.items[2].status = 'blocked';
  assert.equal(parent().status, 'blocked');
  assert.equal(ctx.flowing(parent()), false);
  owner.items[1].status = 'skipped'; owner.items[2].status = 'done';
  assert.equal(parent(), undefined, 'all module steps finished: keep only the root awaiting finish');
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
  assert.match(ctx.markup, /cv-rail-task wait[^>]*data-owner-id="wait-parent"/, 'a root whose child waits is marked in the rail');
  assert.match(ctx.markup, /<em>子任务在等你<\/em>/);
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

test('a ninety-step task shows only its current branch and truthful preceding and following counts', () => {
  const owner = { id: 'large', status: 'running', updatedAt: NOW, title: 'large task', items: Array.from({ length: 90 }, (_, i) => ({ id: `i${i}`, group: `工程 / 阶段${Math.floor(i / 6)}`, text: `step ${i}`, status: i < 72 ? 'done' : i === 72 ? 'doing' : 'todo' })) };
  const ctx = screen([owner]);
  const nodes = ctx.cvFlowNodes();
  assert.deepEqual(plain(nodes.filter(n => n.ownerId).map(n => n.modulePath)), ['工程', '工程 / 阶段12']);
  const scope = ctx.cvModuleScope(owner);
  assert.equal(scope.before.length, 12);
  assert.equal(scope.after.length, 2);
  ctx.renderScreen();
  assert.equal((ctx.markup.match(/class="cv-card /g) || []).length, 3);
  assert.match(ctx.markup, /前面 12 个流程/);
  assert.match(ctx.markup, /后面 2 个流程/);
  ctx.run("CV.moduleLists.set('large', 'before')");
  ctx.renderScreen();
  assert.equal((ctx.markup.match(/class="cv-card /g) || []).length, 3, 'opening preceding work does not spread historical cards over the canvas');
  assert.match(ctx.markup, /class="cv-scope-list"/);
  assert.match(ctx.markup, /阶段0/);
  assert.match(html, /\.cv-scope-list\{[^}]*max-height:180px;overflow:auto/);
});

test('parallel current groups remain visible while old blocked work folds away and ungrouped current stays at the root', () => {
  const owner = { id: 'p', status: 'running', updatedAt: NOW, title: 'parallel', items: [
    { id: 'old', group: '前置', status: 'blocked', text: 'old blocked' },
    { id: 'a', group: '开发 / API', status: 'doing', text: 'api' },
    { id: 'done', group: '其他', status: 'done', text: 'finished' },
    { id: 'b', group: '开发 / UI', status: 'doing', text: 'ui' },
    { id: 'next', group: '发布', status: 'todo', text: 'future' },
  ] };
  const ctx = screen([owner]);
  assert.deepEqual(plain(ctx.cvFlowNodes().filter(n => n.ownerId).map(n => n.modulePath)), ['开发', '开发 / API', '开发 / UI']);
  assert.equal(ctx.cvModuleScope(owner).before[0].items[0].status, 'blocked', 'folding does not claim the old blocked step was completed');
  owner.items[1].status = owner.items[3].status = 'todo';
  owner.items.unshift({ id: 'root', text: 'ungrouped current', status: 'doing' });
  assert.equal(ctx.cvFlowNodes().filter(n => n.ownerId).length, 0, 'an ungrouped doing item does not revive old blocked branches');
  assert.equal(ctx.cvModuleScope(owner).before.length, 0);
  assert.equal(ctx.cvModuleScope(owner).after.length, 5);
});

test('an unstarted task folds every group into following work', () => {
  const owner = { id: 'p', status: 'planning', updatedAt: NOW, title: 'planned', items: Array.from({ length: 20 }, (_, i) => ({ id: `i${i}`, group: `阶段${Math.floor(i / 10)}`, text: `step ${i}`, status: 'todo' })) };
  const ctx = screen([owner]);
  const scope = ctx.cvModuleScope(owner);
  assert.equal(scope.focus.size, 0);
  assert.equal(scope.before.length, 0);
  assert.equal(scope.other.length, 0);
  assert.equal(scope.after.length, 2);
  assert.equal(ctx.cvFlowNodes().length, 1);
  ctx.renderScreen();
  assert.match(ctx.markup, /后面 2 个流程/);
});

test('finished tasks and all-closed modules leave the canvas without changing source data', () => {
  const owner = { id: 'p', status: 'running', updatedAt: NOW, title: 'work', items: [{ id: 'i', group: '阶段', text: 'current', status: 'doing' }] };
  const ctx = screen([owner]);
  assert.equal(ctx.cvFlowNodes().length, 2);
  owner.items[0].status = 'done';
  assert.equal(ctx.cvFlowNodes().length, 1, 'all steps complete, but owner still waiting to finish');
  owner.status = 'done'; owner.endedAt = NOW;
  assert.equal(ctx.cvCards().length, 0);
  assert.equal(ctx.S.sessions.get('p'), owner);
  assert.equal(owner.items[0].status, 'done');
  ctx.renderScreen();
  assert.match(ctx.markup, /scr-idle/);
  assert.doesNotMatch(ctx.markup, /class="cv-world/);
  assert.doesNotMatch(ctx.markup, /data-cv="history"|已结束 1/);
});

test('finished parent context never projects its old modules or resurrects an archived parent', () => {
  const parent = { id: 'p', status: 'done', endedAt: NOW, title: 'finished parent', items: Array.from({ length: 90 }, (_, i) => ({ id: `i${i}`, group: `旧阶段${i}`, text: 'done', status: 'done' })) };
  const child = { id: 'kid', parentId: 'p', status: 'running', updatedAt: NOW, title: 'still working', items: [] };
  const ctx = screen([parent, child]);
  assert.equal(ctx.cvCards().length, 2);
  assert.equal(ctx.cvFlowNodes().filter(n => n.ownerId).length, 0);
  parent.archived = true;
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['kid']);
});

test('ended results remain in data but have no history surface on the big screen', () => {
  const sessions = Array.from({ length: 35 }, (_, i) => ({ id: `ended${i}`, status: 'done', endedAt: NOW - i, title: `ended ${i}`, items: [], summary: 'result' }));
  const ctx = screen(sessions);
  ctx.run('CV.history=true'); // even a stale in-memory choice cannot restore removed UI
  ctx.renderScreen();
  assert.doesNotMatch(ctx.markup, /data-cv="history"|class="cv-history-entry"|class="cv-card /);
  assert.equal(ctx.S.sessions.size, 35);
  assert.equal(ctx.S.sessions.get('ended0').summary, 'result');
  const header = ctx.markup.match(/<header class="scr-h">([\s\S]*?)<\/header>/)[1];
  assert.match(header, /data-scr-date/, 'empty screens also keep the clock in the top-right');
  assert.equal((ctx.markup.match(/data-scr-clk/g) || []).length, 1);
});

test('new current steps collapse a manually expanded module and root back to their focused windows', () => {
  const owner = { id: 'p', status: 'running', updatedAt: NOW, title: 'task', items: Array.from({ length: 90 }, (_, i) => ({ id: `i${i}`, group: '实现', text: `step ${i}`, status: i < 45 ? 'done' : i === 45 ? 'doing' : 'todo' })) };
  const ctx = screen([owner]);
  ctx.renderScreen();
  const module = ctx.cvFlowNodes().find(n => n.ownerId);
  ctx.run(`CV.allModules.add(${JSON.stringify(module.id)});S.open.add('all:p')`);
  ctx.renderScreen();
  assert.equal(ctx.run(`CV.allModules.has(${JSON.stringify(module.id)})`), true, 'manual view remains until execution advances');
  owner.items[45].status = 'done'; owner.items[46].status = 'doing';
  ctx.renderScreen();
  assert.equal(ctx.run(`CV.allModules.has(${JSON.stringify(module.id)})`), false);
  assert.equal(ctx.S.open.has('all:p'), false);
  assert.match(html, /\.cv-mod-list\{max-height:220px;overflow:auto\}/);
  assert.match(html, /\.cv-card \.s-items \.pad\{max-height:280px;overflow:auto\}/);
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
  for (const id of ['q', 'r', 'lost']) assert.equal(at[id].x, 0, 'independent sessions start from the same left edge');
  assert.ok(at.q.y >= 348, 'the next root starts below the complete preceding lane');
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

test('the canvas renders only active cards and leaves checklists folded like the float window', () => {
  const ctx = screen([
    { id: 'a', agent: 'codex', status: 'running', createdAt: 2, updatedAt: NOW, title: 'task a', items: [] },
    { id: 'w', agent: 'kiro', status: 'waiting', createdAt: 1, waitSince: NOW - 3 * MIN, updatedAt: NOW, title: 'task w', statusNote: 'delete the cache?', items: [] },
    { id: 'e', agent: 'kiro', status: 'done', createdAt: 0, endedAt: NOW - MIN, updatedAt: 0, title: 'task e', summary: 'all green', items: [] },
  ]);
  ctx.renderScreen();
  assert.equal((ctx.markup.match(/class="cv-card /g) || []).length, 2);
  assert.doesNotMatch(ctx.markup, /data-cv="history"/);
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
  ctx.document.querySelectorAll = selector => selector === '.cv-detail, .cv-history, .cv-history-error' ? [] : els;
  ctx.document.querySelector = selector => selector === '.scr-h' ? { offsetHeight: 56 } : null;
  let clock = 10000, nextId = 0;
  const timers = new Map();
  ctx.Date = class extends Date { static now() { return clock; } };
  ctx.setTimeout = (fn, delay) => { const id = ++nextId; timers.set(id, { fn, at: clock + delay }); return id; };
  ctx.clearTimeout = id => timers.delete(id);
  ctx.cvApply = ctx.cvSave = () => {};
  ctx.run("CV.nodes=new Map(cvFlowNodes().map(n=>[n.id,n]));CV.focusSid='b';CV.overview=false;CV.pos.set('a',{x:0,y:0});CV.pos.set('b',{x:2200,y:0});CV.view={x:100,y:100,z:1}");
  return {
    ctx, els, timers,
    time: value => { clock = value; },
    fire: () => { const [id, t] = [...timers].sort((a, b) => a[1].at - b[1].at)[0]; timers.delete(id); clock = t.at; t.fn(); },
  };
}

test('selected topology updates coalesce into a camera focus, wait for interaction, and ignore title or progress refreshes', () => {
  const { ctx, timers, fire, time } = cameraScreen();
  let arranged = 0;
  ctx.cvFitView = () => { arranged++; };
  ctx.run('CV.interacting=true;CV.userAt=10000');
  ctx.renderScreen(); ctx.renderScreen();
  assert.equal(timers.size, 1);
  fire();
  assert.equal(arranged, 0);
  ctx.run('CV.interacting=false;CV.userAt=10060'); time(10060);
  fire();
  assert.equal(arranged, 1);
  ctx.S.sessions.get('a').title = 'new title';
  ctx.renderScreen();
  assert.equal(timers.size, 0, 'a title refresh keeps the user layout and camera');
  ctx.S.sessions.get('b').status = 'done';
  ctx.renderScreen();
  assert.equal(timers.size, 1, 'selected completion focuses the remaining root without a global tidy');
  fire();
  assert.equal(arranged, 2);
  ctx.S.sessions.get('a').items = [{ id: 'new-step', status: 'doing' }];
  ctx.renderScreen();
  assert.equal(timers.size, 1, 'an item status change is an update that auto-follow moves the camera to');
  fire();
  assert.equal(arranged, 3); assert.equal(ctx.run('CV.focusSid'), 'a');
});

test('legacy saved canvases upgrade once while the current layout schema keeps a matching topology', () => {
  const task = { id: 'a', status: 'running', updatedAt: NOW, items: [] };
  const saved = { pos: { a: { x: 9000, y: 7000 } }, view: { x: 0, y: 0, z: .25 } };
  const old = screen([task]);
  old.store.get = () => saved;
  old.cvLoad(); old.renderScreen();
  assert.equal(old.run('CV.migrateLayout'), true);
  assert.deepEqual(plain(old.run("CV.pos.get('a')")), saved.pos.a, 'v2 coordinates remain intact until actual overlap is measured');
  const current = screen([task]);
  current.store.get = () => ({ ...saved, layoutVersion: 3, topology: JSON.stringify([['a', '', false, false]]) });
  current.cvLoad(); current.renderScreen();
  assert.equal(current.run('CV.needsArrange'), false);
  assert.deepEqual(plain(current.run("CV.pos.get('a')")), saved.pos.a);
});

test('waiting structure and compact parent context request focus while ordinary text or progress do not', () => {
  const parent = { id: 'p', status: 'running', updatedAt: NOW, title: 'parent', items: [{ id: 'i', status: 'doing', text: 'step' }] };
  const child = { id: 'c', parentId: 'p', status: 'running', updatedAt: NOW, title: 'child', items: [] };
  const ctx = screen([parent, child]);
  ctx.renderScreen();
  ctx.run('CV.needsFocus=false');
  const initial = ctx.run('CV.topology');
  parent.title = 'renamed'; parent.items[0].text = 'changed text'; parent.items[0].spentMs = 5000;
  ctx.renderScreen();
  assert.equal(ctx.run('CV.needsFocus'), false);
  assert.equal(ctx.run('CV.topology'), initial);
  parent.status = 'waiting'; parent.statusNote = 'need a reply';
  ctx.renderScreen();
  assert.equal(ctx.run('CV.needsFocus'), true, 'a new question row changes the card structure');
  assert.match(ctx.markup, /class="cv-q"/);
  ctx.run('CV.needsFocus=false');
  parent.statusNote = 'a longer question';
  ctx.renderScreen();
  assert.equal(ctx.run('CV.needsFocus'), false, 'editing the question does not introduce another structure change');
  parent.status = 'running';
  ctx.renderScreen();
  assert.equal(ctx.run('CV.needsFocus'), true, 'removing the question also compacts the lane');
  ctx.run('CV.needsFocus=false');
  const beforeContextIds = plain([...ctx.run('CV.nodes.keys()')]).sort();
  parent.status = 'done'; parent.endedAt = NOW;
  ctx.renderScreen();
  assert.deepEqual(plain([...ctx.run('CV.nodes.keys()')]).sort(), beforeContextIds, 'the live child retains the same parent ID');
  assert.equal(ctx.run("CV.nodes.get('p').canvasContext"), true);
  assert.equal(ctx.run('CV.needsFocus'), true, 'a compact ended parent has a different structure despite keeping its ID');
});

test('explicit expansion and closing coalesce a camera update without globally rearranging the canvas', () => {
  const { ctx, timers, fire } = cameraScreen();
  let arranged=0,focused=0;
  ctx.cvArrange=()=>{arranged++;};ctx.cvFitView=()=>{focused++;};
  ctx.cvRequestFocus();ctx.cvRequestFocus();
  assert.equal(timers.size,1,'explicit scope/module requests share the existing timer');
  fire();fire();
  assert.equal(arranged,0,'expanding a card must not move other root tasks');assert.equal(focused,1);
  ctx.cvRequestFocus();fire();fire();assert.equal(arranged,0);assert.equal(focused,2);
  assert.match(html,/foldSlide\(row, open\);\s+if \(SCREEN\) cvRequestFocus\(\)/);
  assert.match(html,/case 'items-all':[\s\S]*?if \(SCREEN\) cvRequestFocus\(\)/);
});

test('overview suppresses automatic focus but preserves the existing explicit archive tidy queue', () => {
  const {ctx,fire}=cameraScreen();let arranged=0,focused=0;
  ctx.cvArrange=()=>{arranged++;ctx.run('CV.needsArrange=false');};ctx.cvFitView=()=>{focused++;};
  ctx.run('CV.overview=true;CV.needsFocus=true');ctx.cvNewContent(new Set(),false);fire();
  assert.equal(focused,0);assert.equal(arranged,0,'SSE never repositions the overview camera');
  ctx.cvRequestArrange();fire();fire();
  assert.equal(arranged,1,'successful archive retains its established automatic tidy contract in overview');
});

test('fit and arrange ignore outgoing morph ghosts', () => {
  const ctx = screen();
  const els = ['a', 'ghost'].map(id => ({ dataset: { cvid: id }, offsetWidth: 380, offsetHeight: 100, style: {} }));
  const world = { classList: { add() {}, remove() {} }, querySelectorAll: () => els };
  ctx.document.querySelectorAll = () => els;
  ctx.document.querySelector = selector => selector === '.cv-world' ? world : null;
  ctx.run("CV.nodes.set('a',{id:'a'});CV.pos.set('a',{x:0,y:0});CV.pos.set('ghost',{x:10000,y:10000})");
  assert.equal(ctx.cvRects().length, 1);
  let measured;
  ctx.cvArrangeLayout = nodes => { measured = nodes; return new Map([['a', { x: 0, y: 0 }]]); };
  ctx.cvFitView = ctx.cvEdges = () => {};
  ctx.performance = { now: () => 0 }; ctx.requestAnimationFrame = () => 0;
  ctx.cvArrange();
  assert.deepEqual(plain(measured.map(n => n.id)), ['a']);
  assert.equal(els[1].style.left, undefined);
});

test('manual cleanup archive uses the shared tree contract and shows busy and failures', async () => {
  const owner = { id: 'ended', status: 'done', endedAt: NOW - 40 * MIN, updatedAt: NOW - 40 * MIN, title: 'finished', items: [] };
  const child = { id: 'stale', parentId: 'ended', status: 'running', updatedAt: NOW - 40 * MIN, title: 'stale child', items: [] };
  const ctx = screen([owner, child]);
  const calls = [];
  let finishArchive;
  ctx.fetch = (...args) => { calls.push(args); return new Promise(resolve => { finishArchive = () => resolve({ ok: true, json: async () => ({ sessions: [{ ...owner, archived: true }] }) }); }); };
  ctx.ingest = s => { Object.assign(owner, s); ctx.S.sessions.set(s.id, s); };
  ctx.run('CV.cleanup=true');
  const archive = ctx.cvArchive('ended');
  ctx.renderScreen();
  assert.match(ctx.markup, /data-cv="archive" data-owner-id="ended" disabled>归档中/);
  finishArchive(); await archive;
  assert.equal(ctx.run('CV.needsArrange'), true, 'manual archive requests tidy even when active topology is unchanged');
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], '/api/sessions/ended/archive');
  assert.equal(calls[0][1].method, 'POST');
  assert.deepEqual(JSON.parse(calls[0][1].body), { archived: true });
  ctx.renderScreen();
  assert.doesNotMatch(ctx.markup, /class="cv-history-entry"/);
  owner.archived = false;
  ctx.run('CV.needsArrange=false');
  ctx.S.sessions.set(owner.id, owner);
  ctx.fetch = async () => { throw new Error('offline'); };
  await ctx.cvArchive('ended'); ctx.renderScreen();
  assert.match(ctx.markup, /role="alert">归档没成功：offline/);
  assert.equal(owner.archived, false);
  assert.equal(ctx.run('CV.needsArrange'), false, 'a failed archive does not request tidy');
  assert.match(ctx.markup, /data-cv="archive" data-owner-id="ended" >归档/);
  assert.match(html, /\.cleanup-entry:hover \.cleanup-archive/);
  assert.match(html, /\.cleanup-entry:focus-within \.cleanup-archive/);
});

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
    assert.ok(ctx.run('CV.view.z') >= 1, 'selected current work remains readable instead of fitting every root at 25%');
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
