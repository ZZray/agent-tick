const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(process.env.TICK_HTML || path.join(__dirname, 'index.html'), 'utf8');
function setup(sessions = []) {
  const ctx = {
    FLOAT: true, NATIVE: false, EN: false, RM: { matches: true },
    S: { open: new Set(), sessions: new Map(sessions.map(s => [s.id, s])), dock: {}, celebrate: new Set(), justDone: new Set() },
    esc: s => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;'),
    current: s => s.items.find(it => it.status === 'doing'),
    childrenOf: s => sessions.filter(k => k.parentId === s.id),
    isChild: s => !!s.parentId && !!ctx.S.sessions.get(s.parentId) && !ctx.S.sessions.get(s.parentId).archived,
    isEnded: s => ['done', 'failed', 'cancelled'].includes(s.status),
    isWaiting: s => ['waiting', 'blocked'].includes(s.status),
    isDormant: s => !!s.dormant, isStale: () => false,
    finished: it => ['done', 'skipped'].includes(it.status),
    progress: s => ({ total: s.items.length, done: s.items.filter(it => it.status === 'done').length }),
    agentOf: () => ({name:'Codex',mark:'Cx'}), sicon: () => '', segs: () => '',
    firstLine: s => s, subline: () => 'current step', short: () => '1m', elapsed: () => 0,
    itemMs: () => ({ms:0,approx:false}), dur: () => '0s', modHead: () => '',
    IC: {}, STATUS: {running:'进行中',done:'完成'}, CSS: {escape:s=>s},
    render: () => {}, pip: {}, root: {querySelector:()=>({scrollIntoView:()=>{ctx.scrolled=true;}})},
  };
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('function matches('), html.indexOf('function hitLine(')),ctx);
  vm.runInContext(html.slice(html.indexOf('const ITEMS_LONG'), html.indexOf('function cardHtml')),ctx);
  vm.runInContext(html.slice(html.indexOf('function kidsHtml'), html.indexOf('function floatEmpty')),ctx);
  vm.runInContext(html.slice(html.indexOf('function focusSession'), html.indexOf('window.tickFocus')),ctx);
  return ctx;
}
const item = (i,status='todo') => ({id:`i${i}`,text:`step ${i}`,status,note:''});

test('a renamed task is found by its old title and keeps its current title',()=>{
  const ctx=setup();
  const s={id:'p',agent:'codex',title:'Tick · 展开与标题优化',titleHistory:['了解 Tick 项目','Tick · 修复折叠'],items:[]};
  assert.ok(ctx.matches(s,'了解 tick'));
  assert.ok(ctx.matches(s,'修复折叠'));
  assert.ok(ctx.matches(s,'展开与标题优化'));
  assert.equal(s.title,'Tick · 展开与标题优化');
  assert.equal(ctx.matches(s,'其他项目'),false);
});

test('old task data without title history remains searchable',()=>{
  const ctx=setup();
  const s={id:'old',agent:'codex',title:'既有会话',items:[item(1)]};
  assert.ok(ctx.matches(s,'既有会话'));
  assert.ok(ctx.matches(s,'step 1'));
  assert.equal(ctx.matches(s,'undefined'),false);
});

test('old blocked item does not hide the two current parallel steps',()=>{
  const ctx=setup();
  const items=Array.from({length:26},(_,i)=>item(i,i<17?'done':'todo'));
  items[11].status='blocked'; items[17].status=items[18].status='doing';
  const markup=ctx.itemsHtml({id:'parent',items});
  assert.match(markup,/data-key="i17"/); assert.match(markup,/data-key="i18"/);
  assert.doesNotMatch(markup,/data-key="i11"/);
  ctx.S.open.add('all:parent');
  assert.match(ctx.itemsHtml({id:'parent',items}),/data-key="i11"/);
});
test('blocked is the current window when no item is doing',()=>{
  const ctx=setup(); const items=Array.from({length:18},(_,i)=>item(i,i<10?'done':'todo'));
  items[9].status='blocked';
  assert.match(ctx.itemsHtml({id:'parent',items}),/data-key="i9"/);
});
test('a finished checklist shows its final items',()=>{
  const ctx=setup(); const items=Array.from({length:18},(_,i)=>item(i,'done'));
  const markup=ctx.itemsHtml({id:'parent',items});
  assert.match(markup,/data-key="i17"/); assert.doesNotMatch(markup,/data-key="i0"/);
});
test('children belong to the parent folding body and keep their own open state',()=>{
  const parent={id:'p',agent:'codex',title:'parent',status:'running',items:[item(0,'doing')]};
  const child={id:'c',parentId:'p',title:'child',status:'running',items:[item(1,'doing')]};
  const ctx=setup([parent,child]); ctx.S.open.add('c');
  const markup=ctx.sessionRow(parent,()=>false);
  assert.ok(markup.indexOf('<div class="s-items">')<markup.indexOf('<div class="kids"'));
  assert.match(markup,/class="kid open"/); assert.ok(!ctx.S.open.has('p'));
});
for(const state of ['running','done','dormant']) test(`focusing a child reveals its ${state} parent`,()=>{
  const parent={id:'p',status:state==='done'?'done':'running',dormant:state==='dormant',items:[]};
  const child={id:'c',parentId:'p',status:'running',items:[]}; const ctx=setup([parent,child]);
  ctx.focusSession('c');
  assert.ok(ctx.S.open.has('p')); assert.ok(ctx.S.open.has('c')); assert.ok(ctx.scrolled);
  if(state==='done') assert.equal(ctx.S.histOpen,true);
  if(state==='dormant') assert.equal(ctx.S.staleOpen,true);
});
