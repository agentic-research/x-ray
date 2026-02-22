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

function generateSummary() {
  const nodes = document.querySelectorAll('[data-mache-id]');
  let summary = "Interactive Elements:\n";
  let count = 0;
  nodes.forEach(node => {
    if (count >= 300) return;
    let text = (node.textContent || '').replace(/\s+/g, ' ').trim().substring(0, 60);
    if (!text && node.tagName.toLowerCase() === 'input') {
      text = node.placeholder || node.name || 'input';
    }
    summary += `ID: ${node.getAttribute('data-mache-id')} | Tag: ${node.tagName.toLowerCase()} | Text: "${text}"\n`;
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
  }
});

// Initial tag on load
injectMacheIDs();
