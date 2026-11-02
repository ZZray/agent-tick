const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const state = overrides => ({version:'0.5.14',sounds:[],sound:'',toastLen:'',lang:'',dnd:false,
  autostart:false,theme:{id:'green',brand:'#00AA66'},themes:[{id:'green',name:'Green',brand:'#00AA66'}],...overrides});
const response = value => ({ok:true,json:async()=>value});
function deferred() { let resolve; const promise = new Promise(r=>{resolve=r;}); return {promise,resolve}; }
function setup(fetch = async()=>response(state())) {
  const events = {}, timers = [], saved = {};
  const element = () => ({style:{},attributes:{},offsetWidth:320,markup:'',removed:false,
    setAttribute(k,v){this.attributes[k]=v;},contains(el){return el === this;},
    querySelectorAll(){return [];},querySelector(){return null;},remove(){this.removed=true;},
    animate(frames,options){this.animation={frames,options};}});
  const trigger = element(); trigger.focus=()=>{trigger.focused=true;}; trigger.getBoundingClientRect=()=>({left:10,top:450});
  const doc = {activeElement:null,body:{appendChild(){}},createElement:element,querySelector:()=>trigger,
    addEventListener:(type,fn)=>(events[type] ||= []).push(fn)};
  const ctx = {document:doc,window:{addEventListener:(type,fn)=>(events[type] ||= []).push(fn)},
    S:{desktop:true,appearance:'system',info:{version:'0.5.14'}},innerWidth:720,innerHeight:500,
    systemAppearance:{matches:false,addEventListener:(type,fn)=>{events.system=fn;}},
    RM:{matches:false},IC:{},pk:{el:null,btn:null},EASE_OUT:'ease-out',AbortSignal,fetch,WIN:false,EN:false,URL,
    location:{href:'http://127.0.0.1:17361/?host=panel#s_test',replace:url=>{ctx.reloaded=url;}},
    store:{get:(k,d)=>saved[k]??d,set:(k,v)=>{saved[k]=v;}},
    esc:s=>String(s??'').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('"','&quot;'),
    tr:s=>s,morph:(el,s)=>{el.markup=s;},pickHtml:(name,opts,val)=>`<button data-pick="${name}">${val}</button>`,
    setTheme:t=>{ctx.accent=t;},schedule:()=>{},pickClose:()=>{},tipHide:()=>{},
    setTimeout:fn=>{timers.push(fn);return timers.length;},clearTimeout:()=>{},
  };
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('function appearanceMode('),html.indexOf('// ============ 界面语言')),ctx);
  vm.runInContext(html.slice(html.indexOf('const settings = {'),html.indexOf('// ---------- 全屏卡片：')),ctx);
  ctx.settings=vm.runInContext('settings',ctx);
  ctx.settings.el=element();
  return {ctx,events,timers,saved,trigger};
}
test('settings renders distinct appearance, notification and general groups with release link',()=>{
  const {ctx}=setup();ctx.settings.state=state();ctx.renderSettings();
  const markup=ctx.settings.el.markup;
  for(const text of ['外观','通知','常规','s-appearance','s-sound','s-toastlen','s-lang','切换语言会重启 Tick','data-setting="autostart"','settings-mini','settings-guide']) assert.ok(markup.includes(text),text);
  assert.match(markup,/https:\/\/github.com\/ZZray\/agent-tick\/releases/);
  assert.ok(parseInt(ctx.settings.el.style.maxHeight)<=450);
});
test('headless keeps appearance and local actions without desktop mutations',async()=>{
  let called=false;const {ctx}=setup(async()=>{called=true;});ctx.S.desktop=false;
  ctx.renderSettings();await ctx.readSettings();
  assert.equal(called,false);assert.match(ctx.settings.el.markup,/此模式没有桌面设置/);
  assert.doesNotMatch(ctx.settings.el.markup,/data-setting=/);
});
test('open popup refreshes authority state from tray and accent',async()=>{
  const {ctx}=setup(async()=>response(state({dnd:true,toastLen:'long'})));
  await ctx.readSettings();assert.equal(ctx.settings.state.dnd,true);assert.equal(ctx.settings.state.toastLen,'long');assert.equal(ctx.accent.id,'green');
});
test('a recovered settings read clears its load error but keeps write failure visible',async()=>{
  let failed=true;const {ctx}=setup(async()=>{if(failed) throw Error('offline');return response(state());});
  await ctx.readSettings();assert.match(ctx.settings.readError,/offline/);
  ctx.settings.error='write failed';failed=false;await ctx.readSettings();
  assert.equal(ctx.settings.readError,'');assert.equal(ctx.settings.error,'write failed');
});
test('old GET cannot overwrite a newer settings action',async()=>{
  const get=deferred(),post=deferred();
  const {ctx}=setup((url,opts)=>opts?.method==='POST'?post.promise:get.promise);
  const reading=ctx.readSettings();const writing=ctx.settingsAct('dnd',true);
  post.resolve(response(state({dnd:true})));await writing;
  get.resolve(response(state({dnd:false})));await reading;
  assert.equal(ctx.settings.state.dnd,true);
});
test('write failure retains actual state and visible error, without automatic resubmission',async()=>{
  let calls=0;const {ctx}=setup(async()=>{calls++;return {ok:false,json:async()=>({error:'disk full'})};});
  ctx.settings.state=state();await ctx.settingsAct('dnd',true);
  assert.equal(ctx.settings.state.dnd,false);assert.match(ctx.settings.error,/disk full/);assert.equal(ctx.settings.busy,false);assert.equal(calls,1);
  assert.match(ctx.settings.el.markup,/role="alert"/);
});
test('setting writes follow boolean or value HTTP contract without mixing fields',async()=>{
  const sent=[];const {ctx}=setup(async(url,opts)=>{sent.push(JSON.parse(opts.body));return response(state());});
  await ctx.settingsAct('dnd',true);await ctx.settingsAct('sound',true,'chime');
  assert.deepEqual(sent,[{name:'dnd',on:true},{name:'sound',val:'chime'}]);
});
test('late response after close cannot repopulate popup state',async()=>{
  const get=deferred();const {ctx,trigger}=setup(()=>get.promise);
  const reading=ctx.readSettings();ctx.closeSettings(true);
  get.resolve(response(state({dnd:true})));await reading;
  assert.equal(ctx.settings.el,null);assert.equal(ctx.settings.state,null);assert.equal(trigger.focused,true);
});
test('system appearance follows OS changes while explicit mode stays fixed',()=>{
  const {ctx,events,saved}=setup();ctx.changeAppearance('light');assert.equal(ctx.S.theme,'light');
  ctx.systemAppearance.matches=false;events.system();assert.equal(ctx.S.theme,'light');
  ctx.changeAppearance('system');assert.equal(ctx.S.theme,'dark');ctx.systemAppearance.matches=true;events.system();assert.equal(ctx.S.theme,'light');
  assert.equal(saved.appearance,'system');ctx.changeAppearance('unknown');assert.equal(ctx.S.appearance,'system');
});
test('Escape returns trigger focus; open picker gets first Escape',()=>{
  const {ctx,events,trigger}=setup();let stopped=false;
  const escape={key:'Escape',preventDefault(){},stopImmediatePropagation(){stopped=true;}};
  ctx.pk.el={};events.keydown[0](escape);assert.ok(ctx.settings.el);assert.equal(stopped,false);
  ctx.pk.el=null;events.keydown[0](escape);assert.equal(ctx.settings.el,null);assert.equal(trigger.focused,true);assert.equal(stopped,true);
});
test('reduced motion and keyboard opening omit the popup animation',()=>{
  for(const [reduced,keyboard] of [[false,false],[true,false],[false,true]]){
    const {ctx}=setup();ctx.S.desktop=false;ctx.closeSettings();ctx.RM.matches=reduced;ctx.openSettings(keyboard);
    assert.equal(!!ctx.settings.el.animation,!reduced&&!keyboard);
    if(ctx.settings.el.animation) assert.equal(ctx.settings.el.animation.options.duration,180);
  }
});
test('browser waits for a restarted host before changing language and keeps session hash',()=>{
  const {ctx}=setup();ctx.syncSettingsLanguage(state({restart:true,effectiveLang:'en'}));
  assert.equal(ctx.reloaded,undefined);assert.equal(ctx.settings.restartLang,'en');
  ctx.syncSettingsLanguage(state({effectiveLang:'zh'}));assert.equal(ctx.reloaded,undefined);
  ctx.syncSettingsLanguage(state({effectiveLang:'en'}));
  assert.match(ctx.reloaded,/lang=en/);assert.match(ctx.reloaded,/#s_test$/);assert.equal(ctx.settings.restartLang,'');
});
test('unconfirmed language restart times out with error and blocks duplicate writes while waiting',async()=>{
  let calls=0;const {ctx}=setup(async()=>{calls++;throw Error('offline');});
  ctx.syncSettingsLanguage(state({restart:true,effectiveLang:'en'}));
  await ctx.settingsAct('lang',true,'en');assert.equal(calls,0);
  ctx.settings.restartUntil=0;await ctx.readSettings();
  assert.equal(ctx.settings.restartLang,'');assert.match(ctx.settings.error,/尚未确认重启完成/);
});
test('native panel waits for its existing restart flow',()=>{
  const {ctx}=setup();ctx.syncSettingsLanguage(state({effectiveLang:'en'}));
  assert.match(ctx.reloaded,/lang=en/);assert.match(ctx.reloaded,/#s_test$/);
  ctx.reloaded=null;ctx.WIN=true;ctx.syncSettingsLanguage(state({effectiveLang:'en'}));assert.equal(ctx.reloaded,null);
});
