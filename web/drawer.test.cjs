// Author: Ray Zhang
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const html=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
const source=html.slice(html.indexOf('// 通知页停留：'),html.indexOf('// 通知页停留结束。'));
function setup(length=''){
  let clock=10000,next=0,scheduled=0;const timers=new Map(),events={};
  const countdown={textContent:''},task={id:'p',status:'waiting',waitSince:123,statusNote:'user question'},notice={id:'n1',sessionId:'p',kind:'waiting',at:123,read:false};
  const doc={hidden:false,querySelector:()=>null,querySelectorAll:()=>[countdown],addEventListener:(name,fn)=>events[name]=fn};
  const ctx={S:{drawer:true,drawerAuto:true,drawerWant:false,toastLen:length,sessions:new Map([['p',task]]),notices:[notice]},TOAST_TTL:{waiting:30000},FLOAT:true,document:doc,
    Date:class extends Date{static now(){return clock;}},setTimeout:(fn,delay)=>{const id=++next;timers.set(id,{fn,at:clock+delay});return id;},clearTimeout:id=>timers.delete(id),schedule:()=>{scheduled++;},addEventListener:(name,fn)=>events[name]=fn};
  vm.createContext(ctx);vm.runInContext(source,ctx);ctx.run=code=>vm.runInContext(code,ctx);
  const advance=ms=>{const end=clock+ms;for(;;){const first=[...timers].sort((a,b)=>a[1].at-b[1].at)[0];if(!first||first[1].at>end)break;timers.delete(first[0]);clock=first[1].at;first[1].fn();}clock=end;};
  return {ctx,advance,timers,events,countdown,task,notice,scheduled:()=>scheduled};
}
test('notification view returns to progress at existing short/normal/long reminder duration without changing task or read state',()=>{
  for(const [length,ttl]of [['short',15000],['',30000],['long',60000]]){
    const {ctx,advance,timers,countdown,task,notice,scheduled}=setup(length);const before=JSON.stringify({task,notice});
    ctx.drawerReturnSync(true);assert.equal(countdown.textContent,`${ttl/1000} 秒后回到进度`);
    advance(ttl-1);assert.equal(ctx.S.drawer,true);advance(1);assert.equal(ctx.S.drawer,false);assert.equal(timers.size,0);assert.equal(scheduled(),1);
    assert.equal(JSON.stringify({task,notice}),before,'timeout is navigation only');
  }
});
test('hover and keyboard focus independently pause the remaining budget, then resume without resetting it',()=>{
  const {ctx,advance,countdown,timers}=setup('short');ctx.drawerReturnSync(true);advance(5000);
  ctx.drawerReturnPause('hover',true);assert.match(countdown.textContent,/10 秒.*暂停/);advance(60000);assert.equal(ctx.S.drawer,true);assert.equal(timers.size,0);
  ctx.drawerReturnPause('focus',true);ctx.drawerReturnPause('hover',false);advance(60000);assert.equal(ctx.S.drawer,true);
  ctx.drawerReturnPause('focus',false);advance(9999);assert.equal(ctx.S.drawer,true);advance(1);assert.equal(ctx.S.drawer,false);
});
test('mouse-clicked filter focus does not keep countdown paused after pointer leaves, keyboard focus does',()=>{
  const {ctx,advance,events}=setup('short');ctx.drawerReturnSync(true);advance(2000);
  const drawer={contains:()=>false};
  const mouseButton={closest:()=>drawer,matches:()=>false};
  ctx.drawerReturnPause('hover',true);events.focusin({target:mouseButton});
  ctx.drawerReturnPause('hover',false);advance(13000);assert.equal(ctx.S.drawer,false,'mouse button remaining activeElement must not pin the notification page');
  ctx.S.drawer=true;ctx.drawerReturnSync(true);advance(2000);
  const keyboardButton={closest:()=>drawer,matches:()=>true};events.focusin({target:keyboardButton});advance(60000);
  assert.equal(ctx.S.drawer,true,'keyboard focus-visible pauses while operating');
  events.focusout({target:keyboardButton,relatedTarget:null});advance(13000);assert.equal(ctx.S.drawer,false);
});
test('same-page SSE renders and notifications never restart the timer; later new waiting rounds can still auto open',()=>{
  const {ctx,advance,notice}=setup('short');ctx.S.drawer=false;
  assert.equal(ctx.drawerCanAuto(notice),true);ctx.S.drawer=true;ctx.drawerReturnSync(true);advance(5000);
  const remaining=ctx.run('DRAWER_RETURN.left');ctx.drawerReturnSync(true);assert.equal(ctx.run('DRAWER_RETURN.left'),remaining);
  assert.equal(ctx.drawerCanAuto({...notice,id:'n2',at:999}),false,'notification received during the open page does not reset navigation');
  advance(10000);assert.equal(ctx.S.drawer,false);
  assert.equal(ctx.drawerCanAuto({...notice,id:'n3',at:999}),false,'same waiting round cannot reopen after timeout');
  ctx.S.sessions.get('p').waitSince=555;assert.equal(ctx.drawerCanAuto({...notice,id:'n4'}),false,'closing gets a quiet period');
  advance(15000);assert.equal(ctx.drawerCanAuto({...notice,id:'n5'}),true,'a genuinely new waiting round is not permanently disabled');
});
test('explicit return, hidden host, background page and unload clear timer without marking notifications read',()=>{
  for(const mode of ['return','host','background','unload']){
    const {ctx,advance,timers,events,notice}=setup();ctx.drawerReturnSync(true);advance(2000);
    if(mode==='return'){ctx.S.drawer=false;ctx.drawerReturnSync(false);}
    if(mode==='host'){ctx.S.hostHidden=true;ctx.drawerReturnSync(true);}
    if(mode==='background'){ctx.document.hidden=true;events.visibilitychange();assert.equal(ctx.drawerCanAuto(notice),false);}
    if(mode==='unload')events.pagehide();
    assert.equal(timers.size,0,mode);assert.equal(ctx.run('DRAWER_RETURN.shown'),false);assert.equal(notice.read,false);
  }
});
test('panel waiting filter and default groups distinguish user input, dependency and paused work',()=>{
  const sessions=[{id:'w',status:'waiting'},{id:'b',status:'blocked'},{id:'p',status:'paused'}];
  const ctx={S:{sessions:new Map(sessions.map(s=>[s.id,s])),view:'active',q:'',agent:null},visible:()=>sessions,
    waitingForUser:s=>s.status==='waiting',isWaiting:s=>s.status==='waiting'||s.status==='blocked',isEnded:()=>false,isDormant:()=>false,byUpdated:()=>0,now:()=>10000,MIN:60000};
  vm.createContext(ctx);vm.runInContext(html.slice(html.indexOf('function groups()'),html.indexOf('function renderPanel()')),ctx);
  let groups=ctx.groups();assert.equal(groups.find(g=>g[0]==='等你')[1][0].id,'w');assert.equal(groups.find(g=>g[0]==='等依赖')[1][0].id,'b');assert.equal(groups.find(g=>g[0]==='待续')[1][0].id,'p');
  ctx.S.view='waiting';assert.equal(ctx.groups()[0][1].length,1);assert.equal(ctx.groups()[0][1][0].id,'w');
  assert.match(html,/waiting: live.filter\(waitingForUser\).length/);
});
test('agent sheet and deck use the same waiting-only and resumable/dependency presentation',()=>{
  const sessions=['running','planning','waiting','blocked','paused','done'].map((status,i)=>({id:`s${i}`,agent:'codex',status,title:status,events:[]}));
  const deck={hidden:false};let deckMarkup='';
  const ctx={S:{deck:true,view:'all'},visible:()=>sessions,waitingForUser:s=>s.status==='waiting',isEnded:s=>s.status==='done',isDormant:()=>false,
    isRunning:s=>s.status==='running'||s.status==='planning',agentOf:()=>({name:'Codex',mark:'Cx',color:'green'}),esc:s=>String(s||''),IC:{},connChip:()=>'',EN:false,
    groups:()=>[['全部',sessions]],DECK_MAX:40,$:()=>deck,VIEWS:{all:'全部'},morph:(_,markup)=>{deckMarkup=markup;return[];},RM:{matches:true},deckCard:s=>s.id};
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('function agentsHtml()'),html.indexOf('// ============ 增量更新：')),ctx);
  const sheet=ctx.agentsHtml();assert.match(sheet,/1 个在等你/);assert.match(sheet,/1 个等依赖/);assert.match(sheet,/1 个待续/);assert.match(sheet,/2 个进行中/);
  vm.runInContext(html.slice(html.indexOf('function renderDeck()'),html.indexOf('// 进出卡片。')),ctx);ctx.renderDeck();
  assert.match(deckMarkup,/1 个在等你/);assert.match(deckMarkup,/1 个等依赖/);assert.match(deckMarkup,/1 个待续/);
  assert.match(html,/data-tip="\$\{list.length\} 个会话，轮流显示"/);
  assert.doesNotMatch(html,/filter\(isWaiting\)\.length|if \(isWaiting\(s\)\) g.wait\+\+/,'remaining isWaiting uses are state/color/order rules, not user-wait count');
});
