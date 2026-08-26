(() => {
  'use strict';
  const token = document.body.dataset.token;
  const verifyCard = document.querySelector('#verifyCard');
  const shareCard = document.querySelector('#shareCard');
  const message = document.querySelector('#shareMessage');
  let info = null;
  let currentPath = '';
  let currentEntries = [];
  let navigation = [];
  const selected = new Map();
  const shareColumnKey = 'everything-share-column-widths-v1';

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

  function renderFileIcon(container, type, name) {
    container.innerHTML = window.EverythingShareIcons.markup(type, name);
  }

  function installShareColumnSizing() {
    const header = document.querySelector('#fileListHeader');
    if (!header || header.dataset.resizable === 'true') return;
    header.dataset.resizable = 'true';
    const cells = [...header.querySelectorAll('[data-share-column]')];
    const minimums = [180, 80, 80];
    let ratios = [0.66, 0.17, 0.17];
    try {
      const stored = JSON.parse(localStorage.getItem(shareColumnKey) || 'null');
      if (Array.isArray(stored) && stored.length === 3 && stored.every((value) => Number.isFinite(value) && value > 0)) {
        const total = stored.reduce((sum, value) => sum + value, 0);
        ratios = stored.map((value) => value / total);
      }
    } catch { /* Use defaults. */ }
    const fitWidths = (available) => {
      const widths = ratios.map((ratio) => ratio * available);
      minimums.forEach((minimum, index) => { widths[index] = Math.max(minimum, widths[index]); });
      let excess = widths.reduce((sum, value) => sum + value, 0) - available;
      for (let pass = 0; pass < 3 && excess > 0.1; pass += 1) {
        const room = widths.reduce((sum, value, index) => sum + Math.max(0, value - minimums[index]), 0);
        if (room <= 0) break;
        widths.forEach((value, index) => {
          const reducible = Math.max(0, value - minimums[index]);
          widths[index] -= Math.min(reducible, excess * reducible / room);
        });
        excess = widths.reduce((sum, value) => sum + value, 0) - available;
      }
      return widths;
    };
    const applyWidths = () => {
      if (!matchMedia('(min-width: 768px)').matches) return { available: 0, widths: [] };
      const listWidth = document.querySelector('#fileList').getBoundingClientRect().width || 960;
      const available = Math.max(minimums.reduce((sum, value) => sum + value, 0), listWidth - 104);
      const widths = fitWidths(available);
      shareCard.style.setProperty('--share-name-width', `${Math.round(widths[0])}px`);
      shareCard.style.setProperty('--share-meta-width', `${Math.round(widths[1])}px`);
      shareCard.style.setProperty('--share-action-width', `${Math.round(widths[2])}px`);
      return { available, widths };
    };
    const saveWidths = () => localStorage.setItem(shareColumnKey, JSON.stringify(ratios.map((value) => Number(value.toFixed(5)))));
    const resizePair = (index, delta, starting) => {
      if (!starting.available) return;
      const lastColumn = index === cells.length - 1;
      const partner = lastColumn ? index - 1 : index + 1;
      const widths = [...starting.widths];
      const directedDelta = lastColumn ? -delta : delta;
      const bounded = Math.max(minimums[index] - widths[index], Math.min(widths[partner] - minimums[partner], directedDelta));
      widths[index] += bounded; widths[partner] -= bounded;
      ratios = widths.map((width) => width / starting.available);
      applyWidths();
    };
    cells.forEach((cell, index) => {
      const handle = document.createElement('span');
      handle.className = 'share-column-resizer'; handle.tabIndex = 0; handle.setAttribute('role', 'separator'); handle.setAttribute('aria-orientation', 'vertical'); handle.setAttribute('aria-label', `调整${cell.textContent.trim()}栏宽`);
      cell.append(handle);
      handle.addEventListener('pointerdown', (event) => {
        if (!matchMedia('(min-width: 768px)').matches) return;
        event.preventDefault(); const startX = event.clientX; const starting = applyWidths(); handle.setPointerCapture(event.pointerId);
        const move = (moveEvent) => resizePair(index, moveEvent.clientX - startX, starting);
        const finish = () => { saveWidths(); handle.removeEventListener('pointermove', move); handle.removeEventListener('pointerup', finish); handle.removeEventListener('pointercancel', finish); };
        handle.addEventListener('pointermove', move); handle.addEventListener('pointerup', finish); handle.addEventListener('pointercancel', finish);
      });
      handle.addEventListener('keydown', (event) => {
        if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
        event.preventDefault(); resizePair(index, event.key === 'ArrowRight' ? 16 : -16, applyWidths()); saveWidths();
      });
    });
    window.addEventListener('resize', applyWidths, { passive: true });
    applyWidths();
  }

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
      document.querySelector('#fileListHeader').classList.remove('hidden');
      installShareColumnSizing();
      navigation = [];
      await loadFolder('');
    } else {
      document.querySelector('#singleFile').classList.remove('hidden');
      renderFileIcon(document.querySelector('#singleIcon'), 'file', info.name);
      document.querySelector('#singleName').textContent = info.name;
    }
  }

  const isDescendantPath = (parent, candidate) => {
    const base = String(parent || '').replace(/[\\/]+$/, '').toLowerCase();
    const value = String(candidate || '').replace(/[\\/]+$/, '').toLowerCase();
    return base !== value && value.startsWith(`${base}\\`);
  };

  const coveringFolder = (entry) => [...selected.values()].find((item) => item.type === 'folder' && isDescendantPath(item.path, entry.path));

  function selectEntry(entry) {
    if (selected.has(entry.id)) return true;
    const parent = coveringFolder(entry);
    if (parent) {
      message.textContent = `“${entry.name}”已包含在所选文件夹“${parent.name}”中`;
      return false;
    }
    if (entry.type === 'folder') {
      [...selected.values()].forEach((item) => { if (isDescendantPath(entry.path, item.path)) selected.delete(item.id); });
    }
    if (selected.size >= 128) {
      message.textContent = '一次最多选择 128 个项目';
      return false;
    }
    selected.set(entry.id, entry);
    message.textContent = '';
    return true;
  }

  function updateSelectionUI() {
    document.querySelector('#selectedCount').textContent = `已选 ${selected.size} 项`;
    document.querySelector('#downloadSelected').disabled = selected.size === 0;
    document.querySelector('#clearSelection').disabled = selected.size === 0;
    document.querySelectorAll('.file-row[data-entry-id]').forEach((row) => {
      const entry = currentEntries.find((item) => item.id === row.dataset.entryId);
      if (!entry) return;
      const exact = selected.has(entry.id);
      const inherited = !exact && Boolean(coveringFolder(entry));
      const checkbox = row.querySelector('.entry-select');
      checkbox.checked = exact || inherited;
      checkbox.disabled = inherited;
      checkbox.title = inherited ? '已包含在所选上级文件夹中' : '';
      row.classList.toggle('selected', exact || inherited);
      row.classList.toggle('included', inherited);
    });
    const page = document.querySelector('#selectPage');
    const selectedOnPage = currentEntries.filter((entry) => selected.has(entry.id) || coveringFolder(entry)).length;
    page.checked = currentEntries.length > 0 && selectedOnPage === currentEntries.length;
    page.indeterminate = selectedOnPage > 0 && selectedOnPage < currentEntries.length;
    page.disabled = currentEntries.length === 0;
  }

  async function loadFolder(folderPath, remember = false) {
    if (remember) navigation.push(currentPath);
    currentPath = folderPath || '';
    const data = await api(`/api/v1/public/shares/${token}/entries?path=${encodeURIComponent(currentPath)}`);
    currentEntries = data.entries;
    const list = document.querySelector('#fileList');
    list.replaceChildren();
    document.querySelector('#breadcrumb').textContent = currentPath ? currentPath.split('\\').pop() : '分享项目';
    document.querySelector('#goUp').disabled = navigation.length === 0;
    const zipButton = document.querySelector('#downloadZip');
    zipButton.disabled = !data.canZip;
    zipButton.textContent = data.zipMode === 'stream' ? '下载全部（不可续传）' : data.zipMode === 'disabled' ? '项目过多，无法打包' : '下载全部';
    if (!data.entries.length) {
      const empty = document.createElement('div');
      empty.className = 'empty';
      empty.textContent = '这个文件夹是空的';
      list.append(empty);
      updateSelectionUI();
      return;
    }
    data.entries.forEach((entry) => {
      const row = document.createElement('div');
      row.className = 'file-row';
      row.dataset.entryId = entry.id;
      const select = document.createElement('input');
      select.type = 'checkbox';
      select.className = 'entry-select';
      select.setAttribute('aria-label', `选择 ${entry.name}`);
      select.addEventListener('change', () => {
        if (select.checked) selectEntry(entry); else selected.delete(entry.id);
        updateSelectionUI();
      });
      const main = document.createElement('div');
      main.className = 'file-main';
      const icon = document.createElement('span');
      icon.className = `file-icon ${entry.type}`;
      icon.setAttribute('aria-hidden', 'true');
      renderFileIcon(icon, entry.type, entry.name);
      const text = document.createElement('div');
      const name = document.createElement('div');
      name.className = 'file-name';
      name.textContent = entry.name;
      name.title = entry.name;
      text.append(name);
      main.append(icon, text);
      const meta = document.createElement('div');
      meta.className = 'file-meta';
      meta.textContent = entry.type === 'folder' ? '文件夹' : formatBytes(entry.size);
      const button = document.createElement('button');
      button.className = entry.type === 'folder' ? 'secondary row-action' : 'primary row-action';
      button.textContent = entry.type === 'folder' ? '打开' : '下载';
      button.addEventListener('click', () => entry.type === 'folder' ? loadFolder(entry.path, true) : startDownload({ entryId: entry.id }));
      row.append(select, main, meta, button);
      list.append(row);
    });
    updateSelectionUI();
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
  document.querySelector('#downloadSelected').addEventListener('click', () => startDownload({ zip: true, entryIds: [...selected.keys()] }));
  document.querySelector('#clearSelection').addEventListener('click', () => {
    selected.clear();
    message.textContent = '';
    updateSelectionUI();
  });
  document.querySelector('#selectPage').addEventListener('change', (event) => {
    if (event.currentTarget.checked) {
      currentEntries.forEach((entry) => selectEntry(entry));
    } else {
      currentEntries.forEach((entry) => selected.delete(entry.id));
    }
    updateSelectionUI();
  });
  document.querySelector('#goUp').addEventListener('click', () => {
    const parent = navigation.pop();
    if (parent != null) loadFolder(parent).catch((error) => { message.textContent = error.message; });
  });
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
