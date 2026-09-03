let tiersCache = null;
let processingCache = null;

// Track user-selected options and active form edits to prevent polling reset
const selectedTierBlocks = {};
const dirtyInputs = {};

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

async function fetchTiersView(force = false) {
    try {
        const [tiersRes, procRes] = await Promise.all([
            fetch('/api/tiers'),
            fetch('/api/processing')
        ]);
        if (tiersRes.ok) {
            tiersCache = await tiersRes.json();
            renderTiers(force);
        }
        if (procRes.ok) {
            processingCache = await procRes.json();
            renderProcessing(force);
        }
    } catch (err) {
        console.error('fetchTiersView error:', err);
    }
}

function tierQuotaLabel(quotaBytes) {
    if (!quotaBytes || quotaBytes === 0) return 'Unlimited (full free space)';
    return formatBytes(quotaBytes);
}

function onTierSelectChange(tierId, value) {
    selectedTierBlocks[String(tierId)] = value;
}

function onInputChange(inputId, value) {
    dirtyInputs[inputId] = value;
}

function renderTiers(force = false) {
    const grid = document.getElementById('tiers-grid');
    if (!grid || !tiersCache) return;

    // If user is currently picking a dropdown or typing an input, don't wipe DOM during periodic polling
    if (!force && isUserInteractingWith(grid)) {
        return;
    }

    // Capture currently focused element and cursor position
    const activeEl = document.activeElement;
    const activeId = activeEl && activeEl.id ? activeEl.id : null;
    const selectionStart = (activeEl && typeof activeEl.selectionStart === 'number') ? activeEl.selectionStart : null;
    const selectionEnd = (activeEl && typeof activeEl.selectionEnd === 'number') ? activeEl.selectionEnd : null;

    // Snapshot any live values currently typed before re-render
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
        grid.innerHTML = '<div style="grid-column:1/-1;padding:32px;text-align:center;color:var(--text-secondary);background:var(--bg-surface);border-radius:var(--radius-lg);border:1px dashed var(--border-light);">No storage tiers configured yet. Click "+ Add Tier" above to get started.</div>';
        return;
    }

    grid.innerHTML = tiers.map(t => {
        const blocks = t.blocks || [];
        const blocksHtml = blocks.map((b, idx) => {
            const block = b.block;
            const pct = b.usable_bytes > 0 ? Math.min(100, Math.round((b.used_bytes / b.usable_bytes) * 100)) : 0;
            const barColor = pct > 85 ? 'var(--status-offline)' : pct > 60 ? 'var(--status-stale)' : 'var(--orange-primary)';
            const inputId = `tier-${t.id}-block-${idx}-quota`;
            const quotaGB = block.quota_bytes > 0 ? Math.round(block.quota_bytes / (1024 * 1024 * 1024)) : '';
            const liveQuotaGB = dirtyInputs[inputId] !== undefined ? dirtyInputs[inputId] : (quotaGB || '');
            const hostInputId = `tier-${t.id}-block-${idx}-host`;
            const liveHost = dirtyInputs[hostInputId] !== undefined ? dirtyInputs[hostInputId] : (block.public_host || '');

            return `
                <div class="tier-block-row">
                    <div class="tier-block-top">
                        <div class="tier-block-info">
                            <span class="tier-block-node" title="${escapeHtml(block.node_id)}">${escapeHtml(block.node_id)}</span>
                            <span class="tier-block-path">${escapeHtml(block.path)}</span>
                            <span class="type-pill ${block.disk_type ? block.disk_type.toLowerCase() : 'ssd'}">${escapeHtml(block.disk_type || 'SSD')}</span>
                            ${b.online ? '' : '<span class="node-tag" style="background:var(--status-offline-bg);color:var(--status-offline);">offline</span>'}
                        </div>
                        <div class="tier-block-quota-ctrl">
                            <input type="number" min="0" placeholder="GB" value="${escapeHtml(liveQuotaGB)}" title="Quota in GB — empty or 0 = unlimited"
                                id="${inputId}" class="quota-input" oninput="onInputChange('${inputId}', this.value)">
                            <span style="font-size:11px;color:var(--text-secondary);font-weight:600;">GB</span>
                            <button class="btn-quota-gear" style="color:var(--status-offline);padding:3px 7px;font-size:14px;line-height:1;" title="Remove block from this tier"
                                onclick="removeTierBlock(${t.id}, ${idx})">&#10005;</button>
                        </div>
                    </div>
                    <div class="tier-block-host-wrap" title="Custom streaming domain or CDN host for files stored on this drive">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex-shrink:0;color:var(--text-secondary);"><circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path></svg>
                        <span style="font-size:10.5px;font-weight:700;color:var(--text-secondary);white-space:nowrap;">Host / CDN:</span>
                        <input type="text" placeholder="e.g. cdn1.streammesh.com or https://cdn1.streammesh.com:2053"
                            value="${escapeHtml(liveHost)}" id="${hostInputId}"
                            class="tier-block-host-input" oninput="onInputChange('${hostInputId}', this.value)">
                    </div>
                    <div class="gauge-track" style="height:5px;margin:5px 0 4px 0;">
                        <div class="gauge-fill" style="width:${pct}%;background:${barColor};"></div>
                    </div>
                    <div class="tier-block-meta">
                        <span>${formatBytes(b.used_bytes)} / ${formatBytes(b.usable_bytes)} usable</span>
                        <span>${formatBytes(b.free_bytes)} raw free &bull; quota: ${tierQuotaLabel(block.quota_bytes)}</span>
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
            return `<option value="${escapeHtml(key)}" ${isSelected}>${escapeHtml(b.node_id)} &bull; ${escapeHtml(b.path)} (${escapeHtml(b.disk_type || 'SSD')} &bull; ${formatBytes(b.free_bytes)} free)</option>`;
        }).join('');

        const tierNameId = `tier-name-${t.id}`;
        const liveTierName = dirtyInputs[tierNameId] !== undefined ? dirtyInputs[tierNameId] : t.name;
        const addQuotaId = `tier-${t.id}-add-quota`;
        const liveAddQuota = dirtyInputs[addQuotaId] !== undefined ? dirtyInputs[addQuotaId] : '';
        const addHostId = `tier-${t.id}-add-host`;
        const liveAddHost = dirtyInputs[addHostId] !== undefined ? dirtyInputs[addHostId] : '';
        const isSystem = !!t.system;

        return `
            <div class="tier-card">
                <div class="tier-card-head">
                    <div class="tier-title-wrap">
                        <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
                            <input type="text" value="${escapeHtml(liveTierName)}" id="${tierNameId}"
                                class="tier-title-input" title="Click to rename tier" oninput="onInputChange('${tierNameId}', this.value)">
                            <span class="node-tag" style="background:#eef2ff;color:#4338ca;border:1px solid rgba(99,102,241,0.35);font-weight:800;font-family:'JetBrains Mono',monospace;padding:2px 8px;">${escapeHtml(tierLabel(t.id))}</span>
                        </div>
                        <div class="tier-stat-line">
                            <strong>${formatBytes(t.usable_bytes)}</strong> usable &bull; 
                            <span>${formatBytes(t.used_bytes)} used</span> &bull; 
                            <span>${(t.blocks || []).length} drive block(s)</span>
                        </div>
                    </div>
                    <div style="display:flex;align-items:center;gap:6px;">
                        ${isSystem
                            ? `<span class="node-tag" style="display:inline-flex;align-items:center;gap:4px;background:var(--bg-orange-soft);color:var(--orange-dark);border:1px solid var(--border-orange);font-weight:800;letter-spacing:0.5px;" title="Permanent system tier &mdash; cannot be deleted">
                                <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink:0;"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"></path><polyline points="9 12 11 14 15 10"></polyline></svg>
                                <span>SYSTEM</span>
                            </span>`
                            : ''}
                    </div>
                </div>

                <div class="tier-blocks-list">
                    ${blocksHtml || '<div style="font-size:12px;color:var(--text-secondary);padding:14px;text-align:center;background:var(--bg-body);border-radius:var(--radius-md);border:1px dashed var(--border-light);">No storage blocks bagged into this tier yet.</div>'}
                </div>

                ${unassigned.length > 0 ? `
                <div class="tier-add-bar" style="display:flex;flex-direction:column;gap:6px;">
                    <div style="display:flex;gap:6px;align-items:center;width:100%;">
                        <select id="tier-${t.id}-add-select" class="tier-add-select" style="flex:1;"
                            onchange="onTierSelectChange(${t.id}, this.value)"
                            oninput="onTierSelectChange(${t.id}, this.value)">
                            ${unassignedOpts}
                        </select>
                        <input type="number" min="0" placeholder="Quota GB (0=all)" id="${addQuotaId}" value="${escapeHtml(liveAddQuota)}"
                            class="tier-add-quota" style="width:110px;" title="0 or empty = unlimited full free space" oninput="onInputChange('${addQuotaId}', this.value)">
                    </div>
                    <div style="display:flex;gap:6px;align-items:center;width:100%;">
                        <div class="tier-block-host-wrap" style="margin-top:0;flex:1;" title="Custom domain/CDN link (e.g. cdn1.streammesh.com or https://cdn1.streammesh.com:2053)">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex-shrink:0;color:var(--text-secondary);"><circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path></svg>
                            <span style="font-size:10.5px;font-weight:700;color:var(--text-secondary);white-space:nowrap;">Host / CDN:</span>
                            <input type="text" placeholder="cdn1.streammesh.com or https://... (optional)" id="${addHostId}" value="${escapeHtml(liveAddHost)}"
                                class="tier-block-host-input" oninput="onInputChange('${addHostId}', this.value)">
                        </div>
                        <button class="btn btn-primary tier-add-btn" style="padding:5px 12px;font-size:12px;white-space:nowrap;" onclick="assignBlockToTier(${t.id})">+ Bag Block</button>
                    </div>
                </div>` : `
                <div style="font-size:11px;color:var(--text-muted);padding:8px 0;text-align:center;font-style:italic;">
                    All available node storage drives are assigned to tiers.
                </div>`}

                <div class="tier-actions-bar">
                    <button class="btn btn-primary" style="padding:6px 14px;font-size:12px;" onclick="saveTier(${t.id})">Save Tier</button>
                    ${isSystem ? '' : `<button class="btn btn-secondary" style="padding:6px 12px;font-size:12px;color:var(--status-offline);" onclick="deleteTier(${t.id})">Delete Tier</button>`}
                </div>
            </div>`;
    }).join('');

    // Ensure select elements strictly retain the active chosen key in DOM
    tiers.forEach(t => {
        const select = document.getElementById(`tier-${t.id}-add-select`);
        const chosenKey = selectedTierBlocks[String(t.id)];
        if (select && chosenKey) {
            select.value = chosenKey;
        }
    });

    // Restore focus if user was typing in an input
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

function currentTierById(id) {
    if (!tiersCache) return null;
    return (tiersCache.tiers || []).find(t => t.id === id) || null;
}

function tierPayloadFromStatus(t, overrides) {
    const nameInput = document.getElementById(`tier-name-${t.id}`);
    return Object.assign({
        id: t.id,
        name: nameInput ? nameInput.value : t.name,
        mediums: t.mediums || [],
        default: t.default || false,
        blocks: (t.blocks || []).map(b => ({
            node_id: b.block.node_id,
            path: b.block.path,
            disk_type: b.block.disk_type,
            quota_bytes: b.block.quota_bytes,
            public_host: b.block.public_host || ''
        }))
    }, overrides || {});
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

    await upsertTier(payload);
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

    // Prevent duplicate block in same tier
    const exists = payload.blocks.some(b => b.node_id === chosen.node_id && b.path === chosen.path);
    if (exists) {
        alert('This storage block is already bagged in this tier.');
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

async function deleteTier(id) {
    if (!confirm('Delete this tier? Its storage blocks return to the unassigned pool.')) return;
    try {
        const res = await fetch(`/api/tiers/${id}`, { method: 'DELETE' });
        if (!res.ok) alert('Delete failed: ' + (await res.text()));
        await fetchTiersView(true);
    } catch (err) {
        alert('Network error: ' + err.message);
    }
}

async function addNewTier() {
    const name = prompt('New tier name (e.g. Tier 3 · Archive):');
    if (!name) return;
    // id: -1 asks the master to allocate the next free tier id (0/1/2 are system tiers).
    await upsertTier({ id: -1, name: name, mediums: [], default: false, blocks: [] });
}

function renderProcessing(force = false) {
    const grid = document.getElementById('processing-grid');
    if (!grid || !processingCache) return;

    // If user is currently typing/selecting in processing section, do not wipe DOM during periodic polling
    if (!force && isUserInteractingWith(grid)) {
        return;
    }

    const activeEl = document.activeElement;
    const activeId = activeEl && activeEl.id ? activeEl.id : null;
    const selectionStart = (activeEl && typeof activeEl.selectionStart === 'number') ? activeEl.selectionStart : null;
    const selectionEnd = (activeEl && typeof activeEl.selectionEnd === 'number') ? activeEl.selectionEnd : null;

    // Snapshot any dirty values in processing cards
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
    if (badge) badge.innerText = `${eligibleCount} of ${nodes.length} workers eligible`;

    if (nodes.length === 0) {
        grid.innerHTML = '<div style="grid-column:1/-1;padding:32px;text-align:center;color:var(--text-secondary);background:var(--bg-surface);border-radius:var(--radius-lg);border:1px dashed var(--border-light);">No nodes currently connected to the cluster.</div>';
        return;
    }

    grid.innerHTML = nodes.map(n => {
        const p = n.profile || {};
        const enabledId = `proc-${n.node_id}-enabled`;
        const cpuId = `proc-${n.node_id}-cpu`;
        const ramId = `proc-${n.node_id}-ram`;
        const diskId = `proc-${n.node_id}-disk`;
        const scratchId = `proc-${n.node_id}-scratch`;

        const enabled = dirtyInputs[enabledId] !== undefined ? !!dirtyInputs[enabledId] : !!p.enabled;
        const cpuVal = dirtyInputs[cpuId] !== undefined ? dirtyInputs[cpuId] : (p.reserved_cpu_cores || '');
        const ramMB = p.reserved_ram_bytes ? Math.round(p.reserved_ram_bytes / (1024 * 1024)) : '';
        const ramVal = dirtyInputs[ramId] !== undefined ? dirtyInputs[ramId] : (ramMB || '');
        const prefDisk = (dirtyInputs[diskId] !== undefined ? dirtyInputs[diskId] : (p.preferred_disk_type || ''));
        const scratchGB = p.reserved_storage_bytes ? Math.round(p.reserved_storage_bytes / (1024 * 1024 * 1024)) : '';
        const scratchVal = dirtyInputs[scratchId] !== undefined ? dirtyInputs[scratchId] : (scratchGB || '');

        const statusHtml = enabled
            ? (n.eligible
                ? '<span class="node-tag" style="background:#ecfdf5;color:#059669;border:1px solid rgba(16,185,129,0.3);font-weight:700;">&#10003; Eligible</span>'
                : `<span class="node-tag" style="background:#fef2f2;color:#dc2626;border:1px solid rgba(239,68,68,0.25);font-weight:700;">&#10007; Not eligible</span>`)
            : '<span class="node-tag" style="background:var(--bg-body);color:var(--text-secondary);font-weight:700;">Disabled</span>';

        const reasonsHtml = (!enabled || n.eligible) ? '' : `
            <div class="proc-reasons-box">
                ${(n.reasons || []).map(r => `<div>&#9888; ${escapeHtml(r)}</div>`).join('')}
            </div>`;

        const nodeDisks = n.disks || [];
        const diskOptionsHtml = [
            `<option value="" ${prefDisk === '' ? 'selected' : ''}>Any Disk</option>`,
            ...nodeDisks.map(d => {
                const isSel = (prefDisk === d.path || prefDisk.toUpperCase() === d.path.toUpperCase() || prefDisk.toUpperCase() === (d.disk_type || '').toUpperCase());
                const label = `${d.path} (${d.disk_type || 'Disk'} • ${formatBytes(d.free_bytes)} free)`;
                return `<option value="${escapeHtml(d.path)}" ${isSel ? 'selected' : ''}>${escapeHtml(label)}</option>`;
            })
        ].join('');

        const diskLabelText = p.preferred_disk_type ? `${p.preferred_disk_type} ` : '';

        return `
            <div class="processing-card">
                <div class="proc-card-head">
                    <div style="min-width:0;flex:1 1 auto;">
                        <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
                            <span class="proc-node-title" title="${escapeHtml(n.node_id)}">${escapeHtml(n.node_id)}</span>
                            ${statusHtml}
                        </div>
                        <div class="proc-telemetry-pill">
                            <span>Live: ${n.free_cpu_cores} cores</span> &bull; 
                            <span>${formatBytes(n.free_ram_bytes)} RAM free</span>
                            ${n.scratch_free_bytes > 0 ? ` &bull; <span>${formatBytes(n.scratch_free_bytes)} ${escapeHtml(diskLabelText)}free</span>` : ''}
                        </div>
                    </div>
                    <div>
                        <label class="proc-toggle-label" for="${enabledId}">
                            <span class="proc-switch">
                                <input type="checkbox" id="${enabledId}" ${enabled ? 'checked' : ''} onchange="toggleSingleNodeProcessing('${escapeHtml(n.node_id)}', this.checked)">
                                <span class="proc-slider"></span>
                            </span>
                            <span class="proc-toggle-text">Use for Processing</span>
                        </label>
                    </div>
                </div>

                ${reasonsHtml}

                <div class="proc-form-grid">
                    <div class="proc-field">
                        <label class="proc-field-label">CPU Cores</label>
                        <input type="number" min="0" placeholder="e.g. 1" id="${cpuId}" value="${escapeHtml(cpuVal)}" class="proc-field-input" oninput="onInputChange('${cpuId}', this.value)">
                    </div>
                    <div class="proc-field">
                        <label class="proc-field-label">RAM (MB)</label>
                        <input type="number" min="0" placeholder="e.g. 500" id="${ramId}" value="${escapeHtml(ramVal)}" class="proc-field-input" oninput="onInputChange('${ramId}', this.value)">
                    </div>
                    <div class="proc-field">
                        <label class="proc-field-label">Scratch Disk</label>
                        <select id="${diskId}" class="proc-field-select" onchange="onInputChange('${diskId}', this.value)" oninput="onInputChange('${diskId}', this.value)">
                            ${diskOptionsHtml}
                        </select>
                    </div>
                    <div class="proc-field">
                        <label class="proc-field-label">Scratch (GB) <span style="font-weight:400;text-transform:none;color:var(--text-muted);">(opt.)</span></label>
                        <input type="number" min="0" placeholder="e.g. 10" id="${scratchId}" value="${escapeHtml(scratchVal)}" class="proc-field-input" oninput="onInputChange('${scratchId}', this.value)">
                    </div>
                </div>

                <div style="display:flex;justify-content:flex-start;margin-top:auto;padding-top:10px;">
                    <button class="btn btn-primary" style="padding:6px 14px;font-size:12px;" onclick="saveProcessingProfile('${escapeHtml(n.node_id)}')">Save Reservation</button>
                </div>
            </div>`;
    }).join('');

    // Restore focus in processing section if active
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
    const disk = document.getElementById(`proc-${nodeId}-disk`).value;
    const scratchGB = parseInt(document.getElementById(`proc-${nodeId}-scratch`).value) || 0;

    const payload = {
        node_id: nodeId,
        enabled: enabled,
        reserved_cpu_cores: cpu,
        reserved_ram_bytes: ramMB > 0 ? ramMB * 1024 * 1024 : 0,
        preferred_disk_type: disk,
        reserved_storage_bytes: scratchGB > 0 ? scratchGB * 1024 * 1024 * 1024 : 0
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

async function bulkSetProcessing(enabled) {
    const actionName = enabled ? 'ENABLE' : 'DISABLE';
    if (!confirm(`Are you sure you want to ${actionName} processing across all worker nodes in the pool?`)) {
        return;
    }
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

setInterval(() => {
    if (currentView === 'tiers') fetchTiersView(false);
}, 5000);

document.addEventListener('DOMContentLoaded', () => {
    fetchTiersView(true);
});
