// Author: Ray Zhang
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const MIN = 60000, NOW = 1000 * MIN;
const source = html.slice(html.indexOf('const alive ='), html.indexOf('// “进行中”在所有地方'))
  + html.slice(html.indexOf('const visible ='), html.indexOf('const byUpdated ='))
  + html.slice(html.indexOf('// 会话树与归档：'), html.indexOf('// 左上角 logo 点开的 Agent 面板'))
  + html.slice(html.indexOf('const CV_GAP'), html.indexOf('// 面板设置留在侧栏 morph 之外'));
const plain = value => JSON.parse(JSON.stringify(value));
function setup(sessions, screen = true) {
  const ctx = {
    S: { sessions: new Map(sessions.map(s => [s.id, s])), activity: {}, notices: [], detached: new Set(), dismissed: new Set(), open: new Set(), conn: 'live' },
    now: () => NOW, MIN, SCREEN: screen, STATUS: { waiting: '等你', blocked: '等依赖' }, short: () => '65m',
    waitingForUser: s => s.status === 'waiting',
    isEnded: s => ['done', 'failed', 'cancelled'].includes(s.status),
    isWaiting: s => ['waiting', 'blocked'].includes(s.status),
    byOrder: (a, b) => a.createdAt - b.createdAt, schedule() {},
    esc: value => String(value ?? ''), setTimeout: () => 0, clearTimeout() {},
    document: { querySelector: () => null, querySelectorAll: () => [] }, Date,
  };
  vm.createContext(ctx); vm.runInContext(source, ctx);
  vm.runInContext('CV.overview=true', ctx);
  ctx.run = code => vm.runInContext(code, ctx);
  ctx.ingest = session => ctx.S.sessions.set(session.id, session);
  return ctx;
}
const session = (id, status, parentId = '', extra = {}) => ({ id, status, parentId, title: id, items: [], createdAt: 1, updatedAt: NOW - 65 * MIN, endedAt: status === 'done' ? NOW - 61 * MIN : 0, ...extra });

test('ended parent with only dormant unfinished descendants has one cleanup entry in both views', () => {
  const ctx = setup([session('p', 'done'), session('c', 'running', 'p'), session('g', 'planning', 'c')]);
  const float = ctx.floatData();
  assert.deepEqual(plain(float.cleanup.map(s => s.id)), ['p']);
  assert.deepEqual(plain([float.active.length, float.stale.length, float.recent.length]), [0, 0, 0]);
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), [], 'the same residual tree leaves the screen canvas');
});

test('recent child progress, transcript activity, waiting or blocked descendants keep ended parent reachable', () => {
  for (const kind of ['progress', 'transcript', 'waiting', 'blocked']) {
    const child = session('c', kind === 'waiting' || kind === 'blocked' ? kind : 'running', 'p');
    if (kind === 'progress') child.updatedAt = NOW - MIN;
    const ctx = setup([session('p', 'done'), child]);
    if (kind === 'transcript') ctx.S.activity.c = NOW - MIN;
    assert.equal(ctx.cleanupRoots().length, 0, kind);
    const bucket = kind === 'blocked' ? ctx.floatData().dependencies : ctx.floatData().active;
    assert.equal(bucket[0].id, 'p', `${kind} remains accessible in the float after 30 minutes`);
    assert.equal(bucket[0].canvasContext, true);
    const parent = ctx.cvCards().find(s => s.id === 'p');
    assert.equal(parent.canvasContext, true);
    const markup = ctx.cvCard(parent, () => false);
    assert.match(markup, /归档整组/);
    assert.doesNotMatch(markup, /子 agent 仍在执行/);
    assert.match(markup, kind === 'waiting' ? /子任务在等你/ : kind === 'blocked' ? /子任务等待依赖/ : /子任务有近期活动/);
  }
});

test('explicitly paused descendants stay reachable as resumable work and are never cleanup or waiting counts', () => {
  const ctx = setup([session('p','done'),session('c','paused','p')]);
  assert.equal(ctx.cleanupRoots().length,0);
  const d=ctx.floatData();assert.equal(d.paused[0].id,'p');assert.equal(d.nW,0);assert.equal(d.nR,0);
  assert.match(ctx.parentContextNote(d.paused[0]),/子任务待续/);
});

test('a waiting grandchild protects its entire ended ancestor path and normal dormant roots stay separate', () => {
  const ctx = setup([session('p', 'done'), session('c', 'done', 'p'), session('g', 'waiting', 'c'), session('idle', 'running')]);
  assert.equal(ctx.cleanupRoots().length, 0);
  assert.deepEqual(plain(ctx.cvCards().filter(s => s.canvasContext).map(s => s.id)).sort(), ['c', 'p']);
  assert.deepEqual(plain(ctx.floatData().stale.map(s => s.id)), ['idle']);
  assert.equal(ctx.floatData().nW, 1);
});

test('new progress returns a residual group to both active views; a recovered child under archived parent is independent', () => {
  const parent = session('p', 'done'), child = session('c', 'running', 'p');
  const ctx = setup([parent, child]);
  assert.equal(ctx.cleanupRoots().length, 1);
  child.updatedAt = NOW;
  assert.equal(ctx.cleanupRoots().length, 0);
  assert.equal(ctx.floatData().active[0].id, 'p');
  parent.archived = true;
  assert.deepEqual(plain(ctx.floatData().active.map(s => s.id)), ['c']);
  assert.deepEqual(plain(ctx.cvCards().map(s => s.id)), ['c']);
  assert.equal(parent.archived, true, 'new child progress does not silently unarchive its parent');
});

test('shared archive consumes the authoritative tree response once and failures keep the tree available', async () => {
  const parent = session('p', 'done'), child = session('c', 'running', 'p');
  const ctx = setup([parent, child]);
  const calls = [];
  let complete;
  ctx.fetch = (url, input) => { calls.push({ url, input }); return new Promise(resolve => { complete = () => resolve({ ok: true, json: async () => ({ sessions: [parent, child].map(s => ({ ...s, archived: true })) }) }); }); };
  const request = ctx.archiveTree('p');
  assert.equal(ctx.run("ARCHIVE.busy.has('p')"), true);
  assert.equal(await ctx.archiveTree('p'), null, 'a second click does not start a duplicate request');
  complete(); await request;
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/api/sessions/p/archive');
  assert.equal(calls[0].input.method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].input.body), { archived: true });
  assert.equal(ctx.run('CV.needsArrange'), true);
  assert.equal(ctx.cleanupRoots().length, 0);
  assert.equal(ctx.cvCards().length, 0, 'children do not reappear as orphan roots after the group is archived');
  parent.archived = false; child.archived = false; ctx.S.sessions.set('p', parent); ctx.S.sessions.set('c', child);
  ctx.run('CV.needsArrange=false');
  ctx.fetch = async () => ({ ok: false, status: 503, json: async () => ({ error: 'save unavailable' }) });
  await assert.rejects(ctx.archiveTree('p'), /save unavailable/);
  assert.equal(ctx.cleanupRoots().length, 1);
  assert.equal(ctx.run('CV.needsArrange'), false);
  assert.match(ctx.run('ARCHIVE.error'), /归档没成功：save unavailable/);
  assert.equal(ctx.run('ARCHIVE.busy.size'), 0);
});

test('cleanup actions are visible on hover and keyboard focus in both views and every ordinary archive uses the shared endpoint', () => {
  assert.match(html, /\.cleanup-entry:hover \.cleanup-archive/);
  assert.match(html, /\.cleanup-entry:focus-within \.cleanup-archive/);
  assert.match(html, /\.cv-parent-context:focus-within \.cleanup-archive/);
  assert.match(html, /data-act="cleanup-fold"/);
  assert.match(html, /class="cleanup-title" data-act="open-panel-session" data-sid=/);
  assert.match(html, /data-cv="cleanup"/);
  const onClick = html.slice(html.indexOf('async function onClick'), html.indexOf('document.addEventListener(\'click\', onClick)'));
  assert.match(onClick, /case 'archive-ended':[\s\S]*?archiveTree\(s.id, v\)/);
  assert.match(onClick, /case 'archive':[\s\S]*?archiveTree\(s.id, v\)/);
  assert.doesNotMatch(onClick.slice(onClick.indexOf("case 'archive':")), /\{ archived: v \}, 'PATCH'/);
});

test('one batch SSE event ingests more than 64 archived sessions into each view and schedules once', async () => {
  for (const screen of [false,true]) {
    const sessions=Array.from({length:80},(_,i)=>session(`s${i}`,'done'));
    const ctx=setup(sessions,screen),listeners={};let scheduled=0;
    Object.assign(ctx,{qs:{has:()=>false},HTTP:true,settings:{el:null},fetch:async()=>({json:async()=>({desktop:true})}),reload:async()=>{},scheduleData:()=>{scheduled++;},
      EventSource:class{addEventListener(name,fn){listeners[name]=fn;}},ingestNotice(){}});
    vm.runInContext(html.slice(html.indexOf('async function connect()'),html.indexOf('async function reload()')),ctx);
    await ctx.connect();
    listeners.sessions({data:JSON.stringify(sessions.map(s=>({...s,archived:true})))});
    assert.equal(ctx.run('visible().length'),0);
    assert.equal(scheduled,1);
  }
});

test('undo restores only IDs changed by the archive, preserving previously archived descendants', async () => {
  const ctx=setup([session('p','done'),session('c','running','p'),session('old','done','p',{archived:true})],false);
  const patches=[];let reloaded=0;
  ctx.post=async(url,body,method)=>{patches.push({url,body,method});};
  ctx.reload=async()=>{reloaded++;};
  await ctx.undoArchiveChange(['p','c'],false);
  assert.deepEqual(plain(patches),[
    {url:'/api/sessions/p',body:{archived:false},method:'PATCH'},
    {url:'/api/sessions/c',body:{archived:false},method:'PATCH'}
  ]);
  assert.equal(reloaded,1);
  assert.equal(ctx.S.sessions.get('old').archived,true);
});

test('notification badge switch suppresses edge-strip unread marks while retaining execution state', () => {
  const task = session('p','running','',{updatedAt:NOW});
  const ctx=setup([task]);
  Object.assign(ctx,{progress:()=>({done:1,total:2}),esc:s=>String(s),isChild:()=>false});
  ctx.S.notices=[{sessionId:'p',read:false}];
  vm.runInContext(html.slice(html.indexOf('const STRIP_COLORS'),html.indexOf('// 详情卡：',html.indexOf('function stripHtml'))),ctx);
  let markup=ctx.stripHtml([task]);
  assert.match(markup,/class="sb-g n run/);assert.match(markup,/class="nb"/);assert.match(markup,/class="nf"/);
  ctx.S.notificationBadge=false;markup=ctx.stripHtml([task]);
  assert.match(markup,/class="sb-g run/);assert.doesNotMatch(markup,/class="nb"|class="nf"|sb-g n/);
  assert.equal(ctx.S.notices[0].read,false,'display preference does not read or remove notices');
});
