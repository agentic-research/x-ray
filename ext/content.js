// content.js - Injected into pages to handle ID tagging and execution
//
// Uses an in-memory registry for fast element lookup. Also writes data-mache-id
// attributes to the DOM so that background.js CDP calls (AX enrichment,
// magnifying glass crop) can find tagged elements via querySelectorAll.

let idCounter = 0;
const elementRegistry = new Map();   // "mache-42" -> Element
const reverseRegistry = new Map();   // Element -> "mache-42"

// Check if an element is actually visible to the user.
// Filters out hidden search forms, collapsed modals, aria-hidden elements, etc.
function isVisible(el) {
  // Skip aria-hidden (e.g., GitHub's collapsed search modal internals)
  if (el.getAttribute('aria-hidden') === 'true') return false;
  // Check ancestors for aria-hidden (hidden subtrees)
  if (el.closest('[aria-hidden="true"]')) return false;
  // Skip CSS-hidden elements
  const style = getComputedStyle(el);
  if (style.display === 'none' || style.visibility === 'hidden') return false;
  // Skip zero-size elements (but allow small icons like notification bells)
  const rect = el.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0) return false;
  return true;
}

function buildRegistry() {
  // Incremental rebuild: don't reset idCounter or clear registries.
  // Existing elements keep their IDs across rebuilds (stable mache-IDs).
  // New elements get new IDs. Stale elements are pruned at the end.

  // Phase 1: Tag interactive leaf elements + count interactive descendants per ancestor.
  const interactiveAncestors = new Map(); // node → count of interactive descendants
  const interactiveNodes = document.querySelectorAll('a, button, input, select, textarea, [role="button"]');
  interactiveNodes.forEach(node => {
    if (!reverseRegistry.has(node) && isVisible(node)) {
      const id = `mache-${idCounter++}`;
      elementRegistry.set(id, node);
      reverseRegistry.set(node, id);
      node.setAttribute('data-mache-id', id);
    }
    // Only count visible nodes for Phase 2 ancestor thresholds.
    if (!isVisible(node)) return;
    // Walk up to count interactive descendants for Phase 2 (replaces O(N*M) nested query).
    let parent = node.parentElement;
    while (parent) {
      interactiveAncestors.set(parent, (interactiveAncestors.get(parent) || 0) + 1);
      parent = parent.parentElement;
    }
  });

  // Phase 2: Tag structural containers with 2+ interactive descendants.
  // Uses precomputed ancestor counts — O(C) where C = containers, not O(N*M).
  const containers = document.querySelectorAll(
    'main, section, article, nav, header, footer, aside, form, ul, ol, dl, table, tbody, ' +
    '[role="navigation"], [role="main"], [role="list"], [role="group"], [role="region"]'
  );
  containers.forEach(node => {
    if (!reverseRegistry.has(node) && (interactiveAncestors.get(node) || 0) >= 2) {
      const id = `mache-${idCounter++}`;
      elementRegistry.set(id, node);
      reverseRegistry.set(node, id);
      node.setAttribute('data-mache-id', id);
    }
  });

  // Phase 3: Tag <body> and semantic <div> wrappers.
  // body is always included (provides the root structural context).
  // divs are included only if they have semantic significance: explicit role,
  // id/class containing layout keywords, or 3+ interactive descendants.
  const SEMANTIC_KEYWORDS = /content|main|sidebar|footer|header|wrapper|container|layout|page|app/i;
  const bodyAndDivs = document.querySelectorAll('body, div');
  bodyAndDivs.forEach(node => {
    if (reverseRegistry.has(node)) return;
    const tag = node.tagName.toLowerCase();
    if (tag === 'body') {
      // Always include body.
    } else {
      // div: check for semantic significance.
      const hasRole = node.hasAttribute('role');
      const hasSemantic = SEMANTIC_KEYWORDS.test(node.id || '') || SEMANTIC_KEYWORDS.test(node.className || '');
      const hasChildren = (interactiveAncestors.get(node) || 0) >= 3;
      if (!hasRole && !hasSemantic && !hasChildren) return;
    }
    if (!isVisible(node)) return;
    const id = `mache-${idCounter++}`;
    elementRegistry.set(id, node);
    reverseRegistry.set(node, id);
    node.setAttribute('data-mache-id', id);
  });

  // Prune stale entries: removed from DOM, or now hidden (e.g., collapsed modal).
  for (const [id, node] of elementRegistry) {
    if (!document.contains(node) || !isVisible(node)) {
      elementRegistry.delete(id);
      reverseRegistry.delete(node);
      try { node.removeAttribute('data-mache-id'); } catch (_) {}
    }
  }
}

// Walk up 2-3 parent levels collecting tag.firstClass segments.
// Gives the Cartographer structural context for CSS selector generation.
function getPath(element, maxLevels = 3) {
  const parts = [];
  let el = element;
  for (let i = 0; i < maxLevels && el && el !== document.body; i++) {
    const tag = el.tagName.toLowerCase();
    const cls = el.classList.length > 0 ? '.' + el.classList[0] : '';
    parts.unshift(tag + cls);
    el = el.parentElement;
  }
  return parts.join(' > ');
}

function generateSummary() {
  let summary = "Interactive Elements:\n";
  let count = 0;

  const pageWidth = document.documentElement.scrollWidth || window.innerWidth;
  const pageHeight = document.documentElement.scrollHeight || window.innerHeight;

  for (const [macheId, node] of elementRegistry) {
    if (count >= 300) break;
    const tag = node.tagName.toLowerCase();
    let text = (node.textContent || '').replace(/\s+/g, ' ').trim().substring(0, 60);
    // Fallback to aria-label/title for icon-only elements (e.g., notification bell, menu icons)
    if (!text) {
      text = node.getAttribute('aria-label') || node.getAttribute('title') || '';
    }
    if (!text && tag === 'input') {
      text = node.placeholder || node.name || 'input';
    }
    // Skip interactive elements with no text -- not useful for navigation
    if (!text && !node.children.length) continue;

    // Spatial Grounding: Normalized coordinates [x, y, w, h]
    const rect = node.getBoundingClientRect();
    const x = (rect.left + window.scrollX) / pageWidth;
    const y = (rect.top + window.scrollY) / pageHeight;
    const w = rect.width / pageWidth;
    const h = rect.height / pageHeight;
    const bounds = `[${x.toFixed(3)}, ${y.toFixed(3)}, ${w.toFixed(3)}, ${h.toFixed(3)}]`;

    const color = getSemanticColor(node).name;

    // Semantic fiber data: computed styles for tropical distance enrichment.
    // No layout thrashing — all reads are batched (no DOM writes in this loop).
    const cs = getComputedStyle(node);
    const fontSize = parseFloat(cs.fontSize) || 0;
    const display = cs.display || 'block';
    // TextDensity: chars per normalized area (capped at 1.0).
    const area = rect.width * rect.height;
    const textDensity = area > 0 ? Math.min(1.0, text.length / (area / 1000)) : 0;
    // Interactive: focusable or explicitly interactive.
    const interactive = node.tabIndex >= 0 || ['a', 'button', 'input', 'select', 'textarea'].includes(tag);

    // Find nearest tagged parent via registry
    let parentID = 'none';
    let ancestor = node.parentElement;
    while (ancestor) {
      if (reverseRegistry.has(ancestor)) {
        parentID = reverseRegistry.get(ancestor);
        break;
      }
      ancestor = ancestor.parentElement;
    }
    summary += `ID: ${macheId} | Color: ${color} | Bounds: ${bounds} | Parent: ${parentID} | Tag: ${tag} | Text: "${text}" | Path: ${getPath(node)}` +
      ` | FontSize: ${fontSize.toFixed(0)} | Display: ${display} | Interactive: ${interactive} | TextDensity: ${textDensity.toFixed(2)}\n`;
    count++;
  }
  return summary;
}

function captureSnapshot() {
  buildRegistry();
  const summary = generateSummary();
  console.log("X-Ray: Captured snapshot with", elementRegistry.size, "registered nodes.");
  return { summary, url: window.location.href };
}

// React-safe text injection. Bypasses React/Vue/Angular controlled component
// state by calling the native HTMLInputElement/HTMLTextAreaElement value setter
// directly, then dispatching bubbling input+change events so the framework
// picks up the new value.
function typeText(element, text) {
  element.focus();
  const nativeInputSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
  const nativeTextAreaSetter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;

  if (element.tagName === 'TEXTAREA') {
    nativeTextAreaSetter.call(element, text);
  } else {
    nativeInputSetter.call(element, text);
  }

  element.dispatchEvent(new Event('input', { bubbles: true }));
  element.dispatchEvent(new Event('change', { bubbles: true }));
}

// Simulate pressing Enter on an element (for search bars without visible submit buttons).
function pressEnter(element) {
  element.dispatchEvent(new KeyboardEvent('keydown', {
    bubbles: true, cancelable: true, keyCode: 13, key: 'Enter'
  }));
  element.dispatchEvent(new KeyboardEvent('keyup', {
    bubbles: true, cancelable: true, keyCode: 13, key: 'Enter'
  }));
}

function executeAction(macheId, actionType, payload) {
  let el = elementRegistry.get(macheId);
  if (!el) {
    console.error("X-Ray: Element not found for ID:", macheId);
    return;
  }

  if (actionType === 'type') {
    console.log(`X-Ray: Typing "${payload}" into`, el);
    typeText(el, payload || '');
  } else if (actionType === 'enter') {
    console.log(`X-Ray: Pressing Enter on`, el);
    pressEnter(el);
  } else if (actionType === 'click') {
    // If the target is a structural container (article, section, etc.),
    // find the first <a> or <button> inside it -- clicking a container
    // element does nothing on most sites (React, SPA, etc.).
    const containers = ['article', 'section', 'main', 'aside', 'nav', 'header', 'footer', 'div', 'li', 'ul', 'ol'];
    if (containers.includes(el.tagName.toLowerCase())) {
      const clickable = el.querySelector('a, button, [role="button"]');
      if (clickable) {
        console.log(`X-Ray: Resolved container ${el.tagName} to clickable child`, clickable);
        el = clickable;
      }
    }
    console.log(`X-Ray: Executing click on`, el);
    el.click();
  } else if (actionType === 'focus') {
    console.log(`X-Ray: Executing focus on`, el);
    el.focus();
  }
}

// --- Set-of-Mark Overlay ---
// Draws colored bounding boxes + mache-ID labels over registered elements.
// Visible in CDP screenshots so the Cartographer can see which elements are tagged.

const OVERLAY_ID = 'xray-overlay';

// Semantic color legend for the ACI (Agent-Computer Interface).
// Primary colors are used for high-accuracy identification by Vision models.
const SEMANTIC_COLORS = {
  link: { name: 'BLUE', value: 'rgba(0, 0, 255, 0.3)', border: 'rgba(0, 0, 255, 0.9)' },
  button: { name: 'ORANGE', value: 'rgba(255, 165, 0, 0.3)', border: 'rgba(255, 165, 0, 0.9)' },
  input: { name: 'GREEN', value: 'rgba(0, 200, 0, 0.3)', border: 'rgba(0, 200, 0, 0.9)' },
  container: { name: 'PURPLE', value: 'rgba(160, 32, 240, 0.3)', border: 'rgba(160, 32, 240, 0.9)' },
  other: { name: 'RED', value: 'rgba(255, 0, 0, 0.3)', border: 'rgba(255, 0, 0, 0.9)' }
};

function getSemanticColor(node) {
  const tag = node.tagName.toLowerCase();
  const role = node.getAttribute('role');

  if (tag === 'a') return SEMANTIC_COLORS.link;
  if (tag === 'button' || role === 'button') return SEMANTIC_COLORS.button;
  if (['input', 'textarea', 'select'].includes(tag)) return SEMANTIC_COLORS.input;

  // Containers are typically tagged in Phase 2
  const containers = ['main', 'section', 'article', 'nav', 'header', 'footer', 'aside', 'form', 'ul', 'ol', 'dl', 'table', 'body', 'div'];
  if (containers.includes(tag) || (role && ['navigation', 'main', 'list', 'group', 'region'].includes(role))) {
    return SEMANTIC_COLORS.container;
  }

  return SEMANTIC_COLORS.other;
}

function drawOverlay() {
  removeOverlay();

  const overlay = document.createElement('div');
  overlay.id = OVERLAY_ID;
  overlay.style.cssText =
    'position: absolute; top: 0; left: 0; width: 100%; height: 100%;' +
    'pointer-events: none; z-index: 2147483647;';

  for (const [macheId, node] of elementRegistry) {
    const rect = node.getBoundingClientRect();
    // Skip elements that are off-screen or zero-sized
    if (rect.width === 0 || rect.height === 0) continue;

    const color = getSemanticColor(node);

    // Bounding box with translucent fill + thick border
    const box = document.createElement('div');
    box.style.cssText =
      `position: absolute;` +
      `left: ${rect.left + window.scrollX}px;` +
      `top: ${rect.top + window.scrollY}px;` +
      `width: ${rect.width}px;` +
      `height: ${rect.height}px;` +
      `background: ${color.value};` +
      `border: 2px solid ${color.border};` +
      `pointer-events: none;` +
      `box-sizing: border-box;`;

    // ID label
    const label = document.createElement('span');
    label.textContent = macheId;
    label.style.cssText =
      'position: absolute; top: -14px; left: 0;' +
      'background: rgba(0,0,0,0.75); color: #fff;' +
      'font: bold 10px monospace; padding: 1px 3px;' +
      'white-space: nowrap; pointer-events: none;' +
      'line-height: 12px;';
    box.appendChild(label);

    overlay.appendChild(box);
  }

  document.documentElement.appendChild(overlay);
  console.log(`X-Ray: Drew semantic overlay with ${elementRegistry.size} boxes`);
}

function removeOverlay() {
  const existing = document.getElementById(OVERLAY_ID);
  if (existing) existing.remove();
}

// Listen for messages from background.js
chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  switch (message.type) {
    case 'CAPTURE_SNAPSHOT':
      sendResponse(captureSnapshot());
      return true;

    case 'DRAW_OVERLAY':
      drawOverlay();
      // Wait for browser paint so overlay is visible in screenshot capture.
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          sendResponse({ success: true });
        });
      });
      return true;

    case 'REMOVE_OVERLAY':
      removeOverlay();
      sendResponse({ success: true });
      return true;

    case 'RESOLVE_SELECTORS': {
      // Evaluate CSS selectors against the current registry and return resolved mache-IDs.
      const resolvedItems = {};
      if (message.selectors) {
        for (const [zoneId, selector] of Object.entries(message.selectors)) {
          try {
            const nodes = document.querySelectorAll(selector);
            const ids = [];
            nodes.forEach(n => {
              let mid = reverseRegistry.get(n);
              if (!mid) {
                // Dynamically register the new element
                mid = `mache-${idCounter++}`;
                elementRegistry.set(mid, n);
                reverseRegistry.set(n, mid);
                n.setAttribute('data-mache-id', mid);
              }
              ids.push(mid);
            });
            if (ids.length > 0) resolvedItems[zoneId] = ids;
          } catch (e) {
            console.warn('X-Ray: selector failed for zone', zoneId, e);
          }
        }
        console.log('X-Ray: Resolved selectors for', Object.keys(resolvedItems).length,
          'zones,', Object.values(resolvedItems).reduce((a, b) => a + b.length, 0), 'items total');
      }
      sendResponse({ resolved_items: resolvedItems });
      return true;
    }

    case 'RESET_REGISTRY':
      // Remove DOM attributes before clearing maps.
      for (const [, node] of elementRegistry) {
        try { node.removeAttribute('data-mache-id'); } catch (_) {}
      }
      idCounter = 0;
      elementRegistry.clear();
      reverseRegistry.clear();
      console.log('X-Ray: Registry reset (idCounter=0, maps cleared, DOM attrs removed)');
      sendResponse({ success: true });
      return true;

    case 'EXECUTE_ACTION':
      executeAction(message.mache_id, message.action, message.payload);
      sendResponse({ success: true });
      return true;

    case 'SCROLL': {
      const preScrollSize = elementRegistry.size;
      const preScrollHeight = document.documentElement.scrollHeight;

      if (message.direction === 'up') {
        window.scrollBy({ top: -window.innerHeight, behavior: 'smooth' });
      } else {
        // Scroll 1.5x viewport for infinite-scroll sites. This is enough to
        // trigger lazy loading while keeping overlap with the previous view
        // so the Navigator can track item positions across scrolls.
        const maxScroll = document.documentElement.scrollHeight - window.scrollY - window.innerHeight;
        const target = Math.min(1.5 * window.innerHeight, Math.max(maxScroll - 10, window.innerHeight));
        window.scrollBy({ top: target, behavior: 'smooth' });
      }

      // Poll for new DOM content instead of a fixed timer. Sites like Reddit
      // add nodes asynchronously after scroll triggers an intersection observer.
      let polls = 0;
      const maxPolls = 10; // 500ms * 10 = 5s max wait
      const pollInterval = setInterval(() => {
        polls++;
        buildRegistry();
        const currentSize = elementRegistry.size;
        const heightChanged = document.documentElement.scrollHeight !== preScrollHeight;
        const nodesChanged = currentSize !== preScrollSize;

        if (nodesChanged || heightChanged || polls >= maxPolls) {
          clearInterval(pollInterval);
          const summary = generateSummary();
          console.log(`X-Ray: Scroll complete after ${polls * 500}ms — ` +
            `nodes: ${preScrollSize}->${currentSize}, ` +
            `height: ${preScrollHeight}->${document.documentElement.scrollHeight}`);

          // Evaluate CSS selectors from the Cartographer to resolve fresh primary items.
          const resolvedItems = {};
          if (message.selectors) {
            for (const [zoneId, selector] of Object.entries(message.selectors)) {
              try {
                const nodes = document.querySelectorAll(selector);
                const ids = [];
                nodes.forEach(n => {
                  const mid = reverseRegistry.get(n);
                  if (mid) ids.push(mid);
                });
                if (ids.length > 0) resolvedItems[zoneId] = ids;
              } catch (e) {
                console.warn('X-Ray: selector failed for zone', zoneId, e);
              }
            }
            console.log('X-Ray: Resolved selectors for', Object.keys(resolvedItems).length, 'zones');
          }

          sendResponse({ summary, url: window.location.href, resolved_items: resolvedItems });
        }
      }, 500);
      return true;
    }
  }
});

// Initial registration on load (writes data-mache-id attrs but MutationObserver
// below only watches childList, so attribute writes don't trigger DOM_MUTATED).
buildRegistry();

// MutationObserver: detect in-page DOM changes (e.g., dropdown opens, SPA update)
// and notify the server so the Doer can rescan immediately instead of waiting 2s.
let mutationTimer = null;
const domObserver = new MutationObserver(() => {
  clearTimeout(mutationTimer);
  mutationTimer = setTimeout(() => {
    chrome.runtime.sendMessage({ type: 'DOM_MUTATED' });
  }, 150);
});
domObserver.observe(document.body, { childList: true, subtree: true });
