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
    summary += `ID: ${node.getAttribute('data-mache-id')} | Parent: ${parentID} | Tag: ${node.tagName.toLowerCase()} | Text: "${text}"\n`;
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
      const distance = message.direction === 'up' ? -window.innerHeight : window.innerHeight;
      window.scrollBy({ top: distance, behavior: 'smooth' });
      // Wait for scroll animation + lazy-loaded content, then re-tag and re-summarize
      setTimeout(() => {
        injectMacheIDs();
        const summary = generateSummary();
        sendResponse({ summary, url: window.location.href });
      }, 1500);
      return true;
    }
  }
});

// Initial tag on load
injectMacheIDs();
