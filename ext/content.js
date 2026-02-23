// content.js - Injected into pages to handle ID tagging and execution

let idCounter = 0;

function injectMacheIDs() {
  // Phase 1: Tag interactive leaf elements
  const interactiveNodes = document.querySelectorAll('a, button, input, select, textarea, [role="button"]');
  interactiveNodes.forEach(node => {
    if (!node.hasAttribute('data-mache-id')) {
      node.setAttribute('data-mache-id', `mache-${idCounter++}`);
    }
  });

  // Phase 2: Tag structural containers that hold 2+ interactive elements.
  // This gives the Cartographer container elements to select as zone roots,
  // and gives children a tagged parent for the Parent: field.
  const containers = document.querySelectorAll(
    'main, section, article, nav, header, footer, aside, form, ul, ol, dl, table, tbody, ' +
    '[role="navigation"], [role="main"], [role="list"], [role="group"], [role="region"]'
  );
  containers.forEach(node => {
    if (!node.hasAttribute('data-mache-id')) {
      const childCount = node.querySelectorAll('[data-mache-id]').length;
      if (childCount >= 2) {
        node.setAttribute('data-mache-id', `mache-${idCounter++}`);
      }
    }
  });
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
  const nodes = document.querySelectorAll('[data-mache-id]');
  let summary = "Interactive Elements:\n";
  let count = 0;
  nodes.forEach(node => {
    if (count >= 300) return;
    const tag = node.tagName.toLowerCase();
    let text = (node.textContent || '').replace(/\s+/g, ' ').trim().substring(0, 60);
    if (!text && tag === 'input') {
      text = node.placeholder || node.name || 'input';
    }
    // Skip interactive elements with no text — not useful for navigation
    if (!text && !node.children.length) return;
    const parentTagged = node.parentElement ? node.parentElement.closest('[data-mache-id]') : null;
    const parentID = parentTagged ? parentTagged.getAttribute('data-mache-id') : 'none';
    summary += `ID: ${node.getAttribute('data-mache-id')} | Parent: ${parentID} | Tag: ${node.tagName.toLowerCase()} | Text: "${text}" | Path: ${getPath(node)}\n`;
    count++;
  });
  return summary;
}

function captureSnapshot() {
  injectMacheIDs();
  const summary = generateSummary();
  console.log("X-Ray: Captured snapshot with", idCounter, "tagged nodes.");
  return { summary, url: window.location.href };
}

function executeAction(macheId, actionType) {
  let el = document.querySelector(`[data-mache-id="${macheId}"]`);
  if (!el) {
    console.error("X-Ray: Element not found for ID:", macheId);
    return;
  }

  if (actionType === 'click') {
    // If the target is a structural container (article, section, etc.),
    // find the first <a> or <button> inside it — clicking a container
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

// Listen for messages from background.js
chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  switch (message.type) {
    case 'CAPTURE_SNAPSHOT':
      sendResponse(captureSnapshot());
      return true;

    case 'EXECUTE_ACTION':
      executeAction(message.mache_id, message.action);
      sendResponse({ success: true });
      return true;

    case 'SCROLL': {
      const preScrollCount = document.querySelectorAll('[data-mache-id]').length;
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
        injectMacheIDs();
        const currentCount = document.querySelectorAll('[data-mache-id]').length;
        const heightChanged = document.documentElement.scrollHeight !== preScrollHeight;
        const nodesChanged = currentCount !== preScrollCount;

        if (nodesChanged || heightChanged || polls >= maxPolls) {
          clearInterval(pollInterval);
          const summary = generateSummary();
          console.log(`X-Ray: Scroll complete after ${polls * 500}ms — ` +
            `nodes: ${preScrollCount}→${currentCount}, ` +
            `height: ${preScrollHeight}→${document.documentElement.scrollHeight}`);

          // Evaluate CSS selectors from the Cartographer to resolve fresh primary items.
          const resolvedItems = {};
          if (message.selectors) {
            for (const [zoneId, selector] of Object.entries(message.selectors)) {
              try {
                const nodes = document.querySelectorAll(selector);
                const ids = [];
                nodes.forEach(n => {
                  const mid = n.getAttribute('data-mache-id');
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

// Initial tag on load
injectMacheIDs();
