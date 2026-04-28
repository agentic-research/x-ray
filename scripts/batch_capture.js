// scripts/batch_capture.js — Capture rich testdata for multiple sites via Playwright
// Usage: npx playwright test scripts/batch_capture.js
//    or: node scripts/batch_capture.js (with playwright installed)

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const SITES = [
  { name: 'hackernews', url: 'https://news.ycombinator.com' },
  { name: 'lobsters', url: 'https://lobste.rs' },
  { name: 'github', url: 'https://github.com' },
  { name: 'wikipedia', url: 'https://en.wikipedia.org/wiki/Donkey_Kong' },
  { name: 'reddit', url: 'https://old.reddit.com' },
  { name: 'ecommerce', url: 'https://www.ebay.com' },
  { name: 'youtube', url: 'https://www.youtube.com' },
  { name: 'docs_mdn', url: 'https://developer.mozilla.org/en-US/docs/Web/JavaScript' },
  { name: 'search_ddg', url: 'https://duckduckgo.com/?q=minecraft+java+modding' },
  { name: 'stackoverflow', url: 'https://stackoverflow.com/questions/tagged/minecraft-forge' },
];

const CAPTURE_SCRIPT = `(() => {
  const lines = [];
  let macheIdx = 0;
  const elementMap = new Map();
  const parentMap = new Map();
  const vw = window.innerWidth || document.documentElement.clientWidth;
  const vh = window.innerHeight || document.documentElement.clientHeight;
  const selector = 'a[href],button,input,select,textarea,[role="button"],[role="link"],[role="tab"],[role="menuitem"],[onclick],[tabindex],h1,h2,h3,h4,h5,h6,img[alt],video,audio,nav,main,header,footer,aside,section,article,form';
  const elements = document.querySelectorAll(selector);
  const allElements = new Set();
  elements.forEach(el => {
    allElements.add(el);
    let p = el.parentElement, d = 0;
    while (p && d < 15) {
      if (['nav','main','header','footer','aside','section','article','form','ul','ol','div'].includes(p.tagName.toLowerCase())) allElements.add(p);
      p = p.parentElement; d++;
    }
  });
  const sorted = Array.from(allElements).sort((a, b) => {
    const pos = a.compareDocumentPosition(b);
    return (pos & Node.DOCUMENT_POSITION_FOLLOWING) ? -1 : (pos & Node.DOCUMENT_POSITION_PRECEDING) ? 1 : 0;
  });
  sorted.forEach(el => elementMap.set(el, 'mache-' + macheIdx++));
  sorted.forEach(el => {
    const myId = elementMap.get(el);
    let p = el.parentElement;
    while (p) { if (elementMap.has(p)) { parentMap.set(myId, elementMap.get(p)); break; } p = p.parentElement; }
  });
  lines.push('Interactive Elements:');
  sorted.forEach(el => {
    const id = elementMap.get(el), parentId = parentMap.get(id) || 'none', tag = el.tagName.toLowerCase();
    const rm = {'a':'link','button':'button','input':'textbox','select':'combobox','textarea':'textbox','h1':'heading','h2':'heading','h3':'heading','h4':'heading','h5':'heading','h6':'heading','img':'img','nav':'navigation','main':'main','header':'banner','footer':'contentinfo','aside':'complementary','section':'region','article':'article','form':'form'};
    let role = el.getAttribute('role') || rm[tag] || tag;
    let text = el.getAttribute('aria-label') || '';
    if (!text && (tag==='input'||tag==='textarea')) text = el.getAttribute('type')||el.getAttribute('placeholder')||'';
    else if (!text && tag==='img') text = el.getAttribute('alt')||'';
    else if (!text) { const dt = Array.from(el.childNodes).filter(n=>n.nodeType===3).map(n=>n.textContent.trim()).join(' '); text = dt || (el.textContent?.trim()||''); }
    if (text.length > 80) text = text.slice(0,77)+'...';
    const r = el.getBoundingClientRect();
    const bounds = (r.left/vw).toFixed(4)+','+(r.top/vh).toFixed(4)+','+(r.width/vw).toFixed(4)+','+(r.height/vh).toFixed(4);
    let cssPath = '';
    try { const parts = []; let c = el; for (let d=0; d<4&&c&&c!==document.body; d++) { let s=c.tagName.toLowerCase(); if(c.className&&typeof c.className==='string'){const cls=c.className.split(/\\s+/).filter(x=>x&&x.length<40).slice(0,2).join('.'); if(cls)s+='.'+cls;} parts.unshift(s); c=c.parentElement; } cssPath=parts.join(' > '); } catch(e){}
    lines.push('ID: '+id+' | Parent: '+parentId+' | Tag: '+tag+' | Role: '+role+' | Text: "'+text+'" | Bounds: '+bounds+' | Path: '+cssPath);
  });
  return lines.join('\\n');
})()`;

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
  const page = await context.newPage();

  for (const site of SITES) {
    const dir = path.join('testdata', site.name);
    fs.mkdirSync(dir, { recursive: true });

    try {
      console.log(`Capturing ${site.name} (${site.url})...`);
      await page.goto(site.url, { waitUntil: 'networkidle', timeout: 20000 }).catch(() => {});
      await page.waitForTimeout(2000);

      // Screenshot
      await page.screenshot({ fullPage: true, path: path.join(dir, 'page.png'), type: 'png' });

      // Rich summary
      const summary = await page.evaluate(CAPTURE_SCRIPT);
      fs.writeFileSync(path.join(dir, 'page_summary.txt'), summary);

      const count = summary.split('\n').length - 1;
      console.log(`  ✅ ${site.name}: ${count} elements, screenshot saved`);
    } catch (e) {
      console.log(`  ❌ ${site.name}: ${e.message?.slice(0, 100)}`);
    }
  }

  await browser.close();
  console.log('\nDone! Run: GOWORK=off go run ./cmd/album');
}

main().catch(console.error);
