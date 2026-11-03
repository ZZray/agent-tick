const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const source = html.slice(html.indexOf('function cvExecutionBeams()'), html.indexOf('let cvRO =')) + html.slice(html.indexOf('function edgeBeam('), html.indexOf('if (!TOAST && !MENU) setInterval'));

function scene() {
  const animations=[];
  class Element {
    constructor() { this.children=[];this.attrs=new Set();this.style={setProperty(k,v){this[k]=v;}};this.dataset={};this.offsetWidth=380;this.offsetHeight=120;this.className='';this.classList={add:c=>{this.className+=' '+c;}};this.ownerDocument=doc; }
    appendChild(el) { this.children.push(el);el.parent=this; }
    setAttribute(name) { this.attrs.add(name); }
    hasAttribute(name) { return this.attrs.has(name); }
    querySelector(selector) { return this.children.find(el=>el.className.includes(selector.includes('cv-execution-beam')?'cv-execution-beam':'edge-fx')); }
    animate(frames,options) { const a={frames,options,cancelled:false,cancel(){this.cancelled=true;},finished:Promise.resolve()};(this.animations||=[]).push(a);animations.push(a);return a; }
    getAnimations() { return [...(this.animations||[]),...this.children.flatMap(el=>el.getAnimations())]; }
    remove() { this.parent.children=this.parent.children.filter(el=>el!==this); }
  }
  const doc={hidden:false,createElement:()=>new Element(),querySelectorAll:()=>cards};
  const owner={id:'a',status:'running',items:[{id:'i',group:'module',status:'doing'}]};
  const module={id:'m',ownerId:'a',parentId:'a',focusLeaf:true,items:owner.items};
  const cards=['a','m'].map(id=>{const e=new Element();e.dataset.cvid=id;return e;});
  const ctx={document:doc,CV:{nodes:new Map([['a',owner],['m',module]])},S:{hostHidden:false,sessions:new Map([['a',owner]])},RM:{matches:false},isDormant:()=>false,getComputedStyle:()=>({borderRadius:'16px'})};
  vm.createContext(ctx);vm.runInContext(source,ctx);
  return {ctx,owner,module,cards,animations,sync:()=>ctx.cvExecutionBeams()};
}

test('execution beam stays on the doing leaf, survives refresh, and stops on completion or exit',()=>{
  const s=scene();s.sync();assert.equal(s.cards[0].children.length,0);assert.equal(s.cards[1].children.length,1);
  const fx=s.cards[1].children[0];s.sync();assert.equal(s.cards[1].children[0],fx);assert.equal(s.animations.length,1);
  assert.equal(s.animations[0].options.duration,6000);assert.equal(s.animations[0].options.iterations,Infinity);assert.equal(s.animations[0].options.easing,'linear');
  s.module.items[0].status='done';s.sync();assert.equal(s.cards[1].children.length,0);assert.equal(s.animations[0].cancelled,true);
  s.module.items[0].status='doing';s.sync();s.cards[1].attrs.add('data-leaving');s.ctx.CV.nodes.delete('m');s.sync();
  assert.equal(s.cards[1].children.length,0,'leaving cards are cleaned even after projection removes their node');
});

test('reduced motion, hidden host/document, pause and waiting stop active beams',()=>{
  const s=scene();
  for(const [object,key,value] of [[s.ctx.RM,'matches',true],[s.ctx.document,'hidden',true],[s.ctx.S,'hostHidden',true],[s.owner,'status','paused'],[s.owner,'status','waiting'],[s.owner,'status','blocked']]){
    s.sync();assert.equal(s.cards[1].children.length,1);const before=object[key];object[key]=value;s.sync();assert.equal(s.cards[1].children.length,0);object[key]=before;
  }
  s.sync();assert.equal(s.cards[1].children.length,1);
});

test('ungrouped doing can light a root with future modules, while a parent rollup does not duplicate child work',()=>{
  const s=scene();s.module.items=[{status:'todo'}];s.sync();assert.equal(s.cards[0].children.length,1);assert.equal(s.cards[1].children.length,0);
  s.ctx.CV.nodes.set('child',{id:'child',parentId:'a',status:'running',items:[{status:'doing'}]});s.sync();assert.equal(s.cards[0].children.length,0);
});

test('notification edgeBeam retains its existing one-shot behavior',async()=>{
  const s=scene();s.ctx.edgeBeam(s.cards[0],'orange');assert.equal(s.animations.length,2);assert.equal(s.animations[1].options.duration,1800);assert.equal(s.animations[1].options.iterations,undefined);
  await Promise.resolve();assert.equal(s.cards[0].children.length,0);
});
