const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const html=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function setup(bindings={}){
  const calls=[],events=new Map(),frames=new Map(),values=[];let frameId=0;
  const surface=name=>({addEventListener(type,fn){const key=`${name}:${type}`;events.set(key,[...(events.get(key)||[]),fn]);},
    removeEventListener(type,fn){const key=`${name}:${type}`;events.set(key,(events.get(key)||[]).filter(f=>f!==fn));}});
  const grip={dataset:{resize:'left'},closest:()=>({}),capture:false,setPointerCapture(){this.capture=true;},hasPointerCapture(){return this.capture;},releasePointerCapture(){this.capture=false;}};
  const doc={...surface('document'),documentElement:{style:{setProperty:(key,value)=>values.push(value)},setAttribute(){},removeAttribute(){}}};
  const win={...surface('window'),tickResizeStart:async()=>{calls.push('start');return 340;},
    tickResizeStep:async()=>{calls.push('step');return 480;},tickResizeEnd:async()=>{calls.push('end');return 500;},...bindings};
  const ctx={document:doc,window:win,NATIVE:true,S:{dragging:false,floatWidth:340},performance:{now:()=>0},
    requestAnimationFrame:fn=>{const id=++frameId;frames.set(id,fn);return id;},cancelAnimationFrame:id=>frames.delete(id),
    tipHide(){},pickClose(){},schedule(){ctx.renders=(ctx.renders||0)+1;},toast:s=>{ctx.error=s;},esc:s=>s};
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('let stopFloatResize = null;'),html.indexOf("document.addEventListener('pointerdown', resizeFloat)")),ctx);
  const emit=(name,type,event={})=>{for(const fn of [...(events.get(`${name}:${type}`)||[])])fn(event);};
  const event={target:{closest:()=>grip},button:0,pointerId:1,preventDefault(){}};
  return {ctx,event,grip,calls,values,events,frames,emit};
}
test('only native primary-button resize starts a gesture',async()=>{
  const s=setup();s.ctx.NATIVE=false;await s.ctx.resizeFloat(s.event);s.ctx.NATIVE=true;s.event.button=2;await s.ctx.resizeFloat(s.event);
  assert.equal(s.calls.length,0);assert.equal(s.ctx.S.dragging,false);
});
test('release wakes the animation frame and applies native final width once',async()=>{
  const s=setup();const running=s.ctx.resizeFloat(s.event);await settle();
  assert.equal(s.ctx.S.dragging,true);assert.equal(s.ctx.S.floatWidth,480);assert.equal(s.grip.capture,true);
  s.emit('document','pointerup',{pointerId:1});await running;
  assert.deepEqual(s.calls,['start','step','end']);assert.equal(s.ctx.S.floatWidth,500);assert.equal(s.ctx.S.dragging,false);
  assert.equal(s.grip.capture,false);assert.equal(s.frames.size,0);assert.equal(s.ctx.renders,1);
  assert.ok([...s.events.values()].every(list=>list.length===0));
});
test('quick release during Start still sends End after Start completes',async()=>{
  const start=deferred();const s=setup({tickResizeStart:()=>{s.calls.push('start');return start.promise;}});
  const running=s.ctx.resizeFloat(s.event);s.emit('document','pointercancel',{pointerId:1});start.resolve(340);await running;
  assert.deepEqual(s.calls,['start','end']);assert.equal(s.ctx.S.dragging,false);
});
test('an in-flight Step cannot overwrite End after pointer release',async()=>{
  const step=deferred();const s=setup({tickResizeStep:()=>{s.calls.push('step');return step.promise;}});
  const running=s.ctx.resizeFloat(s.event);await settle();s.emit('document','pointerup',{pointerId:1});
  assert.deepEqual(s.calls,['start','step']);step.resolve(620);await running;
  assert.deepEqual(s.calls,['start','step','end']);assert.equal(s.values.includes('620px'),false);assert.equal(s.values.at(-1),'500px');
});
test('blur finishes even when requestAnimationFrame is no longer running',async()=>{
  const s=setup();const running=s.ctx.resizeFloat(s.event);await settle();s.emit('window','blur');await running;
  assert.equal(s.ctx.S.dragging,false);assert.equal(s.calls.at(-1),'end');
});
test('a host mode change stops an in-flight gesture before temporary-mode sizing',async()=>{
  const step=deferred();const s=setup({tickResizeStep:()=>step.promise});const running=s.ctx.resizeFloat(s.event);await settle();
  vm.runInContext('stopFloatResize()',s.ctx);step.resolve(300);await running;
  assert.equal(s.values.includes('300px'),false);assert.equal(s.ctx.S.dragging,false);assert.equal(vm.runInContext('stopFloatResize',s.ctx),null);
});
test('Start failure clears input capture without ending an unowned gesture',async()=>{
  const s=setup({tickResizeStart:async()=>{throw Error('already dragging');}});await s.ctx.resizeFloat(s.event);
  assert.equal(s.calls.length,0);assert.equal(s.grip.capture,false);assert.equal(s.ctx.S.dragging,false);assert.match(s.ctx.error,/already dragging/);
});
test('persistence failure is visible and never leaves resizing active',async()=>{
  const s=setup({tickResizeEnd:async()=>{throw Error('disk full');}});const running=s.ctx.resizeFloat(s.event);await settle();
  s.emit('document','lostpointercapture',{pointerId:1});await running;
  assert.match(s.ctx.error,/disk full/);assert.equal(s.ctx.S.dragging,false);assert.ok([...s.events.values()].every(list=>list.length===0));
});
test('only this pointer ends the gesture and tiny-work-area width remains usable',async()=>{
  const s=setup();const running=s.ctx.resizeFloat(s.event);await settle();s.emit('document','pointerup',{pointerId:2});
  assert.equal(s.ctx.S.dragging,true);s.emit('document','pointerup',{pointerId:1});await running;
  s.ctx.applyFloatWidth(240);assert.equal(s.ctx.S.floatWidth,240);s.ctx.applyFloatWidth(NaN);assert.equal(s.ctx.S.floatWidth,240);
});
