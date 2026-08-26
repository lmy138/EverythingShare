(() => {
  'use strict';
  const list = document.querySelector('#list');
  const message = document.querySelector('#message');
  const summary = document.querySelector('#summary');

  const api = async (url, options = {}) => {
    const response = await fetch(url, {
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
      ...options,
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || `请求失败（${response.status}）`);
    return data;
  };

  async function copyText(text) {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(text);
        return;
      } catch {
        // Fall through for mobile webviews with a partially implemented API.
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
    if (!copied) throw new Error('浏览器未允许复制，请长按链接手动复制');
  }

  const formatBytes = (n) => {
    n = Number(n || 0);
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), 4);
    return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
  };
  const time = (v) => v
    ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(v * 1000))
    : '永久';
  function renderFileIcon(container, type, name) {
    container.innerHTML = window.EverythingShareIcons.markup(type, name);
  }

  function closeMenus(except) {
    document.querySelectorAll('details.operation-menu[open]').forEach((menu) => {
      if (menu !== except) menu.open = false;
    });
  }

  function operationMenu(share) {
    const details = document.createElement('details');
    details.className = 'operation-menu';
    const trigger = document.createElement('summary');
    trigger.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="12" r="1.7"/><circle cx="12" cy="12" r="1.7"/><circle cx="19" cy="12" r="1.7"/></svg>';
    trigger.setAttribute('aria-label', `操作 ${share.title || share.name}`);
    const panel = document.createElement('div');
    panel.className = 'operation-panel';

    const addButton = (label, handler, { disabled = false, danger = false } = {}) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.textContent = label;
      button.disabled = disabled;
      if (danger) button.classList.add('danger-text');
      button.onclick = async () => {
        details.open = false;
        await handler();
      };
      panel.append(button);
    };

    addButton('刷新源文件', () => action(`/share-api/v1/shares/${share.id}/refresh`), {
      disabled: share.status !== 'active',
    });
    addButton('重置提取码', () => resetCodeAndCopy(share).catch(showError), {
      disabled: share.status !== 'active',
    });
    addButton(share.status === 'active' ? '撤销并删除' : '删除条目', async () => {
      const confirmed = confirm('确认删除该分享？外部链接会立即失效，管理列表中不再保留此条目。');
      if (confirmed) await action(`/share-api/v1/shares/${share.id}/revoke`);
    }, { danger: true });

    details.addEventListener('toggle', () => {
      details.closest('.admin-item')?.classList.toggle('menu-open', details.open);
      if (details.open) closeMenus(details);
    });
    details.append(trigger, panel);
    return details;
  }

  function updateSummary(shares) {
    const active = shares.filter((share) => share.status === 'active').length;
    const expired = shares.filter((share) => share.status === 'expired').length;
    summary.textContent = `共 ${shares.length} 个分享 · 有效 ${active} · 已过期 ${expired}`;
  }

  function showError(error) {
    message.textContent = error?.message || String(error);
  }

  async function resetCodeAndCopy(share) {
    const result = await api(`/share-api/v1/shares/${share.id}/reset-code`, { method: 'POST', body: '{}' });
    await copyText(result.url);
    message.textContent = `已生成新提取码 ${result.code}，直达链接已复制。旧提取码立即失效。`;
    await load();
  }

  function renderShare(share) {
    const item = document.createElement('article');
    item.className = 'card admin-item';
    item.dataset.status = share.status;

    const row = document.createElement('div');
    row.className = 'admin-row';
    const icon = document.createElement('span');
    icon.className = `file-icon ${share.type}`;
    icon.setAttribute('aria-hidden', 'true');
    renderFileIcon(icon, share.type, share.name);

    const info = document.createElement('div');
    info.className = 'admin-info';
    const titleLine = document.createElement('div');
    titleLine.className = 'admin-title-line';
    const title = document.createElement('div');
    title.className = 'admin-name';
    title.textContent = share.title || share.name || '未命名分享';
    const badge = document.createElement('span');
    badge.className = `badge ${share.status}`;
    badge.textContent = share.status === 'expired' ? '已过期' : '有效';
    titleLine.append(title, badge);

    const sourcePath = document.createElement('div');
    sourcePath.className = 'admin-source-path';
    sourcePath.textContent = share.sourcePath || share.name || '旧版本未记录源路径';
    sourcePath.title = sourcePath.textContent;

    const meta = document.createElement('div');
    meta.className = 'muted admin-meta';
    const typeText = share.type === 'folder'
      ? `${share.entryCount} 项 · ${formatBytes(share.totalSize)}`
      : '单文件';
    const limitText = share.maxDownloads == null ? `${share.downloads} 次下载` : `${share.downloads} / ${share.maxDownloads} 次下载`;
    const expiryText = share.expiresAt ? `有效至 ${time(share.expiresAt)}` : '永久有效';
    meta.textContent = `${typeText} · ${limitText} · ${expiryText}`;
    info.append(titleLine, sourcePath, meta);
    row.append(icon, info, operationMenu(share));

    const copy = document.createElement('div');
    copy.className = 'copy-row';
    const input = document.createElement('input');
    input.readOnly = true;
    input.value = share.url;
    input.setAttribute('aria-label', `${share.title} 的分享链接`);
    const button = document.createElement('button');
    button.className = 'secondary';
    button.textContent = share.hasDirectCode ? '复制直达链接' : '生成直达链接';
    button.disabled = share.status !== 'active';
    button.onclick = async () => {
      try {
        if (!share.hasDirectCode) {
          await resetCodeAndCopy(share);
          return;
        }
        await copyText(share.url);
        message.textContent = '包含提取码的直达链接已复制';
      } catch (error) {
        showError(error);
      }
    };
    copy.append(input, button);
    item.append(row, copy);
    return item;
  }

  async function load() {
    message.textContent = '';
    summary.textContent = '';
    list.innerHTML = '<div class="empty">正在加载…</div>';
    try {
      const data = await api('/share-api/v1/shares');
      const shares = (Array.isArray(data.shares) ? data.shares : [])
        .filter((share) => share.status !== 'revoked');
      list.replaceChildren();
      updateSummary(shares);
      if (!shares.length) {
        list.innerHTML = '<div class="card empty">还没有创建分享</div>';
        return;
      }
      shares.forEach((share) => list.append(renderShare(share)));
    } catch (error) {
      list.replaceChildren();
      summary.textContent = '';
      message.textContent = `分享列表加载失败：${error.message}`;
    }
  }

  async function action(url) {
    try {
      await api(url, { method: 'POST', body: '{}' });
      await load();
    } catch (error) {
      showError(error);
    }
  }

  document.addEventListener('click', (event) => {
    if (!event.target.closest('.operation-menu')) closeMenus();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') closeMenus();
  });
  document.querySelector('#reload').onclick = load;
  load();
})();
