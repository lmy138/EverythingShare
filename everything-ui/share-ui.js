(() => {
  'use strict';
  const LOGIN_PATH = '/share-oauth2/start';
  const API = '/share-api/v1';
  const pendingKey = 'everything-share-pending';
  const columnRatioKey = 'everything-name-path-ratio';

  async function copyText(text) {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(text);
        return;
      } catch {
        // Mobile browsers and embedded webviews can expose the API but reject
        // writes. Fall through to the selection-based compatibility path.
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
    if (!copied) throw new Error('浏览器未允许复制，请长按文本手动复制');
  }

  function iconMarkup(type) {
    if (type === 'folder') {
      return '<svg viewBox="0 0 24 24" focusable="false"><path class="ev-icon-soft" d="M3.5 7.2A2.2 2.2 0 0 1 5.7 5h3.55l1.85 2.05h7.2a2.2 2.2 0 0 1 2.2 2.2v7.55a2.2 2.2 0 0 1-2.2 2.2H5.7a2.2 2.2 0 0 1-2.2-2.2V7.2Z"/><path class="ev-icon-main" d="M3.5 10h17v6.8a2.2 2.2 0 0 1-2.2 2.2H5.7a2.2 2.2 0 0 1-2.2-2.2V10Z"/></svg>';
    }
    return '<svg viewBox="0 0 24 24" focusable="false"><path class="ev-icon-paper" d="M6.4 3.5h6.9l4.8 4.8v11.3a.9.9 0 0 1-.9.9H6.4a.9.9 0 0 1-.9-.9V4.4a.9.9 0 0 1 .9-.9Z"/><path class="ev-icon-fold" d="M12.8 3.8v5h5"/><path class="ev-icon-line" d="M8.7 13h6.4M8.7 16.2h4.8"/></svg>';
  }

  function appMarkMarkup() {
    return '<svg viewBox="0 0 24 24" focusable="false"><path d="M7.2 3.5h6.5l4.6 4.6v5.2"/><path d="M13.2 3.8v4.8H18"/><path d="M10.1 18.2 8.4 20a2.8 2.8 0 0 1-4-4l2.2-2.2a2.8 2.8 0 0 1 4-.1"/><path d="m13.9 12.8 1.7-1.7a2.8 2.8 0 1 1 4 4l-2.2 2.2a2.8 2.8 0 0 1-4 .1"/><path d="m9.5 15.5 5-5"/></svg>';
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
    showToast.timer = setTimeout(() => toast.classList.remove('visible'), 1800);
  }

  function closeOperationMenus(except) {
    document.querySelectorAll('details.ev-operation-menu[open]').forEach((menu) => {
      if (menu !== except) menu.open = false;
    });
  }

  function operationMenu(label, items, ariaLabel = label) {
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
      if (item.danger) button.classList.add('danger');
      button.addEventListener('click', async () => {
        details.open = false;
        await item.action();
      });
      panel.append(button);
    });
    details.addEventListener('toggle', () => {
      if (details.open) closeOperationMenus(details);
    });
    details.append(summary, panel);
    return details;
  }

  function ensureFileIcon(row, info) {
    const target = row.querySelector('td.file a[href], td.folder a[href]');
    if (!target || target.querySelector('.ev-file-icon')) return;
    const icon = document.createElement('span');
    icon.className = `ev-file-icon ev-type-${info.type}`;
    icon.setAttribute('aria-hidden', 'true');
    icon.innerHTML = iconMarkup(info.type);
    const label = document.createElement('span');
    label.className = 'ev-file-name';
    label.textContent = info.name;
    target.title = info.name;
    target.replaceChildren(icon, label);
  }

  function installColumnSizing() {
    const nameHeader = document.querySelector('.nameheader');
    const table = nameHeader?.closest('table');
    if (!table || table.dataset.evResizable === 'true') return;
    const headerRow = nameHeader.closest('tr');
    const pathHeader = headerRow?.querySelector('.pathheader');
    if (!headerRow || !pathHeader) return;

    table.dataset.evResizable = 'true';
    table.classList.add('ev-resizable-table');
    if (!headerRow.querySelector('.ev-actions-header')) {
      const actionsHeader = document.createElement('td');
      actionsHeader.className = 'ev-actions-header';
      actionsHeader.textContent = '操作';
      headerRow.append(actionsHeader);
    }

    const colgroup = document.createElement('colgroup');
    const columns = ['name', 'path', 'size', 'modified', 'actions'].map((name) => {
      const column = document.createElement('col');
      column.dataset.column = name;
      colgroup.append(column);
      return column;
    });
    table.prepend(colgroup);

    let ratio = Number.parseFloat(localStorage.getItem(columnRatioKey));
    if (!Number.isFinite(ratio)) ratio = 0.43;
    const clampRatio = (value, flexibleWidth) => {
      const minName = Math.min(180, flexibleWidth * 0.45);
      const minPath = Math.min(220, flexibleWidth * 0.5);
      return Math.min(1 - minPath / flexibleWidth, Math.max(minName / flexibleWidth, value));
    };
    const applyWidths = () => {
      if (!matchMedia('(min-width: 768px)').matches) {
        columns.forEach((column) => { column.style.width = ''; });
        return 0;
      }
      const tableWidth = table.getBoundingClientRect().width || 1180;
      const fixedWidth = Math.min(352, tableWidth * 0.46);
      const flexibleWidth = Math.max(280, tableWidth - fixedWidth);
      ratio = clampRatio(ratio, flexibleWidth);
      columns[0].style.width = `${Math.round(flexibleWidth * ratio)}px`;
      columns[1].style.width = `${Math.round(flexibleWidth * (1 - ratio))}px`;
      columns[2].style.width = '112px';
      columns[3].style.width = '176px';
      columns[4].style.width = '64px';
      return flexibleWidth;
    };

    const handle = document.createElement('span');
    handle.className = 'ev-column-resizer';
    handle.tabIndex = 0;
    handle.setAttribute('role', 'separator');
    handle.setAttribute('aria-orientation', 'vertical');
    handle.setAttribute('aria-label', '调整名称和路径栏宽');
    nameHeader.title = '拖动右侧分隔线调整名称和路径栏宽';
    nameHeader.append(handle);

    handle.addEventListener('pointerdown', (event) => {
      if (!matchMedia('(min-width: 768px)').matches) return;
      event.preventDefault();
      const startX = event.clientX;
      const startRatio = ratio;
      const flexibleWidth = applyWidths();
      handle.setPointerCapture(event.pointerId);
      const move = (moveEvent) => {
        ratio = clampRatio(startRatio + (moveEvent.clientX - startX) / flexibleWidth, flexibleWidth);
        applyWidths();
      };
      const finish = () => {
        localStorage.setItem(columnRatioKey, ratio.toFixed(4));
        handle.removeEventListener('pointermove', move);
        handle.removeEventListener('pointerup', finish);
        handle.removeEventListener('pointercancel', finish);
      };
      handle.addEventListener('pointermove', move);
      handle.addEventListener('pointerup', finish);
      handle.addEventListener('pointercancel', finish);
    });
    handle.addEventListener('keydown', (event) => {
      if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
      event.preventDefault();
      ratio += event.key === 'ArrowRight' ? 0.025 : -0.025;
      applyWidths();
      localStorage.setItem(columnRatioKey, ratio.toFixed(4));
    });
    window.addEventListener('resize', applyWidths, { passive: true });
    applyWidths();
  }

  function fullPathFromHref(href) {
    try {
      const pathname = new URL(href, location.origin).pathname;
      const decoded = decodeURIComponent(pathname).replace(/^\/+/, '').replaceAll('/', '\\');
      return /^[A-Za-z]:\\/.test(decoded) ? decoded : '';
    } catch {
      return '';
    }
  }

  function resultInfo(row) {
    const target = row.querySelector('td.file a[href], td.folder a[href]');
    if (!target) return null;
    const sourcePath = fullPathFromHref(target.href);
    if (!sourcePath) return null;
    return {
      sourcePath,
      type: row.querySelector('td.folder') ? 'folder' : 'file',
      name: target.textContent.trim(),
    };
  }

  function login(returnTo = location.href) {
    const rd = new URL(returnTo, location.origin).href;
    location.assign(`${LOGIN_PATH}?rd=${encodeURIComponent(rd)}`);
  }

  async function adminFetch(url, options = {}) {
    const response = await fetch(url, {
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
      ...options,
    });
    if (response.status === 401) {
      login();
      throw new Error('正在进入管理员认证…');
    }
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || '请求失败，请稍后再试');
    return data;
  }

  function addToolbar() {
    const form = document.querySelector('#searchform');
    if (!form || document.querySelector('.ev-toolbar')) return;
    const toolbar = document.createElement('div');
    toolbar.className = 'ev-toolbar';
    toolbar.innerHTML = `
      <div class="ev-title">
        <span class="ev-title-mark" aria-hidden="true">${appMarkMarkup()}</span>
        <div><strong>文件搜索</strong><div class="ev-subtitle">快速查找、下载并安全分享</div></div>
      </div>
      <div class="ev-actions">
        <button class="ev-button" id="ev-share-manager" type="button">分享管理</button>
      </div>`;
    form.insertAdjacentElement('beforebegin', toolbar);
    toolbar.querySelector('#ev-share-manager').addEventListener('click', async () => {
      try {
        await adminFetch(`${API}/session`);
        location.assign('/share-admin/');
      } catch (error) {
        if (!error.message.includes('认证')) alert(error.message);
      }
    });

    const headers = [...document.querySelectorAll('.nameheader a,.pathheader a,.sizeheader a,.modifiedheader a')];
    if (headers.length) {
      const details = document.createElement('details');
      details.className = 'ev-sort-panel';
      const summary = document.createElement('summary');
      summary.textContent = '排序与筛选';
      const links = document.createElement('div');
      links.className = 'ev-sort-links';
      headers.forEach((source) => {
        const link = source.cloneNode(true);
        link.textContent = source.textContent.trim() || '排序';
        links.append(link);
      });
      details.append(summary, links);
      toolbar.append(details);
    }
  }

  function enhanceRows() {
    document.querySelectorAll('tr.trdata1,tr.trdata2').forEach((row) => {
      if (row.querySelector('.ev-row-actions')) return;
      const info = resultInfo(row);
      if (!info) return;
      ensureFileIcon(row, info);
      const pathCell = row.querySelector('.pathdata');
      const sizeCell = row.querySelector('.sizedata');
      const modifiedCell = row.querySelector('.modifieddata');
      if (pathCell) pathCell.title = info.sourcePath;
      if (!row.querySelector('.ev-mobile-meta')) {
        const meta = document.createElement('td');
        meta.className = 'ev-mobile-meta';
        const size = document.createElement('span');
        size.textContent = sizeCell?.textContent.trim() || (info.type === 'folder' ? '文件夹' : '未知大小');
        const modified = document.createElement('span');
        modified.textContent = modifiedCell?.textContent.trim() || '';
        meta.append(size, modified);
        row.append(meta);
      }
      const cell = document.createElement('td');
      cell.className = 'ev-row-actions';
      cell.append(operationMenu('操作', [
        { label: '创建分享', action: () => openShare(info) },
        {
          label: '复制完整路径',
          action: async () => {
            try {
              await copyText(info.sourcePath);
              showToast('完整路径已复制');
            } catch (error) {
              showToast(error.message || '复制失败', true);
            }
          },
        },
      ], `操作 ${info.name}`));
      row.append(cell);
    });
  }

  function modalElement(info) {
    const backdrop = document.createElement('div');
    backdrop.className = 'ev-modal-backdrop';
    backdrop.innerHTML = `
      <section class="ev-modal" role="dialog" aria-modal="true" aria-labelledby="ev-share-title">
        <header class="ev-modal-header">
          <div><h2 id="ev-share-title">创建安全分享</h2><div class="ev-subtitle">${info.type === 'folder' ? '文件夹快照分享' : '单文件分享'}</div></div>
          <button class="ev-modal-close" type="button" aria-label="关闭">×</button>
        </header>
        <form id="ev-share-form">
          <div class="ev-modal-body">
            <div class="ev-selected-path"></div>
            <div class="ev-field"><label for="ev-title-input">分享标题</label><input id="ev-title-input" maxlength="120"></div>
            <div class="ev-field"><label for="ev-code">提取码（留空自动生成）</label><input id="ev-code" minlength="4" maxlength="12" pattern="[A-Za-z0-9]{4,12}" autocapitalize="characters" placeholder="自动生成4位提取码"></div>
            <div class="ev-field"><label for="ev-expiry">有效期</label><select id="ev-expiry"><option value="1">1天</option><option value="7" selected>7天</option><option value="30">30天</option><option value="permanent">永久</option></select></div>
            <div class="ev-field"><label for="ev-limit">下载次数</label><select id="ev-limit"><option value="" selected>不限</option><option value="1">1次</option><option value="5">5次</option><option value="20">20次</option></select></div>
            <div id="ev-share-message" class="ev-message" role="alert"></div>
            <div id="ev-share-result" class="ev-result ev-hidden"><strong>分享已创建</strong><textarea readonly></textarea><button class="ev-button" type="button" id="ev-copy-result">复制完整分享信息</button></div>
          </div>
          <footer class="ev-modal-footer"><button class="ev-button" type="button" data-close>取消</button><button class="ev-share-button" type="submit">生成分享</button></footer>
        </form>
      </section>`;
    backdrop.querySelector('.ev-selected-path').textContent = info.sourcePath;
    backdrop.querySelector('#ev-title-input').value = info.name;
    const close = () => backdrop.remove();
    backdrop.querySelector('.ev-modal-close').onclick = close;
    backdrop.querySelector('[data-close]').onclick = close;
    backdrop.addEventListener('click', (event) => { if (event.target === backdrop) close(); });
    backdrop.addEventListener('keydown', (event) => { if (event.key === 'Escape') close(); });
    return backdrop;
  }

  async function openShare(info) {
    sessionStorage.setItem(pendingKey, JSON.stringify(info));
    try {
      await adminFetch(`${API}/session`);
    } catch {
      return;
    }
    sessionStorage.removeItem(pendingKey);
    const modal = modalElement(info);
    document.body.append(modal);
    modal.querySelector('.ev-modal-close').focus();
    modal.querySelector('#ev-share-form').addEventListener('submit', async (event) => {
      event.preventDefault();
      const message = modal.querySelector('#ev-share-message');
      const submit = event.submitter;
      message.textContent = info.type === 'folder' ? '正在创建文件夹快照，请稍候…' : '正在创建分享…';
      submit.disabled = true;
      const days = modal.querySelector('#ev-expiry').value;
      const expiresAt = days === 'permanent' ? 'permanent' : new Date(Date.now() + Number(days) * 86400000).toISOString();
      const maxRaw = modal.querySelector('#ev-limit').value;
      const payload = {
        sourcePath: info.sourcePath,
        type: info.type,
        title: modal.querySelector('#ev-title-input').value.trim(),
        expiresAt,
        maxDownloads: maxRaw ? Number(maxRaw) : null,
        code: modal.querySelector('#ev-code').value.trim().toUpperCase(),
      };
      try {
        const result = await adminFetch(`${API}/shares`, { method: 'POST', body: JSON.stringify(payload) });
        const text = `${payload.title || info.name}\n分享链接：${result.url}\n提取码：${result.code}`;
        const resultBox = modal.querySelector('#ev-share-result');
        resultBox.querySelector('textarea').value = text;
        resultBox.classList.remove('ev-hidden');
        modal.querySelector('#ev-copy-result').onclick = async () => {
          try {
            await copyText(text);
            message.textContent = '分享信息已复制';
          } catch (error) {
            message.textContent = error.message;
          }
        };
        message.textContent = '';
        submit.textContent = '已创建';
      } catch (error) {
        message.textContent = error.message;
        submit.disabled = false;
      }
    });
  }

  function boot() {
    if (document.documentElement.dataset.evMenuBehavior !== 'true') {
      document.documentElement.dataset.evMenuBehavior = 'true';
      document.addEventListener('click', (event) => {
        if (!event.target.closest('.ev-operation-menu')) closeOperationMenus();
      });
      document.addEventListener('keydown', (event) => {
        if (event.key === 'Escape') closeOperationMenus();
      });
    }
    addToolbar();
    installColumnSizing();
    enhanceRows();
    const pending = sessionStorage.getItem(pendingKey);
    if (pending) {
      try { openShare(JSON.parse(pending)); } catch { sessionStorage.removeItem(pendingKey); }
    }
    const observer = new MutationObserver(() => {
      addToolbar();
      installColumnSizing();
      enhanceRows();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
