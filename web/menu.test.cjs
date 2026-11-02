const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
function setup(sub=false) {
  const events={}, rootEvents={}, frames=new Map(), fits=[], timers=new Map(), calls=[];
  let timerId=0, frameId=0;
  const trigger={contains:()=>false,getBoundingClientRect:()=>({top:282}),setAttribute(k,v){this[k]=v;},focus(){calls.push('parent-focus');}};
  const parts={'.mn':{offsetWidth:300,offsetHeight:350,focus(){},blur(){}},'.mn-h':{offsetHeight:62},'.mn-content':{offsetHeight:680},'.mn-f':{offsetHeight:38},'[data-menu-sub]':trigger,'.mn-list button':{focus(){calls.push('sub-focus');}}};
  const target={closest:s=>s==='[data-menu-sub]'?trigger:null};
  let observe;
  const ctx={
    root:{querySelector:s=>parts[s],addEventListener:(type,fn)=>rootEvents[type]=fn},
    document:{addEventListener:(type,fn)=>events[type]=fn}, S:{}, RM:{matches:true},PICK:{},pk:{el:null},MENU_SUB:sub,
    window:{tickMenuReady(){},tickMenuFit:(w,h)=>fits.push([w,h]),tickMenuClose(){calls.push('close');},tickMenuSubOpen:(y,f)=>calls.push(['open',y,f]),tickMenuSubClose:f=>calls.push(['sub-close',f])},
    requestAnimationFrame:fn=>{frames.set(++frameId,fn);return frameId;},cancelAnimationFrame:id=>frames.delete(id),
    ResizeObserver:class{constructor(fn){observe=fn;}observe(){}},render(){},pickClose(){},tipHide(){},
    setTimeout:fn=>{timers.set(++timerId,fn);return timerId;},clearTimeout:id=>timers.delete(id),
  };
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('function startMenu()'),html.indexOf('function renderMenu()')),ctx);
  ctx.startMenu();
  return {ctx,parts,events,rootEvents,fits,calls,target,trigger,timers,observe:()=>observe(),flush:()=>{const pending=[...frames.values()];frames.clear();pending.forEach(fn=>fn());},frames,hover:()=>{const pending=[...timers.values()];timers.clear();pending.forEach(fn=>fn());}};
}
test('hidden first open and reopen report natural size without executing an animation frame',()=>{
  const a=setup(true);
  a.ctx.window.tickMenuOpen({});
  assert.deepEqual(a.fits,[[300,780]]); // no rAF flush: a truly hidden WebView may never paint
  a.observe();assert.equal(a.frames.size,1);
  a.ctx.window.tickMenuOpen({});
  assert.deepEqual(a.fits,[[300,780],[300,780]]);
  assert.equal(a.frames.size,0); // stale hidden frame is cancelled rather than blocking the reopen
  a.flush();assert.equal(a.fits.length,2);
});
test('visible content resize remains frame-coalesced and uses natural content height',()=>{
  const a=setup(true);a.ctx.window.tickMenuOpen({});
  a.parts['.mn-content'].offsetHeight=220;
  a.observe();a.observe();a.observe();
  assert.equal(a.frames.size,1);assert.equal(a.fits.length,1);
  a.flush();assert.deepEqual(a.fits,[[300,780],[300,320]]);
});
test('short hover cancels, deliberate hover opens without focus, crossing to submenu does not close',()=>{
  const a=setup();const e={target:a.target,relatedTarget:null};
  a.rootEvents.pointerover(e);a.rootEvents.pointerout(e);a.hover();assert.deepEqual(a.calls,[]);
  a.rootEvents.pointerover(e);a.hover();assert.deepEqual(a.calls,[['open',282,false]]);
  a.ctx.window.tickMenuSubState(true,false);a.rootEvents.pointerout(e);a.rootEvents.pointerover(e);a.hover();
  assert.equal(a.calls.length,1);assert.equal(a.trigger['aria-expanded'],'true');
});
test('explicit entry requests focus; parent actions dismiss only the submenu',()=>{
  const a=setup();a.rootEvents.click({target:a.target});
  a.events.keydown({key:'ArrowRight',target:a.target,preventDefault(){}});
  a.events.keydown({key:'Enter',target:a.target,preventDefault(){}});
  assert.deepEqual(a.calls,[['open',282,true],['open',282,true],['open',282,true]]);
  a.rootEvents.pointerover({target:{closest:s=>s==='.mn-tile,.mn-f button'?{}:null}});
  assert.deepEqual(a.calls.at(-1),['sub-close',false]);
});
test('submenu Left/Escape return one native level and picker retains first Escape',()=>{
  const a=setup(true),e={key:'ArrowLeft',target:{},preventDefault(){}};a.events.keydown(e);
  a.events.keydown({...e,key:'Escape'});assert.deepEqual(a.calls,['close','close']);
  a.ctx.pk.el={};a.events.keydown({...e,key:'Escape'});assert.equal(a.calls.length,2);
  a.ctx.window.tickMenuFocus();assert.equal(a.calls.at(-1),'sub-focus');
});
test('hidden parent cancels delayed hover and return callback restores trigger focus',()=>{
  const a=setup();a.rootEvents.pointerover({target:a.target,relatedTarget:null});
  a.ctx.window.tickMenuHidden();a.hover();assert.deepEqual(a.calls,[]);
  a.ctx.window.tickMenuSubState(false,true);assert.equal(a.calls.at(-1),'parent-focus');
});
