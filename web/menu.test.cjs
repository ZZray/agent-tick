const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
function setup() {
  const events = {}, rootEvents = {}, frames = [], fits = [];
  const parts = {'.mn':{offsetWidth:300,offsetHeight:350,focus(){},blur(){}},'.mn-h':{offsetHeight:62},'.mn-content':{offsetHeight:680},'.mn-f':{offsetHeight:38}};
  let observe, closed=0, tips=0, renders=0;
  const ctx = {
    root:{querySelector:s=>parts[s],addEventListener:(type,fn)=>rootEvents[type]=fn},
    document:{addEventListener:(type,fn)=>events[type]=fn}, S:{}, RM:{matches:true}, PICK:{},
    window:{tickMenuReady(){},tickMenuFit:(w,h)=>fits.push([w,h]),tickMenuClose(){}},
    requestAnimationFrame:fn=>{frames.push(fn);return frames.length;},
    ResizeObserver:class {constructor(fn){observe=fn;}observe(){}},
    render(){renders++;},pickClose(){closed++;},tipHide(){tips++;},setTimeout(){},
  };
  vm.createContext(ctx);
  vm.runInContext(html.slice(html.indexOf('function startMenu()'),html.indexOf('function renderMenu()')),ctx);
  ctx.startMenu();
  return {ctx,parts,events,rootEvents,fits,observe:()=>observe(),flush:()=>{frames.splice(0).forEach(fn=>fn());},counts:()=>({closed,tips,renders})};
}
test('native fit uses natural content height even when shell is capped',()=>{
  const a=setup(); a.ctx.window.tickMenuOpen({version:'test'});a.observe();a.flush();
  assert.deepEqual(a.fits,[[300,780]]);
  a.parts['.mn-content'].offsetHeight=220;
  a.events.toggle({target:{matches:()=>true,open:false}});a.flush();
  assert.deepEqual(a.fits.at(-1),[300,320]);
  assert.equal(a.ctx.S.menuDisplay,false);
});
test('menu reopen reports unchanged size and repeated resize is coalesced',()=>{
  const a=setup();a.ctx.window.tickMenuOpen({});a.flush();
  a.ctx.window.tickMenuOpen({});a.observe();a.observe();a.flush();
  assert.equal(a.fits.length,2);
});
