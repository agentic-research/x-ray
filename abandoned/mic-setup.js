const btn = document.getElementById('grant-btn');
const status = document.getElementById('status');

btn.addEventListener('click', async () => {
  btn.disabled = true;
  status.textContent = 'Requesting permission...';
  status.className = '';
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    // Stop the stream immediately — we just needed the permission grant.
    stream.getTracks().forEach(t => t.stop());
    status.textContent = 'Permission granted! Starting session...';
    status.className = 'ok';
    chrome.runtime.sendMessage({ type: 'MIC_GRANTED' });
    // Auto-close after a brief moment so the user sees confirmation.
    setTimeout(() => window.close(), 600);
  } catch (err) {
    status.textContent = 'Permission denied: ' + (err.message || err);
    status.className = 'error';
    btn.disabled = false;
  }
});
