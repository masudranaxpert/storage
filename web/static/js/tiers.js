let tiersCache = null;
let processingCache = null;

const selectedTierBlocks = {};
const dirtyInputs = {};
const openAttachDrawers = new Set();
const openCdnDrawers = new Set();
const openTierCdnDrawers = new Set();

if (typeof formatBytes !== 'function') {
    window.formatBytes = function(bytes) {
        if (!bytes || bytes <= 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + (sizes[i] || 'B');
    };
}

if (typeof escapeHtml !== 'function') {
    window.escapeHtml = function(str) {
        if (!str) return '';
        return String(str).replace(/[&<>"']/g, m => ({
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#39;'
        }[m]));
    };
}

function makeBlockKey(nodeId, path) {
    return `${nodeId}|||${path}`;
}

function tierLabel(id) {
    return `tier${id}`;
}

function tierQuotaLabel(quotaBytes) {
    if (!quotaBytes || quotaBytes === 0) return 'Full drive space';
    return formatBytes(quotaBytes);
}

function shortCDN(url) {
    if (!url) return '';
    return url.replace(/^https?:\/\//i, '').replace(/\/.*$/, '');
}

function tierTheme(id) {
    switch (id) {
        case 0:
            return {
                icon: `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8.5 14.5A2.5 2.5 0 0 0 11 12c0-1.38-.5-2-1-3-1.072-2.143-.224-4.054 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.153.433-2.294 1-3a2.5 2.5 0 0 0 2.5 2.5z"></path></svg>`,
                themeClass: 'tier-hot',
                desc: ''
            };
        case 1:
            return {
                icon: `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"></polygon></svg>`,
                themeClass: 'tier-warm',
                desc: ''
            };
        case 2:
            return {
                icon: `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="21 8 21 21 3 21 3 8"></polyline><rect x="1" y="3" width="22" height="5"></rect><line x1="10" y1="12" x2="14" y2="12"></line></svg>`,
                themeClass: 'tier-cold',
                desc: ''
            };
        default:
            return {
                icon: `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"></ellipse><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"></path><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"></path></svg>`,
                themeClass: 'tier-custom',
                desc: ''
            };
    }
}

function toggleAttachDrawer(tierId) {
    const drawer = document.getElementById(`attach-drawer-${tierId}`);
    if (!drawer) return;
    const isOpen = drawer.classList.contains('open');
    if (isOpen) {
        drawer.classList.remove('open');
        openAttachDrawers.delete(tierId);
    } else {
        drawer.classList.add('open');
        openAttachDrawers.add(tierId);
    }
}

function toggleBlockCDN(tierId, blockIdx) {
    const key = `${tierId}-${blockIdx}`;
    const drawer = document.getElementById(`cdn-drawer-${key}`);
    if (!drawer) return;
    const isOpen = drawer.classList.contains('open');
    if (isOpen) {
        drawer.classList.remove('open');
        openCdnDrawers.delete(key);
    } else {
        drawer.classList.add('open');
        openCdnDrawers.add(key);
        const input = drawer.querySelector('input');
        if (input) input.focus();
    }
}

function toggleTierCDN(tierId) {
    const drawer = document.getElementById(`tier-cdn-drawer-${tierId}`);
    if (!drawer) return;
    const isOpen = drawer.classList.contains('open');
    if (isOpen) {
        drawer.classList.remove('open');
        openTierCdnDrawers.delete(tierId);
    } else {
        drawer.classList.add('open');
        openTierCdnDrawers.add(tierId);
        const input = drawer.querySelector('input');
        if (input) input.focus();
    }
}

let activeTierModalState = null;

function openTierModal(config) {
    activeTierModalState = config;
    const backdrop = document.getElementById('tier-action-modal');
    if (!backdrop) return;

    const titleEl = document.getElementById('tier-modal-title');
    const subEl = document.getElementById('tier-modal-subtitle');
    const iconEl = document.getElementById('tier-modal-icon');
    const descEl = document.getElementById('tier-modal-desc');
    const inputWrap = document.getElementById('tier-modal-input-wrap');
    const inputEl = document.getElementById('tier-modal-input');
    const labelEl = document.getElementById('tier-modal-label');
    const presetsEl = document.getElementById('tier-modal-presets');
    const submitBtn = document.getElementById('tier-modal-submit-btn');

    if (titleEl) titleEl.innerText = config.title || 'Storage Tier';
    if (subEl) subEl.innerText = config.subtitle || '';
    if (iconEl) {
        iconEl.className = 'tier-modal-icon-box ' + (config.iconTheme || 'theme-orange');
        if (config.iconSvg) iconEl.innerHTML = config.iconSvg;
    }

    if (config.mode === 'input') {
        if (descEl) descEl.style.display = 'none';
        if (inputWrap) inputWrap.style.display = 'flex';
        if (labelEl) labelEl.innerText = config.inputLabel || 'Tier Name';
        if (inputEl) {
            inputEl.value = config.initialValue || '';
            inputEl.placeholder = config.placeholder || 'e.g. Tier 3 · Archive (HDD)';
        }
        if (presetsEl) presetsEl.style.display = config.showPresets ? 'flex' : 'none';
    } else {
        if (inputWrap) inputWrap.style.display = 'none';
        if (descEl) {
            descEl.style.display = 'block';
            descEl.innerHTML = config.description || '';
        }
    }

    if (submitBtn) {
        submitBtn.innerText = config.submitLabel || 'Confirm';
        submitBtn.className = config.danger ? 'btn btn-modal-danger' : 'btn btn-primary';
        submitBtn.disabled = false;
    }

    backdrop.classList.add('open');
    if (typeof window.setModalOpen === 'function') window.setModalOpen(true);
    if (config.mode === 'input' && inputEl) {
        setTimeout(() => {
            inputEl.focus();
            inputEl.select();
        }, 40);
    }
}

function closeTierModal() {
    const backdrop = document.getElementById('tier-action-modal');
    if (backdrop) backdrop.classList.remove('open');
    activeTierModalState = null;
    if (typeof window.setModalOpen === 'function') window.setModalOpen(false);
}

function fillTierModalPreset(val) {
    const inputEl = document.getElementById('tier-modal-input');
    if (inputEl) {
        inputEl.value = val;
        inputEl.focus();
    }
}

async function submitTierModal() {
    if (!activeTierModalState) return;
    const state = activeTierModalState;
    const submitBtn = document.getElementById('tier-modal-submit-btn');

    if (state.mode === 'input') {
        const inputEl = document.getElementById('tier-modal-input');
        const val = inputEl ? inputEl.value.trim() : '';
        if (!val) {
            if (inputEl) inputEl.focus();
            return;
        }
        if (submitBtn) submitBtn.disabled = true;
        closeTierModal();
        if (typeof state.onSubmit === 'function') {
            await state.onSubmit(val);
        }
    } else {
        if (submitBtn) submitBtn.disabled = true;
        closeTierModal();
        if (typeof state.onSubmit === 'function') {
            await state.onSubmit();
        }
    }
}

function renameTierPrompt(tierId, currentName) {
    openTierModal({
        mode: 'input',
        title: 'Rename Storage Tier',
        subtitle: `Update display label for tier${tierId}`,
        inputLabel: 'Tier Display Name',
        initialValue: currentName || '',
        placeholder: 'e.g. Tier 0 · Hot (NVMe)',
        showPresets: false,
        submitLabel: 'Save Name',
        iconTheme: 'theme-blue',
        iconSvg: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>',
        onSubmit: async (newName) => {
            if (!newName || newName === currentName) return;
            const t = currentTierById(tierId);
            if (!t) return;
            const payload = tierPayloadFromStatus(t, { name: newName });
            await upsertTier(payload);
        }
    });
}

function isUserInteractingWith(container) {
    if (!container) return false;
    const active = document.activeElement;
    if (active && container.contains(active)) {
        const tag = (active.tagName || '').toLowerCase();
        if (tag === 'input' || tag === 'select' || tag === 'textarea' || active.isContentEditable) {
            return true;
        }
    }
    return false;
}

function onTierSelectChange(tierId, value) {
    selectedTierBlocks[String(tierId)] = value;
}

function onInputChange(inputId, value) {
    dirtyInputs[inputId] = value;
    // Highlight save tier action if related to a tier
    const tierMatch = inputId.match(/^tier-(\d+)-/);
    if (tierMatch) {
        const btn = document.getElementById(`btn-save-tier-${tierMatch[1]}`);
        if (btn) btn.classList.add('has-changes');
    }
    // Highlight save allocation action if related to a worker node
    const procMatch = inputId.match(/^proc-(.+)-(cpu|ram|disk|scratch)$/);
    if (procMatch) {
        const btn = document.getElementById(`btn-save-proc-${procMatch[1]}`);
        if (btn) btn.classList.add('has-changes');
    }
}

async function fetchTiersView(force = false) {
    try {
        const [tiersRes, procRes] = await Promise.all([
            fetch('/api/tiers'),
            fetch('/api/processing')
        ]);
        if (tiersRes.ok) {
            tiersCache = await tiersRes.json();
            renderTiers(force);
            if (typeof renderStorage === 'function' && typeof cachedPoolData !== 'undefined' && cachedPoolData) {
                renderStorage(cachedPoolData);
            }
        }
        if (procRes.ok) {
            processingCache = await procRes.json();
            renderProcessing(force);
        }
    } catch (err) {
        console.error('fetchTiersView error:', err);
    }
}

function currentTierById(id) {
    if (!tiersCache) return null;
    return (tiersCache.tiers || []).find(t => t.id === id) || null;
}

function tierPayloadFromStatus(t, overrides) {
    return Object.assign({
        id: t.id,
        name: t.name,
        mediums: t.mediums || [],
        default: t.default || false,
        public_host: t.public_host || '',
        blocks: (t.blocks || []).map(b => ({
            node_id: b.block.node_id,
            path: b.block.path,
            disk_type: b.block.disk_type,
            quota_bytes: b.block.quota_bytes,
            public_host: b.block.public_host || ''
        }))
    }, overrides || {});
}

function renderTiers(force = false) {
    const grid = document.getElementById('tiers-grid');
    if (!grid || !tiersCache) return;

    if (!force && isUserInteractingWith(grid)) {
        return;
    }

    const activeEl = document.activeElement;
    const activeId = activeEl && activeEl.id ? activeEl.id : null;
    const selectionStart = (activeEl && typeof activeEl.selectionStart === 'number') ? activeEl.selectionStart : null;
    const selectionEnd = (activeEl && typeof activeEl.selectionEnd === 'number') ? activeEl.selectionEnd : null;

    grid.querySelectorAll('input, select').forEach(el => {
        if (el.id) {
            if (el.tagName === 'SELECT') {
                const tierIdMatch = el.id.match(/^tier-(\d+)-add-select$/);
                if (tierIdMatch && el.value) {
                    selectedTierBlocks[tierIdMatch[1]] = el.value;
                }
            } else if (el.type === 'number' || el.type === 'text') {
                dirtyInputs[el.id] = el.value;
            }
        }
    });

    const tiers = tiersCache.tiers || [];
    const unassigned = tiersCache.unassigned_blocks || [];

    if (tiers.length === 0) {
        grid.innerHTML = '<div style="grid-column:1/-1;padding:36px;text-align:center;color:var(--text-secondary);background:var(--bg-surface);border-radius:var(--radius-lg);border:1px dashed var(--border-light);">No storage tiers configured yet. Click "Add Storage Tier" above to get started.</div>';
        return;
    }

    grid.innerHTML = tiers.map(t => {
        const theme = tierTheme(t.id);
        const isSystem = !!t.system;
        const blocks = t.blocks || [];
        const pct = t.usable_bytes > 0 ? Math.min(100, Math.round((t.used_bytes / t.usable_bytes) * 100)) : 0;

        let hasTierChanges = false;

        const blocksHtml = blocks.map((b, idx) => {
            const block = b.block;
            const blockPct = b.usable_bytes > 0 ? Math.min(100, Math.round((b.used_bytes / b.usable_bytes) * 100)) : 0;
            const barColor = blockPct > 92 ? 'var(--status-offline)' : blockPct > 80 ? 'var(--status-stale)' : 'var(--orange-primary)';
            const inputId = `tier-${t.id}-block-${idx}-quota`;
            const quotaGB = block.quota_bytes > 0 ? Math.round(block.quota_bytes / (1024 * 1024 * 1024)) : '';
            const liveQuotaGB = dirtyInputs[inputId] !== undefined ? dirtyInputs[inputId] : (quotaGB || '');
            const hostInputId = `tier-${t.id}-block-${idx}-host`;
            const liveHost = dirtyInputs[hostInputId] !== undefined ? dirtyInputs[hostInputId] : (block.public_host || '');

            if (dirtyInputs[inputId] !== undefined || dirtyInputs[hostInputId] !== undefined) {
                hasTierChanges = true;
            }

            const cdnKey = `${t.id}-${idx}`;
            const isCdnOpen = openCdnDrawers.has(cdnKey);

            return `
                <div class="tier-block-card">
                    <div class="tier-block-main">
                        <div class="tier-block-id">
                            ${block.node_id && block.node_id.startsWith('bucket:')
                                ? `<span class="block-node-badge" style="background:rgba(37,99,235,0.08);color:#2563eb;border-color:rgba(37,99,235,0.2);">☁️ Object Store</span>`
                                : `<span class="block-node-badge">${escapeHtml(block.node_id)}</span>`}
                            <span class="block-path-code">${escapeHtml(block.path)}</span>
                            <span class="block-medium-pill ${block.disk_type ? block.disk_type.toLowerCase() : 'ssd'}">${escapeHtml(block.disk_type || 'SSD')}</span>
                            ${b.online ? '' : '<span class="block-offline-badge">Offline</span>'}
                        </div>
                        <div class="tier-block-controls">
                            <div class="block-quota-wrap" title="Storage quota in GB (0 or empty = full drive space)">
                                <span class="quota-pill-label">Quota:</span>
                                <input type="number" min="0" placeholder="All" value="${escapeHtml(liveQuotaGB)}"
                                    id="${inputId}" class="quota-number-input tabular-num"
                                    oninput="onInputChange('${inputId}', this.value)">
                                <span class="quota-unit-tag">GB</span>
                            </div>

                            <button class="block-btn-cdn ${liveHost ? 'active' : ''}" onclick="toggleBlockCDN(${t.id}, ${idx})" title="${liveHost ? 'Public Streaming Host: ' + escapeHtml(liveHost) : 'Configure CDN / Public Host'}">
                                <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path></svg>
                                <span>${liveHost ? escapeHtml(shortCDN(liveHost)) : '+ CDN'}</span>
                            </button>

                            <button class="block-btn-remove" onclick="removeTierBlock(${t.id}, ${idx})" title="Remove drive from this tier">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
                            </button>
                        </div>
                    </div>

                    <div class="tier-block-meter">
                        <div class="block-meter-track">
                            <div class="block-meter-fill" style="width:${blockPct}%;background:${barColor};"></div>
                        </div>
                        <div class="block-meter-labels">
                            <span class="tabular-num">${formatBytes(b.used_bytes)} / ${formatBytes(b.usable_bytes)} pooled</span>
                            <span class="tabular-num" style="display:inline-flex;align-items:center;gap:4px;">
                                <span style="color:#059669;font-weight:600;">${formatBytes(b.free_bytes)} free</span>
                                <span style="opacity:0.35;">•</span>
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" style="color:var(--text-secondary);flex-shrink:0;" title="Storage Quota Cap">
                                    <circle cx="12" cy="12" r="10"></circle>
                                    <polyline points="12 6 12 12 16 14"></polyline>
                                </svg>
                                <span style="color:var(--text-secondary);">${tierQuotaLabel(block.quota_bytes)}</span>
                            </span>
                        </div>
                    </div>

                    <div class="block-cdn-drawer ${isCdnOpen ? 'open' : ''}" id="cdn-drawer-${t.id}-${idx}">
                        <div class="cdn-drawer-inner">
                            <span class="cdn-drawer-label">CDN Host:</span>
                            <input type="text" id="${hostInputId}" class="cdn-drawer-input"
                                placeholder="e.g. cdn1.streammesh.com or https://cdn1.streammesh.com:2053"
                                value="${escapeHtml(liveHost)}"
                                oninput="onInputChange('${hostInputId}', this.value)"
                                autocomplete="off" spellcheck="false">
                            <button class="btn btn-secondary btn-sm" onclick="saveTier(${t.id})" style="padding:2px 8px;font-size:11px;">Save</button>
                        </div>
                    </div>
                </div>`;
        }).join('');

        const selectedKey = selectedTierBlocks[String(t.id)];
        const keyExists = unassigned.some(b => makeBlockKey(b.node_id, b.path) === selectedKey);
        const activeKey = keyExists ? selectedKey : (unassigned[0] ? makeBlockKey(unassigned[0].node_id, unassigned[0].path) : '');
        if (activeKey) {
            selectedTierBlocks[String(t.id)] = activeKey;
        }

        const unassignedOpts = unassigned.map(b => {
            const key = makeBlockKey(b.node_id, b.path);
            const isSelected = (key === activeKey) ? 'selected' : '';
            if (b.node_id && b.node_id.startsWith('bucket:')) {
                const prov = (b.disk_type || 'R2').toUpperCase();
                return `<option value="${escapeHtml(key)}" ${isSelected}>☁️ [${escapeHtml(prov)}] Bucket: ${escapeHtml(b.path)}</option>`;
            }
            return `<option value="${escapeHtml(key)}" ${isSelected}>${escapeHtml(b.node_id)} • ${escapeHtml(b.path)} (${escapeHtml(b.disk_type || 'SSD')} • ${formatBytes(b.free_bytes)} free)</option>`;
        }).join('');

        const addQuotaId = `tier-${t.id}-add-quota`;
        const liveAddQuota = dirtyInputs[addQuotaId] !== undefined ? dirtyInputs[addQuotaId] : '';
        const addHostId = `tier-${t.id}-add-host`;
        const liveAddHost = dirtyInputs[addHostId] !== undefined ? dirtyInputs[addHostId] : '';
        const isAttachOpen = openAttachDrawers.has(t.id);

        const tierHostInputId = `tier-${t.id}-host`;
        const liveTierHost = dirtyInputs[tierHostInputId] !== undefined ? dirtyInputs[tierHostInputId] : (t.public_host || '');
        if (dirtyInputs[tierHostInputId] !== undefined) {
            hasTierChanges = true;
        }
        const isTierCdnOpen = openTierCdnDrawers.has(t.id);

        return `
            <div class="tier-card tier-card-${t.id}">
                <div class="tier-card-header">
                    <div class="tier-identity">
                        <div class="tier-icon-box ${theme.themeClass}">
                            ${theme.icon}
                        </div>
                        <div class="tier-identity-text">
                            <div class="tier-title-row">
                                <span class="tier-name-label">${escapeHtml(t.name)}</span>
                                <button class="tier-btn-rename" onclick="renameTierPrompt(${t.id}, '${escapeHtml(t.name)}')" title="Rename tier">
                                    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
                                </button>
                                <span class="tier-code-chip">${tierLabel(t.id)}</span>
                                <button class="tier-btn-cdn ${liveTierHost ? 'active' : ''}" onclick="toggleTierCDN(${t.id})" title="${liveTierHost ? 'Tier CDN Domain: ' + escapeHtml(liveTierHost) : 'Configure Tier CDN / Public Domain'}">
                                    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path></svg>
                                    <span>${liveTierHost ? escapeHtml(shortCDN(liveTierHost)) : '+ Tier CDN'}</span>
                                </button>
                            </div>
                        </div>
                    </div>
                    <div class="tier-header-badges">
                        ${isSystem
                            ? `<span class="tier-badge-system" title="Permanent system managed tier">SYSTEM</span>`
                            : `<button class="btn-remove-tier" onclick="deleteTier(${t.id})" title="Delete custom tier">✕</button>`}
                    </div>
                </div>

                <div class="tier-cdn-drawer ${isTierCdnOpen ? 'open' : ''}" id="tier-cdn-drawer-${t.id}">
                    <div class="tier-cdn-drawer-inner">
                        <div class="tier-cdn-label-wrap">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" class="tier-cdn-icon">
                                <circle cx="12" cy="12" r="10"></circle>
                                <line x1="2" y1="12" x2="22" y2="12"></line>
                                <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path>
                            </svg>
                            <span class="tier-cdn-drawer-label">Tier CDN</span>
                        </div>
                        <div class="tier-cdn-input-wrap">
                            <input type="text" id="${tierHostInputId}" class="tier-cdn-drawer-input"
                                placeholder="https://cdn.example.com"
                                value="${escapeHtml(liveTierHost)}"
                                oninput="onInputChange('${tierHostInputId}', this.value)"
                                onkeydown="if(event.key==='Enter') saveTier(${t.id})"
                                autocomplete="off" spellcheck="false">
                            <button type="button" class="btn btn-primary btn-sm tier-cdn-save-btn" onclick="saveTier(${t.id})">
                                <span>Save</span>
                            </button>
                        </div>
                    </div>
                </div>

                <div class="tier-capacity-panel">
                    <div class="tier-capacity-numbers">
                        <div class="capacity-stat-main">
                            <span class="capacity-used tabular-num">${formatBytes(t.used_bytes)}</span>
                            <span class="capacity-divider">/</span>
                            <span class="capacity-total tabular-num">${t.usable_bytes > 0 ? formatBytes(t.usable_bytes) : '0 B'}</span>
                            <span class="capacity-unit">pooled</span>
                        </div>
                        <span class="capacity-badge tabular-num">${pct}% used</span>
                    </div>
                    <div class="tier-gauge-track">
                        <div class="tier-gauge-fill tier-fill-${t.id}" style="width:${pct}%;"></div>
                    </div>
                    <div class="tier-capacity-sub">
                        <span>Drives: <strong>${blocks.length} block${blocks.length === 1 ? '' : 's'}</strong></span>
                        <span>Raw Free: <strong class="tabular-num">${formatBytes(t.free_bytes)}</strong></span>
                    </div>
                </div>

                <div class="tier-drives-section">
                    <div class="tier-section-subhead">
                        <span class="tier-subhead-title">Attached Storage Drives (${blocks.length})</span>
                        ${unassigned.length > 0 ? `
                        <button class="tier-attach-toggle-btn" id="btn-toggle-attach-${t.id}" onclick="toggleAttachDrawer(${t.id})">
                            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="12" y1="5" x2="12" y2="19"></line><line x1="5" y1="12" x2="19" y2="12"></line></svg>
                            <span>Attach Drive</span>
                        </button>` : ''}
                    </div>

                    <div class="tier-blocks-list">
                        ${blocksHtml || `
                        <div class="tier-empty-drives">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <rect x="2" y="2" width="20" height="8" rx="2" ry="2"></rect>
                                <rect x="2" y="14" width="20" height="8" rx="2" ry="2"></rect>
                                <line x1="6" y1="6" x2="6.01" y2="6"></line>
                                <line x1="6" y1="18" x2="6.01" y2="18"></line>
                            </svg>
                            <span>No storage drives attached to this tier yet</span>
                        </div>`}
                    </div>

                    ${unassigned.length > 0 ? `
                    <div class="tier-attach-drawer ${isAttachOpen ? 'open' : ''}" id="attach-drawer-${t.id}">
                        <div class="attach-drawer-header">
                            <span>Attach Drive to ${escapeHtml(tierLabel(t.id))}</span>
                            <button class="btn-close-drawer" onclick="toggleAttachDrawer(${t.id})">✕</button>
                        </div>
                        <div class="attach-drawer-body">
                            <div class="attach-field">
                                <label class="attach-label" for="tier-${t.id}-add-select">Available Node Drive</label>
                                <select id="tier-${t.id}-add-select" class="attach-select"
                                    onchange="onTierSelectChange(${t.id}, this.value)">
                                    ${unassignedOpts}
                                </select>
                            </div>
                            <div class="attach-row-dual">
                                <div class="attach-field" style="flex:1;">
                                    <label class="attach-label" for="${addQuotaId}">Quota (GB)</label>
                                    <div class="attach-input-unit">
                                        <input type="number" min="0" placeholder="0 (All)" id="${addQuotaId}" value="${escapeHtml(liveAddQuota)}"
                                            class="attach-input tabular-num" oninput="onInputChange('${addQuotaId}', this.value)">
                                        <span class="unit-text">GB</span>
                                    </div>
                                </div>
                                <div class="attach-field" style="flex:2;">
                                    <label class="attach-label" for="${addHostId}">Public CDN Host <span class="opt-tag">(optional)</span></label>
                                    <input type="text" placeholder="cdn1.streammesh.com:2053" id="${addHostId}" value="${escapeHtml(liveAddHost)}"
                                        class="attach-input" oninput="onInputChange('${addHostId}', this.value)">
                                </div>
                            </div>
                            <div class="attach-drawer-actions">
                                <button class="btn btn-secondary btn-sm" onclick="toggleAttachDrawer(${t.id})">Cancel</button>
                                <button class="btn btn-primary btn-sm" onclick="assignBlockToTier(${t.id})">Attach Drive</button>
                            </div>
                        </div>
                    </div>` : ''}
                </div>

                <div class="tier-card-footer">
                    <button class="btn-save-tier-action ${hasTierChanges ? 'has-changes' : ''}" id="btn-save-tier-${t.id}" onclick="saveTier(${t.id})" title="Save quotas and CDN overrides for this tier">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"></path><polyline points="17 21 17 13 7 13 7 21"></polyline><polyline points="7 3 7 8 15 8"></polyline></svg>
                        <span>Save Tier</span>
                    </button>
                </div>
            </div>`;
    }).join('');

    tiers.forEach(t => {
        const select = document.getElementById(`tier-${t.id}-add-select`);
        const chosenKey = selectedTierBlocks[String(t.id)];
        if (select && chosenKey) {
            select.value = chosenKey;
        }
    });

    if (activeId) {
        const target = document.getElementById(activeId);
        if (target) {
            target.focus();
            if (selectionStart !== null && selectionEnd !== null && typeof target.setSelectionRange === 'function') {
                target.setSelectionRange(selectionStart, selectionEnd);
            }
        }
    }
}

async function saveTier(id) {
    const t = currentTierById(id);
    if (!t) return;

    const payload = tierPayloadFromStatus(t);
    document.querySelectorAll(`[id^="tier-${id}-block-"][id$="-quota"]`).forEach(input => {
        const idx = parseInt(input.id.replace(`tier-${id}-block-`, '').replace('-quota', ''));
        if (payload.blocks[idx]) {
            const gb = parseInt(input.value) || 0;
            payload.blocks[idx].quota_bytes = gb > 0 ? gb * 1024 * 1024 * 1024 : 0;
        }
    });
    document.querySelectorAll(`[id^="tier-${id}-block-"][id$="-host"]`).forEach(input => {
        const idx = parseInt(input.id.replace(`tier-${id}-block-`, '').replace('-host', ''));
        if (payload.blocks[idx]) {
            payload.blocks[idx].public_host = (input.value || '').trim();
        }
    });

    const tierHostInput = document.getElementById(`tier-${id}-host`);
    if (tierHostInput) {
        payload.public_host = (tierHostInput.value || '').trim();
    }

    const ok = await upsertTier(payload);
    if (ok) {
        delete dirtyInputs[`tier-${id}-host`];
        document.querySelectorAll(`[id^="tier-${id}-block-"]`).forEach(el => {
            delete dirtyInputs[el.id];
        });
        const btn = document.getElementById(`btn-save-tier-${id}`);
        if (btn) btn.classList.remove('has-changes');
    }
}

async function upsertTier(payload) {
    try {
        const res = await fetch('/api/tiers', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!res.ok) {
            alert('Tier save failed: ' + (await res.text()));
            return false;
        }
        await fetchTiersView(true);
        return true;
    } catch (err) {
        alert('Network error saving tier: ' + err.message);
        return false;
    }
}

async function assignBlockToTier(id) {
    const t = currentTierById(id);
    const select = document.getElementById(`tier-${id}-add-select`);
    const quotaInput = document.getElementById(`tier-${id}-add-quota`);
    const hostInput = document.getElementById(`tier-${id}-add-host`);
    if (!t || !select || !tiersCache) return;

    const chosenKey = select.value;
    const unassigned = tiersCache.unassigned_blocks || [];
    const chosen = unassigned.find(b => makeBlockKey(b.node_id, b.path) === chosenKey);
    if (!chosen) return;

    const gb = parseInt(quotaInput ? quotaInput.value : 0) || 0;
    const publicHost = (hostInput ? hostInput.value : '').trim();
    const payload = tierPayloadFromStatus(t);

    const exists = payload.blocks.some(b => b.node_id === chosen.node_id && b.path === chosen.path);
    if (exists) {
        alert('This storage block is already attached to this tier.');
        return;
    }

    payload.blocks.push({
        node_id: chosen.node_id,
        path: chosen.path,
        disk_type: chosen.disk_type,
        quota_bytes: gb > 0 ? gb * 1024 * 1024 * 1024 : 0,
        public_host: publicHost
    });

    const ok = await upsertTier(payload);
    if (ok) {
        delete dirtyInputs[`tier-${id}-add-quota`];
        delete dirtyInputs[`tier-${id}-add-host`];
        delete selectedTierBlocks[String(id)];
        openAttachDrawers.delete(id);
        if (quotaInput) quotaInput.value = '';
        if (hostInput) hostInput.value = '';
    }
}

async function removeTierBlock(id, blockIdx) {
    const t = currentTierById(id);
    if (!t) return;

    const payload = tierPayloadFromStatus(t);
    payload.blocks.splice(blockIdx, 1);
    await upsertTier(payload);
}

function deleteTier(id) {
    const t = currentTierById(id);
    const tierName = t ? t.name : `tier${id}`;
    openTierModal({
        mode: 'confirm',
        title: 'Delete Custom Tier',
        subtitle: `Remove ${tierName} from the storage pool`,
        description: `Are you sure you want to delete <strong>${escapeHtml(tierName)}</strong>? Any attached drives will return safely to the unassigned drive pool.`,
        submitLabel: 'Delete Tier',
        danger: true,
        iconTheme: 'theme-red',
        iconSvg: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>',
        onSubmit: async () => {
            try {
                const res = await fetch(`/api/tiers/${id}`, { method: 'DELETE' });
                if (!res.ok) alert('Delete failed: ' + (await res.text()));
                await fetchTiersView(true);
            } catch (err) {
                alert('Network error: ' + err.message);
            }
        }
    });
}

function addNewTier() {
    openTierModal({
        mode: 'input',
        title: 'Add Storage Tier',
        subtitle: 'Create a custom placement pool for cluster drives',
        inputLabel: 'New Tier Name',
        initialValue: '',
        placeholder: 'e.g. Tier 3 · Archive (HDD)',
        showPresets: true,
        submitLabel: 'Create Tier',
        iconTheme: 'theme-orange',
        iconSvg: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"></ellipse><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"></path><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"></path></svg>',
        onSubmit: async (name) => {
            await upsertTier({ id: -1, name: name, mediums: [], default: false, blocks: [] });
        }
    });
}

function renderProcessing(force = false) {
    const grid = document.getElementById('processing-grid');
    if (!grid || !processingCache) return;

    if (!force && isUserInteractingWith(grid)) {
        return;
    }

    const activeEl = document.activeElement;
    const activeId = activeEl && activeEl.id ? activeEl.id : null;
    const selectionStart = (activeEl && typeof activeEl.selectionStart === 'number') ? activeEl.selectionStart : null;
    const selectionEnd = (activeEl && typeof activeEl.selectionEnd === 'number') ? activeEl.selectionEnd : null;

    grid.querySelectorAll('input, select').forEach(el => {
        if (el.id) {
            if (el.type === 'checkbox') {
                dirtyInputs[el.id] = el.checked;
            } else {
                dirtyInputs[el.id] = el.value;
            }
        }
    });

    const nodes = processingCache.nodes || [];
    const eligibleCount = nodes.filter(n => n.eligible && n.profile && n.profile.enabled).length;
    const badge = document.getElementById('processing-eligible-count');
    if (badge) badge.innerText = `${eligibleCount} of ${nodes.length} workers active`;

    if (nodes.length === 0) {
        grid.innerHTML = '<div style="grid-column:1/-1;padding:36px;text-align:center;color:var(--text-secondary);background:var(--bg-surface);border-radius:var(--radius-lg);border:1px dashed var(--border-light);">No nodes currently connected to the cluster.</div>';
        return;
    }

    grid.innerHTML = nodes.map(n => {
        const p = n.profile || {};
        const enabledId = `proc-${n.node_id}-enabled`;
        const cpuId = `proc-${n.node_id}-cpu`;
        const ramId = `proc-${n.node_id}-ram`;
        const diskId = `proc-${n.node_id}-disk`;
        const scratchId = `proc-${n.node_id}-scratch`;
        const maxIngestId = `proc-${n.node_id}-max-ingests`;

        const enabled = dirtyInputs[enabledId] !== undefined ? !!dirtyInputs[enabledId] : !!p.enabled;
        const cpuVal = dirtyInputs[cpuId] !== undefined ? dirtyInputs[cpuId] : (p.reserved_cpu_cores || '');
        const ramMB = p.reserved_ram_bytes ? Math.round(p.reserved_ram_bytes / (1024 * 1024)) : '';
        const ramVal = dirtyInputs[ramId] !== undefined ? dirtyInputs[ramId] : (ramMB || '');
        const prefDisk = (dirtyInputs[diskId] !== undefined ? dirtyInputs[diskId] : (p.preferred_disk_type || ''));
        const scratchGB = p.reserved_storage_bytes ? Math.round(p.reserved_storage_bytes / (1024 * 1024 * 1024)) : '';
        const scratchVal = dirtyInputs[scratchId] !== undefined ? dirtyInputs[scratchId] : (scratchGB || '');
        const maxIngestVal = dirtyInputs[maxIngestId] !== undefined ? dirtyInputs[maxIngestId] : (p.max_concurrent_ingests || 2);

        const statusHtml = enabled
            ? (n.eligible
                ? `<span class="proc-status-badge eligible">
                    <span class="badge-dot"></span>
                    <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>
                    <span>Eligible</span>
                </span>`
                : `<span class="proc-status-badge not-eligible">
                    <span class="badge-dot"></span>
                    <span>Not Eligible</span>
                </span>`)
            : `<span class="proc-status-badge disabled">
                <span class="badge-dot"></span>
                <span>Disabled</span>
            </span>`;

        const reasonsHtml = (!enabled || n.eligible) ? '' : `
            <div class="proc-reasons-box">
                ${(n.reasons || []).map(r => `<div>&#9888; ${escapeHtml(r)}</div>`).join('')}
            </div>`;

        const nodeDisks = n.disks || [];
        const diskOptionsHtml = [
            `<option value="" ${prefDisk === '' ? 'selected' : ''}>Any Disk Partition</option>`,
            ...nodeDisks.map(d => {
                const isSel = (prefDisk === d.path || prefDisk.toUpperCase() === d.path.toUpperCase() || prefDisk.toUpperCase() === (d.disk_type || '').toUpperCase());
                const label = `${d.path} (${d.disk_type || 'Disk'} • ${formatBytes(d.free_bytes)} free)`;
                return `<option value="${escapeHtml(d.path)}" ${isSel ? 'selected' : ''}>${escapeHtml(label)}</option>`;
            })
        ].join('');

        const hasProcChanges = (
            dirtyInputs[cpuId] !== undefined ||
            dirtyInputs[ramId] !== undefined ||
            dirtyInputs[diskId] !== undefined ||
            dirtyInputs[scratchId] !== undefined ||
            dirtyInputs[maxIngestId] !== undefined
        );

        return `
            <div class="proc-worker-card ${enabled ? 'active' : 'disabled'}">
                <div class="proc-worker-head">
                    <div class="proc-worker-identity">
                        <div class="proc-icon-box ${enabled ? 'active' : 'disabled'}">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <rect x="4" y="4" width="16" height="16" rx="2"></rect>
                                <rect x="9" y="9" width="6" height="6"></rect>
                                <line x1="9" y1="1" x2="9" y2="4"></line>
                                <line x1="15" y1="1" x2="15" y2="4"></line>
                                <line x1="9" y1="20" x2="9" y2="23"></line>
                                <line x1="15" y1="20" x2="15" y2="23"></line>
                                <line x1="20" y1="9" x2="23" y2="9"></line>
                                <line x1="20" y1="14" x2="23" y2="14"></line>
                                <line x1="1" y1="9" x2="4" y2="9"></line>
                                <line x1="1" y1="14" x2="4" y2="14"></line>
                            </svg>
                        </div>
                        <div>
                            <div class="proc-node-title-row">
                                <h3 class="proc-node-name">${escapeHtml(n.node_id)}</h3>
                                ${statusHtml}
                            </div>
                            <div class="proc-telemetry-strip">
                                <span class="telemetry-chip"><strong>${n.free_cpu_cores}</strong> Cores free</span>
                                <span class="telemetry-chip"><strong>${formatBytes(n.free_ram_bytes)}</strong> RAM free</span>
                                ${n.scratch_free_bytes > 0 ? `<span class="telemetry-chip"><strong>${formatBytes(n.scratch_free_bytes)}</strong> Temp free</span>` : ''}
                            </div>
                        </div>
                    </div>

                    <div class="proc-toggle-wrap">
                        <label class="proc-ios-toggle" for="${enabledId}">
                            <input type="checkbox" id="${enabledId}" ${enabled ? 'checked' : ''} onchange="toggleSingleNodeProcessing('${escapeHtml(n.node_id)}', this.checked)">
                            <span class="proc-ios-slider"></span>
                        </label>
                        <span class="proc-toggle-status-text">${enabled ? 'Processing Active' : 'Standby'}</span>
                    </div>
                </div>

                ${reasonsHtml}

                <div class="proc-allocation-panel">
                    <div class="proc-allocation-title">Transcoding Allocation Limits</div>
                    <div class="proc-inputs-grid">
                        <div class="proc-input-col">
                            <label class="proc-label" for="${cpuId}">CPU Cores</label>
                            <div class="proc-input-wrap">
                                <input type="number" min="0" placeholder="e.g. 1" id="${cpuId}" value="${escapeHtml(cpuVal)}" class="proc-input-ctrl tabular-num" oninput="onInputChange('${cpuId}', this.value)">
                                <span class="proc-input-unit">Cores</span>
                            </div>
                        </div>

                        <div class="proc-input-col">
                            <label class="proc-label" for="${ramId}">RAM Limit</label>
                            <div class="proc-input-wrap">
                                <input type="number" min="0" placeholder="e.g. 500" id="${ramId}" value="${escapeHtml(ramVal)}" class="proc-input-ctrl tabular-num" oninput="onInputChange('${ramId}', this.value)">
                                <span class="proc-input-unit">MB</span>
                            </div>
                        </div>

                        <div class="proc-input-col">
                            <label class="proc-label" for="${maxIngestId}">Max Concurrent <span style="color:var(--orange-primary);font-weight:700;">*</span></label>
                            <div class="proc-input-wrap">
                                <input type="number" min="1" max="20" placeholder="e.g. 2" id="${maxIngestId}" value="${escapeHtml(maxIngestVal)}" class="proc-input-ctrl tabular-num" required oninput="onInputChange('${maxIngestId}', this.value)">
                                <span class="proc-input-unit">Jobs</span>
                            </div>
                        </div>

                        <div class="proc-input-col">
                            <label class="proc-label" for="${diskId}">Temp Partition</label>
                            <select id="${diskId}" class="proc-select-ctrl" onchange="onInputChange('${diskId}', this.value)">
                                ${diskOptionsHtml}
                            </select>
                        </div>

                        <div class="proc-input-col">
                            <label class="proc-label" for="${scratchId}">Temp Directory <span class="opt-tag">(Optional)</span></label>
                            <div class="proc-input-wrap">
                                <input type="number" min="0" placeholder="e.g. 10" id="${scratchId}" value="${escapeHtml(scratchVal)}" class="proc-input-ctrl tabular-num" oninput="onInputChange('${scratchId}', this.value)">
                                <span class="proc-input-unit">GB</span>
                            </div>
                        </div>
                    </div>
                </div>

                <div class="proc-worker-footer">
                    <button class="btn-save-proc-action ${hasProcChanges ? 'has-changes' : ''}" id="btn-save-proc-${escapeHtml(n.node_id)}" onclick="saveProcessingProfile('${escapeHtml(n.node_id)}')" title="Save processing allocation limits for this node">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"></path><polyline points="17 21 17 13 7 13 7 21"></polyline><polyline points="7 3 7 8 15 8"></polyline></svg>
                        <span>Save Allocation</span>
                    </button>
                </div>
            </div>`;
    }).join('');

    if (activeId) {
        const target = document.getElementById(activeId);
        if (target) {
            target.focus();
            if (selectionStart !== null && selectionEnd !== null && typeof target.setSelectionRange === 'function') {
                target.setSelectionRange(selectionStart, selectionEnd);
            }
        }
    }
}

async function saveProcessingProfile(nodeId) {
    const enabled = document.getElementById(`proc-${nodeId}-enabled`).checked;
    const cpu = parseInt(document.getElementById(`proc-${nodeId}-cpu`).value) || 0;
    const ramMB = parseInt(document.getElementById(`proc-${nodeId}-ram`).value) || 0;
    const maxIngests = parseInt(document.getElementById(`proc-${nodeId}-max-ingests`) ? document.getElementById(`proc-${nodeId}-max-ingests`).value : 2) || 2;
    const disk = document.getElementById(`proc-${nodeId}-disk`).value;
    const scratchGB = parseInt(document.getElementById(`proc-${nodeId}-scratch`).value) || 0;

    const payload = {
        node_id: nodeId,
        enabled: enabled,
        reserved_cpu_cores: cpu,
        reserved_ram_bytes: ramMB > 0 ? ramMB * 1024 * 1024 : 0,
        preferred_disk_type: disk,
        reserved_storage_bytes: scratchGB > 0 ? scratchGB * 1024 * 1024 * 1024 : 0,
        max_concurrent_ingests: maxIngests
    };

    try {
        const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/processing`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!res.ok) {
            alert('Save failed: ' + (await res.text()));
            return;
        }
        delete dirtyInputs[`proc-${nodeId}-enabled`];
        delete dirtyInputs[`proc-${nodeId}-cpu`];
        delete dirtyInputs[`proc-${nodeId}-ram`];
        delete dirtyInputs[`proc-${nodeId}-disk`];
        delete dirtyInputs[`proc-${nodeId}-scratch`];
        delete dirtyInputs[`proc-${nodeId}-max-ingests`];
        const btn = document.getElementById(`btn-save-proc-${nodeId}`);
        if (btn) btn.classList.remove('has-changes');
        await fetchTiersView(true);
    } catch (err) {
        alert('Network error saving processing reservation: ' + err.message);
    }
}

async function toggleSingleNodeProcessing(nodeId, enabled) {
    dirtyInputs[`proc-${nodeId}-enabled`] = enabled;
    try {
        const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/processing`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ node_id: nodeId, enabled: enabled })
        });
        if (!res.ok) throw new Error(await res.text());
        delete dirtyInputs[`proc-${nodeId}-enabled`];
        await fetchTiersView(true);
    } catch (err) {
        alert('Failed to toggle worker: ' + err.message);
        delete dirtyInputs[`proc-${nodeId}-enabled`];
        await fetchTiersView(true);
    }
}

function bulkSetProcessing(enabled) {
    openTierModal({
        mode: 'confirm',
        title: enabled ? 'Enable All Processing Workers' : 'Disable All Processing Workers',
        subtitle: enabled ? 'Activate video transcoding across all connected VPS nodes' : 'Pause video transcoding across all connected VPS nodes',
        description: enabled
            ? 'All connected worker nodes in the pool will be marked <strong>Active</strong> and eligible to receive parallel transcoding jobs based on their resource limits.'
            : 'All worker nodes in the pool will be placed in <strong>Standby</strong> and will stop accepting new video transcoding jobs.',
        submitLabel: enabled ? 'Enable All Workers' : 'Disable All Workers',
        danger: !enabled,
        iconTheme: enabled ? 'theme-green' : 'theme-red',
        iconSvg: enabled
            ? '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>'
            : '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>',
        onSubmit: async () => {
            try {
                const res = await fetch('/api/processing', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ enabled: enabled })
                });
                if (!res.ok) throw new Error(await res.text());
                for (const k in dirtyInputs) {
                    if (k.startsWith('proc-') && k.endsWith('-enabled')) {
                        delete dirtyInputs[k];
                    }
                }
                await fetchTiersView(true);
            } catch (err) {
                alert('Failed to update workers: ' + err.message);
            }
        }
    });
}

let tiersPollTimer = null;

function initTiersPage() {
    destroyTiersPage();
    fetchTiersView(true);
    tiersPollTimer = setInterval(() => {
        fetchTiersView(false);
    }, 5000);
}

function destroyTiersPage() {
    if (tiersPollTimer) {
        clearInterval(tiersPollTimer);
        tiersPollTimer = null;
    }
}

if (window.App && window.App.pages) {
    window.App.pages.tiers = { init: initTiersPage, destroy: destroyTiersPage };
}

document.addEventListener('DOMContentLoaded', () => {
    fetchTiersView(true);
});
