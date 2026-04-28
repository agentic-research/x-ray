// Playwright script to extract rich DOM summary from a page.
// Returns the same format as x-ray's live CDP pipeline:
// ID: mache-N | Parent: mache-M | Tag: a | Role: link | Text: "..." | Bounds: x,y,w,h | Path: css>path
//
// Usage: loaded by capture_testdata.js per-site

async (page) => {
  // Wait for page to be reasonably loaded
  await page.waitForLoadState('networkidle').catch(() => {});

  const summary = await page.evaluate(() => {
    const lines = [];
    let macheIdx = 0;
    const elementMap = new Map(); // DOM element -> mache-id
    const parentMap = new Map();  // mache-id -> parent mache-id

    // Get viewport dimensions for normalization
    const vw = window.innerWidth || document.documentElement.clientWidth;
    const vh = window.innerHeight || document.documentElement.clientHeight;

    // Selectors for interactive/meaningful elements
    const selector = [
      'a[href]', 'button', 'input', 'select', 'textarea',
      '[role="button"]', '[role="link"]', '[role="tab"]',
      '[role="menuitem"]', '[role="checkbox"]', '[role="radio"]',
      '[onclick]', '[tabindex]',
      'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
      'img[alt]', 'video', 'audio',
      'nav', 'main', 'header', 'footer', 'aside', 'section', 'article', 'form'
    ].join(', ');

    const elements = document.querySelectorAll(selector);

    // First pass: assign mache IDs to all elements + structural ancestors
    const allElements = new Set();
    elements.forEach(el => {
      allElements.add(el);
      // Also capture structural ancestors
      let parent = el.parentElement;
      let depth = 0;
      while (parent && depth < 15) {
        const tag = parent.tagName.toLowerCase();
        if (['nav', 'main', 'header', 'footer', 'aside', 'section', 'article', 'form', 'ul', 'ol', 'div'].includes(tag)) {
          allElements.add(parent);
        }
        parent = parent.parentElement;
        depth++;
      }
    });

    // Sort by document order
    const sorted = Array.from(allElements).sort((a, b) => {
      const pos = a.compareDocumentPosition(b);
      if (pos & Node.DOCUMENT_POSITION_FOLLOWING) return -1;
      if (pos & Node.DOCUMENT_POSITION_PRECEDING) return 1;
      return 0;
    });

    // Assign IDs
    sorted.forEach(el => {
      elementMap.set(el, `mache-${macheIdx++}`);
    });

    // Build parent mapping
    sorted.forEach(el => {
      const myId = elementMap.get(el);
      let parent = el.parentElement;
      while (parent) {
        if (elementMap.has(parent)) {
          parentMap.set(myId, elementMap.get(parent));
          break;
        }
        parent = parent.parentElement;
      }
    });

    // Generate summary lines
    lines.push('Interactive Elements:');

    sorted.forEach(el => {
      const id = elementMap.get(el);
      const parentId = parentMap.get(id) || 'none';
      const tag = el.tagName.toLowerCase();

      // Role
      let role = el.getAttribute('role') || '';
      if (!role) {
        const roleMap = {
          'a': 'link', 'button': 'button', 'input': 'textbox',
          'select': 'combobox', 'textarea': 'textbox',
          'h1': 'heading', 'h2': 'heading', 'h3': 'heading',
          'h4': 'heading', 'h5': 'heading', 'h6': 'heading',
          'img': 'img', 'nav': 'navigation', 'main': 'main',
          'header': 'banner', 'footer': 'contentinfo',
          'aside': 'complementary', 'section': 'region',
          'article': 'article', 'form': 'form',
          'video': 'video', 'audio': 'audio'
        };
        role = roleMap[tag] || tag;
      }

      // Text
      let text = '';
      if (el.getAttribute('aria-label')) {
        text = el.getAttribute('aria-label');
      } else if (tag === 'input' || tag === 'textarea') {
        text = el.getAttribute('type') || el.getAttribute('placeholder') || '';
      } else if (tag === 'img') {
        text = el.getAttribute('alt') || '';
      } else {
        // Get direct text, not deeply nested
        const directText = Array.from(el.childNodes)
          .filter(n => n.nodeType === Node.TEXT_NODE)
          .map(n => n.textContent.trim())
          .join(' ');
        text = directText || el.textContent?.trim() || '';
      }
      // Truncate
      if (text.length > 80) text = text.slice(0, 77) + '...';

      // Bounds (normalized to viewport)
      const rect = el.getBoundingClientRect();
      const bx = (rect.left / vw).toFixed(4);
      const by = (rect.top / vh).toFixed(4);
      const bw = (rect.width / vw).toFixed(4);
      const bh = (rect.height / vh).toFixed(4);

      // CSS path (short)
      let path = '';
      try {
        const parts = [];
        let cur = el;
        for (let d = 0; d < 4 && cur && cur !== document.body; d++) {
          let sel = cur.tagName.toLowerCase();
          if (cur.className && typeof cur.className === 'string') {
            const cls = cur.className.split(/\s+/).filter(c => c && c.length < 40).slice(0, 2).join('.');
            if (cls) sel += '.' + cls;
          }
          parts.unshift(sel);
          cur = cur.parentElement;
        }
        path = parts.join(' > ');
      } catch(e) {}

      const line = `ID: ${id} | Parent: ${parentId} | Tag: ${tag} | Role: ${role} | Text: "${text}" | Bounds: ${bx},${by},${bw},${bh} | Path: ${path}`;
      lines.push(line);
    });

    return lines.join('\n');
  });

  return summary;
}
