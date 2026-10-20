// Nodes page module: node cards, rescan/delete, tool installer, node config modal, and VPS provisioning modal.
(function (global) {
    'use strict';

    let currentLayout = 'grid';
    let currentFilter = 'all';
    const expandedNodeIDs = new Set();
    let activeSelectedNode = null;
    let pollTimer = null;
    const toolPollTimers = new Set();
    let listenersBound = false;

    function renderDriveItem(d) {
        const usedPct = (typeof d.used_percent === 'number') ? d.used_percent : 0;
        let fillClass = 'fill-normal';
        if (usedPct > 92) {
            fillClass = 'fill-critical';
        } else if (usedPct > 80) {
            fillClass = 'fill-warning';
        }

        const diskType = (d.disk_type || 'SSD').toUpperCase();
        let mediumClass = 'ssd';
        if (diskType.includes('NVME')) mediumClass = 'nvme';
        else if (diskType.includes('HDD')) mediumClass = 'hdd';

        return `
            <div class="node-drive-item">
                <div class="drive-item-header">
                    <div class="drive-path-info">
                        <span class="drive-mount">${global.escapeHtml(d.path)}</span>
                        <span class="drive-medium-tag ${mediumClass}">${global.escapeHtml(diskType)}</span>
                        ${d.fs_type ? `<span class="drive-fs-tag">${global.escapeHtml(d.fs_type)}</span>` : ''}
                    </div>
                    <div class="drive-space-info">
                        <strong class="tabular-num">${global.formatBytes(d.free_bytes)} free</strong>
                        <span class="drive-used-text tabular-num">${global.formatBytes(d.used_bytes)} / ${global.formatBytes(d.total_bytes)} (${usedPct.toFixed(1)}%)</span>
                    </div>
                </div>
                <div class="drive-progress-track">
                    <div class="drive-progress-fill ${fillClass}" style="width:${Math.min(100, Math.max(0, usedPct))}%"></div>
                </div>
            </div>
        `;
    }

    function createNodeCardHTML(n) {
        const m = n.metrics;
        const status = n.status || 'online';
        const isExpanded = expandedNodeIDs.has(m.node_id);

        let totalDiskBytes = 0;
        let freeDiskBytes = 0;
        const disks = m.disks || [];
        disks.forEach(d => {
            totalDiskBytes += (d.total_bytes || 0);
            freeDiskBytes += (d.free_bytes || 0);
        });
        const usedDiskBytes = Math.max(0, totalDiskBytes - freeDiskBytes);
        const storagePct = totalDiskBytes > 0 ? (usedDiskBytes / totalDiskBytes) * 100 : 0;
        const storageFillClass = storagePct > 92 ? 'fill-critical' : (storagePct > 80 ? 'fill-warning' : 'fill-normal');

        const cleanIPList = global.cleanNodeIPs(m.ips);
        const primaryIP = cleanIPList[0] || (m.ips && m.ips[0]) || m.hostname || '127.0.0.1';
        const extraIPCount = cleanIPList.length - 1;
        const extraIPBadge = extraIPCount > 0
            ? ` <span class="badge-extra-ips" title="Other interfaces: ${global.escapeHtml(cleanIPList.slice(1).join(', '))}">+${extraIPCount}</span>`
            : '';

        const caps = m.capabilities || {};
        const hasAria = (caps.has_aria2c === true);
        const hasFFmpeg = (caps.has_ffmpeg === true);
        const hasRclone = (caps.has_rclone === true);

        const cpuUsed = m.cpu ? (m.cpu.used_percent || 0) : 0;
        const cpuCores = m.cpu ? (m.cpu.cores || 1) : 1;
        const cpuFillClass = cpuUsed > 90 ? 'fill-critical' : (cpuUsed > 75 ? 'fill-warning' : 'fill-normal');

        const ramTotal = m.memory ? (m.memory.total_bytes || 0) : 0;
        const ramAvail = m.memory ? (m.memory.available_bytes || 0) : 0;
        const ramUsedBytes = Math.max(0, ramTotal - ramAvail);
        const ramUsedPct = ramTotal > 0 ? (ramUsedBytes / ramTotal) * 100 : 0;
        const ramFillClass = ramUsedPct > 90 ? 'fill-critical' : (ramUsedPct > 80 ? 'fill-warning' : 'fill-normal');

        const displayName = n.custom_label || m.node_id;
        const hasCustomLabel = !!n.custom_label && n.custom_label !== m.node_id;

        return `
            <div class="node-item-card ${isExpanded ? 'expanded' : ''}" id="card-${global.escapeHtml(m.node_id)}">
                <div class="node-card-head" onclick="toggleNodeExpand('${global.escapeHtml(m.node_id)}', event)">
                    <div class="node-card-identity">
                        <div class="node-icon-box ${global.escapeHtml(status)}">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <rect x="2" y="2" width="20" height="8" rx="2" ry="2"></rect>
                                <rect x="2" y="14" width="20" height="8" rx="2" ry="2"></rect>
                                <line x1="6" y1="6" x2="6.01" y2="6"></line>
                                <line x1="6" y1="18" x2="6.01" y2="18"></line>
                            </svg>
                            <span class="node-status-pip ${global.escapeHtml(status)}" title="Node is ${global.escapeHtml(status)}"></span>
                        </div>

                        <div class="node-identity-meta">
                            <div class="node-name-row">
                                <h3 class="node-name">${global.escapeHtml(displayName)}</h3>
                                ${hasCustomLabel ? `<span class="node-id-chip">${global.escapeHtml(m.node_id)}</span>` : ''}
                                <span class="node-status-pill ${global.escapeHtml(status)}">
                                    <span class="status-pulse-dot"></span>
                                    ${global.escapeHtml(status)}
                                </span>
                                ${caps.version ? `<span class="node-version-badge" title="Stream Agent Version">${global.escapeHtml(caps.version)}</span>` : ''}
                            </div>
                            <div class="node-sub-row">
                                <span class="meta-label">${global.escapeHtml(m.hostname || 'localhost')}</span>
                                <span class="meta-divider">&bull;</span>
                                <span>${global.escapeHtml(m.os || 'linux')} (${global.escapeHtml(m.platform || 'linux')})</span>
                                <span class="meta-divider">&bull;</span>
                                <span class="node-ip-copy" data-ip="${global.escapeHtml(primaryIP)}" onclick="event.stopPropagation(); copyNodeIP('${global.escapeHtml(primaryIP)}', this)" title="Click to copy IP">
                                    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                    <span>${global.escapeHtml(primaryIP)}</span>
                                </span>
                                ${extraIPBadge}
                            </div>
                        </div>
                    </div>

                    <div class="node-toolbar" onclick="event.stopPropagation()">
                        <button class="node-btn-action" id="btn-edit-${global.escapeHtml(m.node_id)}" onclick="event.stopPropagation(); openNodeDetail('${global.escapeHtml(m.node_id)}')" title="Edit VPS Configuration & Quota">
                            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path>
                                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path>
                            </svg>
                            <span>Edit</span>
                        </button>

                        <button class="node-btn-action node-btn-icon btn-rescan" id="btn-rescan-${global.escapeHtml(m.node_id)}" onclick="event.stopPropagation(); triggerNodeRescan('${global.escapeHtml(m.node_id)}', this)" title="Rescan hardware metrics" aria-label="Rescan hardware metrics">
                            <svg class="rescan-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                <polyline points="23 4 23 10 17 10"></polyline>
                                <polyline points="1 20 1 14 7 14"></polyline>
                                <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
                            </svg>
                        </button>

                        ${!m.node_id.includes('(master)') ? `
                        <button class="node-btn-action node-btn-icon btn-restart" id="btn-restart-${global.escapeHtml(m.node_id)}" onclick="event.stopPropagation(); restartNodeAgent('${global.escapeHtml(m.node_id)}', this)" title="Restart stream-agent daemon via SSH" aria-label="Restart stream-agent">
                            <svg class="restart-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/>
                            </svg>
                        </button>` : ''}

                        ${!m.node_id.includes('(master)') ? `
                        <button class="node-btn-action node-btn-icon danger" onclick="event.stopPropagation(); openDeleteNodeModal('${global.escapeHtml(m.node_id)}')" title="Remove node from cluster" aria-label="Remove node from cluster">
                            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                <polyline points="3 6 5 6 21 6"></polyline>
                                <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                            </svg>
                        </button>` : ''}

                        <button class="node-expand-btn" onclick="toggleNodeExpand('${global.escapeHtml(m.node_id)}', event)" title="${isExpanded ? 'Collapse' : 'Expand'} details" aria-label="Toggle details">
                            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                                <polyline points="6 9 12 15 18 9"></polyline>
                            </svg>
                        </button>
                    </div>
                </div>

                ${status === 'offline' && !m.node_id.includes('(master)') ? `
                <div class="node-offline-recovery-bar" style="display:flex;align-items:center;justify-content:space-between;gap:8px;padding:8px 16px;background:rgba(239,68,68,0.08);border-bottom:1px solid rgba(239,68,68,0.18);font-size:12px;">
                    <span style="color:var(--status-offline);font-weight:600;display:inline-flex;align-items:center;gap:6px;">
                        <span style="width:7px;height:7px;border-radius:50%;background:var(--status-offline);display:inline-block;"></span>
                        Node is currently offline
                    </span>
                    <div style="display:flex;align-items:center;gap:8px;">
                        <button class="btn btn-xs btn-secondary" onclick="event.stopPropagation(); restartNodeAgent('${global.escapeHtml(m.node_id)}', this)" style="height:26px;font-size:11.5px;padding:0 10px;border-radius:var(--radius-sm);cursor:pointer;">
                            Restart Agent
                        </button>
                        <button class="btn btn-xs btn-primary" onclick="event.stopPropagation(); redeployNodeAgent('${global.escapeHtml(m.node_id)}', this)" style="height:26px;font-size:11.5px;padding:0 10px;border-radius:var(--radius-sm);cursor:pointer;">
                            Redeploy / Reconnect
                        </button>
                    </div>
                </div>` : ''}

                <div class="node-metrics-bar">
                    <div class="node-metric-cell">
                        <span class="cell-label">Drive Storage</span>
                        <div class="cell-value-wrap">
                            <span class="cell-value tabular-num">${global.formatBytes(freeDiskBytes)} Free</span>
                            <span class="cell-sub tabular-num">${storagePct.toFixed(0)}% used</span>
                        </div>
                        <div class="cell-progress">
                            <div class="cell-progress-fill ${storageFillClass}" style="width:${Math.min(100, Math.max(0, storagePct))}%"></div>
                        </div>
                    </div>

                    <div class="node-metric-cell">
                        <span class="cell-label">Compute (CPU)</span>
                        <div class="cell-value-wrap">
                            <span class="cell-value tabular-num">${cpuUsed.toFixed(1)}%</span>
                            <span class="cell-sub tabular-num">${cpuCores} ${cpuCores === 1 ? 'Core' : 'Cores'}</span>
                        </div>
                        <div class="cell-progress">
                            <div class="cell-progress-fill ${cpuFillClass}" style="width:${Math.min(100, Math.max(0, cpuUsed))}%"></div>
                        </div>
                    </div>

                    <div class="node-metric-cell">
                        <span class="cell-label">Memory (RAM)</span>
                        <div class="cell-value-wrap">
                            <span class="cell-value tabular-num">${global.formatBytes(ramAvail)} Free</span>
                            <span class="cell-sub tabular-num">${ramUsedPct.toFixed(0)}% used</span>
                        </div>
                        <div class="cell-progress">
                            <div class="cell-progress-fill ${ramFillClass}" style="width:${Math.min(100, Math.max(0, ramUsedPct))}%"></div>
                        </div>
                    </div>
                </div>

                <div class="node-drawer">
                    <div class="node-drawer-section">
                        <div class="drawer-section-head">
                            <span class="drawer-title">Worker Bundles &amp; Capabilities</span>
                            <div style="display:inline-flex;align-items:center;gap:6px;">
                                ${caps.version ? `<span class="node-version-badge" title="Stream Agent Binary Version">${global.escapeHtml(caps.version)}</span>` : ''}
                                <span class="drawer-agent-tag">⚡ Stream Agent :${caps.agent_port || 2052}</span>
                            </div>
                        </div>
                        <div class="drawer-tools-grid">
                            <div class="engine-badge ${hasAria ? 'ready' : 'missing'}">
                                <span class="engine-indicator"></span>
                                <span class="engine-name">aria2c</span>
                                <span class="engine-status">${hasAria ? 'Ready' : 'Missing'}</span>
                            </div>
                            <div class="engine-badge ${hasFFmpeg ? 'ready' : 'missing'}">
                                <span class="engine-indicator"></span>
                                <span class="engine-name">ffmpeg</span>
                                <span class="engine-status">${hasFFmpeg ? 'Ready' : 'Missing'}</span>
                            </div>
                            <div class="engine-badge ${hasRclone ? 'ready' : 'missing'}">
                                <span class="engine-indicator"></span>
                                <span class="engine-name">rclone</span>
                                <span class="engine-status">${hasRclone ? 'Ready' : 'Missing'}</span>
                            </div>
                        </div>
                        <div class="drawer-paths-row">
                            ${caps.version ? `<div class="path-item"><span class="path-key">Version:</span> <span class="path-val" style="font-weight:700;font-family:'JetBrains Mono',monospace;">${global.escapeHtml(caps.version)}</span></div>` : ''}
                            <div class="path-item"><span class="path-key">Binaries:</span> <span class="path-val">/opt/stream/bin</span></div>
                            ${m.media_path ? `<div class="path-item"><span class="path-key">Media Root:</span> <span class="path-val">${global.escapeHtml(m.media_path)}</span></div>` : ''}
                        </div>
                    </div>

                    <div class="node-drawer-section">
                        <div class="drawer-section-head">
                            <span class="drawer-title">Mounted Storage Partitions (${disks.length})</span>
                            <span class="tabular-num" style="font-size:11px;color:var(--text-secondary);font-weight:600;">${global.formatBytes(freeDiskBytes)} Total Available</span>
                        </div>
                        <div class="node-drives-list">
                            ${disks.length > 0 ? disks.map(renderDriveItem).join('') : '<div style="font-size:12px;color:var(--text-secondary);padding:8px;">No storage drives mounted</div>'}
                        </div>
                    </div>
                </div>
            </div>
        `;
    }

    function renderNodes(data) {
        const container = document.getElementById('nodes-full-list');
        if (!container || !data) return;

        const searchTerm = (document.getElementById('nodes-search-input')?.value || '').toLowerCase();
        const sortedNodes = global.sortNodesDeterministic(data.nodes || []);

        const onlineCount = sortedNodes.filter(n => n.status === 'online').length;
        const staleCount = sortedNodes.filter(n => n.status === 'stale').length;
        const offlineCount = sortedNodes.filter(n => n.status === 'offline' || n.status === 'connecting').length;

        const countAllEl = document.getElementById('count-filter-all');
        if (countAllEl) countAllEl.innerText = sortedNodes.length;
        const countOnlineEl = document.getElementById('count-filter-online');
        if (countOnlineEl) countOnlineEl.innerText = onlineCount;
        const countStaleEl = document.getElementById('count-filter-stale');
        if (countStaleEl) countStaleEl.innerText = staleCount;
        const countOfflineEl = document.getElementById('count-filter-offline');
        if (countOfflineEl) countOfflineEl.innerText = offlineCount;

        const filteredNodes = sortedNodes.filter(n => {
            const matchesFilter = currentFilter === 'all' ||
                (currentFilter === 'offline' ? (n.status === 'offline' || n.status === 'connecting') : n.status === currentFilter);
            const matchesSearch = !searchTerm ||
                n.metrics.node_id.toLowerCase().includes(searchTerm) ||
                n.metrics.hostname.toLowerCase().includes(searchTerm) ||
                (n.metrics.ips || []).some(ip => ip.includes(searchTerm));
            return matchesFilter && matchesSearch;
        });

        const totalCountEl = document.getElementById('nodes-total-count');
        if (totalCountEl) totalCountEl.innerText = `${filteredNodes.length} ${filteredNodes.length === 1 ? 'Node' : 'Nodes'}`;

        if (filteredNodes.length === 0) {
            container.innerHTML = `<div style="grid-column: 1 / -1; background:var(--bg-surface);border:2px dashed var(--border-light);border-radius:var(--radius-lg);padding:48px;text-align:center;">
                <h3 style="font-size:17px;font-weight:700;margin-bottom:6px;color:var(--text-primary);">No Matching Nodes Found</h3>
                <p style="color:var(--text-secondary);font-size:14px;">Try changing your filter criteria or connect a new VPS agent.</p>
            </div>`;
            return;
        }

        container.className = `nodes-container ${currentLayout}-view`;
        container.innerHTML = filteredNodes.map(n => createNodeCardHTML(n, currentLayout)).join('');
    }

    function toggleNodeExpand(nodeID, event) {
        if (event) event.stopPropagation();
        const cardEl = document.getElementById(`card-${nodeID}`);
        if (expandedNodeIDs.has(nodeID)) {
            expandedNodeIDs.delete(nodeID);
            if (cardEl) {
                cardEl.classList.remove('expanded');
                const expandBtn = cardEl.querySelector('.node-expand-btn');
                if (expandBtn) expandBtn.setAttribute('title', 'Expand details');
            }
        } else {
            expandedNodeIDs.add(nodeID);
            if (cardEl) {
                cardEl.classList.add('expanded');
                const expandBtn = cardEl.querySelector('.node-expand-btn');
                if (expandBtn) expandBtn.setAttribute('title', 'Collapse details');
            }
        }
    }

    function copyNodeIP(ip, el) {
        if (!ip) return;
        const doSuccess = () => {
            if (!el) return;
            const origHTML = el.innerHTML;
            el.innerHTML = `<span style="color:#16a34a;font-weight:700;">✓ Copied</span>`;
            setTimeout(() => {
                el.innerHTML = origHTML;
            }, 1500);
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast(`IP copied: ${ip}`, 'success');
            }
        };

        if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
            navigator.clipboard.writeText(ip).then(doSuccess).catch(() => {
                fallbackCopyTextSilent(ip, doSuccess);
            });
        } else {
            fallbackCopyTextSilent(ip, doSuccess);
        }
    }

    function fallbackCopyTextSilent(text, onSuccess) {
        try {
            const ta = document.createElement('textarea');
            ta.value = text;
            ta.style.position = 'fixed';
            ta.style.left = '-9999px';
            ta.style.top = '-9999px';
            document.body.appendChild(ta);
            ta.focus();
            ta.select();
            const ok = document.execCommand('copy');
            document.body.removeChild(ta);
            if (ok) {
                if (onSuccess) onSuccess();
                return;
            }
        } catch (_) {}
        prompt('Copy IP:', text);
    }

    async function triggerNodeRescan(nodeId, btnElement) {
        if (!btnElement) btnElement = document.getElementById(`btn-rescan-${nodeId}`);
        const originalHtml = btnElement ? btnElement.innerHTML : '';

        if (btnElement) {
            btnElement.disabled = true;
            btnElement.classList.add('scanning');
            btnElement.innerHTML = `
                <svg class="spin-fast" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                    <polyline points="23 4 23 10 17 10"></polyline>
                    <polyline points="1 20 1 14 7 14"></polyline>
                    <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
                </svg>
            `;
        }

        try {
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/rescan`, {
                method: 'POST'
            });

            await new Promise(r => setTimeout(r, 600));

            if (res.ok) {
                if (btnElement) {
                    btnElement.classList.remove('scanning');
                    btnElement.classList.add('scanned');
                    btnElement.innerHTML = `
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                            <polyline points="20 6 9 17 4 12"></polyline>
                        </svg>
                    `;
                }
                await global.fetchClusterTelemetry();
                setTimeout(() => {
                    if (btnElement) {
                        btnElement.innerHTML = originalHtml;
                        btnElement.disabled = false;
                        btnElement.classList.remove('scanned');
                    }
                }, 1000);
            } else {
                const errText = await res.text();
                global.showToast('Rescan error: ' + errText, 'error');
                if (btnElement) {
                    btnElement.innerHTML = originalHtml;
                    btnElement.disabled = false;
                    btnElement.classList.remove('scanning');
                }
            }
        } catch (err) {
            console.error('Error rescanning node:', err);
            if (btnElement) {
                btnElement.innerHTML = originalHtml;
                btnElement.disabled = false;
                btnElement.classList.remove('scanning');
            }
        }
    }

    async function restartNodeAgent(nodeId, btnElement) {
        if (!btnElement) btnElement = document.getElementById(`btn-restart-${nodeId}`);
        const origHtml = btnElement ? btnElement.innerHTML : '';
        if (btnElement) {
            btnElement.disabled = true;
            btnElement.innerHTML = `
                <svg class="spin-fast" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/>
                </svg>
            `;
        }

        try {
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/restart`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({})
            });
            const data = await res.json().catch(() => ({}));

            if (res.ok) {
                global.showToast(data.message || 'Remote agent restarted successfully', 'success');
                setTimeout(() => global.fetchClusterTelemetry(), 1500);
            } else if (data.needs_ssh) {
                global.showToast('Please provide SSH credentials in Node Settings to manage this VPS', 'warning');
                openNodeDetail(nodeId);
            } else {
                global.showToast(data.error || 'Failed to restart agent over SSH', 'error');
            }
        } catch (e) {
            global.showToast('Network error while restarting agent: ' + e.message, 'error');
        } finally {
            if (btnElement) {
                btnElement.disabled = false;
                btnElement.innerHTML = origHtml;
            }
        }
    }

    async function redeployNodeAgent(nodeId, btnElement) {
        const origHtml = btnElement ? btnElement.innerHTML : '';
        if (btnElement) {
            btnElement.disabled = true;
            btnElement.innerHTML = `
                <svg class="spin-fast" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/>
                </svg>
            `;
        }

        try {
            const coordUrl = `${window.location.protocol}//${window.location.host}`;
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/redeploy`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ coordinator_url: coordUrl })
            });
            const data = await res.json().catch(() => ({}));

            if (res.ok) {
                global.showToast(data.message || 'Remote agent reconfigured & reconnected successfully', 'success');
                setTimeout(() => global.fetchClusterTelemetry(), 1500);
            } else if (data.needs_ssh) {
                global.showToast('Please provide SSH credentials in Node Settings to redeploy this VPS', 'warning');
                openNodeDetail(nodeId);
            } else {
                global.showToast(data.error || 'Failed to redeploy agent over SSH', 'error');
            }
        } catch (e) {
            global.showToast('Network error while redeploying agent: ' + e.message, 'error');
        } finally {
            if (btnElement) {
                btnElement.disabled = false;
                btnElement.innerHTML = origHtml;
            }
        }
    }

    async function handleModalRestartAgent() {
        if (!activeSelectedNode) return;
        const nodeId = activeSelectedNode.metrics.node_id;
        const btn = document.getElementById('btn-modal-restart-agent');
        const textSpan = document.getElementById('btn-modal-restart-text');
        const sshHost = document.getElementById('modal-edit-ssh-host')?.value?.trim();
        const sshPort = parseInt(document.getElementById('modal-edit-ssh-port')?.value, 10) || 22;
        const sshUser = document.getElementById('modal-edit-ssh-user')?.value?.trim() || 'root';
        const sshPassword = document.getElementById('modal-edit-ssh-password')?.value;

        if (btn) btn.disabled = true;
        if (textSpan) textSpan.innerText = 'Restarting...';

        try {
            const payload = {
                ssh_host: sshHost,
                ssh_port: sshPort,
                ssh_user: sshUser
            };
            if (sshPassword) payload.ssh_password = sshPassword;

            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/restart`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });
            const data = await res.json().catch(() => ({}));

            if (res.ok) {
                if (textSpan) textSpan.innerText = 'Restarted ✓';
                global.showToast(data.message || 'Remote agent restarted successfully', 'success');
                setTimeout(() => global.fetchClusterTelemetry(), 1500);
                setTimeout(() => {
                    if (textSpan) textSpan.innerText = 'Restart Agent';
                    if (btn) btn.disabled = false;
                }, 2500);
            } else {
                if (textSpan) textSpan.innerText = 'Restart Failed';
                global.showToast(data.error || 'Failed to restart agent', 'error');
                if (btn) btn.disabled = false;
            }
        } catch (e) {
            if (textSpan) textSpan.innerText = 'Error';
            global.showToast('Network error: ' + e.message, 'error');
            if (btn) btn.disabled = false;
        }
    }

    async function handleModalRedeployAgent() {
        if (!activeSelectedNode) return;
        const nodeId = activeSelectedNode.metrics.node_id;
        const btn = document.getElementById('btn-modal-redeploy-agent');
        const textSpan = document.getElementById('btn-modal-redeploy-text');
        const coordUrl = document.getElementById('modal-edit-coord-url')?.value?.trim() || `${window.location.protocol}//${window.location.host}`;
        const sshHost = document.getElementById('modal-edit-ssh-host')?.value?.trim();
        const sshPort = parseInt(document.getElementById('modal-edit-ssh-port')?.value, 10) || 22;
        const sshUser = document.getElementById('modal-edit-ssh-user')?.value?.trim() || 'root';
        const sshPassword = document.getElementById('modal-edit-ssh-password')?.value;

        if (btn) btn.disabled = true;
        if (textSpan) textSpan.innerText = 'Redeploying...';

        try {
            const payload = {
                coordinator_url: coordUrl,
                ssh_host: sshHost,
                ssh_port: sshPort,
                ssh_user: sshUser
            };
            if (sshPassword) payload.ssh_password = sshPassword;

            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/redeploy`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });
            const data = await res.json().catch(() => ({}));

            if (res.ok) {
                if (textSpan) textSpan.innerText = 'Reconnected ✓';
                global.showToast(data.message || 'Remote agent reconfigured & reconnected successfully', 'success');
                setTimeout(() => global.fetchClusterTelemetry(), 1500);
                setTimeout(() => {
                    if (textSpan) textSpan.innerText = 'Redeploy / Reconnect';
                    if (btn) btn.disabled = false;
                }, 2500);
            } else {
                if (textSpan) textSpan.innerText = 'Redeploy Failed';
                global.showToast(data.error || 'Failed to redeploy agent', 'error');
                if (btn) btn.disabled = false;
            }
        } catch (e) {
            if (textSpan) textSpan.innerText = 'Error';
            global.showToast('Network error: ' + e.message, 'error');
            if (btn) btn.disabled = false;
        }
    }

    let activeDecommissionNodeId = null;
    let activeDecommissionConfig = null;

    async function openDeleteNodeModal(nodeId) {
        activeDecommissionNodeId = nodeId;
        const modal = document.getElementById('modal-delete-node');
        if (!modal) return;

        document.getElementById('del-modal-node-id').innerText = nodeId;
        document.getElementById('del-modal-host').innerText = 'Loading...';
        const badge = document.getElementById('del-modal-cred-badge');
        badge.innerText = 'Checking credentials...';
        badge.style.background = 'var(--bg-body)';
        badge.style.color = 'var(--text-secondary)';

        document.getElementById('del-modal-error-box').style.display = 'none';
        document.getElementById('del-modal-loading').style.display = 'none';
        document.getElementById('del-modal-submit-btn').disabled = false;
        document.getElementById('del-modal-manual-ssh-wrap').style.display = 'none';

        const radioStop = document.querySelector('input[name="del-node-mode"][value="stop_remote"]');
        if (radioStop) radioStop.checked = true;

        modal.style.display = 'flex';

        try {
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/config`);
            if (res.ok) {
                activeDecommissionConfig = await res.json();
                const host = activeDecommissionConfig.ssh_host || 'Unknown';
                document.getElementById('del-modal-host').innerText = host;

                if (activeDecommissionConfig.ssh_password) {
                    badge.innerText = '✓ Saved in DB';
                    badge.style.background = 'rgba(22, 163, 74, 0.12)';
                    badge.style.color = '#16a34a';
                    document.getElementById('del-modal-manual-ssh-wrap').style.display = 'none';
                } else {
                    badge.innerText = 'None Stored';
                    badge.style.background = 'rgba(234, 88, 12, 0.12)';
                    badge.style.color = '#ea580c';
                    toggleDeleteNodeMode();
                }
            } else {
                document.getElementById('del-modal-host').innerText = 'Unknown';
                badge.innerText = 'None Stored';
                toggleDeleteNodeMode();
            }
        } catch (e) {
            console.warn('Could not fetch node config:', e);
            document.getElementById('del-modal-host').innerText = 'Unknown';
            badge.innerText = 'None Stored';
            toggleDeleteNodeMode();
        }
    }

    function toggleDeleteNodeMode() {
        const selectedMode = document.querySelector('input[name="del-node-mode"]:checked')?.value;
        const manualWrap = document.getElementById('del-modal-manual-ssh-wrap');
        const submitText = document.getElementById('del-modal-submit-text');
        const submitBtn = document.getElementById('del-modal-submit-btn');

        if (selectedMode === 'cluster_only') {
            if (manualWrap) manualWrap.style.display = 'none';
            if (submitText) submitText.innerText = 'Unregister';
            if (submitBtn) submitBtn.style.background = '#475569';
        } else {
            if (submitText) submitText.innerText = 'Remove Node';
            if (submitBtn) submitBtn.style.background = '#dc2626';
            if (!activeDecommissionConfig || !activeDecommissionConfig.ssh_password) {
                if (manualWrap) {
                    manualWrap.style.display = 'block';
                    if (activeDecommissionConfig && activeDecommissionConfig.ssh_user) {
                        const userEl = document.getElementById('del-modal-ssh-user');
                        if (userEl) userEl.value = activeDecommissionConfig.ssh_user;
                    }
                }
            } else {
                if (manualWrap) manualWrap.style.display = 'none';
            }
        }
    }

    function closeDeleteNodeModal() {
        const modal = document.getElementById('modal-delete-node');
        if (modal) modal.style.display = 'none';
        activeDecommissionNodeId = null;
        activeDecommissionConfig = null;
    }

    async function executeDecommissionNode() {
        if (!activeDecommissionNodeId) return;
        const nodeId = activeDecommissionNodeId;
        const selectedMode = document.querySelector('input[name="del-node-mode"]:checked')?.value;
        const isStopRemote = selectedMode === 'stop_remote';

        const errorBox = document.getElementById('del-modal-error-box');
        const errorText = document.getElementById('del-modal-error-text');
        const loadingBox = document.getElementById('del-modal-loading');
        const loadingText = document.getElementById('del-modal-loading-text');
        const submitBtn = document.getElementById('del-modal-submit-btn');
        const cancelBtn = document.getElementById('del-modal-cancel-btn');

        if (errorBox) errorBox.style.display = 'none';
        if (loadingBox) {
            loadingBox.style.display = 'block';
            if (loadingText) {
                loadingText.innerText = isStopRemote ? 'Connecting via SSH to stop remote agent...' : 'Unregistering node from cluster...';
            }
        }
        if (submitBtn) submitBtn.disabled = true;
        if (cancelBtn) cancelBtn.disabled = true;

        const payload = {
            stop_remote: isStopRemote
        };

        if (isStopRemote) {
            const passEl = document.getElementById('del-modal-ssh-pass');
            const userEl = document.getElementById('del-modal-ssh-user');
            const manualPass = passEl ? passEl.value.trim() : '';
            const manualUser = userEl ? userEl.value.trim() : '';
            if (manualPass) {
                payload.ssh_password = manualPass;
            }
            if (manualUser) {
                payload.ssh_user = manualUser;
            }
        }

        try {
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}`, {
                method: 'DELETE',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            if (res.ok) {
                closeDeleteNodeModal();
                const cardEl = document.getElementById(`card-${nodeId}`);
                if (cardEl) {
                    cardEl.style.transition = 'all 0.3s ease';
                    cardEl.style.opacity = '0';
                    cardEl.style.transform = 'scale(0.95)';
                    setTimeout(() => cardEl.remove(), 300);
                }
                if (global.showToast) {
                    global.showToast(isStopRemote ? `Node '${nodeId}' decommissioned and stopped over SSH.` : `Node '${nodeId}' removed from cluster.`, 'success');
                }
                await global.fetchClusterTelemetry();
            } else {
                let errMsg = 'Failed to decommission node.';
                try {
                    const data = await res.json();
                    if (data.error) errMsg = data.error;
                } catch (_) {
                    errMsg = await res.text();
                }
                if (errorText) errorText.innerText = errMsg;
                if (errorBox) errorBox.style.display = 'block';
            }
        } catch (err) {
            if (errorText) errorText.innerText = `Network error: ${err.message}`;
            if (errorBox) errorBox.style.display = 'block';
        } finally {
            if (loadingBox) loadingBox.style.display = 'none';
            if (submitBtn) submitBtn.disabled = false;
            if (cancelBtn) cancelBtn.disabled = false;
        }
    }

    async function deleteNodeConfirm(nodeId) {
        openDeleteNodeModal(nodeId);
    }

    async function triggerAutoInstallTools(nodeId, btnElement, sshPassword, sshUser, tool) {
        if (!tool) tool = 'all';
        if (!btnElement) btnElement = document.getElementById(`btn-autoinstall-${nodeId}`);
        const originalHtml = btnElement ? btnElement.innerHTML : '';
        const toolName = tool === 'aria2' ? 'aria2c' : (tool === 'ffmpeg' ? 'ffmpeg' : (tool === 'rclone' ? 'rclone' : 'aria2, ffmpeg & rclone'));

        if (btnElement) {
            btnElement.disabled = true;
            btnElement.innerHTML = `
                <svg class="spin-fast" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                    <polyline points="23 4 23 10 17 10"></polyline>
                    <polyline points="1 20 1 14 7 14"></polyline>
                    <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
                </svg>
                <span>Installing ${toolName}...</span>
            `;
            btnElement.style.opacity = '0.9';
        }

        try {
            const payload = { tool: tool };
            if (sshPassword) payload.ssh_password = sshPassword;
            if (sshUser) payload.ssh_user = sshUser;

            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/install-tools`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            if (res.ok) {
                const data = await res.json();
                if (data.status === 'ssh_auth_required') {
                    const enteredPassword = prompt(`Enter SSH Password for node '${nodeId}' (${data.host || 'VPS'}):`);
                    if (!enteredPassword) {
                        if (btnElement) {
                            btnElement.innerHTML = originalHtml;
                            btnElement.disabled = false;
                            btnElement.style.opacity = '1';
                        }
                        return;
                    }
                    return triggerAutoInstallTools(nodeId, btnElement, enteredPassword, data.user || 'root', tool);
                }

                let attempts = 0;
                const pollInterval = setInterval(async () => {
                    attempts++;
                    await global.fetchClusterTelemetry();

                    if (global.cachedPoolData && global.cachedPoolData.nodes) {
                        const targetNode = global.cachedPoolData.nodes.find(n => n.metrics && n.metrics.node_id === nodeId);
                        if (targetNode && targetNode.metrics && targetNode.metrics.capabilities) {
                            const caps = targetNode.metrics.capabilities;
                            const ok = (tool === 'aria2' && caps.has_aria2c) ||
                                       (tool === 'ffmpeg' && caps.has_ffmpeg) ||
                                       (tool === 'rclone' && caps.has_rclone) ||
                                       (tool === 'all' && caps.has_ffmpeg && caps.has_aria2c && caps.has_rclone);
                            if (ok) {
                                clearInterval(pollInterval);
                                toolPollTimers.delete(pollInterval);
                                if (btnElement) {
                                    btnElement.innerHTML = `<span>✓ Installed & Active</span>`;
                                    btnElement.style.background = 'var(--status-online)';
                                }
                                return;
                            }
                        }
                    }

                    if (attempts >= 14) {
                        clearInterval(pollInterval);
                        toolPollTimers.delete(pollInterval);
                        if (btnElement) {
                            btnElement.innerHTML = originalHtml;
                            btnElement.disabled = false;
                            btnElement.style.opacity = '1';
                        }
                    }
                }, 3000);
                toolPollTimers.add(pollInterval);
            } else {
                alert('Failed to trigger installation: ' + (await res.text()));
                if (btnElement) {
                    btnElement.innerHTML = originalHtml;
                    btnElement.disabled = false;
                    btnElement.style.opacity = '1';
                }
            }
        } catch (err) {
            console.error('Error auto-installing tools:', err);
            if (btnElement) {
                btnElement.innerHTML = originalHtml;
                btnElement.disabled = false;
                btnElement.style.opacity = '1';
            }
        }
    }

    async function triggerUninstallTools(nodeId, btnElement, tool, sshPassword, sshUser) {
        if (!tool) tool = 'all';
        const toolName = tool === 'aria2' ? 'aria2c' : (tool === 'ffmpeg' ? 'ffmpeg' : (tool === 'rclone' ? 'rclone' : 'aria2c, ffmpeg & rclone'));

        if (!sshPassword) {
            if (!confirm(`Are you sure you want to uninstall ${toolName} from node '${nodeId}'?\n\nThis will remove the worker tool(s) so this node won't be used for processing jobs.`)) {
                return;
            }
        }

        const originalHtml = btnElement ? btnElement.innerHTML : '';

        if (btnElement) {
            btnElement.disabled = true;
            btnElement.innerHTML = `
                <svg class="spin-fast" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                    <polyline points="23 4 23 10 17 10"></polyline>
                    <polyline points="1 20 1 14 7 14"></polyline>
                    <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
                </svg>
                <span>Removing ${toolName}...</span>
            `;
            btnElement.style.opacity = '0.9';
        }

        try {
            const payload = { tool: tool };
            if (sshPassword) payload.ssh_password = sshPassword;
            if (sshUser) payload.ssh_user = sshUser;

            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/uninstall-tools`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            if (res.ok) {
                const data = await res.json();
                if (data.status === 'ssh_auth_required') {
                    const enteredPassword = prompt(`Enter SSH Password for node '${nodeId}' (${data.host || 'VPS'}):`);
                    if (!enteredPassword) {
                        if (btnElement) {
                            btnElement.innerHTML = originalHtml;
                            btnElement.disabled = false;
                            btnElement.style.opacity = '1';
                        }
                        return;
                    }
                    return triggerUninstallTools(nodeId, btnElement, tool, enteredPassword, data.user || 'root');
                }

                let attempts = 0;
                const pollInterval = setInterval(async () => {
                    attempts++;
                    await global.fetchClusterTelemetry();

                    if (global.cachedPoolData && global.cachedPoolData.nodes) {
                        const targetNode = global.cachedPoolData.nodes.find(n => n.metrics && n.metrics.node_id === nodeId);
                        if (targetNode && targetNode.metrics && targetNode.metrics.capabilities) {
                            const caps = targetNode.metrics.capabilities;
                            const removed = (tool === 'aria2' && !caps.has_aria2c) ||
                                            (tool === 'ffmpeg' && !caps.has_ffmpeg) ||
                                            (tool === 'rclone' && !caps.has_rclone) ||
                                            (tool === 'all' && !caps.has_ffmpeg && !caps.has_aria2c && !caps.has_rclone);
                            if (removed) {
                                clearInterval(pollInterval);
                                toolPollTimers.delete(pollInterval);
                                if (btnElement) {
                                    btnElement.innerHTML = `<span>✓ Removed</span>`;
                                }
                                return;
                            }
                        }
                    }

                    if (attempts >= 14) {
                        clearInterval(pollInterval);
                        toolPollTimers.delete(pollInterval);
                        if (btnElement) {
                            btnElement.innerHTML = originalHtml;
                            btnElement.disabled = false;
                            btnElement.style.opacity = '1';
                        }
                    }
                }, 3000);
                toolPollTimers.add(pollInterval);
            } else {
                alert('Failed to trigger uninstall: ' + (await res.text()));
                if (btnElement) {
                    btnElement.innerHTML = originalHtml;
                    btnElement.disabled = false;
                    btnElement.style.opacity = '1';
                }
            }
        } catch (err) {
            console.error('Error uninstalling tools:', err);
            if (btnElement) {
                btnElement.innerHTML = originalHtml;
                btnElement.disabled = false;
                btnElement.style.opacity = '1';
            }
        }
    }

    async function openNodeDetail(nodeID) {
        if (!global.cachedPoolData || !global.cachedPoolData.nodes) return;
        const node = global.cachedPoolData.nodes.find(n => n.metrics.node_id === nodeID);
        if (!node) return;

        activeSelectedNode = node;
        const m = node.metrics;

        const titleEl = document.getElementById('modal-node-title');
        const subEl = document.getElementById('modal-node-sub');
        const cpuEl = document.getElementById('modal-cpu-info');
        const ramEl = document.getElementById('modal-ram-info');

        if (titleEl) titleEl.innerText = `Edit Node: ${m.node_id}`;
        const cleanIPs = global.cleanNodeIPs(m.ips);
        const primaryIP = cleanIPs[0] || (m.ips && m.ips[0]) || '127.0.0.1';
        const extraCount = cleanIPs.length - 1;
        const extraBadge = extraCount > 0 ? ` <span class="badge-extra-ips" title="Other interfaces: ${global.escapeHtml(cleanIPs.slice(1).join(', '))}">+${extraCount} more</span>` : '';
        if (subEl) {
            subEl.innerHTML = `<span style="display:inline-flex;align-items:center;gap:6px;flex-wrap:wrap;">` +
                `<span>${global.escapeHtml(m.hostname)} (${global.escapeHtml(m.os)} &bull; ${global.escapeHtml(m.platform || 'linux')})</span>` +
                `<span style="color:var(--text-muted);">&bull;</span>` +
                `<span>IP: <code class="mono-code">${global.escapeHtml(primaryIP)}</code>${extraBadge}</span>` +
                (m.capabilities?.version ? `<span style="color:var(--text-muted);">&bull;</span><span class="node-version-badge" style="font-size:10px;padding:1px 6px;">${global.escapeHtml(m.capabilities.version)}</span>` : '') +
                `</span>`;
        }
        if (cpuEl) cpuEl.innerText = `${m.cpu?.cores || 0} Cores • ${m.cpu?.model_name || 'Generic CPU'} • Load: ${(m.cpu?.used_percent || 0).toFixed(1)}%`;
        if (ramEl) ramEl.innerText = `${global.formatBytes(m.memory?.available_bytes || 0)} Free of ${global.formatBytes(m.memory?.total_bytes || 0)}`;

        const input = document.getElementById('modal-quota-input');
        const display = document.getElementById('modal-quota-display');
        const coordUrlInput = document.getElementById('modal-edit-coord-url');

        try {
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeID)}/config`);
            if (res.ok) {
                const cfg = await res.json();
                const allocGB = cfg.allocated_max_bytes ? Math.round(cfg.allocated_max_bytes / (1024 * 1024 * 1024)) : 0;
                if (input) input.value = allocGB > 0 ? allocGB : '';
                if (display) display.innerText = allocGB === 0 ? 'Unlimited (Full Drive)' : `Capped at ${allocGB} GB`;

                if (coordUrlInput) {
                    coordUrlInput.value = cfg.coordinator_url || `${window.location.protocol}//${window.location.host}`;
                    checkEditCoordUrlSafety(coordUrlInput.value);
                }

                const corsEl = document.getElementById('modal-edit-cors-origins');
                if (corsEl) corsEl.value = cfg.cors_allowed_origins || '*';

                const labelEl = document.getElementById('modal-edit-label');
                if (labelEl) labelEl.value = cfg.custom_label || '';

                const enabledEl = document.getElementById('modal-edit-enabled');
                if (enabledEl) enabledEl.checked = (cfg.enabled !== false);

                const sshHostEl = document.getElementById('modal-edit-ssh-host');
                if (sshHostEl) sshHostEl.value = cfg.ssh_host || primaryIP || '';

                const sshPortEl = document.getElementById('modal-edit-ssh-port');
                if (sshPortEl) sshPortEl.value = cfg.ssh_port || 22;

                const sshUserEl = document.getElementById('modal-edit-ssh-user');
                if (sshUserEl) sshUserEl.value = cfg.ssh_user || 'root';

                const sshPassEl = document.getElementById('modal-edit-ssh-password');
                if (sshPassEl) sshPassEl.value = '';

                const sudoEl = document.getElementById('modal-edit-ssh-sudo');
                if (sudoEl) sudoEl.checked = !!cfg.ssh_use_sudo;
            }
        } catch (e) {
            console.warn('Failed to fetch node config:', e);
        }

        const modal = document.getElementById('node-detail-modal');
        if (modal) {
            modal.classList.add('open', 'active');
            global.setModalOpen(true);
            document.getElementById('modal-edit-label')?.focus();
        }
    }

    function closeNodeDetailModal() {
        const modal = document.getElementById('node-detail-modal');
        if (modal) {
            modal.classList.remove('open', 'active');
        }
        activeSelectedNode = null;
        global.setModalOpen(false);
    }

    function onQuotaSliderChange(val) {
        onQuotaNumberChange(val);
    }

    function onQuotaNumberChange(val) {
        const numVal = Math.max(0, parseInt(val) || 0);
        const display = document.getElementById('modal-quota-display');
        if (display) {
            display.innerText = numVal === 0 ? 'Unlimited (Full Drive)' : `Capped at ${numVal} GB`;
        }
    }

    async function handleSaveNodeConfig(e) {
        if (e) e.preventDefault();
        if (!activeSelectedNode) return;

        const nodeID = activeSelectedNode.metrics.node_id;
        const submitBtn = document.getElementById('btn-save-node-config');
        if (submitBtn) submitBtn.disabled = true;

        const customLabel = document.getElementById('modal-edit-label')?.value?.trim() || '';
        const enabled = document.getElementById('modal-edit-enabled')?.checked ?? true;
        const allocGB = parseInt(document.getElementById('modal-quota-input')?.value) || 0;
        const maxBytes = allocGB * 1024 * 1024 * 1024;
        const coordUrl = document.getElementById('modal-edit-coord-url')?.value?.trim() || '';
        const corsOrigins = document.getElementById('modal-edit-cors-origins')?.value?.trim() || '*';

        const sshHost = document.getElementById('modal-edit-ssh-host')?.value?.trim() || '';
        const sshPort = parseInt(document.getElementById('modal-edit-ssh-port')?.value) || 22;
        const sshUser = document.getElementById('modal-edit-ssh-user')?.value?.trim() || 'root';
        const sshPassword = document.getElementById('modal-edit-ssh-password')?.value || '';
        const sshUseSudo = document.getElementById('modal-edit-ssh-sudo')?.checked || false;

        const payload = {
            custom_label: customLabel,
            enabled: enabled,
            allocated_max_bytes: maxBytes,
            coordinator_url: coordUrl,
            cors_allowed_origins: corsOrigins,
            ssh_host: sshHost,
            ssh_port: sshPort,
            ssh_user: sshUser,
            ssh_use_sudo: sshUseSudo
        };
        if (sshPassword) {
            payload.ssh_password = sshPassword;
        }

        try {
            const res = await fetch(`/api/nodes/${encodeURIComponent(nodeID)}/config`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            if (res.ok) {
                closeNodeDetailModal();
                global.fetchClusterTelemetry();
            } else {
                const errText = await res.text();
                alert(`Failed to save node settings: ${errText}`);
            }
        } catch (err) {
            console.error('Error saving node config:', err);
            alert(`Network error saving settings: ${err.message}`);
        } finally {
            if (submitBtn) submitBtn.disabled = false;
        }
    }

    function renderCoordSafetyBadge(rawUrl, badge, warning) {
        if (!badge) return;

        const url = (rawUrl || '').trim();
        const isPrivate = /localhost|127\.0\.0\.1|^https?:\/\/(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[0-1])\.)/i.test(url);

        if (isPrivate) {
            badge.innerText = 'Private / LAN';
            badge.style.color = 'var(--orange-dark)';
            badge.style.background = 'var(--bg-orange-soft)';
            badge.style.border = '1px solid var(--border-orange)';
            badge.style.padding = '2px 8px';
            badge.style.borderRadius = '4px';
            if (warning) warning.style.display = 'block';
        } else if (url.length > 4) {
            badge.innerText = 'Public / Tunnel';
            badge.style.color = '#059669';
            badge.style.background = '#ecfdf5';
            badge.style.border = '1px solid rgba(16,185,129,0.3)';
            badge.style.padding = '2px 8px';
            badge.style.borderRadius = '4px';
            if (warning) warning.style.display = 'none';
        } else {
            badge.innerText = '';
            if (warning) warning.style.display = 'none';
        }
    }

    function checkCoordUrlSafety(rawUrl) {
        const badge = document.getElementById('coord-reachability-badge');
        const warning = document.getElementById('coord-warning-box');
        renderCoordSafetyBadge(rawUrl, badge, warning);
    }

    function checkEditCoordUrlSafety(rawUrl) {
        const badge = document.getElementById('modal-edit-coord-badge');
        const warning = document.getElementById('modal-edit-coord-warning');
        renderCoordSafetyBadge(rawUrl, badge, warning);
    }

    function openAddVPSModal() {
        const modal = document.getElementById('modal-add-vps');
        if (modal) {
            modal.classList.add('open', 'active');
            global.setModalOpen(true);
            document.getElementById('provision-log-box').style.display = 'none';
            document.getElementById('btn-submit-provision').disabled = false;
            document.getElementById('btn-provision-text').innerText = 'Provision & Join Cluster';

            const coordInput = document.getElementById('vps-input-coordinator-url');
            if (coordInput) {
                if (!coordInput.value) {
                    coordInput.value = `${window.location.protocol}//${window.location.host}`;
                }
                checkCoordUrlSafety(coordInput.value);
            }

            const portToggle = document.getElementById('toggle-custom-port');
            if (portToggle) portToggle.checked = false;
            const portWrap = document.getElementById('wrap-custom-port');
            if (portWrap) portWrap.style.display = 'none';
            const portInput = document.getElementById('vps-input-port');
            if (portInput) portInput.value = '22';

            const sudoToggle = document.getElementById('toggle-use-sudo');
            if (sudoToggle) sudoToggle.checked = false;

            document.getElementById('vps-input-host').focus();
        }
    }

    function closeAddVPSModal() {
        const modal = document.getElementById('modal-add-vps');
        if (modal) {
            modal.classList.remove('open', 'active');
        }
        global.setModalOpen(false);
    }

    function handleCustomPortToggle() {
        const toggle = document.getElementById('toggle-custom-port');
        const wrap = document.getElementById('wrap-custom-port');
        if (wrap && toggle) {
            wrap.style.display = toggle.checked ? 'block' : 'none';
            if (toggle.checked) {
                document.getElementById('vps-input-port')?.focus();
            }
        }
    }

    function handleUserInputChange(val) {
        const sudoToggle = document.getElementById('toggle-use-sudo');
        if (sudoToggle) {
            if (val.trim() && val.trim().toLowerCase() !== 'root') {
                sudoToggle.checked = true;
            } else {
                sudoToggle.checked = false;
            }
        }
    }

    async function waitForProvisionedNode(nodeName, host, { timeoutMs = 180000, intervalMs = 2000 } = {}) {
        const deadline = Date.now() + timeoutMs;
        const wantName = (nodeName || '').trim();
        while (Date.now() < deadline) {
            try {
                const nodesRes = await fetch('/api/nodes');
                if (nodesRes.ok) {
                    const nodesData = await nodesRes.json();
                    const list = Array.isArray(nodesData) ? nodesData : (nodesData.nodes || []);
                    const found = list.some(n => {
                        const m = n.metrics || {};
                        const id = m.node_id || '';
                        const ips = m.ips || [];
                        const hostname = m.hostname || '';
                        const match = (wantName && id === wantName) ||
                                      (host && ips.includes(host)) ||
                                      (host && hostname.includes(host));
                        return match && n.status === 'online';
                    });
                    if (found) return true;
                }
            } catch (_) {}
            await new Promise(r => setTimeout(r, intervalMs));
        }
        return false;
    }

    async function handleProvisionSubmit(e) {
        e.preventDefault();
        const host = document.getElementById('vps-input-host').value.trim();
        const user = document.getElementById('vps-input-user').value.trim() || 'root';
        const password = document.getElementById('vps-input-password').value;
        const name = document.getElementById('vps-input-name').value.trim() || `vps-${host.replace(/\./g, '-')}`;

        const isCustomPort = document.getElementById('toggle-custom-port')?.checked;
        const port = isCustomPort ? (parseInt(document.getElementById('vps-input-port')?.value) || 22) : 22;
        const useSudo = document.getElementById('toggle-use-sudo')?.checked || false;

        const coordUrlInput = document.getElementById('vps-input-coordinator-url')?.value?.trim();
        const coordinatorUrl = coordUrlInput || `${window.location.protocol}//${window.location.host}`;

        const logBox = document.getElementById('provision-log-box');
        const logContent = document.getElementById('provision-log-content');
        const submitBtn = document.getElementById('btn-submit-provision');
        const submitText = document.getElementById('btn-provision-text');

        logBox.style.display = 'block';
        submitBtn.disabled = true;
        submitText.innerText = 'Provisioning in progress...';
        logContent.innerHTML = `
            <div style="color:var(--orange-primary);">SSH to ${global.escapeHtml(host)}:${port} as ${global.escapeHtml(user)}...</div>
            <div style="color:var(--text-secondary);margin-top:4px;">Deploying worker bundle &amp; configuring service. Keep this window open.</div>
        `;

        const payload = {
            host: host,
            port: port,
            user: user,
            password: password,
            use_sudo: useSudo,
            node_name: name,
            advertise_addr: host,
            coordinator_url: coordinatorUrl
        };

        const finishSuccess = (extraHtml = '') => {
            logContent.innerHTML = `<div style="color:var(--status-online);font-weight:bold;">VPS <strong>${global.escapeHtml(name)}</strong> joined the cluster.</div>${extraHtml}`;
            submitText.innerText = 'Completed!';
            setTimeout(() => {
                closeAddVPSModal();
                global.fetchClusterTelemetry();
            }, 1500);
        };

        const joinedPromise = waitForProvisionedNode(name, host);

        try {
            const res = await fetch('/api/nodes/provision', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            const raw = await res.text();
            let data = {};
            try { data = raw ? JSON.parse(raw) : {}; } catch (_) {
                data = { error: raw || res.statusText || 'Provisioning failed' };
            }
            if (res.ok) {
                const logs = (data.logs || []).map(l => {
                    if (l.includes('⚠️')) {
                        return `<div style="color:var(--orange-dark);background:var(--bg-orange-soft);border:1px solid var(--border-orange);padding:4px 8px;border-radius:4px;margin:4px 0;font-weight:600;">${global.escapeHtml(l)}</div>`;
                    }
                    return `<div style="color:var(--status-online);">${global.escapeHtml(l)}</div>`;
                }).join('');

                logContent.innerHTML = `${logs}
                    <div style="color:var(--orange-primary);font-weight:bold;margin-top:10px;">
                        ⚡ SSH deployment finished! Awaiting live agent telemetry heartbeat...
                    </div>
                `;
                submitText.innerText = 'Awaiting Heartbeat...';
                global.fetchClusterTelemetry();

                const joined = await waitForProvisionedNode(name, host, { timeoutMs: 30000 });
                if (joined) {
                    finishSuccess(`<div style="color:var(--status-online);margin-top:6px;font-weight:600;">❤️ First live heartbeat verified! Node is online.</div>`);
                    return;
                }

                logContent.innerHTML += `
                    <div style="color:var(--status-offline);font-weight:bold;margin-top:10px;">
                        ⚠️ Heartbeat Timeout: Agent is not reaching Coordinator.
                    </div>
                    <div style="color:var(--text-secondary);font-size:12px;margin-top:6px;line-height:1.5;">
                        The systemd service was installed on <strong>${global.escapeHtml(host)}</strong>, but telemetry has not arrived at <code>${global.escapeHtml(coordinatorUrl)}</code>.<br/>
                        <strong>Probable causes:</strong>
                        <ul style="margin:4px 0 0 16px;">
                            <li>Coordinator target is a private LAN/loopback address (<code>${global.escapeHtml(coordinatorUrl)}</code>) unreachable from the public internet.</li>
                            <li>Firewall is blocking port 1212 or 9090 on the coordinator machine.</li>
                            <li>Check logs on remote VPS: <code>journalctl -u stream-agent -n 30 --no-pager</code></li>
                        </ul>
                    </div>
                `;
                submitBtn.disabled = false;
                submitText.innerText = 'Retry Provisioning';
                global.fetchClusterTelemetry();
                return;
            }

            if (await waitForProvisionedNode(name, host, { timeoutMs: 45000 })) {
                finishSuccess(`<div style="color:var(--text-secondary);margin-top:6px;">Joined after a slow provision response.</div>`);
                return;
            }
            const errLogs = (data.result?.logs || []).map(l => `<div>${global.escapeHtml(l)}</div>`).join('');
            logContent.innerHTML = `${errLogs}<div style="color:var(--status-offline);font-weight:bold;margin-top:8px;">Error: ${global.escapeHtml(data.error || 'Provisioning failed')}</div>`;
            submitBtn.disabled = false;
            submitText.innerText = 'Retry Provisioning';
        } catch (err) {
            logContent.innerHTML += `<div style="color:var(--text-secondary);margin-top:6px;">Browser lost the long HTTP response (${global.escapeHtml(err.message)}). Checking if the node joined...</div>`;
            const joined = await joinedPromise;
            if (joined || await waitForProvisionedNode(name, host, { timeoutMs: 180000 })) {
                finishSuccess(`<div style="color:var(--text-secondary);margin-top:6px;">Node is online (provision finished on server).</div>`);
                return;
            }
            logContent.innerHTML += `<div style="color:var(--status-offline);margin-top:6px;">Could not confirm join. Check Nodes page or retry.</div>`;
            submitBtn.disabled = false;
            submitText.innerText = 'Retry Provisioning';
        }
    }

    function bindNodeControlsOnce() {
        if (listenersBound) return;
        listenersBound = true;

        document.getElementById('btn-layout-grid')?.addEventListener('click', () => {
            currentLayout = 'grid';
            document.getElementById('btn-layout-grid')?.classList.add('active');
            document.getElementById('btn-layout-list')?.classList.remove('active');
            if (global.cachedPoolData) renderNodes(global.cachedPoolData);
        });

        document.getElementById('btn-layout-list')?.addEventListener('click', () => {
            currentLayout = 'list';
            document.getElementById('btn-layout-list')?.classList.add('active');
            document.getElementById('btn-layout-grid')?.classList.remove('active');
            if (global.cachedPoolData) renderNodes(global.cachedPoolData);
        });

        document.querySelectorAll('#view-nodes .filter-btn').forEach(btn => {
            btn.addEventListener('click', () => {
                document.querySelectorAll('#view-nodes .filter-btn').forEach(b => b.classList.remove('active'));
                btn.classList.add('active');
                currentFilter = btn.getAttribute('data-filter') || 'all';
                if (global.cachedPoolData) renderNodes(global.cachedPoolData);
            });
        });

        document.getElementById('nodes-search-input')?.addEventListener('input', () => {
            if (global.cachedPoolData) renderNodes(global.cachedPoolData);
        });
    }

    function init() {
        destroy();
        bindNodeControlsOnce();
        if (global.cachedPoolData) renderNodes(global.cachedPoolData);
        global.fetchClusterTelemetry();
        pollTimer = setInterval(global.fetchClusterTelemetry, 2500);
    }

    function destroy() {
        if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
        toolPollTimers.forEach(t => clearInterval(t));
        toolPollTimers.clear();
    }

    global.renderNodes = renderNodes;
    global.toggleNodeExpand = toggleNodeExpand;
    global.copyNodeIP = copyNodeIP;
    global.renderDriveItem = renderDriveItem;
    global.createNodeCardHTML = createNodeCardHTML;
    global.triggerNodeRescan = triggerNodeRescan;
    global.deleteNodeConfirm = deleteNodeConfirm;
    global.triggerAutoInstallTools = triggerAutoInstallTools;
    global.triggerUninstallTools = triggerUninstallTools;
    global.openNodeDetail = openNodeDetail;
    global.closeNodeDetailModal = closeNodeDetailModal;
    global.onQuotaSliderChange = onQuotaSliderChange;
    global.onQuotaNumberChange = onQuotaNumberChange;
    global.handleSaveNodeConfig = handleSaveNodeConfig;
    global.renderCoordSafetyBadge = renderCoordSafetyBadge;
    global.checkCoordUrlSafety = checkCoordUrlSafety;
    global.checkEditCoordUrlSafety = checkEditCoordUrlSafety;
    global.openAddVPSModal = openAddVPSModal;
    global.closeAddVPSModal = closeAddVPSModal;
    global.handleCustomPortToggle = handleCustomPortToggle;
    global.handleUserInputChange = handleUserInputChange;
    global.waitForProvisionedNode = waitForProvisionedNode;
    global.handleProvisionSubmit = handleProvisionSubmit;
    global.openDeleteNodeModal = openDeleteNodeModal;
    global.closeDeleteNodeModal = closeDeleteNodeModal;
    global.executeDecommissionNode = executeDecommissionNode;
    global.toggleDeleteNodeMode = toggleDeleteNodeMode;
    global.restartNodeAgent = restartNodeAgent;
    global.redeployNodeAgent = redeployNodeAgent;
    global.handleModalRestartAgent = handleModalRestartAgent;
    global.handleModalRedeployAgent = handleModalRedeployAgent;

    if (global.App && global.App.pages) {
        global.App.pages.nodes = { init, destroy, render: renderNodes };
    }
})(window);
