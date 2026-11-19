const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname,'index.html'),'utf8');

// 取出 startMarquee 及其常量，用假 DOM 跑完整个播放队列
function setup({rm=true,manual=false}={}) {
  const added=[], calls=[], handlers={}, timers=[];
  const el = () => ({style:{setProperty(){}}, className:'', innerHTML:'', clientWidth:900, scrollWidth:400,
    firstChild:null, addEventListener(t,fn){handlers[t]=fn;}, appendChild(c){added.push(c);this.firstChild=c;}, querySelector:()=>el(), querySelectorAll:()=>[], remove(){}, replaceChildren(){this.firstChild=null;},
    getAnimations:()=>[], animate:()=>({finished:Promise.resolve(),finish(){},pause(){calls.push('PAUSED');},play(){},cancel(){},playState:'finished'}), getBoundingClientRect:()=>({left:0,right:900,top:0,height:76})});
  const ctx={
    root:el(), RM:{matches:rm}, TOAST_TTL:{done:9000,waiting:30000,failed:12000,blocked:30000},
    esc:v=>String(v), firstLine:t=>String(t||'').split('\n')[0],
    document:{createElement:()=>({...el(),getContext:()=>({setTransform(){},clearRect(){}})}), documentElement:{dataset:{}}},
    addEventListener(){}, innerWidth:1000, innerHeight:140, devicePixelRatio:1,
    requestAnimationFrame:()=>1, getComputedStyle:()=>({transform:'none'}), DOMMatrix:class{constructor(){this.m41=0;}},
    window:{tickMarqueeReady(){calls.push('ready');}, tickMarqueeIdle(){calls.push('idle');}, tickMarqueeOpen(sid){calls.push('open:'+sid);}}, performance:{now:()=>Date.now()},
    setTimeout:(fn)=>{ if(manual){timers.push(fn);return timers.length;} fn();return 0;}, clearTimeout:id=>{ if(manual&&id>0) timers[id-1]=null; }, console,
  };
  vm.createContext(ctx);
  const from=html.indexOf('const MQ_COLOR'), to=html.indexOf('// 托盘悬停预览（view=peek）');
  vm.runInContext(html.slice(from,to).replace(/\/\/ =+\s*$/m,''),ctx);
  ctx.startMarquee();
  return {ctx,calls,added,handlers,timers};
}

test('marquee reports ready, plays queued cards one by one and tells the host when idle', async () => {
  const {ctx,calls} = setup();
  assert.deepEqual(calls,['ready']);
  ctx.window.tickMarqueeAdd({kind:'done',agent:'Claude',text:'做完了',sub:'x',ttl:9000});
  await new Promise(r=>setImmediate(r));
  for (let i=0;i<20 && !calls.includes('idle');i++) await new Promise(r=>setImmediate(r));
  assert.deepEqual(calls,['ready','idle']);
});

test('marquee queue keeps at most 4 pending cards (drops oldest)', async () => {
  const {ctx,calls,added} = setup();
  for (let i=0;i<7;i++) ctx.window.tickMarqueeAdd({kind:'waiting',agent:'A'+i,text:'t',ttl:30000});
  for (let i=0;i<40 && !calls.includes('idle');i++) await new Promise(r=>setImmediate(r));
  assert.equal(calls.filter(c=>c==='idle').length,1);
  // 第 1 条已开始播放；其余 6 条里只保留最新 4 条 → 共播放 5 条
  assert.equal(added.length,5);
});

const tick=async()=>{ for(let i=0;i<10;i++) await new Promise(r=>setImmediate(r)); };
const drain=async timers=>{ await tick(); for(let i=0;i<20;i++){ const fn=timers.find(Boolean); if(!fn) break; timers[timers.indexOf(fn)]=null; fn(); await tick(); } };

test('hovering holds the countdown (card stays) without pausing any animation; leaving resumes it', async () => {
  const {ctx,calls,handlers,timers} = setup({manual:true});
  ctx.window.tickMarqueeAdd({kind:'done',agent:'A',text:'t',sid:'s_1',ttl:9000});
  await tick();
  assert.equal(timers.filter(Boolean).length,1); // 减少动态分支：停留倒计时在走
  handlers.pointerenter();
  assert.equal(timers.filter(Boolean).length,0); // 悬停：倒计时被清掉
  await tick();
  assert.ok(!calls.includes('idle'));
  handlers.pointerleave();
  assert.equal(timers.filter(Boolean).length,1); // 移开：接着走
  await drain(timers);
  assert.deepEqual(calls,['ready','idle']); // 且没有任何动画被 pause
});

test('clicking opens the session of the card, dismisses it and drops the queue', async () => {
  const {ctx,calls,handlers,timers} = setup({manual:true});
  ctx.window.tickMarqueeAdd({kind:'waiting',agent:'A',text:'t',sid:'s_9',ttl:30000});
  ctx.window.tickMarqueeAdd({kind:'done',agent:'B',text:'t',sid:'s_2',ttl:9000});
  await tick();
  handlers.click();
  assert.deepEqual(calls,['ready','idle','open:s_9']);
  await drain(timers);
  assert.ok(!calls.includes('open:s_2')); // 队列被清掉，没有继续播第二条
  assert.equal(calls.filter(c=>c==='idle').length,2); // 点击收起一次 + 被中止的 pump 收尾一次
});

test('clicking a card without a session id only dismisses', async () => {
  const {ctx,calls,handlers} = setup({manual:true});
  ctx.window.tickMarqueeAdd({kind:'done',agent:'A',text:'t',ttl:9000});
  await tick();
  handlers.click();
  assert.ok(!calls.some(c=>String(c).startsWith('open:')));
  assert.ok(calls.includes('idle'));
});

test('marquee is wired into tray menu, settings page and the settings API list', () => {
  assert.ok(html.includes("sw('marquee'"));
  assert.ok(html.includes("toggle('marquee'"));
  assert.ok(html.includes("'notificationbadge','marquee'].includes(name)"));
  assert.ok(html.includes("if (MARQUEE)"));
});
