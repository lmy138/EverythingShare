(() => {
  'use strict';

  const LOGIN_PATH = '/share-oauth2/start';
  const API = '/share-api/v1';
  const SELECTION_VERSION = 1;
  const MAX_SELECTION = 128;
  const selectionKey = 'everything-share-selection-v1';
  const pendingKey = 'everything-share-pending-v2';
  const columnWidthsStorageName = 'everything-column-widths-v3';
  const selected = loadSelection();
  let qrLoader;
  let lastSelectionAnchor = null;
  let batchPositionFrame = 0;

  async function copyText(text) {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      try { await navigator.clipboard.writeText(text); return; } catch { /* Use compatibility path. */ }
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
    if (!copied) throw new Error('浏览器未允许复制，请长按文本手动复制');
  }

  function loadQRCode() {
    if (typeof window.qrcode === 'function') return Promise.resolve(window.qrcode);
    if (!qrLoader) {
      qrLoader = new Promise((resolve, reject) => {
        const script = document.createElement('script');
        script.src = '/qrcode.js';
        script.async = true;
        script.onload = () => typeof window.qrcode === 'function' ? resolve(window.qrcode) : reject(new Error('二维码组件加载失败'));
        script.onerror = () => reject(new Error('二维码组件加载失败'));
        document.head.append(script);
      });
    }
    return qrLoader;
  }

  async function qrMarkup(text) {
    const factory = await loadQRCode();
    factory.stringToBytes = factory.stringToBytesFuncs['UTF-8'];
    const code = factory(0, 'M');
    code.addData(text, 'Byte');
    code.make();
    return code.createSvgTag({ cellSize: 5, margin: 20, scalable: true, title: '文件分享二维码', alt: '扫描二维码打开文件分享' });
  }

  function renderFileIcon(container, type, name) {
    container.innerHTML = window.EverythingShareIcons.markup(type, name);
  }

  function appMarkMarkup() {
    return '<img src="/assets/app-icon.svg" alt="">';
  }

  function installBranding() {
    let favicon = document.querySelector('link[rel~="icon"]');
    if (!favicon) {
      favicon = document.createElement('link');
      favicon.rel = 'icon';
      document.head.append(favicon);
    }
    favicon.type = 'image/svg+xml';
    favicon.href = '/assets/app-icon.svg';
  }

  function actionIcon(name) {
    const icons = {
      share: '<circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="m8.3 10.8 7.4-3.6M8.3 13.2l7.4 3.6"/>',
      download: '<path d="M12 3v12m0 0 4-4m-4 4-4-4M5 20h14"/>',
      clear: '<path d="M5 5l14 14M19 5 5 19"/>',
      link: '<path d="M10 13a5 5 0 0 0 7.1.1l2-2a5 5 0 0 0-7.1-7.1l-1.1 1.1M14 11a5 5 0 0 0-7.1-.1l-2 2A5 5 0 0 0 12 20l1.1-1.1"/>',
      qr: '<path d="M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h2v2h-2zM18 14h2v6h-2zM14 18h2v2h-2z"/>',
    };
    return `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[name]}</svg>`;
  }

  function showToast(text, error = false) {
    let toast = document.querySelector('.ev-toast');
    if (!toast) {
      toast = document.createElement('div');
      toast.className = 'ev-toast';
      toast.setAttribute('role', 'status');
      document.body.append(toast);
    }
    toast.classList.toggle('error', error);
    toast.textContent = text;
    toast.classList.add('visible');
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(() => toast.classList.remove('visible'), 2400);
  }

  function normalizePath(value) {
    const path = String(value || '').replaceAll('/', '\\').replace(/\\+$/, '');
    return /^[a-z]:$/i.test(path) ? `${path}\\` : path;
  }

  function selectionID(item) { return normalizePath(item.sourcePath).toLocaleLowerCase('zh-CN'); }
  function validSelectionItem(item) {
    return item && typeof item.sourcePath === 'string' && item.sourcePath && ['file', 'folder'].includes(item.type) && typeof item.name === 'string' && item.name;
  }

  function loadSelection() {
    try {
      const stored = JSON.parse(sessionStorage.getItem(selectionKey) || 'null');
      if (!stored || stored.version !== SELECTION_VERSION || !Array.isArray(stored.items)) return new Map();
      const result = new Map();
      stored.items.filter(validSelectionItem).slice(0, MAX_SELECTION).forEach((item) => {
        const clean = { sourcePath: normalizePath(item.sourcePath), type: item.type, name: item.name };
        addToSelectionMap(result, clean);
      });
      return result;
    } catch { return new Map(); }
  }

  function saveSelection() {
    try { sessionStorage.setItem(selectionKey, JSON.stringify({ version: SELECTION_VERSION, items: [...selected.values()] })); }
    catch { showToast('浏览器未允许保存跨页选择', true); }
  }

  function isDescendant(parent, candidate) {
    const root = normalizePath(parent).toLocaleLowerCase('zh-CN').replace(/\\+$/, '');
    const path = normalizePath(candidate).toLocaleLowerCase('zh-CN').replace(/\\+$/, '');
    return root !== path && path.startsWith(`${root}\\`);
  }

  function addToSelectionMap(map, item) {
    const id = selectionID(item);
    if (map.has(id)) return { added: false, reason: 'duplicate' };
    for (const parent of map.values()) {
      if (parent.type === 'folder' && isDescendant(parent.sourcePath, item.sourcePath)) return { added: false, reason: 'covered', parent };
    }
    if (item.type === 'folder') {
      for (const [key, existing] of map) if (isDescendant(item.sourcePath, existing.sourcePath)) map.delete(key);
    }
    if (map.size >= MAX_SELECTION) return { added: false, reason: 'limit' };
    map.set(id, { sourcePath: normalizePath(item.sourcePath), type: item.type, name: item.name });
    return { added: true };
  }

  function selectItem(item, anchor) {
    const result = addToSelectionMap(selected, item);
    if (result.reason === 'covered') showToast(`“${item.name}”已包含在文件夹“${result.parent.name}”中`);
    if (result.reason === 'limit') showToast(`一次最多选择 ${MAX_SELECTION} 个项目`, true);
    if (result.added) {
      lastSelectionAnchor = anchor || lastSelectionAnchor;
      saveSelection();
    }
    updateSelectionUI();
    return result.added;
  }

  function deselectItem(item) {
    if (selected.delete(selectionID(item))) saveSelection();
    if (lastSelectionAnchor?.dataset.evSelectionId === selectionID(item)) lastSelectionAnchor = null;
    updateSelectionUI();
  }

  function positionBatchBar() {
    batchPositionFrame = 0;
    const bar = document.querySelector('.ev-batchbar');
    if (!bar || bar.hidden) return;
    if (!matchMedia('(min-width: 768px)').matches) {
      bar.style.removeProperty('--ev-batchbar-top');
      return;
    }
    if (!lastSelectionAnchor?.isConnected || !lastSelectionAnchor.classList.contains('ev-selected')) {
      const visibleSelected = [...document.querySelectorAll('tr.ev-selected')];
      lastSelectionAnchor = visibleSelected.at(-1) || null;
    }
    const halfHeight = Math.max(27, bar.offsetHeight / 2);
    const anchorRect = lastSelectionAnchor?.getBoundingClientRect();
    const desiredTop = anchorRect ? anchorRect.top + anchorRect.height / 2 : window.innerHeight - halfHeight - 18;
    const top = Math.min(window.innerHeight - halfHeight - 12, Math.max(halfHeight + 12, desiredTop));
    bar.style.setProperty('--ev-batchbar-top', `${Math.round(top)}px`);
  }

  function requestBatchBarPosition() {
    if (batchPositionFrame) return;
    batchPositionFrame = requestAnimationFrame(positionBatchBar);
  }

  function closeOperationMenus(except) {
    document.querySelectorAll('details.ev-operation-menu[open]').forEach((menu) => { if (menu !== except) menu.open = false; });
  }

  function operationMenu(items, ariaLabel) {
    const details = document.createElement('details');
    details.className = 'ev-operation-menu';
    const summary = document.createElement('summary');
    summary.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="12" r="1.7"/><circle cx="12" cy="12" r="1.7"/><circle cx="19" cy="12" r="1.7"/></svg>';
    summary.setAttribute('aria-label', ariaLabel);
    const panel = document.createElement('div');
    panel.className = 'ev-operation-panel';
    items.forEach((item) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.textContent = item.label;
      button.addEventListener('click', async () => { details.open = false; await item.action(); });
      panel.append(button);
    });
    details.addEventListener('toggle', () => { if (details.open) closeOperationMenus(details); });
    details.append(summary, panel);
    return details;
  }

  function fullPathFromHref(href) {
    try {
      const raw = String(href || '');
      let pathname;
      if (raw.startsWith('//')) {
        pathname = `\\\\${decodeURIComponent(raw.slice(2).split(/[?#]/, 1)[0])}`;
      } else {
        const parsed = new URL(raw, location.origin);
        if (parsed.origin !== location.origin) return '';
        pathname = decodeURIComponent(parsed.pathname).replaceAll('/', '\\');
      }
      if (pathname.startsWith('\\\\')) return normalizePath(pathname);
      const decoded = pathname.replace(/^\\+/, '');
      return /^[A-Za-z]:(?:\\|$)/.test(decoded) ? normalizePath(decoded) : '';
    } catch { return ''; }
  }

  function everythingHref(sourcePath) {
    const normalized = normalizePath(sourcePath);
    const unc = normalized.startsWith('\\\\');
    const parts = normalized.replace(/^\\\\/, '').split('\\').map(encodeURIComponent);
    return `${unc ? '//' : '/'}${parts.join('/')}`;
  }

  function resultInfo(row) {
    const target = row.querySelector('td.file a[href], td.folder a[href]');
    if (!target) return null;
    const sourcePath = fullPathFromHref(target.getAttribute('href'));
    if (!sourcePath) return null;
    return { sourcePath, type: row.querySelector('td.folder') ? 'folder' : 'file', name: target.textContent.trim() };
  }

  function parentPath(sourcePath) {
    const normalized = normalizePath(sourcePath);
    const separator = normalized.lastIndexOf('\\');
    if (separator < 0) return '';
    if (separator === 2 && /^[A-Za-z]:/.test(normalized)) return normalized.slice(0, 3);
    return normalized.slice(0, separator);
  }

  function ensurePathCell(row, info) {
    let cell = row.querySelector('td.pathdata');
    const pathText = parentPath(info.sourcePath);
    if (!cell) {
      cell = document.createElement('td');
      cell.className = 'pathdata';
      const sizeCell = row.querySelector('td.sizedata');
      row.insertBefore(cell, sizeCell || row.querySelector('td.modifieddata'));
    }
    if (!cell.textContent.trim()) cell.textContent = pathText;
    cell.title = pathText;
    return cell;
  }

  function setHeaderText(cell, text) {
    if (!cell) return;
    const link = cell.querySelector('a');
    const target = link || cell;
    if (target.textContent.trim() !== text) target.textContent = text;
    cell.setAttribute('aria-label', `${text}栏`);
  }

  function localizeEverythingUI() {
    document.documentElement.lang = 'zh-CN';
    if (document.title !== '文件搜索') document.title = '文件搜索';
    setHeaderText(document.querySelector('.nameheader'), '名称');
    setHeaderText(document.querySelector('.pathheader'), '路径');
    setHeaderText(document.querySelector('.sizeheader'), '大小');
    setHeaderText(document.querySelector('.modifiedheader'), '修改日期');
    document.querySelectorAll('.prevnext a,.nav a').forEach((link) => {
      const key = link.textContent.trim().toLowerCase();
      const labels = { first: '首页', previous: '上一页', prev: '上一页', next: '下一页', last: '末页' };
      if (labels[key]) link.textContent = labels[key];
      const title = link.getAttribute('title')?.trim().toLowerCase();
      if (labels[title]) link.title = labels[title];
    });
    document.querySelectorAll('.numresults').forEach((node) => {
      const text = node.textContent.trim();
      if (/volumes?/i.test(text)) node.textContent = '磁盘列表';
      else if (/results?|objects?|files?/i.test(text)) {
        const count = text.match(/[\d,]+/)?.[0];
        node.textContent = count ? `找到 ${count} 个结果` : '搜索结果';
      }
    });
  }

  function normalizeTableStructure() {
    const nameHeader = document.querySelector('.nameheader');
    const headerRow = nameHeader?.closest('tr');
    if (!headerRow) return;
    let pathHeader = headerRow.querySelector('.pathheader');
    if (!pathHeader) {
      pathHeader = document.createElement(nameHeader.tagName || 'td');
      pathHeader.className = 'pathheader';
      pathHeader.textContent = '路径';
      headerRow.insertBefore(pathHeader, headerRow.querySelector('.sizeheader,.modifiedheader'));
    }
    setHeaderText(pathHeader, '路径');
    currentRows().forEach(({ row, info }) => ensurePathCell(row, info));
  }

  function ensureFileIcon(row, info) {
    const target = row.querySelector('td.file a[href], td.folder a[href]');
    if (!target || target.querySelector('.ev-file-icon')) return;
    target.classList.add('ev-file-entry');
    const icon = document.createElement('span');
    icon.className = `ev-file-icon ev-type-${info.type}`;
    icon.setAttribute('aria-hidden', 'true');
    renderFileIcon(icon, info.type, info.name);
    const label = document.createElement('span');
    label.className = 'ev-file-name';
    label.textContent = info.name;
    target.title = info.name;
    target.replaceChildren(icon, label);
  }

  function ensureParentDirectoryIcon() {
    document.querySelectorAll('td.updir a[href]').forEach((target) => {
      if (target.querySelector('.ev-file-icon')) return;
      target.querySelectorAll('img.icon').forEach((icon) => icon.remove());
      target.classList.add('ev-file-entry');
      const icon = document.createElement('span');
      icon.className = 'ev-file-icon ev-type-folder';
      icon.setAttribute('aria-hidden', 'true');
      renderFileIcon(icon, 'folder', '');
      target.prepend(icon);
    });
  }

  function login(returnTo = location.href) {
    const rd = new URL(returnTo, location.origin).href;
    location.assign(`${LOGIN_PATH}?rd=${encodeURIComponent(rd)}`);
  }

  async function adminFetch(url, options = {}) {
    const response = await fetch(url, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(options.headers || {}) }, ...options });
    if (response.status === 401) { login(); throw new Error('正在进入管理员认证…'); }
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || '请求失败，请稍后再试');
    return data;
  }

  function addToolbar() {
    const form = document.querySelector('#searchform');
    if (!form || document.querySelector('.ev-toolbar')) return;
    const searchInput = form.querySelector('input[type="text"],input[name="search"]');
    if (searchInput) searchInput.placeholder = '搜索我的文件';
    const toolbar = document.createElement('div');
    toolbar.className = 'ev-toolbar';
    toolbar.innerHTML = `<div class="ev-title"><a class="ev-title-mark ev-title-home" href="/" aria-label="返回首页">${appMarkMarkup()}</a><strong>全部文件</strong></div><div class="ev-actions"><button class="ev-button" id="ev-share-manager" type="button">分享管理</button></div>`;
    form.insertAdjacentElement('beforebegin', toolbar);
    toolbar.querySelector('#ev-share-manager').addEventListener('click', async () => {
      try { await adminFetch(`${API}/session`); location.assign('/share-admin/'); }
      catch (error) { if (!error.message.includes('认证')) showToast(error.message, true); }
    });
    const headers = [...document.querySelectorAll('.nameheader a,.pathheader a,.sizeheader a,.modifiedheader a')];
    if (headers.length) {
      const details = document.createElement('details');
      details.className = 'ev-sort-panel';
      const summary = document.createElement('summary'); summary.textContent = '排序与筛选';
      const links = document.createElement('div'); links.className = 'ev-sort-links';
      headers.forEach((source) => { const link = source.cloneNode(true); link.textContent = source.textContent.trim() || '排序'; links.append(link); });
      details.append(summary, links); toolbar.append(details);
    }
  }

  function addBatchBar() {
    const table = document.querySelector('.nameheader')?.closest('table');
    if (!table) return;
    let bar = document.querySelector('.ev-batchbar');
    if (!bar) {
      bar = document.createElement('section');
      bar.className = 'ev-batchbar';
      bar.hidden = true;
      bar.setAttribute('aria-live', 'polite');
      bar.innerHTML = `<div class="ev-selected-copy"><strong>已选中0个文件/文件夹</strong></div><div class="ev-batch-actions"><button type="button" data-action="share" aria-label="分享">${actionIcon('share')}<span>分享</span></button><button type="button" data-action="download" aria-label="下载">${actionIcon('download')}<span>下载</span></button><button type="button" data-action="clear" aria-label="清除选择">${actionIcon('clear')}<span>清除选择</span></button></div>`;
      bar.querySelector('[data-action="share"]').addEventListener('click', () => openShare([...selected.values()]));
      bar.querySelector('[data-action="download"]').addEventListener('click', downloadSelection);
      bar.querySelector('[data-action="clear"]').addEventListener('click', () => { selected.clear(); lastSelectionAnchor = null; saveSelection(); updateSelectionUI(); showToast('已清除全部跨页选择'); });
    }
    if (bar.nextElementSibling !== table) table.insertAdjacentElement('beforebegin', bar);
  }

  function currentRows() {
    return [...document.querySelectorAll('tr.trdata1,tr.trdata2')].map((row) => ({ row, info: resultInfo(row) })).filter((value) => value.info);
  }

  function toggleCurrentPage(checked) {
    const rows = currentRows();
    if (!checked) {
      rows.forEach(({ info }) => selected.delete(selectionID(info)));
      saveSelection(); updateSelectionUI(); return;
    }
    const next = new Map(selected);
    let exceedsLimit = false;
    for (const { info } of rows) {
      if (addToSelectionMap(next, info).reason === 'limit') exceedsLimit = true;
    }
    if (exceedsLimit) { showToast(`全选后会超过 ${MAX_SELECTION} 项上限`, true); updateSelectionUI(); return; }
    selected.clear(); next.forEach((value, key) => selected.set(key, value));
    lastSelectionAnchor = rows.at(-1)?.row || lastSelectionAnchor;
    saveSelection(); updateSelectionUI();
  }

  function installColumnSizing() {
    const nameHeader = document.querySelector('.nameheader');
    const table = nameHeader?.closest('table');
    const headerRow = nameHeader?.closest('tr');
    const pathHeader = headerRow?.querySelector('.pathheader');
    if (!table || !headerRow || !pathHeader) return;
    if (!headerRow.querySelector('.ev-select-header')) {
      const cell = document.createElement('td'); cell.className = 'ev-select-header';
      const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.className = 'ev-select-box ev-page-select'; checkbox.setAttribute('aria-label', '选择当前页全部项目');
      checkbox.addEventListener('change', () => toggleCurrentPage(checkbox.checked));
      cell.append(checkbox); headerRow.prepend(cell);
    }
    if (!headerRow.querySelector('.ev-actions-header')) {
      const cell = document.createElement('td'); cell.className = 'ev-actions-header'; cell.textContent = '操作'; headerRow.append(cell);
    }
    const columnCount = headerRow.children.length;
    [...table.rows].forEach((row) => {
      if (row === headerRow || row.matches('.trdata1,.trdata2')) return;
      const spanningCell = row.children.length === 1 ? row.cells[0] : null;
      if (spanningCell?.hasAttribute('colspan')) spanningCell.colSpan = columnCount;
    });
    if (table.dataset.evResizable === 'true') return;
    table.dataset.evResizable = 'true'; table.classList.add('ev-resizable-table');
    const colgroup = document.createElement('colgroup');
    const columns = ['select', 'name', 'path', 'size', 'modified', 'actions'].map((name) => { const column = document.createElement('col'); column.dataset.column = name; colgroup.append(column); return column; });
    table.prepend(colgroup);
    const headers = [nameHeader, pathHeader, headerRow.querySelector('.sizeheader'), headerRow.querySelector('.modifiedheader'), headerRow.querySelector('.ev-actions-header')];
    const labels = ['名称', '路径', '大小', '修改日期', '操作'];
    const minimums = [160, 150, 80, 120, 64];
    let ratios = [0.36, 0.32, 0.10, 0.16, 0.06];
    try {
      const stored = JSON.parse(localStorage.getItem(columnWidthsStorageName) || 'null');
      if (Array.isArray(stored) && stored.length === 5 && stored.every((value) => Number.isFinite(value) && value > 0)) {
        const total = stored.reduce((sum, value) => sum + value, 0);
        ratios = stored.map((value) => value / total);
      }
    } catch { /* Use defaults. */ }
    const fitWidths = (available) => {
      const widths = ratios.map((ratio) => ratio * available);
      minimums.forEach((minimum, index) => { widths[index] = Math.max(minimum, widths[index]); });
      let excess = widths.reduce((sum, value) => sum + value, 0) - available;
      for (let pass = 0; pass < 4 && excess > 0.1; pass += 1) {
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
      if (!matchMedia('(min-width: 768px)').matches) { columns.forEach((column) => { column.style.width = ''; }); return 0; }
      const tableWidth = table.getBoundingClientRect().width || 1180;
      const flexibleWidth = Math.max(minimums.reduce((sum, value) => sum + value, 0), tableWidth - 42);
      const widths = fitWidths(flexibleWidth);
      columns[0].style.width = '42px';
      widths.forEach((width, index) => { columns[index + 1].style.width = `${Math.round(width)}px`; });
      return { flexibleWidth, widths };
    };
    const saveWidths = () => localStorage.setItem(columnWidthsStorageName, JSON.stringify(ratios.map((value) => Number(value.toFixed(5)))));
    const resizePair = (index, delta, starting) => {
      const lastColumn = index === headers.length - 1;
      const partner = lastColumn ? index - 1 : index + 1;
      const widths = [...starting.widths];
      const directedDelta = lastColumn ? -delta : delta;
      const bounded = Math.max(minimums[index] - widths[index], Math.min(widths[partner] - minimums[partner], directedDelta));
      widths[index] += bounded; widths[partner] -= bounded;
      ratios = widths.map((width) => width / starting.flexibleWidth);
      applyWidths();
    };
    headers.slice(0, -1).forEach((header, index) => {
      if (!header) return;
      const handle = document.createElement('span');
      handle.className = 'ev-column-resizer'; handle.tabIndex = 0; handle.setAttribute('role', 'separator'); handle.setAttribute('aria-orientation', 'vertical'); handle.setAttribute('aria-label', `调整${labels[index]}栏宽`);
      header.title = `拖动分隔线调整${labels[index]}栏宽`; header.append(handle);
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
    window.addEventListener('resize', applyWidths, { passive: true }); applyWidths();
  }

  function enhanceRows() {
    document.querySelectorAll('tr.trdata1,tr.trdata2').forEach((row) => {
      const info = resultInfo(row); if (!info) return;
      row.dataset.evSelectionId = selectionID(info); ensureFileIcon(row, info);
      if (!row.querySelector('.ev-select-cell')) {
        const cell = document.createElement('td'); cell.className = 'ev-select-cell';
        const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.className = 'ev-select-box ev-row-select'; checkbox.setAttribute('aria-label', `选择 ${info.name}`);
        checkbox.addEventListener('change', () => { if (checkbox.checked) { if (!selectItem(info, row)) checkbox.checked = false; } else deselectItem(info); });
        cell.append(checkbox); row.prepend(cell);
      }
      const pathCell = ensurePathCell(row, info); const sizeCell = row.querySelector('.sizedata'); const modifiedCell = row.querySelector('.modifieddata');
      if (!row.querySelector('.ev-mobile-meta')) {
        const meta = document.createElement('td'); meta.className = 'ev-mobile-meta';
        const size = document.createElement('span'); size.textContent = sizeCell?.textContent.trim() || (info.type === 'folder' ? '文件夹' : '未知大小');
        const modified = document.createElement('span'); modified.textContent = modifiedCell?.textContent.trim() || '';
        meta.append(size, modified); row.append(meta);
      }
      if (!row.querySelector('.ev-row-actions')) {
        const cell = document.createElement('td'); cell.className = 'ev-row-actions';
        cell.append(operationMenu([{ label: '创建分享', action: () => openShare([info]) }, { label: '复制完整路径', action: async () => { try { await copyText(info.sourcePath); showToast('完整路径已复制'); } catch (error) { showToast(error.message || '复制失败', true); } } }], `操作 ${info.name}`));
        row.append(cell);
      }
    });
    updateSelectionUI();
  }

  function updateSelectionUI() {
    const rows = currentRows();
    rows.forEach(({ row, info }) => { const checked = selected.has(selectionID(info)); const checkbox = row.querySelector('.ev-row-select'); if (checkbox) checkbox.checked = checked; row.classList.toggle('ev-selected', checked); });
    const checkedCount = rows.filter(({ info }) => selected.has(selectionID(info))).length;
    const pageCheckbox = document.querySelector('.ev-page-select');
    if (pageCheckbox) { pageCheckbox.checked = rows.length > 0 && checkedCount === rows.length; pageCheckbox.indeterminate = checkedCount > 0 && checkedCount < rows.length; }
    const bar = document.querySelector('.ev-batchbar');
    if (bar) {
      bar.hidden = selected.size === 0;
      const count = `已选中${selected.size}个文件/文件夹`;
      const label = bar.querySelector('.ev-selected-copy strong');
      if (label.textContent !== count) label.textContent = count;
      requestBatchBarPosition();
    }
  }

  function commonRootLabel(items) {
    const paths = items.map((item) => normalizePath(item.sourcePath).split('\\'));
    if (!paths.length) return '—';
    if (!paths.every((parts) => parts[0].toLocaleLowerCase('zh-CN') === paths[0][0].toLocaleLowerCase('zh-CN'))) return '跨盘符：按 C盘、D盘或网络共享标签分组';
    const common = []; const minimum = Math.min(...paths.map((parts) => parts.length));
    for (let index = 0; index < minimum; index += 1) { const part = paths[0][index]; if (!paths.every((parts) => parts[index].toLocaleLowerCase('zh-CN') === part.toLocaleLowerCase('zh-CN'))) break; common.push(part); }
    if (items.length === 1 || common.length === paths[0].length) common.pop();
    return common.join('\\') || paths[0][0];
  }

  function modalElement(items) {
    const backdrop = document.createElement('div'); backdrop.className = 'ev-modal-backdrop';
    backdrop.innerHTML = `<section class="ev-share-modal" role="dialog" aria-modal="true" aria-labelledby="ev-share-title"><header class="ev-share-head"><h2 id="ev-share-title"></h2><button class="ev-modal-close" type="button" aria-label="关闭">×</button></header><nav class="ev-share-tabs" aria-label="分享方式"><span>链接分享</span></nav><div class="ev-share-body"><div class="ev-selection-summary"><strong></strong><span class="ev-common-root"></span><details><summary>查看所选项目</summary><ul></ul></details></div><div class="ev-setting-row"><div class="ev-setting-label">有效期：</div><div class="ev-segments" role="radiogroup" aria-label="有效期"><label><input type="radio" name="ev-expiry" value="1">1天</label><label><input type="radio" name="ev-expiry" value="7" checked>7天</label><label><input type="radio" name="ev-expiry" value="30">30天</label><label><input type="radio" name="ev-expiry" value="365">365天</label><label><input type="radio" name="ev-expiry" value="permanent">永久有效</label></div></div><div class="ev-code-settings"><div class="ev-setting-row"><div class="ev-setting-label">提取码：</div><div class="ev-segments" role="radiogroup" aria-label="提取码方式"><label><input type="radio" name="ev-code-mode" value="random" checked>随机生成</label><label><input type="radio" name="ev-code-mode" value="custom">自定义</label></div></div><label class="ev-auto-code"><input class="ev-select-box" type="checkbox" name="ev-auto-fill" checked>分享链接自动填充提取码 <span title="接收者打开链接后自动填入提取码">ⓘ</span></label><div class="ev-custom-code" hidden><input name="ev-code" minlength="4" maxlength="12" pattern="[A-Za-z0-9]{4,12}" placeholder="输入4至12位字母或数字" aria-label="自定义提取码"></div></div><div class="ev-setting-row"><div class="ev-setting-label">下载次数：</div><div class="ev-segments" role="radiogroup" aria-label="下载次数"><label><input type="radio" name="ev-limit" value="" checked>不限</label><label><input type="radio" name="ev-limit" value="1">1次</label><label><input type="radio" name="ev-limit" value="5">5次</label><label><input type="radio" name="ev-limit" value="20">20次</label></div></div><div class="ev-share-message" role="alert"></div><div class="ev-share-result" hidden><strong>分享已创建</strong><div class="ev-result-row"><input name="ev-result-link" readonly aria-label="分享链接"><button type="button" data-copy-link>复制</button></div><div class="ev-result-row ev-result-code"><span>提取码：<b></b></span><button type="button" data-copy-code>复制提取码</button></div></div><div class="ev-qr-result" hidden></div></div><footer class="ev-share-foot"><button class="ev-share-button ev-share-action" type="button" data-create-link>${actionIcon('link')}复制链接</button><button class="ev-button ev-share-action" type="button" data-create-qr>${actionIcon('qr')}生成二维码</button></footer></section>`;
    backdrop.querySelector('#ev-share-title').textContent = items.length === 1 ? `分享文件(夹)：${items[0].name}` : `分享文件(夹)：已选 ${items.length} 项`;
    backdrop.querySelector('.ev-selection-summary strong').textContent = `已选择 ${items.length} 个文件/文件夹`;
    backdrop.querySelector('.ev-common-root').textContent = `结构根目录：${commonRootLabel(items)}`;
    const list = backdrop.querySelector('.ev-selection-summary ul');
    items.forEach((item) => { const li = document.createElement('li'); li.textContent = item.sourcePath; list.append(li); });
    return backdrop;
  }

  async function openShare(items, resumed = false) {
    if (!items.length || document.querySelector('.ev-modal-backdrop')) return;
    if (!resumed) try { sessionStorage.setItem(pendingKey, JSON.stringify(items)); } catch { /* Continue. */ }
    try { await adminFetch(`${API}/session`); } catch (error) { if (!error.message.includes('认证')) showToast(error.message, true); return; }
    try { sessionStorage.removeItem(pendingKey); } catch { /* No-op. */ }
    const previousFocus = document.activeElement; const modal = modalElement(items); const message = modal.querySelector('.ev-share-message'); const settings = [...modal.querySelectorAll('input[name^="ev-"]')];
    let created; let creating; let linkWithCode = true;
    const close = () => { modal.remove(); if (previousFocus instanceof HTMLElement) previousFocus.focus({ preventScroll: true }); };
    modal.querySelector('.ev-modal-close').addEventListener('click', close);
    modal.addEventListener('click', (event) => { if (event.target === modal) close(); });
    modal.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') { event.preventDefault(); close(); return; }
      if (event.key !== 'Tab') return;
      const focusable = [...modal.querySelectorAll('button:not(:disabled),input:not(:disabled),summary,[tabindex]:not([tabindex="-1"])')].filter((element) => element.getClientRects().length);
      if (!focusable.length) return; const first = focusable[0]; const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    });
    modal.querySelectorAll('input[name="ev-code-mode"]').forEach((radio) => radio.addEventListener('change', () => { const custom = modal.querySelector('input[name="ev-code-mode"]:checked').value === 'custom'; modal.querySelector('.ev-custom-code').hidden = !custom; if (custom) modal.querySelector('input[name="ev-code"]').focus(); }));
    const renderResult = () => {
      const result = modal.querySelector('.ev-share-result'); const link = linkWithCode ? created.url : created.baseUrl;
      result.hidden = false; result.querySelector('input[name="ev-result-link"]').value = link; result.querySelector('.ev-result-code b').textContent = created.code; result.querySelector('.ev-result-code').hidden = linkWithCode;
      result.querySelector('[data-copy-link]').onclick = async () => { try { await copyText(link); showToast('分享链接已复制'); } catch (error) { showToast(error.message, true); } };
      result.querySelector('[data-copy-code]').onclick = async () => { try { await copyText(created.code); showToast('提取码已复制'); } catch (error) { showToast(error.message, true); } };
      return link;
    };
    const ensureCreated = async () => {
      if (created) return created; if (creating) return creating;
      const customMode = modal.querySelector('input[name="ev-code-mode"]:checked').value === 'custom'; const code = modal.querySelector('input[name="ev-code"]').value.trim().toUpperCase();
      if (customMode && !/^[A-Z0-9]{4,12}$/.test(code)) throw new Error('自定义提取码必须为4至12位字母或数字');
      const expiry = modal.querySelector('input[name="ev-expiry"]:checked').value; const limit = modal.querySelector('input[name="ev-limit"]:checked').value; linkWithCode = modal.querySelector('input[name="ev-auto-fill"]').checked;
      const payload = { sources: items.map((item) => ({ sourcePath: item.sourcePath, type: item.type })), expiresAt: expiry === 'permanent' ? 'permanent' : new Date(Date.now() + Number(expiry) * 86400000).toISOString(), maxDownloads: limit ? Number(limit) : null, code: customMode ? code : '' };
      message.textContent = items.some((item) => item.type === 'folder') ? '正在生成目录快照，请稍候…' : '正在创建分享…';
      creating = adminFetch(`${API}/shares`, { method: 'POST', body: JSON.stringify(payload) }).then((result) => { created = result; settings.forEach((input) => { input.disabled = true; }); modal.classList.add('ev-share-created'); message.textContent = ''; renderResult(); return result; }).finally(() => { creating = null; });
      return creating;
    };
    modal.querySelector('[data-create-link]').addEventListener('click', async (event) => {
      const button = event.currentTarget; button.disabled = true;
      try { await ensureCreated(); const link = renderResult(); await copyText(link); message.textContent = linkWithCode ? '分享链接已复制' : '基础链接已复制，请另行发送提取码'; }
      catch (error) { message.textContent = error.message || '创建分享失败'; }
      finally { button.disabled = false; }
    });
    modal.querySelector('[data-create-qr]').addEventListener('click', async (event) => {
      const button = event.currentTarget; button.disabled = true;
      try { await ensureCreated(); const link = renderResult(); const panel = modal.querySelector('.ev-qr-result'); panel.innerHTML = await qrMarkup(link); panel.hidden = false; panel.scrollIntoView({ block: 'nearest' }); message.textContent = '二维码已在本地生成'; }
      catch (error) { message.textContent = error.message || '二维码生成失败'; }
      finally { button.disabled = false; }
    });
    document.body.append(modal); modal.querySelector('.ev-modal-close').focus();
  }

  async function downloadSelection() {
    const items = [...selected.values()]; if (!items.length) return;
    if (items.length === 1 && items[0].type === 'file') { location.assign(everythingHref(items[0].sourcePath)); return; }
    const button = document.querySelector('.ev-batchbar [data-action="download"]'); if (button) button.disabled = true;
    try {
      const result = await adminFetch(`${API}/download-packages`, { method: 'POST', body: JSON.stringify({ sources: items.map((item) => ({ sourcePath: item.sourcePath, type: item.type })) }) });
      showToast(result.zipMode === 'cached' ? '正在准备完整压缩包…' : '即将开始流式下载，不支持断点续传'); location.assign(result.url);
    } catch (error) { showToast(error.message || '无法创建下载包', true); }
    finally { if (button) button.disabled = false; }
  }

  function boot() {
    installBranding();
    if (document.documentElement.dataset.evMenuBehavior !== 'true') {
      document.documentElement.dataset.evMenuBehavior = 'true';
      document.addEventListener('click', (event) => { if (!event.target.closest('.ev-operation-menu')) closeOperationMenus(); });
      document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeOperationMenus(); });
      window.addEventListener('scroll', requestBatchBarPosition, { passive: true });
      window.addEventListener('resize', requestBatchBarPosition, { passive: true });
    }
    const enhance = () => { localizeEverythingUI(); normalizeTableStructure(); ensureParentDirectoryIcon(); addToolbar(); enhanceRows(); installColumnSizing(); addBatchBar(); updateSelectionUI(); };
    enhance();
    let scheduled = false;
    const observerOptions = { childList: true, subtree: true };
    const observer = new MutationObserver(() => {
      if (scheduled) return;
      scheduled = true;
      queueMicrotask(() => {
        observer.disconnect();
        scheduled = false;
        enhance();
        observer.observe(document.body, observerOptions);
      });
    });
    observer.observe(document.body, observerOptions);
    try { const pending = JSON.parse(sessionStorage.getItem(pendingKey) || 'null'); if (Array.isArray(pending) && pending.length && pending.every(validSelectionItem)) openShare(pending, true); }
    catch { try { sessionStorage.removeItem(pendingKey); } catch { /* No-op. */ } }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
