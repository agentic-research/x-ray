// Run with: node ext/registry_bug_test.js

// Mock DOM environment to test content.js logic
const nodes = [];
global.document = {
  querySelectorAll: () => nodes,
  contains: (n) => nodes.includes(n),
  documentElement: { scrollWidth: 1000, scrollHeight: 1000 }
};
global.window = { innerWidth: 1000, innerHeight: 1000, scrollX: 0, scrollY: 0 };
global.getComputedStyle = () => ({ display: 'block', visibility: 'visible' });

// --- The exact logic from ext/content.js ---
let idCounter = 0;
const elementRegistry = new Map();
const reverseRegistry = new Map();

function isVisible(el) { return true; } // simplified

function buildRegistry() {
  nodes.forEach(node => {
    if (!reverseRegistry.has(node) && isVisible(node)) {
      const id = `mache-${idCounter++}`;
      elementRegistry.set(id, node);
      reverseRegistry.set(node, id);
    }
  });

  for (const [id, node] of elementRegistry) {
    if (!document.contains(node) || !isVisible(node)) {
      elementRegistry.delete(id);
      reverseRegistry.delete(node);
    }
  }
}
// ---------------------------------------------

console.log("Simulating SPA page navigation / RESCAN...
");

for (let i = 0; i < 5; i++) {
  // 1. Create a new "page" of 100 interactive elements (e.g. clicking a link in a React SPA)
  nodes.length = 0; // clear previous DOM
  for (let j = 0; j < 100; j++) {
    nodes.push({ tagName: 'A', getBoundingClientRect: () => ({width:10, height:10, left:0, top:0}), getAttribute:()=>null, parentElement: null });
  }

  // 2. Extension captures snapshot
  buildRegistry();

  console.log(`Navigation ${i+1}: elementRegistry size: ${elementRegistry.size}, next ID will be: mache-${idCounter}`);
}

if (idCounter > 100) {
  console.log("
❌ BUG CONFIRMED: idCounter is " + idCounter + " instead of 100.");
  console.log("IDs are monotonically increasing across full page swaps without bounding.");
  console.log("If a user leaves this tab open for days, they will eventually see mache-15024, bloating the LLM context.");
  process.exit(1);
} else {
  console.log("
✅ Fixed: idCounter was reset cleanly.");
  process.exit(0);
}
