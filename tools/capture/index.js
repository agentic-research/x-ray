const puppeteer = require('puppeteer');
const fs = require('fs');
const path = require('path');

async function capture(url, name) {
  console.log(`Capturing ${name} from ${url}...`);
  const browser = await puppeteer.launch({ headless: 'new' });
  const page = await browser.newPage();

  // Set viewport to a standard desktop size
  await page.setViewport({ width: 1280, height: 800 });

  // Go to the page and wait for it to fully load
  await page.goto(url, { waitUntil: 'networkidle2', timeout: 60000 });

  // Inject Mache IDs and sanitize the DOM
  const rawData = await page.evaluate(() => {
    let idCounter = 0;

    // Inject IDs
    const interactiveNodes = document.querySelectorAll('a, button, input, select, textarea, [role="button"]');
    interactiveNodes.forEach(node => {
      if (!node.hasAttribute('data-mache-id')) {
        const id = `mache-${idCounter++}`;
        node.setAttribute('data-mache-id', id);
      }
    });

    // Sanitize DOM (remove scripts, styles, huge SVGs)
    const clone = document.documentElement.cloneNode(true);

    // Remove scripts and styles
    const scripts = clone.querySelectorAll('script, style, link[rel="stylesheet"], noscript');
    scripts.forEach(s => s.remove());

    // Simplify SVGs to save tokens
    const svgs = clone.querySelectorAll('svg');
    svgs.forEach(svg => {
      const replacement = document.createElement('span');
      replacement.textContent = '[SVG Icon]';
      if (svg.hasAttribute('data-mache-id')) {
        replacement.setAttribute('data-mache-id', svg.getAttribute('data-mache-id'));
      }
      svg.replaceWith(replacement);
    });

    // Aggressive attribute stripping to reduce token count for LLM
    const allNodes = clone.querySelectorAll('*');
    allNodes.forEach(node => {
      if (node.attributes) {
        for (let i = node.attributes.length - 1; i >= 0; i--) {
          const attr = node.attributes[i].name;
          // Keep ONLY structural/semantic attributes
          if (!['data-mache-id', 'id', 'href', 'role', 'type', 'name', 'value'].includes(attr)) {
            node.removeAttribute(attr);
          }
        }
      }
    });

    // Generate a flattened summary for the LLM (drastically smaller than raw HTML)
    let summary = "Interactive Elements:\n";
    const summaryNodes = clone.querySelectorAll('[data-mache-id]');
    let count = 0;
    summaryNodes.forEach(node => {
      if (count >= 300) return; // Cap at 300 elements to prevent context/latency explosion
      let text = (node.textContent || '').replace(/\\s+/g, ' ').trim().substring(0, 60);
      if (!text && node.tagName.toLowerCase() === 'input') text = node.placeholder || node.name || 'input';
      summary += `ID: ${node.getAttribute('data-mache-id')} | Tag: ${node.tagName.toLowerCase()} | Text: "${text}"\n`;
      count++;
    });

    return { html: clone.outerHTML, summary: summary };
  });

  // Take a screenshot
  const outDir = path.join(__dirname, '../../testdata', name);
  if (!fs.existsSync(outDir)) {
    fs.mkdirSync(outDir, { recursive: true });
  }

  const pngPath = path.join(outDir, 'page.png');
  const htmlPath = path.join(outDir, 'page.html');
  const summaryPath = path.join(outDir, 'page_summary.txt');

  await page.screenshot({ path: pngPath, fullPage: false }); // Just capture the viewport (what the user sees)
  fs.writeFileSync(htmlPath, rawData.html, 'utf8');
  fs.writeFileSync(summaryPath, rawData.summary, 'utf8');

  console.log(`Saved ${name} to ${outDir}`);
  await browser.close();
}

async function main() {
  const sites = [
    { name: 'hackernews', url: 'https://news.ycombinator.com/' },
    { name: 'github', url: 'https://github.com/trending' },
    { name: 'wikipedia', url: 'https://en.wikipedia.org/wiki/Main_Page' },
    { name: 'lobsters', url: 'https://lobste.rs/' },
    { name: 'ecommerce', url: 'https://www.ebay.com/b/Apple-iPhone/9355/bn_319682' }
  ];

  for (const site of sites) {
    try {
      await capture(site.url, site.name);
    } catch (e) {
      console.error(`Failed to capture ${site.name}:`, e);
    }
  }
}

main();
