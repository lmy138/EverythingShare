(() => {
  'use strict';
  const token = document.body.dataset.token;
  const verifyCard = document.querySelector('#verifyCard');
  const shareCard = document.querySelector('#shareCard');
  const message = document.querySelector('#shareMessage');
  let info = null;
  let currentPath = '';

  async function copyText(text) {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(text);
        return;
      } catch {
        // Fall through for iOS webviews and browsers that expose but reject it.
      }
    }
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.setAttribute('readonly', '');
    textarea.style.cssText = 'position:fixed;inset:0 auto auto -9999px;opacity:0;pointer-events:none';
    document.body.append(textarea);
    textarea.focus({ preventScroll: true });
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    const copied = document.execCommand('copy');
    textarea.remove();
    if (!copied) throw new Error('浏览器未允许复制，请长按地址栏复制链接');
  }

  const iconMarkup = (type) => type === 'folder'
    ? '<svg viewBox="0 0 24 24" focusable="false"><path class="icon-soft" d="M3.5 7.2A2.2 2.2 0 0 1 5.7 5h3.55l1.85 2.05h7.2a2.2 2.2 0 0 1 2.2 2.2v7.55a2.2 2.2 0 0 1-2.2 2.2H5.7a2.2 2.2 0 0 1-2.2-2.2V7.2Z"/><path class="icon-main" d="M3.5 10h17v6.8a2.2 2.2 0 0 1-2.2 2.2H5.7a2.2 2.2 0 0 1-2.2-2.2V10Z"/></svg>'
    : '<svg viewBox="0 0 24 24" focusable="false"><path class="icon-paper" d="M6.4 3.5h6.9l4.8 4.8v11.3a.9.9 0 0 1-.9.9H6.4a.9.9 0 0 1-.9-.9V4.4a.9.9 0 0 1 .9-.9Z"/><path class="icon-fold" d="M12.8 3.8v5h5"/><path class="icon-line" d="M8.7 13h6.4M8.7 16.2h4.8"/></svg>';

  const api = async (url, options = {}) => {
    const response = await fetch(url, {
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
      ...options,
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || '请求失败，请稍后重试');
    return data;
  };

  const formatBytes = (value) => {
    const n = Number(value || 0);
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
    return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
  };

  const formatTime = (unix) => unix ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(unix * 1000)) : '永久有效';

  async function verify(code) {
    const normalizedCode = String(code || '').trim().toUpperCase();
    info = await api(`/api/v1/public/shares/${token}/verify`, {
      method: 'POST',
      body: JSON.stringify({ code: normalizedCode }),
    });
    history.replaceState(null, '', `${location.pathname}${location.search}#pwd=${encodeURIComponent(normalizedCode)}`);
    verifyCard.classList.add('hidden');
    shareCard.classList.remove('hidden');
    document.querySelector('#shareTitle').textContent = info.title;
    document.querySelector('#shareExpiry').textContent = `有效期：${formatTime(info.expiresAt)}`;
    document.querySelector('#shareDownloads').textContent = info.maxDownloads == null
      ? `已下载 ${info.downloads} 次`
      : `已下载 ${info.downloads} / ${info.maxDownloads} 次`;
    if (info.type === 'folder') {
      document.querySelector('#folderToolbar').classList.remove('hidden');
      await loadFolder('');
    } else {
      document.querySelector('#singleFile').classList.remove('hidden');
      document.querySelector('#singleIcon').innerHTML = iconMarkup('file');
      document.querySelector('#singleName').textContent = info.name;
    }
  }

  async function loadFolder(folderPath) {
    currentPath = folderPath || '';
    const data = await api(`/api/v1/public/shares/${token}/entries?path=${encodeURIComponent(currentPath)}`);
    const list = document.querySelector('#fileList');
    list.replaceChildren();
    document.querySelector('#breadcrumb').textContent = currentPath || '根目录';
    document.querySelector('#goUp').disabled = !currentPath;
    const zipButton = document.querySelector('#downloadZip');
    zipButton.disabled = !data.canZip;
    zipButton.textContent = data.zipMode === 'stream' ? '打包下载（不可续传）' : data.zipMode === 'disabled' ? '项目过多，无法打包' : '打包下载';
    if (!data.entries.length) {
      const empty = document.createElement('div');
      empty.className = 'empty';
      empty.textContent = '这个文件夹是空的';
      list.append(empty);
      return;
    }
    data.entries.forEach((entry) => {
      const row = document.createElement('div');
      row.className = 'file-row';
      const main = document.createElement('div');
      main.className = 'file-main';
      main.innerHTML = `<span class="file-icon ${entry.type}" aria-hidden="true">${iconMarkup(entry.type)}</span>`;
      const text = document.createElement('div');
      const name = document.createElement('div');
      name.className = 'file-name';
      name.textContent = entry.name;
      text.append(name);
      main.append(text);
      const meta = document.createElement('div');
      meta.className = 'file-meta';
      meta.textContent = entry.type === 'folder' ? '文件夹' : formatBytes(entry.size);
      const button = document.createElement('button');
      button.className = entry.type === 'folder' ? 'secondary row-action' : 'primary row-action';
      button.textContent = entry.type === 'folder' ? '打开' : '下载';
      button.addEventListener('click', () => entry.type === 'folder' ? loadFolder(entry.path) : startDownload({ entryId: entry.id }));
      row.append(main, meta, button);
      list.append(row);
    });
  }

  async function startDownload(payload) {
    message.textContent = payload.zip ? '正在准备压缩包，大文件夹可能需要一些时间…' : '正在创建安全下载链接…';
    try {
      const data = await api(`/api/v1/public/shares/${token}/downloads`, {
        method: 'POST',
        body: JSON.stringify(payload),
      });
      message.textContent = '';
      window.location.assign(data.url);
    } catch (error) {
      message.textContent = error.message;
    }
  }

  document.querySelector('#verifyForm').addEventListener('submit', async (event) => {
    event.preventDefault();
    const status = document.querySelector('#verifyMessage');
    status.textContent = '';
    try {
      await verify(document.querySelector('#code').value.trim().toUpperCase());
    } catch (error) {
      status.textContent = error.message;
    }
  });
  document.querySelector('#downloadSingle').addEventListener('click', () => startDownload({}));
  document.querySelector('#downloadZip').addEventListener('click', () => startDownload({ zip: true }));
  document.querySelector('#goUp').addEventListener('click', () => loadFolder(currentPath.includes('\\') ? currentPath.slice(0, currentPath.lastIndexOf('\\')) : ''));
  document.querySelector('#systemShare').addEventListener('click', async () => {
    const shareData = { title: info?.title || '文件分享', text: '文件分享', url: location.href };
    if (navigator.share) {
      await navigator.share(shareData).catch(async (error) => {
        if (error?.name !== 'AbortError') {
          await copyText(location.href);
          message.textContent = '分享链接已复制';
        }
      });
    } else {
      await copyText(location.href);
      message.textContent = '分享链接已复制';
    }
  });

  const directCode = new URLSearchParams(location.hash.slice(1)).get('pwd');
  if (directCode) {
    const codeInput = document.querySelector('#code');
    codeInput.value = directCode.toUpperCase();
    verify(directCode).catch((error) => {
      document.querySelector('#verifyMessage').textContent = error.message;
      codeInput.focus();
      codeInput.select();
    });
  }
})();
