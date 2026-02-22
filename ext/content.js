// content.js - Injected into pages to handle ID tagging and execution

let idCounter = 0;

function injectMacheIDs() {
  const interactiveNodes = document.querySelectorAll('a, button, input, select, textarea, [role="button"]');
  interactiveNodes.forEach(node => {
    if (!node.hasAttribute('data-mache-id')) {
      const id = `mache-${idCounter++}`;
      node.setAttribute('data-mache-id', id);
    }
  });
}

function sanitizeDOM(htmlString) {
  // Simple regex-based strip for prototype, ideally use DOMParser
  return htmlString
    .replace(/<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/gi, '')
    .replace(/<style\b[^<]*(?:(?!<\/style>)<[^<]*)*<\/style>/gi, '')
    .replace(/<svg\b[^<]*(?:(?!<\/svg>)<[^<]*)*<\/svg>/gi, '<svg>...</svg>'); // Minimize SVGs
}

function captureSnapshot() {
  injectMacheIDs();
  const rawHTML = sanitizeDOM(document.documentElement.outerHTML);
  // Note: Capturing screenshot from content script requires messaging background.js

  console.log("X-Ray: Captured semantic snapshot with", idCounter, "nodes.");
  return rawHTML;
}

function executeAction(macheId, actionType) {
  const el = document.querySelector(`[data-mache-id="${macheId}"]`);
  if (!el) {
    console.error("X-Ray: Element not found for ID:", macheId);
    return;
  }

  console.log(`X-Ray: Executing ${actionType} on`, el);
  if (actionType === 'click') {
    el.click();
  } else if (actionType === 'focus') {
    el.focus();
  }
}

// Initial tag on load
injectMacheIDs();

// TODO: Set up MutationObserver to re-tag dynamically added nodes.
// TODO: Listen for messages from background.js to execute actions.
