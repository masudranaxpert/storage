// Storage page module: physical drives table, tier eligibility toggles, storage folders tab, and directory cleanup modal.
(function (global) {
    'use strict';

    let currentStorageTab = 'disks';
    let currentCleanTask = null;
    let pollTimer = null;

    function getTiersCache() {
        return (typeof tiersCache !== 'undefined' && tiersCache) ? tiersCache : global.tiersCache;
    }

    function isDriveExcludedFromTiers(nodeID, path) {
        const cache = getTiersCache();
        if (!cache || !Array.isArray(cache.excluded_blocks)) return false;
        return cache.excluded_blocks.includes(`${nodeID}|${path}`);
    }

    function tierBadgeForBlock(nodeID, path) {
        const cache = getTiersCache();
        if (cache && Array.isArray(cache.tiers)) {
            for (const t of cache.tiers) {
                for (const b of (t.blocks || [])) {
                    if (b.block && b.block.node_id === nodeID && b.block.path === path) {
                        const themeClass = t.id === 0 ? 'tier-hot' : (t.id === 1 ? 'tier-warm' : (t.id === 2 ? 'tier-cold' : 'tier-custom'));
                        return `<span class="storage-tier-pill ${themeClass}" title="Assigned to ${global.escapeHtml(t.name)}"><span class="tier-pill-dot"></span><span>${global.escapeHtml(t.name)}</span></span>`;
                    }
                }
            }
        }
        if (isDriveExcludedFromTiers(nodeID, path)) {
            return `<span class="storage-tier-pill tier-excluded" title="Drive excluded from storage tiers">Excluded</span>`;
        }
        return '';
    }

    async function toggleDriveTierEligibility(nodeID, path, eligible, checkboxEl) {
        if (checkboxEl) checkboxEl.disabled = true;
        try {
            const res = await fetch('/api/storage/drive-eligibility', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    node_id: nodeID,
                    path: path,
                    eligible: !!eligible
                })
            });
            if (!res.ok) throw new Error(await res.text());
            if (typeof global.fetchTiersView === 'function') {
                await global.fetchTiersView(true);
            }
            if (global.cachedPoolData) {
                renderStorage(global.cachedPoolData);
            }
        } catch (err) {
            alert('Failed to update drive tier eligibility: ' + err.message);
            if (checkboxEl) checkboxEl.checked = !eligible;
        } finally {
            if (checkboxEl) checkboxEl.disabled = false;
        }
    }

    function renderStorage(data) {
        const tbody = document.getElementById('storage-table-body');
        if (!tbody || !data) return;

        const nodes = data.nodes || [];
        let rows = '';
        let volumeCount = 0;

        nodes.forEach(n => {
            const m = n.metrics;
            const isOnline = n.status === 'online';

            (m.disks || []).forEach(d => {
                volumeCount++;
                const pct = d.used_percent || 0;
                const dtype = d.disk_type || 'SSD';
                const dtypeClass = dtype.toLowerCase().includes('nvme') ? 'nvme' : (dtype.toLowerCase().includes('hdd') ? 'hdd' : 'ssd');
                const isEligible = !isDriveExcludedFromTiers(m.node_id, d.path);
                const utilCellHtml = (global.UI && typeof global.UI.capacityBar === 'function')
                    ? global.UI.capacityBar({ percent: pct })
                    : `<div style="min-width:120px;"><div class="bui-stage-pill-track" style="height:10px;margin:0 0 5px 0;"><div class="bui-stage-pill-fill" style="width:${pct}%;background:var(--orange-primary)"></div></div><div class="tabular-num" style="font-size:11.5px;color:var(--text-secondary);font-weight:500;">${pct.toFixed(1)}% used</div></div>`;

                rows += `
                    <tr>
                        <td data-label="Node / Host">
                            <div style="display:flex;flex-direction:column;gap:3px;">
                                <div style="display:flex;align-items:center;gap:6px;flex-wrap:wrap;">
                                    <span class="status-indicator-dot ${isOnline ? 'online' : 'offline'}" style="width:8px;height:8px;box-shadow:none;" title="${isOnline ? 'Node Online' : 'Node Offline'}"></span>
                                    <strong style="color:var(--text-primary);font-size:13.5px;">${global.escapeHtml(m.node_id)}</strong>
                                </div>
                                <div style="font-size:12px;color:var(--text-secondary);">${global.escapeHtml(m.hostname)}</div>
                            </div>
                        </td>
                        <td data-label="Mount &amp; Tier">
                            <div class="storage-mount-cell">
                                <code class="storage-path-code">${global.escapeHtml(d.path)}</code>
                                ${tierBadgeForBlock(m.node_id, d.path)}
                            </div>
                        </td>
                        <td data-label="Medium"><span class="type-pill ${dtypeClass}">${global.escapeHtml(dtype)}</span></td>
                        <td data-label="FS Type"><span class="fs-type-pill">${global.escapeHtml(d.fs_type || 'ext4')}</span></td>
                        <td data-label="Total Size"><strong class="tabular-num">${global.formatBytes(d.total_bytes)}</strong></td>
                        <td data-label="Free Space" class="tabular-num" style="color:#047857;font-weight:700;">${global.formatBytes(d.free_bytes)}</td>
                        <td data-label="Utilization" class="storage-util-cell" style="width:165px;">
                            ${utilCellHtml}
                        </td>
                        <td data-label="Tier Pool">
                            <div class="drive-tier-toggle-cell">
                                <label class="proc-ios-toggle" title="${isEligible ? 'Allowed in Storage Tiers (click to exclude)' : 'Excluded from Storage Tiers (click to allow)'}">
                                    <input type="checkbox" ${isEligible ? 'checked' : ''} onchange="toggleDriveTierEligibility('${global.escapeHtml(m.node_id)}', '${global.escapeHtml(d.path)}', this.checked, this)">
                                    <span class="proc-ios-slider"></span>
                                </label>
                                <span class="drive-tier-toggle-label ${isEligible ? 'allowed' : 'excluded'}">${isEligible ? 'Allowed' : 'Excluded'}</span>
                            </div>
                        </td>
                    </tr>
                `;
            });
        });

        const volCountBadge = document.getElementById('storage-volumes-count-badge');
        if (volCountBadge) {
            volCountBadge.innerText = `${volumeCount} ${volumeCount === 1 ? 'Volume' : 'Volumes'}`;
        }

        const emptyHtml = (global.UI && typeof global.UI.emptyState === 'function')
            ? global.UI.emptyState({ colspan: 9, message: 'No storage drives pooled yet.' })
            : '<tr><td colspan="9" style="text-align:center;padding:24px;color:var(--text-secondary);">No storage drives pooled yet.</td></tr>';
        tbody.innerHTML = rows || emptyHtml;
    }

    function switchStorageTab(tab) {
        currentStorageTab = tab;
        document.querySelectorAll('.storage-tab-btn').forEach(btn => btn.classList.remove('active'));
        document.querySelectorAll('.storage-tab-content').forEach(content => content.style.display = 'none');

        const btn = document.getElementById(`tab-btn-${tab}`);
        const content = document.getElementById(`storage-tab-${tab}`);
        if (btn) btn.classList.add('active');
        if (content) content.style.display = 'block';

        if (tab === 'folders') {
            fetchStorageFolders();
        }
    }

    async function fetchStorageFolders() {
        const tbody = document.getElementById('storage-folders-table-body');
        if (!tbody) return;

        try {
            const res = await fetch('/api/storage/folders');
            if (!res.ok) throw new Error(await res.text());
            const data = await res.json();
            renderStorageFolders(data.nodes || []);
        } catch (err) {
            tbody.innerHTML = `<tr><td colspan="6" style="text-align:center; padding:24px; color:var(--status-offline);">Failed to load storage folders: ${global.escapeHtml(err.message)}</td></tr>`;
        }
    }

    function renderStorageFolders(nodes) {
        const tbody = document.getElementById('storage-folders-table-body');
        if (!tbody) return;

        let totalMedia = 0;
        let totalMediaFiles = 0;
        let totalProc = 0;
        let totalProcFiles = 0;

        let rows = '';
        nodes.forEach(n => {
            const cleanIPs = global.cleanNodeIPs(n.ips);
            const primaryIP = n.primary_ip || cleanIPs[0] || (n.ips && n.ips[0]) || '127.0.0.1';
            const hostname = n.hostname || 'Host';
            const statusBadge = (global.UI && typeof global.UI.statusPill === 'function')
                ? global.UI.statusPill(n.status, null, 'proc')
                : (n.status === 'online'
                    ? `<span class="proc-status-badge eligible"><span class="badge-dot"></span><span>Online</span></span>`
                    : `<span class="proc-status-badge not-eligible"><span class="badge-dot"></span><span>${global.escapeHtml(n.status || 'Offline')}</span></span>`);

            const drives = n.drives && n.drives.length > 0 ? n.drives : [{
                drive_name: 'System SSD (/)',
                mount_point: '/',
                stream_root: '/stream',
                media_dir: '/stream/media',
                media_size_bytes: n.media_size_bytes || 0,
                media_file_count: n.media_file_count || 0,
                processing_dir: '/stream/processing',
                processing_size_bytes: n.processing_size_bytes || 0,
                processing_file_count: n.processing_file_count || 0,
                total_stream_bytes: n.total_stream_bytes || 0
            }];

            drives.forEach((d) => {
                totalMedia += d.media_size_bytes || 0;
                totalMediaFiles += d.media_file_count || 0;
                totalProc += d.processing_size_bytes || 0;
                totalProcFiles += d.processing_file_count || 0;

                const isMultiDrive = drives.length > 1;
                const driveTag = d.drive_name || d.mount_point;

                rows += `
                    <tr>
                        <td data-label="Node &amp; Host">
                            <div>
                                <div style="display:flex; align-items:center; gap:8px; flex-wrap:wrap;">
                                    <span style="font-size:14px; font-weight:700; color:var(--text-primary);">${global.escapeHtml(n.node_id)}</span>
                                    ${isMultiDrive ? `<span class="ip-chip" style="color:var(--orange-dark); font-weight:700; background:var(--bg-orange-soft); border-color:var(--border-hover);"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="22" y1="12" x2="2" y2="12"></line><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"></path></svg> ${global.escapeHtml(driveTag)}</span>` : ''}
                                </div>
                                <div style="font-size:12px; color:var(--text-secondary); margin:4px 0 6px 0; display:flex; align-items:center; gap:6px; flex-wrap:wrap;">
                                    <span>${global.escapeHtml(hostname)}</span>
                                    <span class="ip-chip">${global.escapeHtml(primaryIP)}</span>
                                </div>
                                <div>${statusBadge}</div>
                            </div>
                        </td>
                        <td data-label="Stream Root">
                            <div class="path-chip" title="${global.escapeHtml(d.stream_root)}">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="color:var(--text-muted); flex-shrink:0;">
                                    <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path>
                                </svg>
                                <span>${global.escapeHtml(d.stream_root)}</span>
                            </div>
                        </td>
                        <td data-label="Media Folder">
                            <div>
                                <div class="path-chip media" title="${global.escapeHtml(d.media_dir)}">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex-shrink:0;">
                                        <polygon points="23 7 16 12 23 17 23 7"></polygon>
                                        <rect x="1" y="5" width="15" height="14" rx="2" ry="2"></rect>
                                    </svg>
                                    <span>${global.escapeHtml(d.media_dir)}</span>
                                </div>
                                <div class="folder-stat-line">
                                    <span class="size tabular-num">${global.formatBytes(d.media_size_bytes || 0)}</span>
                                    <span class="count">• ${d.media_file_count || 0} Files</span>
                                </div>
                            </div>
                        </td>
                        <td data-label="Processing Folder">
                            <div>
                                <div class="path-chip proc" title="${global.escapeHtml(d.processing_dir)}">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex-shrink:0;">
                                        <rect x="4" y="4" width="16" height="16" rx="2"></rect>
                                        <line x1="9" y1="1" x2="9" y2="4"></line>
                                        <line x1="15" y1="1" x2="15" y2="4"></line>
                                        <line x1="9" y1="20" x2="9" y2="23"></line>
                                        <line x1="15" y1="20" x2="15" y2="23"></line>
                                    </svg>
                                    <span>${global.escapeHtml(d.processing_dir)}</span>
                                </div>
                                <div class="folder-stat-line">
                                    <span class="size tabular-num" style="${d.processing_size_bytes > 0 ? 'color:#2563eb;' : 'color:var(--text-muted);'}">
                                        ${global.formatBytes(d.processing_size_bytes || 0)}
                                    </span>
                                    <span class="count">• ${d.processing_file_count || 0} temp files</span>
                                </div>
                            </div>
                        </td>
                        <td data-label="Total Usage">
                            ${(d.total_stream_bytes || 0) > 0 ? `
                                <span class="total-usage-pill tabular-num">
                                    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                                        <ellipse cx="12" cy="5" rx="9" ry="3"></ellipse>
                                        <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"></path>
                                        <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"></path>
                                    </svg>
                                    <span>${global.formatBytes(d.total_stream_bytes || 0)}</span>
                                </span>
                            ` : `
                                <span class="total-usage-pill empty tabular-num">
                                    <span>0 B</span>
                                </span>
                            `}
                        </td>
                        <td data-label="Actions" style="text-align:right;">
                            <div style="display:inline-flex;align-items:center;gap:6px;justify-content:flex-end;">
                                <button class="tbl-icon-btn" onclick="cleanDriveFolder('${global.escapeHtml(n.node_id)}', '${global.escapeHtml(d.processing_dir)}', 'processing')" title="Clean temporary scratch files (${global.escapeHtml(d.processing_dir)})" aria-label="Clean Temp">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                        <path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3Z"></path>
                                        <path d="M5 3v4"></path>
                                        <path d="M3 5h4"></path>
                                    </svg>
                                </button>
                                <button class="tbl-icon-btn is-danger" onclick="cleanDriveFolder('${global.escapeHtml(n.node_id)}', '${global.escapeHtml(d.media_dir)}', 'media')" title="Delete stored media files (${global.escapeHtml(d.media_dir)})" aria-label="Delete Media">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                        <polyline points="3 6 5 6 21 6"></polyline>
                                        <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                                    </svg>
                                </button>
                            </div>
                        </td>
                    </tr>
                `;
            });
        });

        const emptyHtml = (global.UI && typeof global.UI.emptyState === 'function')
            ? global.UI.emptyState({ colspan: 6, message: 'No nodes connected.' })
            : '<tr><td colspan="6" style="text-align:center; padding:24px; color:var(--text-secondary);">No nodes connected.</td></tr>';
        tbody.innerHTML = rows || emptyHtml;

        const mediaEl = document.getElementById('summary-total-media');
        const mediaCountEl = document.getElementById('summary-total-media-files');
        const procEl = document.getElementById('summary-total-processing');
        const procCountEl = document.getElementById('summary-total-processing-files');

        if (mediaEl) mediaEl.innerText = global.formatBytes(totalMedia);
        if (mediaCountEl) mediaCountEl.innerText = `${totalMediaFiles} Files stored across cluster`;
        if (procEl) procEl.innerText = global.formatBytes(totalProc);
        if (procCountEl) procCountEl.innerText = `${totalProcFiles} temporary processing artifacts`;
    }

    async function refreshStorageView(btnEl) {
        const icon = btnEl ? btnEl.querySelector('svg') : document.querySelector('#btn-storage-refresh svg');
        const label = btnEl ? btnEl.querySelector('span') : document.querySelector('#btn-storage-refresh span');

        if (icon) icon.classList.add('spin-fast');
        if (label) label.innerText = 'Refreshing...';
        if (btnEl) btnEl.disabled = true;

        try {
            await Promise.all([
                fetchStorageFolders(),
                global.fetchClusterTelemetry(),
                typeof global.fetchTiersView === 'function' ? global.fetchTiersView(true) : Promise.resolve()
            ]);
            if (label) label.innerText = 'Refreshed!';
            setTimeout(() => {
                if (label) label.innerText = 'Refresh';
            }, 1200);
        } catch (_) {
            if (label) label.innerText = 'Failed';
            setTimeout(() => {
                if (label) label.innerText = 'Refresh';
            }, 1500);
        } finally {
            if (icon) icon.classList.remove('spin-fast');
            if (btnEl) btnEl.disabled = false;
        }
    }

    function openStorageCleanModal(config) {
        currentCleanTask = config;

        const modal = document.getElementById('storage-clean-modal');
        if (!modal) return;

        const titleEl = document.getElementById('clean-modal-title');
        const subEl = document.getElementById('clean-modal-sub');
        const descEl = document.getElementById('clean-modal-desc');
        const nodeBadge = document.getElementById('clean-modal-node-badge');
        const pathBadge = document.getElementById('clean-modal-path-badge');
        const iconWrap = document.getElementById('clean-modal-icon-wrap');
        const iconEl = document.getElementById('clean-modal-icon');
        const actionBtn = document.getElementById('clean-modal-action-btn');
        const actionText = document.getElementById('clean-modal-action-text');
        const cancelBtn = document.getElementById('clean-modal-cancel-btn');
        const doneBtn = document.getElementById('clean-modal-done-btn');
        const progressEl = document.getElementById('clean-modal-progress');
        const resultEl = document.getElementById('clean-modal-result');
        const confirmWrap = document.getElementById('clean-modal-confirm-wrap');
        const confirmInput = document.getElementById('clean-modal-confirm-input');

        if (titleEl) titleEl.innerText = config.title || 'Clean Storage';
        if (subEl) subEl.innerText = config.sub || 'Storage Maintenance';
        if (descEl) descEl.innerHTML = config.desc || 'Are you sure you want to clean this directory?';
        if (nodeBadge) nodeBadge.innerText = config.nodeId === 'all' ? 'All Connected Nodes' : config.nodeId;
        if (pathBadge) pathBadge.innerText = config.dirPath || (config.target === 'media' ? 'All /stream/media Folders' : 'All /stream/processing Folders');

        if (config.target === 'media') {
            if (iconWrap) {
                iconWrap.style.background = '#fef2f2';
                iconWrap.style.color = '#dc2626';
            }
            if (iconEl) {
                iconEl.innerHTML = '<polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>';
            }
            if (actionBtn) {
                actionBtn.className = 'btn btn-clean-media';
                actionBtn.style.padding = '8px 16px';
            }
        } else {
            if (iconWrap) {
                iconWrap.style.background = '#eff6ff';
                iconWrap.style.color = '#2563eb';
            }
            if (iconEl) {
                iconEl.innerHTML = '<path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3Z"></path><path d="M5 3v4"></path><path d="M3 5h4"></path>';
            }
            if (actionBtn) {
                actionBtn.className = 'btn btn-primary';
                actionBtn.style.padding = '8px 16px';
            }
        }

        if (actionText) actionText.innerText = config.actionLabel || 'Confirm & Clean';

        if (config.requireConfirmation) {
            if (confirmWrap) confirmWrap.style.display = 'block';
            if (confirmInput) confirmInput.value = '';
            if (actionBtn) actionBtn.disabled = true;
        } else {
            if (confirmWrap) confirmWrap.style.display = 'none';
            if (actionBtn) actionBtn.disabled = false;
        }

        if (progressEl) progressEl.style.display = 'none';
        if (resultEl) resultEl.style.display = 'none';
        if (cancelBtn) cancelBtn.style.display = 'inline-block';
        if (actionBtn) actionBtn.style.display = 'inline-block';
        if (doneBtn) doneBtn.style.display = 'none';

        modal.classList.add('open', 'active');
        global.setModalOpen(true);
    }

    function onCleanConfirmInput(val) {
        const actionBtn = document.getElementById('clean-modal-action-btn');
        if (!actionBtn || !currentCleanTask || !currentCleanTask.requireConfirmation) return;
        actionBtn.disabled = (val.trim().toUpperCase() !== 'DELETE');
    }

    function closeStorageCleanModal() {
        const modal = document.getElementById('storage-clean-modal');
        if (modal) modal.classList.remove('open', 'active');
        currentCleanTask = null;
        global.setModalOpen(false);
    }

    async function executeStorageCleanAction() {
        if (!currentCleanTask) return;

        const actionBtn = document.getElementById('clean-modal-action-btn');
        const cancelBtn = document.getElementById('clean-modal-cancel-btn');
        const doneBtn = document.getElementById('clean-modal-done-btn');
        const progressEl = document.getElementById('clean-modal-progress');
        const resultEl = document.getElementById('clean-modal-result');
        const resultTitle = document.getElementById('clean-modal-result-title');
        const resultMsg = document.getElementById('clean-modal-result-msg');
        const confirmWrap = document.getElementById('clean-modal-confirm-wrap');

        if (actionBtn) actionBtn.style.display = 'none';
        if (cancelBtn) cancelBtn.style.display = 'none';
        if (confirmWrap) confirmWrap.style.display = 'none';
        if (progressEl) progressEl.style.display = 'block';

        const payload = {
            node_id: currentCleanTask.nodeId,
            target: currentCleanTask.target
        };
        if (currentCleanTask.dirPath) {
            payload.dir = currentCleanTask.dirPath;
        }

        try {
            const res = await fetch('/api/storage/clean', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });
            if (!res.ok) throw new Error(await res.text());
            const data = await res.json();
            const freed = data.freed_bytes || 0;
            const items = data.freed_items || 0;

            if (progressEl) progressEl.style.display = 'none';
            if (resultEl) {
                resultEl.style.display = 'block';
                resultEl.style.background = '#f0fdf4';
                resultEl.style.borderColor = '#bbf7d0';
                if (resultTitle) {
                    resultTitle.innerText = 'Cleanup Complete!';
                    resultTitle.style.color = '#16a34a';
                }
                if (resultMsg) {
                    if (freed === 0 && items === 0) {
                        resultMsg.innerText = 'Folder was already clean (0 B used).';
                    } else {
                        resultMsg.innerText = `Freed ${global.formatBytes(freed)} (${items} item${items === 1 ? '' : 's'} removed).`;
                    }
                    resultMsg.style.color = '#15803d';
                }
            }
            if (doneBtn) doneBtn.style.display = 'inline-block';
            refreshStorageView();
        } catch (err) {
            if (progressEl) progressEl.style.display = 'none';
            if (resultEl) {
                resultEl.style.display = 'block';
                resultEl.style.background = '#fef2f2';
                resultEl.style.borderColor = '#fecaca';
                if (resultTitle) {
                    resultTitle.innerText = 'Cleanup Failed';
                    resultTitle.style.color = '#dc2626';
                }
                if (resultMsg) {
                    resultMsg.innerText = err.message || 'An error occurred during cleaning.';
                    resultMsg.style.color = '#b91c1c';
                }
            }
            if (doneBtn) doneBtn.style.display = 'inline-block';
        }
    }

    function cleanDriveFolder(nodeId, dirPath, target) {
        const isMedia = target === 'media';
        openStorageCleanModal({
            nodeId: nodeId,
            dirPath: dirPath,
            target: target,
            title: isMedia ? 'Delete Stored Media' : 'Clean Temp Files',
            sub: `Drive cleanup on ${nodeId}`,
            desc: isMedia
                ? `⚠️ <strong>Warning:</strong> This will permanently delete <strong>all media video files</strong> inside <code style="background:#fee2e2; padding:2px 6px; border-radius:4px; font-size:12px;">${global.escapeHtml(dirPath)}</code> on node <strong>${global.escapeHtml(nodeId)}</strong>. This will set its storage usage to 0 B.`
                : `This will remove all temporary download and working files from <code style="background:#eff6ff; padding:2px 6px; border-radius:4px; font-size:12px;">${global.escapeHtml(dirPath)}</code> on node <strong>${global.escapeHtml(nodeId)}</strong>.`,
            actionLabel: isMedia ? 'Delete Media Now' : 'Clean Temp Now',
            requireConfirmation: false
        });
    }

    function cleanNodeFolder(nodeId, target) {
        const isMedia = target === 'media';
        openStorageCleanModal({
            nodeId: nodeId,
            target: target,
            title: isMedia ? 'Delete Node Media' : 'Clean Temp Files',
            sub: `Cluster maintenance on ${nodeId}`,
            desc: isMedia
                ? `⚠️ <strong>Warning:</strong> This will permanently delete all stored media packages across all drives on node <strong>${global.escapeHtml(nodeId)}</strong>.`
                : `This will clean all temporary working files on node <strong>${global.escapeHtml(nodeId)}</strong>.`,
            actionLabel: isMedia ? 'Delete Node Media' : 'Clean Temp',
            requireConfirmation: false
        });
    }

    function cleanAllProcessingScratch() {
        openStorageCleanModal({
            nodeId: 'all',
            target: 'processing',
            title: 'Clean Temp Workspaces',
            sub: 'Cluster-wide Maintenance',
            desc: 'This will remove all temporary processing, remuxing, and download files from <strong>all connected nodes</strong>. Active video streams will not be affected.',
            actionLabel: 'Clean All Temp Files',
            requireConfirmation: false
        });
    }

    function purgeAllMedia() {
        openStorageCleanModal({
            nodeId: 'all',
            target: 'media',
            title: 'Purge All Cluster Media',
            sub: 'Destructive Cluster Operation',
            desc: '⚠️ <strong>CRITICAL WARNING:</strong> This action will permanently <strong>DELETE ALL media video files</strong> across the ENTIRE cluster, resetting total media storage to 0 B across all nodes.',
            actionLabel: 'Permanently Delete All Media',
            requireConfirmation: true
        });
    }

    function init() {
        destroy();
        if (global.cachedPoolData) renderStorage(global.cachedPoolData);
        global.fetchClusterTelemetry();
        if (currentStorageTab === 'folders') {
            fetchStorageFolders();
        }
        pollTimer = setInterval(() => {
            global.fetchClusterTelemetry();
            if (currentStorageTab === 'folders') {
                fetchStorageFolders();
            }
        }, 3000);
    }

    function destroy() {
        if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
    }

    global.isDriveExcludedFromTiers = isDriveExcludedFromTiers;
    global.tierBadgeForBlock = tierBadgeForBlock;
    global.toggleDriveTierEligibility = toggleDriveTierEligibility;
    global.renderStorage = renderStorage;
    global.switchStorageTab = switchStorageTab;
    global.fetchStorageFolders = fetchStorageFolders;
    global.renderStorageFolders = renderStorageFolders;
    global.refreshStorageView = refreshStorageView;
    global.openStorageCleanModal = openStorageCleanModal;
    global.onCleanConfirmInput = onCleanConfirmInput;
    global.closeStorageCleanModal = closeStorageCleanModal;
    global.executeStorageCleanAction = executeStorageCleanAction;
    global.cleanDriveFolder = cleanDriveFolder;
    global.cleanNodeFolder = cleanNodeFolder;
    global.cleanAllProcessingScratch = cleanAllProcessingScratch;
    global.purgeAllMedia = purgeAllMedia;

    if (global.App && global.App.pages) {
        global.App.pages.storage = { init, destroy, render: renderStorage };
    }
})(window);
