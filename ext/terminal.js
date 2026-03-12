// terminal.js — ghostty-web terminal in the X-Ray side panel
// Provides a pseudo-shell that lets you `ls` the page via mache.

import { init, Terminal } from './vendor/ghostty-web.js';

const PROMPT = '\x1b[38;5;42mx-ray\x1b[0m:\x1b[38;5;75m~\x1b[0m$ ';
const WELCOME = [
  '\x1b[38;5;208m  X-RAY Terminal\x1b[0m  \x1b[38;5;245m— browse any page like a filesystem\x1b[0m',
  '',
  '  \x1b[38;5;245mCommands:\x1b[0m',
  '    ls [path]      list zones / elements',
  '    cd <zone>      enter a zone',
  '    cat <id>       show element details',
  '    tree [path]    show zone hierarchy',
  '    pwd            current path',
  '    clear          clear screen',
  '    help           show this message',
  '',
].join('\r\n');

let term = null;
let port = null;
let cwd = '/';
let inputBuffer = '';
let pendingResolve = null;
let initialized = false;

// --- Tab switching ---
document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.panel').forEach(p => p.classList.remove('visible'));
    btn.classList.add('active');
    document.getElementById(btn.dataset.panel).classList.add('visible');

    // Lazy-init terminal on first switch
    if (btn.dataset.panel === 'term-panel' && !initialized) {
      initTerminal();
    }
    // Fit terminal when switching to it
    if (btn.dataset.panel === 'term-panel' && term) {
      requestAnimationFrame(() => fitTerminal());
    }
  });
});

// --- Connect to background.js ---
function connectPort() {
  port = chrome.runtime.connect({ name: 'sidepanel' });
  port.onMessage.addListener((msg) => {
    if (msg.type === 'SHELL_RESPONSE' && pendingResolve) {
      pendingResolve(msg);
      pendingResolve = null;
    }
    // Also handle WS status for the dot indicator
    if (msg.type === 'WS_STATUS') {
      document.getElementById('ws-dot').classList.toggle('connected', msg.connected);
    }
    // Forward agent log entries to the log panel (sidepanel.js handles this via its own port)
  });
  port.onDisconnect.addListener(() => {
    setTimeout(connectPort, 1000);
  });
}
connectPort();

function sendShellCommand(command, args) {
  return new Promise((resolve) => {
    pendingResolve = resolve;
    port.postMessage({ type: 'SHELL_COMMAND', command, args, cwd });
    // Timeout after 5s
    setTimeout(() => {
      if (pendingResolve === resolve) {
        pendingResolve = null;
        resolve({ output: '\x1b[38;5;196mTimeout: no response from server\x1b[0m', error: true });
      }
    }, 5000);
  });
}

// --- Terminal init ---
async function initTerminal() {
  initialized = true;
  const container = document.getElementById('terminal-container');

  try {
    await init();
  } catch (e) {
    container.innerHTML = `<div style="color:#e74c3c;padding:20px;font-size:12px;">
      Failed to load ghostty-web WASM:<br>${e.message}<br><br>
      Falling back to basic terminal...
    </div>`;
    initFallbackTerminal(container);
    return;
  }

  term = new Terminal({
    fontSize: 12,
    fontFamily: "'SF Mono', 'Menlo', 'Consolas', monospace",
    theme: {
      background: '#1a1b26',
      foreground: '#a9b1d6',
      cursor: '#c0caf5',
      selectionBackground: '#33467c',
    },
    cursorBlink: true,
    scrollback: 1000,
  });

  term.open(container);
  fitTerminal();

  // Handle resize
  const ro = new ResizeObserver(() => fitTerminal());
  ro.observe(container);

  // Write welcome message
  term.write(WELCOME + '\r\n');
  showPrompt();

  // Handle input
  term.onData((data) => handleInput(data));
}

function fitTerminal() {
  // ghostty-web auto-fits based on container size, but we may need to trigger a resize
  if (term && term.resize) {
    const container = document.getElementById('terminal-container');
    const w = container.clientWidth;
    const h = container.clientHeight;
    if (w > 0 && h > 0) {
      // Approximate cols/rows from container size
      const cols = Math.max(40, Math.floor(w / 7.2));
      const rows = Math.max(10, Math.floor(h / 16));
      try { term.resize(cols, rows); } catch (_) {}
    }
  }
}

function showPrompt() {
  const displayPath = cwd === '/' ? '~' : cwd;
  const prompt = `\x1b[38;5;42mx-ray\x1b[0m:\x1b[38;5;75m${displayPath}\x1b[0m$ `;
  term.write(prompt);
}

function handleInput(data) {
  for (const ch of data) {
    if (ch === '\r' || ch === '\n') {
      term.write('\r\n');
      processCommand(inputBuffer.trim());
      inputBuffer = '';
    } else if (ch === '\x7f' || ch === '\b') {
      // Backspace
      if (inputBuffer.length > 0) {
        inputBuffer = inputBuffer.slice(0, -1);
        term.write('\b \b');
      }
    } else if (ch === '\x03') {
      // Ctrl+C
      inputBuffer = '';
      term.write('^C\r\n');
      showPrompt();
    } else if (ch === '\x0c') {
      // Ctrl+L — clear
      term.clear();
      showPrompt();
    } else if (ch >= ' ') {
      inputBuffer += ch;
      term.write(ch);
    }
  }
}

async function processCommand(line) {
  if (!line) {
    showPrompt();
    return;
  }

  const parts = line.split(/\s+/);
  const cmd = parts[0];
  const args = parts.slice(1);

  switch (cmd) {
    case 'help':
      term.write(WELCOME + '\r\n');
      break;

    case 'clear':
      term.clear();
      showPrompt();
      return;

    case 'pwd':
      term.write(cwd + '\r\n');
      break;

    case 'cd': {
      const target = args[0] || '/';
      const newPath = resolvePath(target);
      const resp = await sendShellCommand('ls', [newPath]);
      if (resp.error) {
        term.write(`cd: ${target}: not found\r\n`);
      } else {
        cwd = newPath;
      }
      break;
    }

    case 'ls': {
      const target = args[0] ? resolvePath(args[0]) : cwd;
      const resp = await sendShellCommand('ls', [target]);
      if (resp.output) {
        term.write(resp.output + '\r\n');
      }
      break;
    }

    case 'cat': {
      if (!args[0]) {
        term.write('usage: cat <mache-id or path>\r\n');
        break;
      }
      const target = resolvePath(args[0]);
      const resp = await sendShellCommand('cat', [target]);
      if (resp.output) {
        term.write(resp.output + '\r\n');
      }
      break;
    }

    case 'tree': {
      const target = args[0] ? resolvePath(args[0]) : cwd;
      const resp = await sendShellCommand('tree', [target]);
      if (resp.output) {
        term.write(resp.output + '\r\n');
      }
      break;
    }

    default:
      term.write(`command not found: ${cmd}\r\n`);
  }

  showPrompt();
}

function resolvePath(p) {
  if (p.startsWith('/')) return normalizePath(p);
  if (p === '..') {
    const parts = cwd.split('/').filter(Boolean);
    parts.pop();
    return '/' + parts.join('/');
  }
  if (p === '.') return cwd;
  const base = cwd === '/' ? '' : cwd;
  return normalizePath(base + '/' + p);
}

function normalizePath(p) {
  const parts = p.split('/').filter(Boolean);
  const resolved = [];
  for (const part of parts) {
    if (part === '..') resolved.pop();
    else if (part !== '.') resolved.push(part);
  }
  return '/' + resolved.join('/');
}

// --- Fallback terminal (pure DOM, no WASM) ---
function initFallbackTerminal(container) {
  container.innerHTML = '';
  const pre = document.createElement('pre');
  pre.style.cssText = 'padding:12px;color:#a9b1d6;background:#1a1b26;height:100%;overflow-y:auto;white-space:pre-wrap;word-break:break-word;';
  container.appendChild(pre);

  const input = document.createElement('input');
  input.style.cssText = 'position:absolute;bottom:8px;left:12px;right:12px;background:#111;border:1px solid #333;color:#a9b1d6;font:12px monospace;padding:4px 8px;outline:none;';
  input.placeholder = 'Type a command...';
  container.appendChild(input);

  function writeLine(text) {
    pre.textContent += text + '\n';
    pre.scrollTop = pre.scrollHeight;
  }

  writeLine('X-RAY Terminal (fallback mode — ghostty-web WASM failed to load)');
  writeLine('Type "help" for commands.\n');
  writeLine(`x-ray:~$ `);

  input.addEventListener('keydown', async (e) => {
    if (e.key !== 'Enter') return;
    const line = input.value.trim();
    input.value = '';
    writeLine(line);

    if (line === 'help') {
      writeLine('  ls [path]   — list zones/elements');
      writeLine('  cd <zone>   — enter a zone');
      writeLine('  cat <id>    — show element details');
      writeLine('  tree [path] — show zone hierarchy');
      writeLine('  pwd         — current path');
      writeLine('  clear       — clear screen');
    } else if (line === 'clear') {
      pre.textContent = '';
    } else if (line === 'pwd') {
      writeLine(cwd);
    } else if (line) {
      const parts = line.split(/\s+/);
      const resp = await sendShellCommand(parts[0], parts.slice(1));
      if (resp.output) writeLine(resp.output.replace(/\x1b\[[0-9;]*m/g, '')); // strip ANSI
    }

    writeLine(`x-ray:${cwd === '/' ? '~' : cwd}$ `);
  });

  input.focus();
}
