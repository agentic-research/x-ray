const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const SITES = [
  { name: 'reddit', url: 'https://old.reddit.com/r/programming' },
  { name: 'ecommerce', url: 'https://www.amazon.com' },
  { name: 'youtube', url: 'https://www.youtube.com/results?search_query=minecraft+java+modding' },
];

const CAPTURE_SCRIPT = `(() => {
  const lines = [];
  let idx = 0;
  const eMap = new Map(), pMap = new Map();
  const vw = window.innerWidth, vh = window.innerHeight;
  const sel = 'a[href],button,input,select,textarea,[role="button"],[role="link"],[role="tab"],[onclick],[tabindex],h1,h2,h3,h4,h5,h6,img[alt],nav,main,header,footer,aside,section,article,form';
  const els = document.querySelectorAll(sel);
  const all = new Set();
  els.forEach(el => { all.add(el); let p=el.parentElement,d=0; while(p&&d<15){if(['nav','main','header','footer','aside','section','article','form','ul','ol','div'].includes(p.tagName.toLowerCase()))all.add(p);p=p.parentElement;d++;}});
  const sorted = Array.from(all).sort((a,b)=>{const p=a.compareDocumentPosition(b);return(p&Node.DOCUMENT_POSITION_FOLLOWING)?-1:(p&Node.DOCUMENT_POSITION_PRECEDING)?1:0;});
  sorted.forEach(el=>eMap.set(el,'mache-'+idx++));
  sorted.forEach(el=>{const id=eMap.get(el);let p=el.parentElement;while(p){if(eMap.has(p)){pMap.set(id,eMap.get(p));break;}p=p.parentElement;}});
  lines.push('Interactive Elements:');
  sorted.forEach(el=>{
    const id=eMap.get(el),pid=pMap.get(id)||'none',tag=el.tagName.toLowerCase();
    const rm={'a':'link','button':'button','input':'textbox','select':'combobox','textarea':'textbox','h1':'heading','h2':'heading','h3':'heading','img':'img','nav':'navigation','main':'main','header':'banner','footer':'contentinfo','aside':'complementary','section':'region','article':'article','form':'form'};
    let role=el.getAttribute('role')||rm[tag]||tag;
    let text=el.getAttribute('aria-label')||'';
    if(!text&&(tag==='input'||tag==='textarea'))text=el.getAttribute('type')||el.getAttribute('placeholder')||'';
    else if(!text&&tag==='img')text=el.getAttribute('alt')||'';
    else if(!text){const dt=Array.from(el.childNodes).filter(n=>n.nodeType===3).map(n=>n.textContent.trim()).join(' ');text=dt||(el.textContent?.trim()||'');}
    if(text.length>80)text=text.slice(0,77)+'...';
    const r=el.getBoundingClientRect();
    const bounds=(r.left/vw).toFixed(4)+','+(r.top/vh).toFixed(4)+','+(r.width/vw).toFixed(4)+','+(r.height/vh).toFixed(4);
    let css='';try{const p=[];let c=el;for(let d=0;d<4&&c&&c!==document.body;d++){let s=c.tagName.toLowerCase();if(c.className&&typeof c.className==='string'){const cl=c.className.split(/\\s+/).filter(x=>x&&x.length<40).slice(0,2).join('.');if(cl)s+='.'+cl;}p.unshift(s);c=c.parentElement;}css=p.join(' > ');}catch(e){}
    lines.push('ID: '+id+' | Parent: '+pid+' | Tag: '+tag+' | Role: '+role+' | Text: "'+text+'" | Bounds: '+bounds+' | Path: '+css);
  });
  return lines.join('\\n');
})()`;

async function main() {
  const browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
  const page = await ctx.newPage();

  for (const site of SITES) {
    const dir = path.join('testdata', site.name);
    fs.mkdirSync(dir, { recursive: true });
    try {
      console.log(`Capturing ${site.name}...`);
      await page.goto(site.url, { waitUntil: 'networkidle', timeout: 20000 }).catch(() => {});
      await page.waitForTimeout(3000);
      await page.screenshot({ fullPage: false, path: path.join(dir, 'page.png'), type: 'png' });
      const summary = await page.evaluate(CAPTURE_SCRIPT);
      fs.writeFileSync(path.join(dir, 'page_summary.txt'), summary);
      const count = summary.split('\n').length - 1;
      console.log(`  ✅ ${site.name}: ${count} elements`);
    } catch (e) {
      console.log(`  ❌ ${site.name}: ${e.message?.slice(0, 80)}`);
    }
  }
  await browser.close();
}
main().catch(console.error);
