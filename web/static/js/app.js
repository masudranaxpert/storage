let currentView = 'dashboard';
let currentFilter = 'all';
let currentLayout = 'grid';
let cachedPoolData = null;
let activeSelectedNode = null;

function formatBytes(bytes) {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

const pageRoutes = {
    'dashboard': '/',
    'nodes': '/nodes',
    'storage': '/storage',
    'tiers': '/tiers',
    'media': '/media',
    'database': '/database',
    'settings': '/settings',
    'docs': '/docs'
};

function toggleMobileSidebar(forceState) {
    const sidebar = document.querySelector('.app-sidebar');
    const backdrop = document.getElementById('sidebar-backdrop');
    if (!sidebar) return;

    const isOpen = sidebar.classList.contains('open');
    const newState = forceState !== undefined ? forceState : !isOpen;

    if (newState) {
        sidebar.classList.add('open');
        if (backdrop) backdrop.classList.add('active');
    } else {
        sidebar.classList.remove('open');
        if (backdrop) backdrop.classList.remove('active');
    }
}

function navigateTo(pageId, pushHistory = true) {
    currentView = pageId;
    toggleMobileSidebar(false);
    
    const targetUrl = pageRoutes[pageId] || '/';
    if (pushHistory && window.location.pathname !== targetUrl) {
        window.history.pushState({ page: pageId }, '', targetUrl);
    }

    document.querySelectorAll('.nav-link').forEach(link => {
        if (link.getAttribute('data-page') === pageId) {
            link.classList.add('active');
        } else {
            link.classList.remove('active');
        }
    });

    document.querySelectorAll('.page-view').forEach(view => {
        if (view.id === 'view-' + pageId) {
            view.classList.add('active');
        } else {
            view.classList.remove('active');
        }
    });

    const titles = {
        'dashboard': 'Cluster Dashboard & Pool Overview',
        'nodes': 'VPS Nodes & Compute Hardware',
        'storage': 'Pooled Drives & Filesystem Storage',
        'tiers': 'Storage Tiers & Processing Workers',
        'media': 'Media Library & Video Ingest Manager',
        'database': 'BadgerDB Studio — Tables & CRUD Console',
        'settings': 'BadgerDB & Cluster Settings',
        'docs': 'Interactive Swagger API & OpenAPI 3.0 Explorer'
    };
    const titleEl = document.getElementById('current-page-title');
    if (titleEl) titleEl.innerText = titles[pageId] || 'Cluster Manager';

    if (cachedPoolData) renderAllViews(cachedPoolData);
    if (pageId === 'media') fetchMediaList();
    if (pageId === 'tiers') fetchTiersView();
    if (pageId === 'storage' && typeof fetchStorageFolders === 'function' && currentStorageTab === 'folders') fetchStorageFolders();
    if (pageId === 'database' && window.DBStudio) DBStudio.refresh();
}

async function fetchClusterTelemetry() {
    try {
        const res = await fetch('/api/pool');
        if (!res.ok) throw new Error('API request failed');
        const data = await res.json();
        cachedPoolData = data;
        renderAllViews(data);
    } catch (err) {
        console.error('Failed to load cluster telemetry:', err);
    }
}

function fetchPoolMetrics() {
    return fetchClusterTelemetry();
}
window.fetchPoolMetrics = fetchClusterTelemetry;

function sortNodesDeterministic(nodes) {
    return (nodes || []).slice()
        .filter(n => {
            const id = (n.metrics?.node_id || '').toLowerCase();
            if (!id || id === 'local-master' || id.includes('(master)')) return false;
            const os = String(n.metrics?.os || n.metrics?.platform || '').toLowerCase();
            if (os.includes('windows') || os.includes('darwin') || os.includes('macos')) return false;
            return true;
        })
        .sort((a, b) => (a.metrics?.node_id || '').localeCompare(b.metrics?.node_id || ''));
}

function renderAllViews(data) {
    renderDashboard(data);
    renderNodes(data);
    renderStorage(data);
    renderSettings(data);
}

function safeSetText(id, text) {
    const el = document.getElementById(id);
    if (el) el.innerText = text;
}

const RING_CIRCUMFERENCE = 207.34; // 2 * PI * 33 (ring radius in SVG viewBox)

function setRing(id, pct) {
    const ring = document.getElementById(id);
    if (!ring) return;
    const clamped = Math.max(0, Math.min(100, pct || 0));
    ring.style.strokeDashoffset = `${(RING_CIRCUMFERENCE * (1 - clamped / 100)).toFixed(2)}`;
}

function setRingPct(id, pct) {
    safeSetText(id, `${Math.max(0, Math.min(100, Math.round(pct || 0)))}%`);
}

function renderDashboard(data) {
    // ---------- Hero Band ----------
    const storageUsed = data.storage_used_percent || 0;
    const totalNodes = data.total_nodes || 0;
    const activeNodes = data.active_nodes || 0;
    const offlineNodes = data.offline_nodes || 0;

    safeSetText('dash-hero-storage', formatBytes(data.total_storage_bytes));
    safeSetText('dash-hero-free', formatBytes(data.free_storage_bytes));
    safeSetText('dash-hero-nodes', `${activeNodes}/${totalNodes || 0}`);
    safeSetText('dash-hero-drives', data.total_drives || 0);
    safeSetText('dash-hero-cores', data.total_cpu_cores || 0);

    const heroDot = document.getElementById('dash-hero-dot');
    const heroStatus = document.getElementById('dash-hero-status');
    const heroSub = document.getElementById('dash-hero-sub');
    if (heroDot && heroStatus && heroSub) {
        if (totalNodes === 0) {
            heroDot.className = 'hero-status-dot critical';
            heroStatus.innerText = 'No Nodes Connected';
            heroSub.innerText = 'Add a VPS node to build your storage mesh';
        } else if (offlineNodes > 0) {
            heroDot.className = 'hero-status-dot degraded';
            heroStatus.innerText = 'Cluster Degraded';
            heroSub.innerText = `${offlineNodes} node${offlineNodes === 1 ? '' : 's'} offline — needs attention`;
        } else {
            heroDot.className = 'hero-status-dot healthy';
            heroStatus.innerText = 'Cluster Healthy';
            heroSub.innerText = `All ${totalNodes} node${totalNodes === 1 ? '' : 's'} operational in the mesh`;
        }
    }

    // ---------- KPI Ring: Storage ----------
    safeSetText('dash-stat-storage', formatBytes(data.total_storage_bytes));
    safeSetText('dash-stat-storage-free-text', `${formatBytes(data.free_storage_bytes)} Free`);
    safeSetText('dash-stat-storage-sub', `${storageUsed.toFixed(1)}% Used`);
    setRing('dash-ring-storage', storageUsed);
    setRingPct('dash-ring-storage-pct', storageUsed);

    // ---------- KPI Ring: RAM ----------
    safeSetText('dash-stat-ram', formatBytes(data.total_ram_bytes));
    safeSetText('dash-stat-ram-free-text', `${formatBytes(data.available_ram_bytes)} Free Buffer`);
    const usedRam = (data.total_ram_bytes || 0) - (data.available_ram_bytes || 0);
    const ramPct = data.total_ram_bytes > 0 ? (usedRam / data.total_ram_bytes) * 100 : 0;
    setRing('dash-ring-ram', ramPct);
    setRingPct('dash-ring-ram-pct', ramPct);

    // ---------- KPI Ring: CPU ----------
    safeSetText('dash-stat-cpu', data.total_cpu_cores || 0);
    const cpuLoad = data.avg_cpu_load_percent || 0;
    safeSetText('dash-stat-cpu-sub', `${cpuLoad.toFixed(1)}% Avg Load`);
    setRing('dash-ring-cpu', cpuLoad);
    setRingPct('dash-ring-cpu-pct', cpuLoad);

    // ---------- KPI Ring: Nodes ----------
    safeSetText('dash-stat-nodes', totalNodes);
    safeSetText('dash-stat-nodes-sub', `${activeNodes} Active \u2022 ${offlineNodes} Offline`);
    const activePct = totalNodes > 0 ? (activeNodes / totalNodes) * 100 : 100;
    setRing('dash-ring-nodes', activePct);
    setRingPct('dash-ring-nodes-pct', activePct);

    // ---------- Cluster Fleet Strip ----------
    const fleetStrip = document.getElementById('dash-fleet-strip');
    if (fleetStrip) {
        const nodes = sortNodesDeterministic(data.nodes || []);
        if (nodes.length === 0) {
            fleetStrip.innerHTML = `
                <div class="fleet-node-card" style="cursor:default;align-items:center;text-align:center;padding:28px;">
                    <span style="font-weight:700;font-size:14px;color:var(--text-primary);">No nodes in the mesh yet</span>
                    <span style="font-size:12.5px;color:var(--text-secondary);">Provision your first VPS to start pooling storage &amp; compute.</span>
                </div>`;
        } else {
            fleetStrip.innerHTML = nodes.map(n => {
                const m = n.metrics || {};
                const status = (n.status || 'offline').toLowerCase();
                const cpu = Math.round(m.cpu?.used_percent || 0);
                const ram = Math.round(m.memory?.used_percent || 0);
                const cpuClass = cpu >= 85 ? 'hot' : (cpu >= 60 ? 'warm' : '');
                const ramClass = ram >= 85 ? 'hot' : (ram >= 60 ? 'warm' : '');
                const driveCount = (m.disks || []).length;
                const os = (m.os || 'os').toLowerCase().slice(0, 7);
                const version = m.capabilities?.version || '';
                const freeTotal = (m.disks || []).reduce((acc, d) => acc + (d.free_bytes || 0), 0);
                return `
                <div class="fleet-node-card" onclick="navigateTo('nodes')" title="Manage ${escapeHtml(m.node_id)}">
                    <div class="fleet-node-top">
                        <span class="fleet-status-dot ${status}"></span>
                        <span class="fleet-node-name">${escapeHtml(m.node_id)}</span>
                        <span class="fleet-os-chip">${escapeHtml(os)}</span>
                    </div>
                    <div class="fleet-node-meters">
                        <div class="fleet-meter">
                            <div class="fleet-meter-head"><span>CPU</span><strong>${cpu}%</strong></div>
                            <div class="fleet-meter-track"><div class="fleet-meter-fill ${cpuClass}" style="width:${Math.min(100, cpu)}%;"></div></div>
                        </div>
                        <div class="fleet-meter">
                            <div class="fleet-meter-head"><span>RAM</span><strong>${ram}%</strong></div>
                            <div class="fleet-meter-track"><div class="fleet-meter-fill ${ramClass}" style="width:${Math.min(100, ram)}%;"></div></div>
                        </div>
                    </div>
                    <div class="fleet-node-foot">
                        <span>${driveCount} drive${driveCount === 1 ? '' : 's'} &bull; ${formatBytes(freeTotal)} free</span>
                        <span>${escapeHtml(version)}</span>
                    </div>
                </div>`;
            }).join('');
        }
    }

    safeSetText('dash-total-drives-badge', `${data.total_drives || 0} Total Drives Active`);

    // ---------- Storage Medium Cards ----------
    const nvme = data.nvme || { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
    safeSetText('dash-medium-nvme-total', formatBytes(nvme.total_bytes));
    safeSetText('dash-medium-nvme-free-text', `${formatBytes(nvme.free_bytes)} Free`);
    safeSetText('dash-medium-nvme-sub', `${(nvme.used_percent || 0).toFixed(1)}% Used \u2022 ${nvme.drive_count} Drive${nvme.drive_count === 1 ? '' : 's'} Active`);
    const nvmeMeter = document.getElementById('dash-medium-nvme-meter');
    if (nvmeMeter) nvmeMeter.style.width = `${Math.min(100, nvme.used_percent || 0)}%`;
    const nvmeSharePct = data.total_storage_bytes > 0 ? ((nvme.total_bytes / data.total_storage_bytes) * 100).toFixed(1) : '0.0';
    safeSetText('dash-medium-nvme-pct', `${nvmeSharePct}% Share`);

    const ssd = data.ssd || { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
    safeSetText('dash-medium-ssd-total', formatBytes(ssd.total_bytes));
    safeSetText('dash-medium-ssd-free-text', `${formatBytes(ssd.free_bytes)} Free`);
    safeSetText('dash-medium-ssd-sub', `${(ssd.used_percent || 0).toFixed(1)}% Used \u2022 ${ssd.drive_count} Drive${ssd.drive_count === 1 ? '' : 's'} Active`);
    const ssdMeter = document.getElementById('dash-medium-ssd-meter');
    if (ssdMeter) ssdMeter.style.width = `${Math.min(100, ssd.used_percent || 0)}%`;
    const ssdSharePct = data.total_storage_bytes > 0 ? ((ssd.total_bytes / data.total_storage_bytes) * 100).toFixed(1) : '0.0';
    safeSetText('dash-medium-ssd-pct', `${ssdSharePct}% Share`);

    const hdd = data.hdd || { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
    safeSetText('dash-medium-hdd-total', formatBytes(hdd.total_bytes));
    safeSetText('dash-medium-hdd-free-text', `${formatBytes(hdd.free_bytes)} Free`);
    safeSetText('dash-medium-hdd-sub', `${(hdd.used_percent || 0).toFixed(1)}% Used \u2022 ${hdd.drive_count} Drive${hdd.drive_count === 1 ? '' : 's'} Active`);
    const hddMeter = document.getElementById('dash-medium-hdd-meter');
    if (hddMeter) hddMeter.style.width = `${Math.min(100, hdd.used_percent || 0)}%`;
    const hddSharePct = data.total_storage_bytes > 0 ? ((hdd.total_bytes / data.total_storage_bytes) * 100).toFixed(1) : '0.0';
    safeSetText('dash-medium-hdd-pct', `${hddSharePct}% Share`);

    // ---------- Composition Bar ----------
    safeSetText('dash-storage-share-text', `Total Pooled: ${formatBytes(data.total_storage_bytes)}`);
    const segNvme = document.getElementById('dash-segment-nvme');
    if (segNvme) segNvme.style.width = `${nvmeSharePct}%`;
    const segSsd = document.getElementById('dash-segment-ssd');
    if (segSsd) segSsd.style.width = `${ssdSharePct}%`;
    const segHdd = document.getElementById('dash-segment-hdd');
    if (segHdd) segHdd.style.width = `${hddSharePct}%`;

    safeSetText('dash-legend-nvme', `${formatBytes(nvme.total_bytes)} (${nvmeSharePct}%)`);
    safeSetText('dash-legend-ssd', `${formatBytes(ssd.total_bytes)} (${ssdSharePct}%)`);
    safeSetText('dash-legend-hdd', `${formatBytes(hdd.total_bytes)} (${hddSharePct}%)`);
}

function renderNodes(data) {
    const container = document.getElementById('nodes-full-list');
    if (!container) return;

    const searchTerm = (document.getElementById('nodes-search-input')?.value || '').toLowerCase();
    const sortedNodes = sortNodesDeterministic(data.nodes);
    
    let filteredNodes = sortedNodes.filter(n => {
        const matchesFilter = currentFilter === 'all' || n.status === currentFilter;
        const matchesSearch = !searchTerm || 
            n.metrics.node_id.toLowerCase().includes(searchTerm) ||
            n.metrics.hostname.toLowerCase().includes(searchTerm) ||
            (n.metrics.ips || []).some(ip => ip.includes(searchTerm));
        return matchesFilter && matchesSearch;
    });

    document.getElementById('nodes-total-count').innerText = `${filteredNodes.length} Nodes`;

    if (filteredNodes.length === 0) {
        container.innerHTML = `<div style="grid-column: 1 / -1; background:var(--bg-surface);border:2px dashed var(--border-light);border-radius:var(--radius-lg);padding:48px;text-align:center;">
            <h3 style="font-size:17px;font-weight:700;margin-bottom:6px;">No Matching Nodes Found</h3>
            <p style="color:var(--text-secondary);font-size:14px;">Try changing your filter criteria or connect a new VPS agent.</p>
        </div>`;
        return;
    }

    container.className = `nodes-container ${currentLayout}-view`;
    container.innerHTML = filteredNodes.map(n => createNodeCardHTML(n, currentLayout)).join('');
}

const expandedNodeIDs = new Set();

function toggleNodeExpand(nodeID, event) {
    if (event) event.stopPropagation();
    if (expandedNodeIDs.has(nodeID)) {
        expandedNodeIDs.delete(nodeID);
    } else {
        expandedNodeIDs.add(nodeID);
    }
    if (cachedPoolData) renderNodes(cachedPoolData);
}

function createNodeCardHTML(n, layout) {
    const m = n.metrics;
    const initial = (m.node_id && m.node_id.length > 0) ? m.node_id.charAt(0).toUpperCase() : 'N';
    const status = n.status || 'online';
    const isExpanded = expandedNodeIDs.has(m.node_id);

    let totalDiskBytes = 0;
    let freeDiskBytes = 0;
    const disks = m.disks || [];
    disks.forEach(d => {
        totalDiskBytes += d.total_bytes;
        freeDiskBytes += d.free_bytes;
    });

    const ips = (m.ips || []).slice(0, 2).map(ip => `<span class="node-tag">${ip}</span>`).join('');

    const caps = m.capabilities || {};
    const hasAria = (caps.has_aria2c === true);
    const hasFFmpeg = (caps.has_ffmpeg === true);
    const hasRclone = (caps.has_rclone === true);
    const hasMissingTools = (!hasAria || !hasFFmpeg || !hasRclone);
    const hasAnyTools = (hasAria || hasFFmpeg || hasRclone);

    const agentBadge = `<span class="node-tag" style="background:var(--bg-orange-soft);color:var(--orange-dark);border:1px solid var(--border-orange);font-weight:700;">⚡ Stream Agent :${caps.agent_port || 2052}</span>`;

    const mediaBadge = (m.media_path && m.media_path.length > 0) ?
        `<span class="node-tag" style="background:#eef2ff;color:#4338ca;border:1px solid rgba(99,102,241,0.3);font-weight:600;" title="Media storage location on this node">💾 ${m.media_path}</span>` : '';

    const diskPreview = disks.map(d => {
        const barColor = d.used_percent > 85 ? 'var(--status-offline)' : 'var(--orange-primary)';
        return `
            <div class="disk-entry-box">
                <div class="disk-entry-flex">
                    <span>${d.path} <span style="font-weight:600;color:var(--text-secondary);font-size:12px;">(${d.fs_type || 'FS'} &bull; ${d.disk_type || 'SSD'})</span></span>
                    <span>${formatBytes(d.free_bytes)} free</span>
                </div>
                <div class="gauge-track" style="height:6px;margin:4px 0 6px 0;">
                    <div class="gauge-fill" style="width:${d.used_percent}%;background:${barColor}"></div>
                </div>
                <div style="font-size:11px;color:var(--text-secondary);">${formatBytes(d.used_bytes)} used of ${formatBytes(d.total_bytes)} (${d.used_percent.toFixed(1)}%)</div>
            </div>
        `;
    }).join('');

    const toolStatusPill = (installed) => installed ?
        `<span style="display:inline-flex;align-items:center;gap:4px;font-size:10px;font-weight:700;padding:2px 8px;border-radius:999px;background:#ecfdf5;color:#059669;border:1px solid rgba(16,185,129,0.25);">
            <span style="width:5px;height:5px;border-radius:50%;background:#10b981;"></span>Installed
        </span>` :
        `<span style="display:inline-flex;align-items:center;gap:4px;font-size:10px;font-weight:700;padding:2px 8px;border-radius:999px;background:var(--bg-subtle);color:var(--text-secondary);border:1px solid var(--border-light);">
            <span style="width:5px;height:5px;border-radius:50%;background:#94a3b8;"></span>Not Installed
        </span>`;

    const toolActionBtn = (toolKey, installed, nodeId) => installed ?
        `<button class="tool-action-btn" id="btn-uninstall-${toolKey}-${nodeId}" onclick="event.stopPropagation(); triggerUninstallTools('${nodeId}', this, '${toolKey}')" title="Uninstall from this node">
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
            <span>Uninstall</span>
        </button>` :
        `<button class="tool-action-btn install" id="btn-install-${toolKey}-${nodeId}" onclick="event.stopPropagation(); triggerAutoInstallTools('${nodeId}', this, null, null, '${toolKey}')" title="Install on this node">
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"></line><line x1="5" y1="12" x2="19" y2="12"></line></svg>
            <span>Install</span>
        </button>`;

    const toolCard = (opts) => `
        <div style="display:flex;align-items:center;gap:10px;background:var(--bg-body);padding:10px 12px;border-radius:var(--radius-sm);border:1px solid var(--border-light);">
            <div style="width:32px;height:32px;flex-shrink:0;display:flex;align-items:center;justify-content:center;border-radius:8px;background:${opts.iconBg};color:${opts.iconColor};">
                ${opts.icon}
            </div>
            <div style="flex:1;min-width:0;display:flex;flex-direction:column;gap:3px;">
                <div style="display:flex;align-items:center;gap:7px;">
                    <span style="font-weight:700;font-size:12.5px;font-family:'JetBrains Mono',monospace;color:var(--text-primary);">${opts.name}</span>
                    ${toolStatusPill(opts.installed)}
                </div>
                <span style="font-size:11px;color:var(--text-secondary);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${opts.desc}</span>
            </div>
            ${toolActionBtn(opts.key, opts.installed, m.node_id)}
        </div>`;

    const toolsPanel = `
        <div class="node-tools-panel" style="background:var(--bg-surface);border:1px solid var(--border-light);border-radius:var(--radius-md);padding:14px;margin-bottom:14px;">
            <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:12px;flex-wrap:wrap;">
                <div style="display:flex;align-items:center;gap:9px;">
                    <div style="width:28px;height:28px;display:flex;align-items:center;justify-content:center;border-radius:7px;background:var(--bg-orange-soft);color:var(--orange-dark);">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"></path></svg>
                    </div>
                    <div style="display:flex;flex-direction:column;gap:1px;">
                        <span style="font-size:12px;font-weight:800;letter-spacing:0.2px;color:var(--text-primary);">Media Worker Tools</span>
                        <span style="font-size:10.5px;color:var(--text-secondary);">Download, package &amp; transfer (S3/FTP/node)</span>
                    </div>
                </div>
                <div style="display:flex;gap:6px;flex-wrap:wrap;">
                    ${hasMissingTools ? `
                    <button class="tool-action-btn install" id="btn-autoinstall-${m.node_id}" onclick="event.stopPropagation(); triggerAutoInstallTools('${m.node_id}', this, null, null, 'all')" title="Install aria2c, ffmpeg & rclone">
                        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"></polygon></svg>
                        <span>Install All</span>
                    </button>` : ''}
                    ${hasAnyTools ? `
                    <button class="tool-action-btn danger" id="btn-uninstall-all-${m.node_id}" onclick="event.stopPropagation(); triggerUninstallTools('${m.node_id}', this, 'all')" title="Uninstall all media tools if this node won't process/transfer jobs">
                        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                        <span>Remove All</span>
                    </button>` : ''}
                </div>
            </div>

            <div style="display:grid;grid-template-columns:repeat(auto-fit, minmax(260px, 1fr));gap:8px;">
                ${toolCard({
                    key: 'aria2',
                    name: 'aria2c',
                    desc: 'Multi-thread download engine',
                    installed: hasAria,
                    iconBg: 'rgba(99,102,241,0.12)',
                    iconColor: '#4f46e5',
                    icon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>`
                })}
                ${toolCard({
                    key: 'ffmpeg',
                    name: 'ffmpeg',
                    desc: 'Audio transcode &amp; CMAF muxer',
                    installed: hasFFmpeg,
                    iconBg: 'rgba(16,185,129,0.12)',
                    iconColor: '#059669',
                    icon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="23 7 16 12 23 17 23 7"></polygon><rect x="1" y="5" width="15" height="14" rx="2" ry="2"></rect></svg>`
                })}
                ${toolCard({
                    key: 'rclone',
                    name: 'rclone',
                    desc: 'Node / S3 / FTP transfer engine',
                    installed: hasRclone,
                    iconBg: 'rgba(14,165,233,0.12)',
                    iconColor: '#0284c7',
                    icon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="17 1 21 5 17 9"></polyline><path d="M3 11V9a4 4 0 0 1 4-4h14"></path><polyline points="7 23 3 19 7 15"></polyline><path d="M21 13v2a4 4 0 0 1-4 4H3"></path></svg>`
                })}
            </div>
        </div>
    `;

    return `
        <div class="node-item-card ${isExpanded ? 'expanded' : ''}" id="card-${m.node_id}">
            <!-- Header Row (Click to toggle expand/collapse) -->
            <div class="node-header-row" onclick="toggleNodeExpand('${m.node_id}', event)">
                <div class="node-title-group">
                    <div class="node-avatar-square">${initial}</div>
                    <div class="node-title-wrap">
                        <div class="node-title-text">${m.node_id}</div>
                        <div class="node-subtext">
                            <span>${m.hostname}</span>
                            <span>&bull;</span>
                            <span>${m.os}</span>
                        </div>
                    </div>
                </div>

                <div class="node-actions-right">
                    <button class="btn-quota-gear" onclick="event.stopPropagation(); openNodeDetail('${m.node_id}')" title="Configure Storage Quota">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                            <line x1="4" y1="21" x2="4" y2="14"></line>
                            <line x1="4" y1="10" x2="4" y2="3"></line>
                            <line x1="12" y1="21" x2="12" y2="12"></line>
                            <line x1="12" y1="8" x2="12" y2="3"></line>
                            <line x1="20" y1="21" x2="20" y2="16"></line>
                            <line x1="20" y1="12" x2="20" y2="3"></line>
                            <line x1="1" y1="14" x2="7" y2="14"></line>
                            <line x1="9" y1="8" x2="15" y2="8"></line>
                            <line x1="17" y1="16" x2="23" y2="16"></line>
                        </svg>
                        <span>Quota</span>
                    </button>

                    <button class="btn-quota-gear" id="btn-rescan-${m.node_id}" onclick="event.stopPropagation(); triggerNodeRescan('${m.node_id}', this)" title="Rescan & Sync Hardware">
                        <svg class="rescan-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                            <polyline points="23 4 23 10 17 10"></polyline>
                            <polyline points="1 20 1 14 7 14"></polyline>
                            <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
                        </svg>
                        <span>Rescan</span>
                    </button>

                    ${!m.node_id.includes('(master)') ? `
                    <button class="btn-quota-gear" style="color:var(--status-offline);background:var(--status-offline-bg);border-color:rgba(239,68,68,0.25);" onclick="event.stopPropagation(); deleteNodeConfirm('${m.node_id}')" title="Remove Node">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                            <polyline points="3 6 5 6 21 6"></polyline>
                            <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                        </svg>
                        <span>Remove</span>
                    </button>` : ''}

                    <span class="node-badge ${status}">
                        <span style="width:6px;height:6px;border-radius:50%;background:currentColor;"></span>
                        ${status}
                    </span>

                    <div class="chevron-toggle-btn" title="${isExpanded ? 'Collapse' : 'Expand'} Details">
                        <svg class="chevron-icon" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                            <polyline points="6 9 12 15 18 9"></polyline>
                        </svg>
                    </div>
                </div>
            </div>

            <!-- Compact Summary Row (Visible when collapsed) -->
            <div class="node-compact-summary" onclick="toggleNodeExpand('${m.node_id}', event)" style="cursor:pointer;">
                <div class="summary-pills-list">
                    <span class="metric-tag-pill">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <ellipse cx="12" cy="5" rx="9" ry="3"></ellipse>
                            <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"></path>
                            <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"></path>
                        </svg>
                        ${disks.length} Drives (${formatBytes(freeDiskBytes)} Free)
                    </span>
                    <span class="metric-tag-pill">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <rect x="4" y="4" width="16" height="16" rx="2"></rect>
                            <line x1="9" y1="1" x2="9" y2="4"></line>
                            <line x1="15" y1="1" x2="15" y2="4"></line>
                            <line x1="9" y1="20" x2="9" y2="23"></line>
                            <line x1="15" y1="20" x2="15" y2="23"></line>
                        </svg>
                        ${m.cpu.cores} Cores (${m.cpu.used_percent.toFixed(1)}%)
                    </span>
                    <span class="metric-tag-pill">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <rect x="2" y="8" width="20" height="8" rx="2"></rect>
                            <path d="M6 19v-3"></path>
                            <path d="M10 19v-3"></path>
                            <path d="M14 19v-3"></path>
                            <path d="M18 19v-3"></path>
                        </svg>
                        RAM ${formatBytes(m.memory.available_bytes)} Free
                    </span>
                </div>
                <div class="toggle-label-action">
                    <span>${isExpanded ? 'Hide Details' : 'Show Details'}</span>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="transform:${isExpanded ? 'rotate(180deg)' : 'rotate(0deg)'};transition:transform 0.2s;">
                        <polyline points="6 9 12 15 18 9"></polyline>
                    </svg>
                </div>
            </div>

            <!-- Expandable Detailed View -->
            <div class="node-details-collapsible">
                ${toolsPanel}
                <div class="node-tag-list" style="display:flex;flex-wrap:wrap;gap:6px;margin-bottom:14px;">
                    <span class="node-tag">${m.platform || m.os}</span>
                    ${ips}
                    ${agentBadge}
                    ${mediaBadge}
                </div>

                <div class="resource-bar-grid">
                    <div class="resource-bar-item">
                        <span>Compute Load</span>
                        <strong>${m.cpu.cores} Cores (${m.cpu.used_percent.toFixed(1)}%)</strong>
                    </div>
                    <div class="resource-bar-item">
                        <span>RAM Free</span>
                        <strong>${formatBytes(m.memory.available_bytes)} / ${formatBytes(m.memory.total_bytes)}</strong>
                    </div>
                </div>

                <div class="disk-listing">
                    <div class="disk-listing-header">
                        <span>All Mounted Drives (${disks.length})</span>
                        <span>${formatBytes(freeDiskBytes)} Total Free</span>
                    </div>
                    ${diskPreview || '<div style="font-size:12px;color:var(--text-secondary);">No storage drives mounted</div>'}
                </div>
            </div>
        </div>
    `;
}

async function triggerNodeRescan(nodeId, btnElement) {
    if (!btnElement) btnElement = document.getElementById(`btn-rescan-${nodeId}`);
    const originalHtml = btnElement ? btnElement.innerHTML : '';
    
    if (btnElement) {
        btnElement.disabled = true;
        btnElement.innerHTML = `
            <svg class="spin-fast" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                <polyline points="23 4 23 10 17 10"></polyline>
                <polyline points="1 20 1 14 7 14"></polyline>
                <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
            </svg>
            <span style="color:var(--orange-primary);font-weight:700;">Scanning...</span>
        `;
        btnElement.style.borderColor = 'var(--border-orange)';
    }

    try {
        const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/rescan`, {
            method: 'POST'
        });

        await new Promise(r => setTimeout(r, 700));

        if (res.ok) {
            if (btnElement) {
                btnElement.innerHTML = `
                    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="3">
                        <polyline points="20 6 9 17 4 12"></polyline>
                    </svg>
                    <span style="color:#10b981;font-weight:700;">Scanned!</span>
                `;
            }
            await fetchClusterTelemetry();
            setTimeout(() => {
                if (btnElement) {
                    btnElement.innerHTML = originalHtml;
                    btnElement.disabled = false;
                    btnElement.style.borderColor = '';
                }
            }, 1200);
        } else {
            alert('Rescan error: ' + (await res.text()));
            if (btnElement) {
                btnElement.innerHTML = originalHtml;
                btnElement.disabled = false;
                btnElement.style.borderColor = '';
            }
        }
    } catch (err) {
        console.error('Error rescanning node:', err);
        if (btnElement) {
            btnElement.innerHTML = originalHtml;
            btnElement.disabled = false;
            btnElement.style.borderColor = '';
        }
    }
}

async function deleteNodeConfirm(nodeId) {
    if (!confirm(`Are you sure you want to remove node '${nodeId}' from the cluster?\n\nThis unregisters the node and stops the remote stream-agent over SSH.`)) {
        return;
    }

    let sshPassword = '';
    try {
        const cfgRes = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}/config`);
        if (cfgRes.ok) {
            const cfg = await cfgRes.json();
            if (!cfg.ssh_password) {
                sshPassword = prompt(`SSH password for '${nodeId}' (needed to stop the remote agent):`) || '';
            }
        } else {
            sshPassword = prompt(`SSH password for '${nodeId}' (needed to stop the remote agent):`) || '';
        }
    } catch (_) {
        sshPassword = prompt(`SSH password for '${nodeId}' (needed to stop the remote agent):`) || '';
    }

    try {
        const res = await fetch(`/api/nodes/${encodeURIComponent(nodeId)}`, {
            method: 'DELETE',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(sshPassword ? { ssh_password: sshPassword, ssh_user: 'root' } : {})
        });

        if (res.ok) {
            const cardEl = document.getElementById(`card-${nodeId}`);
            if (cardEl) {
                cardEl.style.transition = 'all 0.3s ease';
                cardEl.style.opacity = '0';
                cardEl.style.transform = 'scale(0.95)';
                setTimeout(() => cardEl.remove(), 300);
            }
            await fetchClusterTelemetry();
        } else {
            const errText = await res.text();
            alert(`Failed to remove node '${nodeId}': ${errText}`);
        }
    } catch (err) {
        console.error('Error removing node:', err);
        alert(`Network error removing node: ${err.message}`);
    }
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
                await fetchClusterTelemetry();
                
                if (cachedPoolData && cachedPoolData.nodes) {
                    const targetNode = cachedPoolData.nodes.find(n => n.metrics && n.metrics.node_id === nodeId);
                    if (targetNode && targetNode.metrics && targetNode.metrics.capabilities) {
                        const caps = targetNode.metrics.capabilities;
                        const ok = (tool === 'aria2' && caps.has_aria2c) ||
                                   (tool === 'ffmpeg' && caps.has_ffmpeg) ||
                                   (tool === 'rclone' && caps.has_rclone) ||
                                   (tool === 'all' && caps.has_ffmpeg && caps.has_aria2c && caps.has_rclone);
                        if (ok) {
                            clearInterval(pollInterval);
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
                    if (btnElement) {
                        btnElement.innerHTML = originalHtml;
                        btnElement.disabled = false;
                        btnElement.style.opacity = '1';
                    }
                }
            }, 3000);
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
                await fetchClusterTelemetry();
                
                if (cachedPoolData && cachedPoolData.nodes) {
                    const targetNode = cachedPoolData.nodes.find(n => n.metrics && n.metrics.node_id === nodeId);
                    if (targetNode && targetNode.metrics && targetNode.metrics.capabilities) {
                        const caps = targetNode.metrics.capabilities;
                        const removed = (tool === 'aria2' && !caps.has_aria2c) ||
                                        (tool === 'ffmpeg' && !caps.has_ffmpeg) ||
                                        (tool === 'rclone' && !caps.has_rclone) ||
                                        (tool === 'all' && !caps.has_ffmpeg && !caps.has_aria2c && !caps.has_rclone);
                        if (removed) {
                            clearInterval(pollInterval);
                            if (btnElement) {
                                btnElement.innerHTML = `<span>✓ Removed</span>`;
                            }
                            return;
                        }
                    }
                }

                if (attempts >= 14) {
                    clearInterval(pollInterval);
                    if (btnElement) {
                        btnElement.innerHTML = originalHtml;
                        btnElement.disabled = false;
                        btnElement.style.opacity = '1';
                    }
                }
            }, 3000);
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

function tierBadgeForBlock(nodeID, path) {
    if (!tiersCache || !tiersCache.tiers) return '';
    for (const t of tiersCache.tiers) {
        for (const b of (t.blocks || [])) {
            if (b.block && b.block.node_id === nodeID && b.block.path === path) {
                return ` <span class="node-tag" style="background:#eef2ff;color:#4338ca;border:1px solid rgba(99,102,241,0.3);font-weight:600;" title="Assigned storage tier">${escapeHtml(t.name)}</span>`;
            }
        }
    }
    return '';
}

function renderStorage(data) {
    const tbody = document.getElementById('storage-table-body');
    if (!tbody) return;

    const nodes = data.nodes || [];
    let rows = '';

    nodes.forEach(n => {
        const m = n.metrics;
        (m.disks || []).forEach(d => {
            const barColor = d.used_percent > 85 ? 'var(--status-offline)' : 'var(--orange-primary)';
            const dtype = d.disk_type || 'SSD';
            const dtypeClass = dtype.toLowerCase().includes('nvme') ? 'nvme' : (dtype.toLowerCase().includes('hdd') ? 'hdd' : 'ssd');
            rows += `
                <tr>
                    <td><strong>${m.node_id}</strong><div style="font-size:12px;color:var(--text-secondary);">${m.hostname}</div></td>
                    <td><code>${d.path}</code>${tierBadgeForBlock(m.node_id, d.path)}</td>
                    <td><span class="type-pill ${dtypeClass}">${dtype}</span></td>
                    <td><span class="node-tag">${d.fs_type || 'FS'}</span></td>
                    <td><strong>${formatBytes(d.total_bytes)}</strong></td>
                    <td style="color:var(--status-online);font-weight:700;">${formatBytes(d.free_bytes)}</td>
                    <td style="width:160px;">
                        <div class="gauge-track" style="margin:0 0 4px 0;">
                            <div class="gauge-fill" style="width:${d.used_percent}%;background:${barColor}"></div>
                        </div>
                        <div style="font-size:11px;color:var(--text-secondary);">${d.used_percent.toFixed(1)}% used</div>
                    </td>
                    <td><span class="node-badge ${n.status}">${n.status}</span></td>
                </tr>
            `;
        });
    });

    tbody.innerHTML = rows || '<tr><td colspan="8" style="text-align:center;padding:24px;color:var(--text-secondary);">No storage drives pooled yet.</td></tr>';
}

let currentStorageTab = 'disks';

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
        tbody.innerHTML = `<tr><td colspan="6" style="text-align:center; padding:24px; color:var(--status-offline);">Failed to load storage folders: ${err.message}</td></tr>`;
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
        const primaryIP = n.primary_ip || (n.ips && n.ips[0]) || '127.0.0.1';
        const hostname = n.hostname || 'Host';
        const isOnline = n.status === 'online';
        const statusBadge = `<span class="node-badge ${n.status}">${n.status}</span>`;

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
                    <td>
                        <div style="display:flex; align-items:center; gap:8px; flex-wrap:wrap;">
                            <span style="font-size:14px; font-weight:700; color:var(--text-primary);">${escapeHtml(n.node_id)}</span>
                            ${isMultiDrive ? `<span class="ip-chip" style="color:var(--orange-dark); font-weight:700; background:var(--bg-orange-soft); border-color:var(--border-hover);"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="22" y1="12" x2="2" y2="12"></line><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"></path></svg> ${escapeHtml(driveTag)}</span>` : ''}
                        </div>
                        <div style="font-size:12px; color:var(--text-secondary); margin:4px 0 6px 0; display:flex; align-items:center; gap:6px; flex-wrap:wrap;">
                            <span>${escapeHtml(hostname)}</span>
                            <span class="ip-chip">${escapeHtml(primaryIP)}</span>
                        </div>
                        <div>${statusBadge}</div>
                    </td>
                    <td>
                        <div class="path-chip" title="${escapeHtml(d.stream_root)}">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="color:var(--text-muted); flex-shrink:0;">
                                <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path>
                            </svg>
                            <span>${escapeHtml(d.stream_root)}</span>
                        </div>
                    </td>
                    <td>
                        <div>
                            <div class="path-chip media" title="${escapeHtml(d.media_dir)}">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex-shrink:0;">
                                    <polygon points="23 7 16 12 23 17 23 7"></polygon>
                                    <rect x="1" y="5" width="15" height="14" rx="2" ry="2"></rect>
                                </svg>
                                <span>${escapeHtml(d.media_dir)}</span>
                            </div>
                            <div class="folder-stat-line">
                                <span class="size">${formatBytes(d.media_size_bytes || 0)}</span>
                                <span class="count">• ${d.media_file_count || 0} Files</span>
                            </div>
                        </div>
                    </td>
                    <td>
                        <div>
                            <div class="path-chip proc" title="${escapeHtml(d.processing_dir)}">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex-shrink:0;">
                                    <rect x="4" y="4" width="16" height="16" rx="2"></rect>
                                    <line x1="9" y1="1" x2="9" y2="4"></line>
                                    <line x1="15" y1="1" x2="15" y2="4"></line>
                                    <line x1="9" y1="20" x2="9" y2="23"></line>
                                    <line x1="15" y1="20" x2="15" y2="23"></line>
                                </svg>
                                <span>${escapeHtml(d.processing_dir)}</span>
                            </div>
                            <div class="folder-stat-line">
                                <span class="size" style="${d.processing_size_bytes > 0 ? 'color:#2563eb;' : 'color:var(--text-muted);'}">
                                    ${formatBytes(d.processing_size_bytes || 0)}
                                </span>
                                <span class="count">• ${d.processing_file_count || 0} temp files</span>
                            </div>
                        </div>
                    </td>
                    <td>
                        ${(d.total_stream_bytes || 0) > 0 ? `
                            <span class="total-usage-pill">
                                <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                                    <ellipse cx="12" cy="5" rx="9" ry="3"></ellipse>
                                    <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"></path>
                                    <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"></path>
                                </svg>
                                <span>${formatBytes(d.total_stream_bytes || 0)}</span>
                            </span>
                        ` : `
                            <span class="total-usage-pill empty">
                                <span>0 B</span>
                            </span>
                        `}
                    </td>
                    <td>
                        <div class="folder-actions-wrap">
                            <button class="btn-table-icon-clean" onclick="cleanDriveFolder('${n.node_id}', '${d.processing_dir}', 'processing')" title="Clean Scratch (${escapeHtml(d.processing_dir)})" aria-label="Clean Scratch">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3">
                                    <path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"></path>
                                </svg>
                            </button>
                            <button class="btn-table-icon-purge" onclick="cleanDriveFolder('${n.node_id}', '${d.media_dir}', 'media')" title="Purge Media (${escapeHtml(d.media_dir)})" aria-label="Purge Media">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3">
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

    tbody.innerHTML = rows || '<tr><td colspan="6" style="text-align:center; padding:24px; color:var(--text-secondary);">No nodes connected.</td></tr>';

    const mediaEl = document.getElementById('summary-total-media');
    const mediaCountEl = document.getElementById('summary-total-media-files');
    const procEl = document.getElementById('summary-total-processing');
    const procCountEl = document.getElementById('summary-total-processing-files');

    if (mediaEl) mediaEl.innerText = formatBytes(totalMedia);
    if (mediaCountEl) mediaCountEl.innerText = `${totalMediaFiles} Files stored across cluster`;
    if (procEl) procEl.innerText = formatBytes(totalProc);
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
            typeof fetchStorage === 'function' ? fetchStorage() : Promise.resolve()
        ]);
        if (label) label.innerText = 'Refreshed!';
        setTimeout(() => {
            if (label) label.innerText = 'Refresh';
        }, 1200);
    } catch (err) {
        if (label) label.innerText = 'Failed';
        setTimeout(() => {
            if (label) label.innerText = 'Refresh';
        }, 1500);
    } finally {
        if (icon) icon.classList.remove('spin-fast');
        if (btnEl) btnEl.disabled = false;
    }
}

let currentCleanTask = null;

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
            iconEl.innerHTML = '<path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"></path>';
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
                    resultMsg.innerText = `Freed ${formatBytes(freed)} (${items} item${items === 1 ? '' : 's'} removed).`;
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
        title: isMedia ? 'Purge Stored Media' : 'Clean Scratch Workspace',
        sub: `Drive cleanup on ${nodeId}`,
        desc: isMedia
            ? `⚠️ <strong>Warning:</strong> This will permanently delete <strong>all media video files</strong> inside <code style="background:#fee2e2; padding:2px 6px; border-radius:4px; font-size:12px;">${escapeHtml(dirPath)}</code> on node <strong>${escapeHtml(nodeId)}</strong>. This will set its storage usage to 0 B.`
            : `This will remove all temporary download and remuxing artifacts from <code style="background:#eff6ff; padding:2px 6px; border-radius:4px; font-size:12px;">${escapeHtml(dirPath)}</code> on node <strong>${escapeHtml(nodeId)}</strong>.`,
        actionLabel: isMedia ? 'Purge Media Now' : 'Clean Scratch Now',
        requireConfirmation: false
    });
}

function cleanNodeFolder(nodeId, target) {
    const isMedia = target === 'media';
    openStorageCleanModal({
        nodeId: nodeId,
        target: target,
        title: isMedia ? 'Purge Node Media' : 'Clean Node Scratch',
        sub: `Cluster maintenance on ${nodeId}`,
        desc: isMedia
            ? `⚠️ <strong>Warning:</strong> This will permanently delete all stored media packages across all drives on node <strong>${escapeHtml(nodeId)}</strong>.`
            : `This will clean all temporary scratch artifacts on node <strong>${escapeHtml(nodeId)}</strong>.`,
        actionLabel: isMedia ? 'Purge Node Media' : 'Clean Scratch',
        requireConfirmation: false
    });
}

function cleanAllProcessingScratch() {
    openStorageCleanModal({
        nodeId: 'all',
        target: 'processing',
        title: 'Clean Scratch Workspaces',
        sub: 'Cluster-wide Maintenance',
        desc: 'This will remove all temporary processing, remuxing, and download files from <strong>all connected nodes</strong>. Active video streams will not be affected.',
        actionLabel: 'Clean All Scratch Workspaces',
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

function renderSettings(data) {
    const nodeCountEl = document.getElementById('settings-node-count');
    if (nodeCountEl) nodeCountEl.innerText = `${data.total_nodes} Registered Nodes`;
}

async function openNodeDetail(nodeID) {
    if (!cachedPoolData || !cachedPoolData.nodes) return;
    const node = cachedPoolData.nodes.find(n => n.metrics.node_id === nodeID);
    if (!node) return;

    activeSelectedNode = node;
    const m = node.metrics;

    document.getElementById('modal-node-title').innerText = `Node: ${m.node_id}`;
    document.getElementById('modal-node-sub').innerText = `${m.hostname} (${m.os} - ${m.platform || ''}) \u2022 IPs: ${(m.ips || []).join(', ')}`;
    document.getElementById('modal-cpu-info').innerText = `${m.cpu.cores} Cores \u2022 ${m.cpu.model_name || 'Generic CPU'} \u2022 Load: ${m.cpu.used_percent.toFixed(1)}%`;
    document.getElementById('modal-ram-info').innerText = `${formatBytes(m.memory.available_bytes)} Free of ${formatBytes(m.memory.total_bytes)} (${m.memory.used_percent.toFixed(1)}% used)`;

    let totalDiskBytes = 0;
    (m.disks || []).forEach(d => totalDiskBytes += d.total_bytes);
    const totalGB = Math.round(totalDiskBytes / (1024 * 1024 * 1024));

    try {
        const res = await fetch(`/api/nodes/${encodeURIComponent(nodeID)}/config`);
        if (res.ok) {
            const cfg = await res.json();
            const allocGB = cfg.allocated_max_bytes ? Math.round(cfg.allocated_max_bytes / (1024 * 1024 * 1024)) : 0;
            document.getElementById('modal-quota-slider').max = totalGB;
            document.getElementById('modal-quota-slider').value = allocGB;
            document.getElementById('modal-quota-value').value = allocGB === 0 ? `Unlimited (${totalGB} GB)` : `${allocGB} GB`;
        }
    } catch (e) {
        console.warn('Failed to fetch node config:', e);
    }

    document.getElementById('node-detail-modal').classList.add('open');
}

function closeNodeDetailModal() {
    document.getElementById('node-detail-modal').classList.remove('open');
    activeSelectedNode = null;
}

function onQuotaSliderChange(val) {
    const input = document.getElementById('modal-quota-value');
    if (parseInt(val) === 0) {
        input.value = 'Unlimited (Full Capacity)';
    } else {
        input.value = `${val} GB`;
    }
}

async function saveNodeAllocation() {
    if (!activeSelectedNode) return;
    const nodeID = activeSelectedNode.metrics.node_id;
    const sliderVal = parseInt(document.getElementById('modal-quota-slider').value);
    const maxBytes = sliderVal === 0 ? 0 : sliderVal * 1024 * 1024 * 1024;

    try {
        const res = await fetch(`/api/nodes/${encodeURIComponent(nodeID)}/allocate`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ allocated_max_bytes: maxBytes })
        });
        if (res.ok) {
            alert(`Allocation saved in BadgerDB for ${nodeID}!`);
            closeNodeDetailModal();
            fetchClusterTelemetry();
        } else {
            alert('Failed to save allocation');
        }
    } catch (err) {
        console.error('Error saving allocation:', err);
    }
}

document.addEventListener('DOMContentLoaded', () => {
    const initialPath = window.location.pathname;
    let initialPage = 'dashboard';
    if (initialPath === '/nodes') initialPage = 'nodes';
    else if (initialPath === '/storage') initialPage = 'storage';
    else if (initialPath === '/tiers') initialPage = 'tiers';
    else if (initialPath === '/media') initialPage = 'media';
    else if (initialPath === '/database') initialPage = 'database';
    else if (initialPath === '/settings') initialPage = 'settings';
    else if (initialPath === '/docs') initialPage = 'docs';

    navigateTo(initialPage, false);

    window.addEventListener('popstate', () => {
        const path = window.location.pathname;
        let page = 'dashboard';
        if (path === '/nodes') page = 'nodes';
        else if (path === '/storage') page = 'storage';
        else if (path === '/tiers') page = 'tiers';
        else if (path === '/media') page = 'media';
        else if (path === '/database') page = 'database';
        else if (path === '/settings') page = 'settings';
        else if (path === '/docs') page = 'docs';
        navigateTo(page, false);
    });

    document.querySelectorAll('.nav-link').forEach(link => {
        link.addEventListener('click', (e) => {
            const page = link.getAttribute('data-page');
            if (page) {
                e.preventDefault();
                navigateTo(page, true);
            }
        });
    });

    document.getElementById('btn-layout-grid')?.addEventListener('click', () => {
        currentLayout = 'grid';
        document.getElementById('btn-layout-grid').classList.add('active');
        document.getElementById('btn-layout-list').classList.remove('active');
        if (cachedPoolData) renderNodes(cachedPoolData);
    });

    document.getElementById('btn-layout-list')?.addEventListener('click', () => {
        currentLayout = 'list';
        document.getElementById('btn-layout-list').classList.add('active');
        document.getElementById('btn-layout-grid').classList.remove('active');
        if (cachedPoolData) renderNodes(cachedPoolData);
    });

    document.querySelectorAll('.filter-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            currentFilter = btn.getAttribute('data-filter');
            if (cachedPoolData) renderNodes(cachedPoolData);
        });
    });

    document.getElementById('nodes-search-input')?.addEventListener('input', () => {
        if (cachedPoolData) renderNodes(cachedPoolData);
    });

    fetchClusterTelemetry();
    setInterval(fetchClusterTelemetry, 2500);
});

function openAddVPSModal() {
    const modal = document.getElementById('modal-add-vps');
    if (modal) {
        modal.classList.add('open');
        modal.classList.add('active');
        document.getElementById('provision-log-box').style.display = 'none';
        document.getElementById('btn-submit-provision').disabled = false;
        document.getElementById('btn-provision-text').innerText = 'Provision & Join Cluster';
        
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
        modal.classList.remove('open');
        modal.classList.remove('active');
    }
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
                    if (wantName && id === wantName) return true;
                    if (host && ips.includes(host)) return true;
                    if (host && hostname.includes(host)) return true;
                    return false;
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

    const logBox = document.getElementById('provision-log-box');
    const logContent = document.getElementById('provision-log-content');
    const submitBtn = document.getElementById('btn-submit-provision');
    const submitText = document.getElementById('btn-provision-text');

    logBox.style.display = 'block';
    submitBtn.disabled = true;
    submitText.innerText = 'Provisioning in progress...';
    logContent.innerHTML = `
        <div style="color:var(--orange-primary);">SSH to ${host}:${port} as ${user}...</div>
        <div style="color:var(--text-secondary);margin-top:4px;">Installing tools can take 1–3 minutes. Keep this window open.</div>
    `;

    const payload = {
        host: host,
        port: port,
        user: user,
        password: password,
        use_sudo: useSudo,
        node_name: name,
        advertise_addr: host,
        coordinator_url: `${window.location.protocol}//${window.location.host}`
    };

    const finishSuccess = (extraHtml = '') => {
        logContent.innerHTML = `<div style="color:var(--status-online);font-weight:bold;">VPS <strong>${name}</strong> joined the cluster.</div>${extraHtml}`;
        submitText.innerText = 'Completed!';
        setTimeout(() => {
            closeAddVPSModal();
            fetchClusterTelemetry();
        }, 1500);
    };

    // Parallel watcher: SSH provision often outlives the browser HTTP wait.
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
            const logs = (data.logs || []).map(l => `<div style="color:var(--status-online);">${l}</div>`).join('');
            finishSuccess(logs);
            return;
        }

        // HTTP error — still might have joined (partial success / late heartbeat).
        if (await waitForProvisionedNode(name, host, { timeoutMs: 45000 })) {
            finishSuccess(`<div style="color:var(--text-secondary);margin-top:6px;">Joined after a slow provision response.</div>`);
            return;
        }
        const errLogs = (data.result?.logs || []).map(l => `<div>${l}</div>`).join('');
        logContent.innerHTML = `${errLogs}<div style="color:var(--status-offline);font-weight:bold;margin-top:8px;">Error: ${data.error || 'Provisioning failed'}</div>`;
        submitBtn.disabled = false;
        submitText.innerText = 'Retry Provisioning';
    } catch (err) {
        logContent.innerHTML += `<div style="color:var(--text-secondary);margin-top:6px;">Browser lost the long HTTP response (${err.message}). Checking if the node joined...</div>`;
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

async function triggerNodeRescan(nodeID, btnEl) {
    const icon = btnEl ? btnEl.querySelector('.rescan-icon') : null;
    if (icon) icon.classList.add('spin-fast');

    try {
        const resp = await fetch(`/api/nodes/${encodeURIComponent(nodeID)}/rescan`, {
            method: 'POST'
        });
        if (resp.ok) {
            await fetchClusterTelemetry();
        }
    } catch (err) {
        console.error('Rescan node error:', err);
    } finally {
        if (icon) {
            setTimeout(() => icon.classList.remove('spin-fast'), 600);
        }
    }
}

let mediaList = [];
let mediaCurrentPage = 1;
let mediaPageSize = 10;
let mediaSearchQuery = '';
let mediaFilterStatus = 'all';
let mediaPollInterval = null;
let selectedUploadFile = null;

async function fetchMediaList() {
    try {
        const res = await fetch('/api/v1/files');
        if (!res.ok) return;
        const data = await res.json();
        mediaList = (data.files || []).sort((a, b) => {
            const timeA = new Date(a.created_at).getTime() || 0;
            const timeB = new Date(b.created_at).getTime() || 0;
            if (timeA !== timeB) return timeB - timeA;
            return (b.key || '').localeCompare(a.key || '');
        });
        renderMediaTable();
    } catch (err) {
        console.error('Failed to load media files:', err);
    }
}

setInterval(() => {
    if (currentView === 'media') {
        fetchMediaList();
    }
}, 1500);

function setMediaFilter(status) {
    mediaFilterStatus = status;
    mediaCurrentPage = 1;
    document.querySelectorAll('.filter-btn[data-media-filter]').forEach(btn => {
        if (btn.getAttribute('data-media-filter') === status) {
            btn.classList.add('active');
        } else {
            btn.classList.remove('active');
        }
    });
    renderMediaTable();
}

function onMediaSearchChange() {
    const input = document.getElementById('media-search-input');
    mediaSearchQuery = (input ? input.value : '').toLowerCase().trim();
    mediaCurrentPage = 1;
    renderMediaTable();
}

function onMediaPageSizeChange() {
    const select = document.getElementById('media-page-size');
    if (select) {
        mediaPageSize = parseInt(select.value, 10) || 10;
        mediaCurrentPage = 1;
        renderMediaTable();
    }
}

function renderPipelineStepper(item) {
    const isUpload = item.source_type === 'upload';
    const hasTransfer = item.worker_node_id && item.placement?.node_id && item.worker_node_id !== item.placement?.node_id;

    let s1 = 'pending', s2 = 'pending', s3 = 'pending', s4 = 'pending';

    if (item.state === 'completed') {
        s1 = s2 = s3 = s4 = 'done';
    } else if (item.state === 'failed') {
        // not applicable
    } else if (item.state === 'awaiting_upload' || item.state === 'uploading' || item.state === 'detected' || item.state === 'downloading') {
        s1 = 'active';
    } else if (item.state === 'processing') {
        s1 = 'done';
        s2 = 'active';
    } else if (item.state === 'transferring') {
        s1 = 'done';
        s2 = 'done';
        s3 = 'active';
    }

    const stepIcon = (st, icon, label) => {
        let bg = 'rgba(255, 255, 255, 0.05)';
        let color = 'var(--text-muted)';
        let border = 'var(--border-light)';
        if (st === 'done') {
            bg = 'rgba(16, 185, 129, 0.12)';
            color = '#10b981';
            border = 'rgba(16, 185, 129, 0.3)';
        } else if (st === 'active') {
            bg = 'rgba(255, 122, 24, 0.15)';
            color = 'var(--orange-primary)';
            border = 'var(--orange-primary)';
        }
        return `
            <div style="display:inline-flex;align-items:center;gap:3px;padding:2px 6px;border-radius:6px;font-size:9.5px;font-weight:700;background:${bg};color:${color};border:1px solid ${border};" title="${label}">
                <span>${st === 'done' ? '✓' : icon}</span>
                <span>${label}</span>
            </div>
        `;
    };

    return `
        <div style="display:flex;align-items:center;gap:3px;margin-bottom:6px;flex-wrap:wrap;">
            ${stepIcon(s1, isUpload ? '📤' : '📥', isUpload ? 'Upload' : 'Download')}
            <span style="font-size:9px;color:var(--text-muted);">➔</span>
            ${stepIcon(s2, '⚙️', 'CMAF')}
            ${hasTransfer ? `
                <span style="font-size:9px;color:var(--text-muted);">➔</span>
                ${stepIcon(s3, '🔀', 'Sync')}
            ` : ''}
            <span style="font-size:9px;color:var(--text-muted);">➔</span>
            ${stepIcon(s4, '✓', 'Ready')}
        </div>
    `;
}

function renderMediaSpecs(meta) {
    if (!meta || (!meta.video && (!meta.audio_tracks || meta.audio_tracks.length === 0))) {
        return `<span class="badge-video">MP4 Stream</span>`;
    }
    const v = meta.video || {};
    const resLabel = v.height ? `${v.height}p` : (v.width ? `${v.width}x${v.height}` : '');
    const fpsLabel = v.fps ? `${Math.round(v.fps)}fps` : '';
    const durLabel = meta.duration_sec ? formatDuration(meta.duration_sec) : '';

    let audioPills = '';
    if (meta.audio_tracks && meta.audio_tracks.length > 0) {
        audioPills = meta.audio_tracks.map(t => {
            const lang = t.language || t.title || 'Audio';
            const full = t.title || lang;
            return `<span class="badge-audio" title="${escapeHtml(full)} (${escapeHtml(t.codec || 'aac')})">${escapeHtml(lang.toUpperCase())}</span>`;
        }).join(' ');
    }

    let subPills = '';
    if (meta.subtitles && meta.subtitles.length > 0) {
        subPills = meta.subtitles.map(s => {
            const lang = s.language || s.title || 'Sub';
            return `<span class="badge-sub" title="${escapeHtml(s.title || lang)} (WebVTT)">${escapeHtml(lang.toUpperCase())}</span>`;
        }).join(' ');
    }

    return `
        <div style="display:flex; flex-direction:column; gap:4px;">
            <div style="display:flex; align-items:center; gap:4px; flex-wrap:wrap;">
                ${resLabel ? `<span class="badge-spec">${escapeHtml(resLabel)}</span>` : ''}
                ${fpsLabel ? `<span class="badge-spec">${escapeHtml(fpsLabel)}</span>` : ''}
                ${durLabel ? `<span style="font-size:11px; color:var(--text-secondary); font-weight:700; font-family:'JetBrains Mono',monospace; margin-left:2px;">${escapeHtml(durLabel)}</span>` : ''}
            </div>
            ${(audioPills || subPills) ? `
                <div style="display:flex; align-items:center; gap:4px; flex-wrap:wrap; margin-top:2px;">
                    ${audioPills}
                    ${subPills}
                </div>
            ` : ''}
        </div>
    `;
}

function renderMediaTable() {
    const tbody = document.getElementById('media-table-body');
    if (!tbody) return;

    const totalCount = mediaList.length;
    let totalBytes = 0;
    let readyCount = 0;
    let activeTasksCount = 0;

    mediaList.forEach(m => {
        if (m.size_bytes) totalBytes += m.size_bytes;
        if (m.state === 'completed') readyCount++;
        if (m.state === 'downloading' || m.state === 'processing' || m.state === 'transferring' || m.state === 'detected' || m.state === 'awaiting_upload') activeTasksCount++;
    });

    safeSetText('media-stat-count', totalCount);
    safeSetText('media-stat-size', formatBytes(totalBytes));
    safeSetText('media-stat-ready', readyCount);
    safeSetText('media-stat-tasks', activeTasksCount);
    safeSetText('media-total-badge', `${totalCount} Media Asset${totalCount === 1 ? '' : 's'}`);

    let filtered = mediaList.filter(item => {
        if (mediaFilterStatus !== 'all') {
            if (mediaFilterStatus === 'downloading') {
                if (!['downloading', 'processing', 'transferring', 'detected', 'awaiting_upload'].includes(item.state)) return false;
            } else if (item.state !== mediaFilterStatus) {
                return false;
            }
        }
        if (mediaSearchQuery) {
            const id = (item.key || '').toLowerCase();
            const name = (item.filename || '').toLowerCase();
            const node = (item.worker_node_id || item.placement?.node_id || '').toLowerCase();
            if (!id.includes(mediaSearchQuery) && !name.includes(mediaSearchQuery) && !node.includes(mediaSearchQuery)) {
                return false;
            }
        }
        return true;
    });

    const totalItems = filtered.length;
    const totalPages = Math.max(1, Math.ceil(totalItems / mediaPageSize));
    if (mediaCurrentPage > totalPages) mediaCurrentPage = totalPages;

    const startIndex = (mediaCurrentPage - 1) * mediaPageSize;
    const endIndex = Math.min(startIndex + mediaPageSize, totalItems);
    const paginatedItems = filtered.slice(startIndex, endIndex);

    const infoEl = document.getElementById('media-pagination-info');
    if (infoEl) {
        if (totalItems === 0) {
            infoEl.innerText = 'Showing 0 to 0 of 0 entries';
        } else {
            infoEl.innerText = `Showing ${startIndex + 1} to ${endIndex} of ${totalItems} entries`;
        }
    }

    renderPaginationButtons(totalPages);

    if (paginatedItems.length === 0) {
        tbody.innerHTML = `
            <tr>
                <td colspan="7" style="text-align: center; padding: 40px; color: var(--text-muted);">
                    No media items match your search or filter.
                </td>
            </tr>`;
        return;
    }

    tbody.innerHTML = paginatedItems.map(item => {
        const isReady = item.state === 'completed';
        const isFailed = item.state === 'failed';
        const filename = item.filename || item.key;
        const sizeStr = item.size_bytes ? formatBytes(item.size_bytes) : '-';
        const dateStr = item.created_at ? new Date(item.created_at).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' }) : '-';
        let meta = item.metadata;
        const specsHtml = renderMediaSpecs(meta);

        // Auto-fetch metadata if not cached yet on completed asset
        if (isReady && (!meta || !meta.video) && !item._fetchingMeta) {
            item._fetchingMeta = true;
            const metaUrl = item.stream_url ? (item.stream_url.replace(/\/+$/, '') + '/metadata.json') : (`/stream/${encodeURIComponent(item.key)}/metadata.json`);
            fetch(metaUrl)
                .then(r => r.ok ? r.json() : null)
                .then(m => {
                    if (m && (m.video || m.audio_tracks)) {
                        item.metadata = m;
                        const el = document.getElementById(`specs-cell-${item.key}`);
                        if (el) {
                            el.innerHTML = renderMediaSpecs(m);
                        }
                    }
                })
                .catch(() => {});
        }

        // 2. Storage Location & Pipeline Routing rendering
        const placement = item.placement || {};
        const storageNode = placement.node_id || item.worker_node_id || item.node_id || 'Auto';
        const computeNode = item.node_id || item.worker_node_id || '';
        const tierLabel = placement.tier_label || (placement.tier_id === 2 ? 'tier2' : (placement.tier_id === 1 ? 'tier1' : 'storage'));
        const drivePath = placement.path || '/';
        const isDecoupled = computeNode && storageNode && computeNode !== storageNode;
        const hasCustomHost = placement.public_host ? true : false;

        const locationHtml = `
            <div style="display:flex; flex-direction:column; gap:4px;">
                ${isDecoupled ? `
                    <div style="display:inline-flex; align-items:center; gap:5px; flex-wrap:wrap;">
                        <span class="badge-compute" title="Processing Worker: Ingest & Remux performed here">${escapeHtml(computeNode)}</span>
                        <span style="display:inline-flex; align-items:center; gap:5px; white-space:nowrap;">
                            <span style="color:var(--text-muted);font-size:11px;font-weight:700;">&rarr;</span>
                            <span class="badge-location" title="Final Storage Node: Stored here">${escapeHtml(storageNode)}</span>
                        </span>
                    </div>
                ` : `
                    <div style="display:inline-flex; align-items:center; gap:5px;">
                        <span class="badge-location" title="Direct Compute & Storage Node">${escapeHtml(storageNode)}</span>
                    </div>
                `}
                <div style="font-size:11px; color:var(--text-secondary); display:flex; align-items:center; gap:5px; flex-wrap:wrap;">
                    <span class="badge-drive" title="Disk mount point">${escapeHtml(drivePath)}</span>
                    <span class="badge-tier" title="Storage Tier">${escapeHtml(tierLabel)}</span>
                </div>
                ${hasCustomHost ? `<div style="font-size:10px; font-weight:700; color:var(--orange-primary);">Custom CDN / Host</div>` : ''}
            </div>
        `;

        // 3. Status & Pipeline rendering
        let statusHtml = '';
        if (isReady) {
            statusHtml = `<span class="badge-cmaf">✓ Ready Stream</span>`;
        } else if (isFailed) {
            statusHtml = `
                <div style="max-width:240px;">
                    <span style="color:var(--status-offline);font-weight:700;font-size:12px;display:inline-flex;align-items:center;gap:4px;">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"></circle><line x1="15" y1="9" x2="9" y2="15"></line><line x1="9" y1="9" x2="15" y2="15"></line></svg>
                        Failed
                    </span>
                    <div style="font-size:11px;color:var(--status-offline);margin-top:3px;line-height:1.35;word-break:break-word;font-family:system-ui,sans-serif;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;max-width:260px;" title="${escapeHtml(item.error || 'Download or packaging error')}">
                        ${escapeHtml(item.error || 'Download error')}
                    </div>
                </div>`;
        } else {
            const pct = Math.min(100, Math.max(0, item.progress_percent || 0)).toFixed(1);
            const stageLabel = item.stage_name || item.state.replace('_', ' ');
            const speedDisplay = item.speed || '';
            let byteProgress = '';
            if (item.transferred_bytes && item.total_bytes && item.total_bytes > 0) {
                byteProgress = `${formatBytes(item.transferred_bytes)} / ${formatBytes(item.total_bytes)}`;
            }

            statusHtml = `
                <div style="width:100%;max-width:300px;">
                    ${renderPipelineStepper(item)}
                    <div style="display:flex;justify-content:space-between;align-items:center;font-size:11px;font-weight:700;margin-bottom:4px;">
                        <span style="color:var(--orange-primary);display:inline-flex;align-items:center;gap:5px;text-transform:capitalize;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:160px;" title="${escapeHtml(stageLabel)}">
                            <svg class="spin-fast" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"></circle><path d="M12 2a10 10 0 0 1 10 10"></path></svg>
                            ${escapeHtml(stageLabel)}
                        </span>
                        <span style="color:var(--text-primary);font-family:'JetBrains Mono',monospace;font-size:12px;">${pct}%</span>
                    </div>
                    <div class="gauge-track" style="height:7px;background:var(--bg-body);border:1px solid var(--border-light);border-radius:5px;overflow:hidden;">
                        <div class="gauge-fill" style="width:${pct}%;background:linear-gradient(90deg, var(--orange-primary), #ff9f43);height:100%;transition:width 0.4s ease;box-shadow:0 0 8px rgba(255,122,24,0.4);"></div>
                    </div>
                    <div style="display:flex;justify-content:space-between;align-items:center;font-size:10.5px;color:var(--text-secondary);margin-top:4px;font-family:'JetBrains Mono',monospace;">
                        ${speedDisplay ? `<span>⚡ ${escapeHtml(speedDisplay)}</span>` : `<span></span>`}
                        ${byteProgress ? `<span style="font-weight:600;">${escapeHtml(byteProgress)}</span>` : ''}
                    </div>
                    ${item.details ? `<div style="font-size:9.5px;color:var(--text-muted);margin-top:2px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${escapeHtml(item.details)}">${escapeHtml(item.details)}</div>` : ''}
                </div>`;
        }

        return `
            <tr>
                <td>
                    <div style="font-weight:700;color:var(--text-primary);max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${escapeHtml(filename)}">
                        ${escapeHtml(filename)}
                    </div>
                    <div style="font-size:11px;color:var(--text-muted);font-family:'JetBrains Mono',monospace;margin-top:3px;max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">
                        ${escapeHtml(item.key)} &bull; <span style="opacity:0.8;">${dateStr}</span>
                    </div>
                </td>
                <td id="specs-cell-${escapeHtml(item.key)}">
                    ${specsHtml}
                </td>
                <td>
                    ${locationHtml}
                </td>
                <td>
                    <strong style="color:var(--text-primary);font-family:'JetBrains Mono',monospace;font-size:12px;">${sizeStr}</strong>
                </td>
                <td>
                    ${statusHtml}
                </td>
                <td style="text-align:right;white-space:nowrap;">
                    <div style="display:flex; align-items:center; justify-content:flex-end; gap:6px;">
                        ${isReady ? `
                            <button class="btn btn-primary" style="padding:5px 12px;font-size:12px;font-weight:700;display:inline-flex;align-items:center;gap:5px;box-shadow:0 2px 8px rgba(255,122,24,0.35);" onclick="openStreamDetailsModal('${escapeHtml(item.key)}')">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>
                                <span>Play</span>
                            </button>
                        ` : ''}
                        <button class="btn btn-secondary" style="padding:5px 9px;font-size:12px;color:var(--status-offline);border-color:rgba(239,68,68,0.25);" onclick="deleteMediaConfirm('${escapeHtml(item.key)}')" title="Delete from disk and database">
                            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                        </button>
                    </div>
                </td>
            </tr>
        `;
    }).join('');
}

function renderPaginationButtons(totalPages) {
    const container = document.getElementById('media-pagination-buttons');
    if (!container) return;

    if (totalPages <= 1) {
        container.innerHTML = '';
        return;
    }

    let html = '';
    html += `<button class="page-btn" ${mediaCurrentPage === 1 ? 'disabled' : ''} onclick="goToMediaPage(${mediaCurrentPage - 1})">&laquo; Prev</button>`;

    for (let i = 1; i <= totalPages; i++) {
        if (i === 1 || i === totalPages || (i >= mediaCurrentPage - 1 && i <= mediaCurrentPage + 1)) {
            html += `<button class="page-btn ${i === mediaCurrentPage ? 'active' : ''}" onclick="goToMediaPage(${i})">${i}</button>`;
        } else if (i === mediaCurrentPage - 2 || i === mediaCurrentPage + 2) {
            html += `<span style="padding:0 4px;color:var(--text-muted);display:inline-flex;align-items:center;">...</span>`;
        }
    }

    html += `<button class="page-btn" ${mediaCurrentPage === totalPages ? 'disabled' : ''} onclick="goToMediaPage(${mediaCurrentPage + 1})">Next &raquo;</button>`;

    container.innerHTML = html;
}

function goToMediaPage(page) {
    mediaCurrentPage = page;
    renderMediaTable();
}

function openIngestURLModal() {
    const modal = document.getElementById('modal-ingest-url');
    if (!modal) return;
    
    const select = document.getElementById('ingest-node-select');
    if (select && cachedPoolData) {
        select.innerHTML = '<option value="auto">⚡ Auto (Least-loaded optimal worker node)</option>';
        (cachedPoolData.nodes || []).forEach(n => {
            if (n.status === 'online') {
                select.innerHTML += `<option value="${escapeHtml(n.metrics.node_id)}">${escapeHtml(n.metrics.node_id)} (CPU: ${(n.metrics.cpu.used_percent || 0).toFixed(1)}%)</option>`;
            }
        });
    }

    const input = document.getElementById('ingest-url-input');
    if (input) input.value = '';
    modal.classList.add('active');
}

function closeIngestURLModal() {
    const modal = document.getElementById('modal-ingest-url');
    if (modal) modal.classList.remove('active');
}

async function submitIngestURL() {
    const urlInput = document.getElementById('ingest-url-input');
    const submitBtn = document.getElementById('btn-submit-ingest');

    if (!urlInput || !urlInput.value.trim()) {
        alert('Please enter a valid video download URL');
        return;
    }

    submitBtn.disabled = true;
    submitBtn.innerText = 'Starting...';

    try {
        const resp = await fetch('/api/v1/files', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url: urlInput.value.trim() })
        });

        if (resp.ok || resp.status === 201) {
            closeIngestURLModal();
            fetchMediaList();
        } else {
            const errText = await resp.text();
            alert('Ingest error: ' + errText);
        }
    } catch (err) {
        alert('Network error: ' + err.message);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerText = 'Start Ingest';
    }
}

function openUploadModal() {
    const modal = document.getElementById('modal-upload-file');
    if (!modal) return;
    selectedUploadFile = null;
    document.getElementById('upload-filename-text').innerText = 'Click to choose or drag & drop video file';
    document.getElementById('upload-progress-container').style.display = 'none';
    document.getElementById('btn-submit-upload').disabled = true;
    modal.classList.add('active');
}

function closeUploadModal() {
    const modal = document.getElementById('modal-upload-file');
    if (modal) modal.classList.remove('active');
}

function onFileSelected(input) {
    if (input.files && input.files[0]) {
        selectedUploadFile = input.files[0];
        document.getElementById('upload-filename-text').innerText = `Selected: ${selectedUploadFile.name} (${formatBytes(selectedUploadFile.size)})`;
        document.getElementById('btn-submit-upload').disabled = false;
    }
}

async function startFileUpload() {
    if (!selectedUploadFile) return;

    const progressContainer = document.getElementById('upload-progress-container');
    const progressBar = document.getElementById('upload-progress-bar');
    const pctText = document.getElementById('upload-pct-text');
    const statusText = document.getElementById('upload-status-text');
    const submitBtn = document.getElementById('btn-submit-upload');

    progressContainer.style.display = 'block';
    submitBtn.disabled = true;
    statusText.innerText = 'Reserving upload slot...';

    let uploadURL = '';
    try {
        const reserve = await fetch('/api/v1/files-upload', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                filename: selectedUploadFile.name,
                size_bytes: selectedUploadFile.size
            })
        });
        if (!reserve.ok) {
            alert('Reserve error: ' + await reserve.text());
            submitBtn.disabled = false;
            return;
        }
        const reserved = await reserve.json();
        uploadURL = reserved.upload_url;
        if (!uploadURL) {
            alert('No upload_url returned from coordinator');
            submitBtn.disabled = false;
            return;
        }
    } catch (err) {
        alert('Network error: ' + err.message);
        submitBtn.disabled = false;
        return;
    }

    const formData = new FormData();
    formData.append('file', selectedUploadFile);

    const xhr = new XMLHttpRequest();
    xhr.open('POST', uploadURL, true);

    xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) {
            const pct = (e.loaded / e.total) * 100;
            progressBar.style.width = `${pct}%`;
            pctText.innerText = `${pct.toFixed(0)}%`;
            if (pct >= 100) {
                statusText.innerText = 'Packaging CMAF & extracting audio...';
            }
        }
    };

    xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
            statusText.innerText = 'Upload complete! Packaging in background...';
            setTimeout(() => {
                closeUploadModal();
                fetchMediaList();
            }, 500);
        } else {
            alert('Upload error: ' + xhr.responseText);
            submitBtn.disabled = false;
        }
    };

    xhr.onerror = () => {
        alert('Network error during upload');
        submitBtn.disabled = false;
    };

    statusText.innerText = 'Uploading to worker...';
    xhr.send(formData);
}

// =============================================================================
// Storage Audit & Reconcile Engine
// =============================================================================

async function triggerStorageSync() {
    const btn = document.getElementById('btn-sync-storage');
    const icon = document.getElementById('icon-sync-storage');
    const text = document.getElementById('text-sync-storage');
    if (btn) btn.disabled = true;
    if (icon) icon.classList.add('spin-fast');
    if (text) text.innerText = 'Auditing nodes...';

    try {
        const resp = await fetch('/api/v1/files-sync', { method: 'POST' });
        const data = await resp.json();
        if (resp.ok) {
            let msg = `Storage audit complete!`;
            if (data.purged > 0) {
                msg += ` Cleaned ${data.purged} deleted ghost file(s).`;
            } else {
                msg += ` All physical disks are 100% in sync.`;
            }
            msg += ` (${data.remaining} active media assets)`;
            alert(msg);
            await fetchMediaList();
            if (typeof fetchPoolMetrics === 'function') {
                fetchPoolMetrics();
            }
        } else {
            alert('Storage sync error: ' + (data.error || 'Server error'));
        }
    } catch(err) {
        alert('Network error during storage audit: ' + err.message);
    } finally {
        if (btn) btn.disabled = false;
        if (icon) icon.classList.remove('spin-fast');
        if (text) text.innerText = 'Audit & Sync Storage';
    }
}

// =============================================================================
// ArtPlayer Studio & Multi-Audio Playback Controller
// =============================================================================

let currentArtPlayer = null;
let lockstepController = null;

async function openStreamDetailsModal(mediaID) {
    const modal = document.getElementById('modal-stream-details');
    if (!modal) return;

    const job = mediaList.find(m => m.key === mediaID);
    if (!job) return;

    const filename = job.filename || mediaID;
    safeSetText('stream-modal-title', filename);

    const streamUrl = job.stream_url || `${window.location.origin}/stream/${encodeURIComponent(mediaID)}`;

    const directInput = document.getElementById('stream-direct-url');
    if (directInput) directInput.value = streamUrl;

    // Badges
    const badgesWrap = document.getElementById('stream-modal-badges');
    if (badgesWrap) {
        const computeNode = job.node_id || job.worker_node_id || '';
        const storageNode = job.placement?.node_id || computeNode || 'Auto';
        const drive = job.placement?.path || '/';
        const tier = job.placement?.tier_label || (job.placement?.tier_id === 2 ? 'tier2' : 'tier1');
        const isDecoupled = computeNode && storageNode && computeNode !== storageNode;

        let badges = `<span style="font-size:11px;color:var(--text-muted);font-family:'JetBrains Mono',monospace;">ID: ${escapeHtml(mediaID)}</span>`;
        if (isDecoupled) {
            badges += `<span class="badge-compute" title="Processed on ${escapeHtml(computeNode)}">${escapeHtml(computeNode)}</span>`;
            badges += `<span style="color:var(--text-muted);font-size:10px;">➔</span>`;
            badges += `<span class="badge-location" title="Stored on ${escapeHtml(storageNode)}">${escapeHtml(storageNode)}</span>`;
        } else {
            badges += `<span class="badge-location">${escapeHtml(storageNode)}</span>`;
        }
        badges += `<span class="badge-drive">${escapeHtml(drive)}</span>`;
        badges += `<span class="badge-tier">${escapeHtml(tier)}</span>`;
        badgesWrap.innerHTML = badges;
    }

    modal.classList.add('active');

    // Fetch live metadata if not cached
    let meta = job.metadata;
    if (!meta) {
        try {
            const metaUrl = `${window.location.origin}/stream/${encodeURIComponent(mediaID)}/metadata.json`;
            const r = await fetch(metaUrl);
            if (r.ok) {
                meta = await r.json();
                job.metadata = meta;
            }
        } catch(e) {
            console.warn('Could not fetch metadata.json:', e);
        }
    }

    // Pattern 3: Prefer native HLS master playlist for seamless multi-audio track switching & MSE hardware sync
    let activeStreamUrl = streamUrl;
    const baseDirUrl = `${window.location.origin}/stream/${encodeURIComponent(mediaID)}`;
    if (meta && (meta.master_m3u8 || (meta.audio_tracks && meta.audio_tracks.length > 1))) {
        const candidateHlsUrl = `${baseDirUrl}/${meta.master_m3u8 || 'master.m3u8'}`;
        try {
            const check = await fetch(candidateHlsUrl, { method: 'HEAD' });
            if (check.ok) {
                activeStreamUrl = candidateHlsUrl;
            }
        } catch (_) {}
    }

    if (directInput) directInput.value = activeStreamUrl;

    // Render Audio / Subtitle Drawers
    renderMediaModalDrawers(job, meta, activeStreamUrl);

    // Initialize ArtPlayer
    initArtPlayer(activeStreamUrl, job, meta);
}

function renderMediaModalDrawers(job, meta, streamUrl) {
    const tracksInfo = document.getElementById('stream-modal-tracks-info');
    const audioDrawer = document.getElementById('stream-modal-audio-drawer');
    const audioList = document.getElementById('stream-modal-audio-list');
    const subDrawer = document.getElementById('stream-modal-sub-drawer');
    const subList = document.getElementById('stream-modal-sub-list');

    const key = job.key;
    const baseDirUrl = `${window.location.origin}/stream/${encodeURIComponent(key)}`;

    if (meta && meta.audio_tracks && meta.audio_tracks.length > 1) {
        if (tracksInfo) {
            tracksInfo.style.display = 'block';
            tracksInfo.innerHTML = `
                <div style="display:flex;align-items:center;gap:9px;font-weight:800;font-size:12.5px;color:var(--orange-dark);">
                    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"></path><circle cx="6" cy="18" r="3"></circle><circle cx="18" cy="16" r="3"></circle></svg>
                    <span>Studio Multi-Audio Engine Active (${meta.audio_tracks.length} Tracks)</span>
                </div>
                <div style="color:var(--text-secondary);margin-top:4px;font-size:11.5px;line-height:1.5;">
                    Switch languages seamlessly via ArtPlayer's <strong>Settings &rarr; Audio Track</strong> menu (${meta.audio_tracks.map(t => t.title || t.language).join(', ')}).
                </div>
            `;
        }

        if (audioDrawer && audioList) {
            audioDrawer.style.display = 'block';
            audioList.innerHTML = meta.audio_tracks.map((t, idx) => {
                const trackUrl = `${baseDirUrl}/${t.file || `audio_${t.index}_${t.language}.m4a`}`;
                const title = t.title || t.language || `Track ${idx + 1}`;
                return `
                    <div class="track-row">
                        <span class="track-row-title">
                            <span class="track-icon"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"></path><circle cx="6" cy="18" r="3"></circle><circle cx="18" cy="16" r="3"></circle></svg></span>
                            ${escapeHtml(title)}
                            <span class="track-row-meta">${escapeHtml(t.codec || 'aac')} &middot; ${t.channels || 2}ch</span>
                        </span>
                        <span class="track-chip">#${idx + 1}</span>
                        <button class="btn btn-secondary" style="padding:4px 12px;font-size:11px;" onclick="copyToClipboard('${escapeHtml(trackUrl)}', 'Audio URL copied!')">Copy Link</button>
                    </div>
                `;
            }).join('');
        }
    } else {
        if (tracksInfo) tracksInfo.style.display = 'none';
        if (audioDrawer) audioDrawer.style.display = 'none';
    }

    if (meta && meta.subtitles && meta.subtitles.length > 0) {
        if (subDrawer && subList) {
            subDrawer.style.display = 'block';
            subList.innerHTML = meta.subtitles.map(s => {
                const subUrl = `${baseDirUrl}/${s.file || `subtitle_${s.index}_${s.language}.vtt`}`;
                const title = s.title || s.language || 'Subtitles';
                return `
                    <div class="track-row">
                        <span class="track-row-title">
                            <span class="track-icon"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="4" width="20" height="16" rx="2"></rect><path d="M6 16h4M14 16h4M4 12h2M9 12h6M18 12h2"></path></svg></span>
                            ${escapeHtml(title)}
                            <span class="track-row-meta">WebVTT</span>
                        </span>
                        <button class="btn btn-secondary" style="padding:4px 12px;font-size:11px;" onclick="copyToClipboard('${escapeHtml(subUrl)}', 'Subtitle URL copied!')">Copy VTT</button>
                    </div>
                `;
            }).join('');
        }
    } else {
        if (subDrawer) subDrawer.style.display = 'none';
    }
}

// Cinema overlay state: loading spinner until first frame, friendly error card on failure.
let cinemaRetryCtx = null;
let cinemaLoadTimer = null;

function cinemaReady() {
    clearTimeout(cinemaLoadTimer);
    const errorBox = document.getElementById('cinema-error');
    if (errorBox) errorBox.style.display = 'none';
}

function cinemaFail(job, reason) {
    clearTimeout(cinemaLoadTimer);
    if (currentArtPlayer && currentArtPlayer.notice) {
        try { currentArtPlayer.notice.show = ''; } catch (_) {}
    }
    const box = document.getElementById('cinema-error');
    if (!box) return;
    box.style.display = 'flex';
    box.innerHTML = `
        <div class="cinema-error-icon">
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
        </div>
        <strong>Stream Unavailable</strong>
        <span>${escapeHtml(job.filename || job.key)} &mdash; ${escapeHtml(reason || 'The storage node did not respond. It may be offline, or the file was moved or deleted.')}</span>
        <button class="cinema-retry-btn" onclick="retryArtPlayer()">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"></path></svg>
            Retry
        </button>
    `;
}

function retryArtPlayer() {
    if (!cinemaRetryCtx) return;
    closeArtPlayer();
    initArtPlayer(cinemaRetryCtx.streamUrl, cinemaRetryCtx.job, cinemaRetryCtx.meta);
}

function mountFallbackVideo(container, streamUrl, job) {
    const video = document.createElement('video');
    video.src = streamUrl;
    video.controls = true;
    video.autoplay = true;
    video.addEventListener('error', () => cinemaFail(job));
    container.appendChild(video);
}

function initArtPlayer(streamUrl, job, meta) {
    const container = document.getElementById('artplayer-container');
    if (!container) return;
    container.innerHTML = '';
    cinemaRetryCtx = { streamUrl, job, meta };
    const errorBox = document.getElementById('cinema-error');
    if (errorBox) {
        errorBox.style.display = 'none';
        errorBox.innerHTML = '';
    }

    const filename = job.filename || job.key;
    const baseDirUrl = `${window.location.origin}/stream/${encodeURIComponent(job.key)}`;
    const hasMultiAudio = meta && meta.audio_tracks && meta.audio_tracks.length > 1;

    // Subtitle setup
    let subtitleOption = undefined;
    if (meta && meta.subtitles && meta.subtitles.length > 0) {
        const firstSub = meta.subtitles[0];
        if (firstSub && (firstSub.file || firstSub.language)) {
            subtitleOption = {
                url: `${baseDirUrl}/${firstSub.file || `subtitle_${firstSub.index}_${firstSub.language}.vtt`}`,
                type: 'vtt',
                style: {
                    color: '#ffffff',
                    fontSize: '20px',
                    textShadow: '0 2px 4px rgba(0,0,0,0.8)'
                },
                encoding: 'utf-8'
            };
        }
    }

    // Custom Settings Menu
    const customSettings = [];

    // 1. Audio Track Selector (inside ArtPlayer's settings menu)
    if (hasMultiAudio) {
        const activeTrack = meta.audio_tracks[0];
        const audioSelectorItems = meta.audio_tracks.map((t, idx) => ({
            default: idx === 0,
            html: `<span style="display:inline-flex;align-items:center;gap:6px;">🎵 ${escapeHtml(t.title || t.language || `Track ${idx+1}`)} <span style="font-size:10px;opacity:0.6;">(${escapeHtml(t.codec || 'aac')}, ${t.channels || 2}ch)</span></span>`,
            url: `${baseDirUrl}/${t.file || `audio_${t.index}_${t.language}.m4a`}`,
            track: t,
            index: idx,
        }));

        customSettings.push({
            width: 240,
            html: 'Audio Track',
            name: 'audio_track',
            tooltip: activeTrack.title || activeTrack.language || 'Default',
            selector: audioSelectorItems,
            onSelect: function(item) {
                switchArtPlayerAudio(item.index, item.url);
                return item.track.title || item.track.language;
            }
        });
    }

    // 2. Subtitles Selector (inside ArtPlayer's settings menu)
    if (meta && meta.subtitles && meta.subtitles.length > 0) {
        const subItems = meta.subtitles.map((s, idx) => ({
            default: idx === 0,
            html: `<span style="display:inline-flex;align-items:center;gap:6px;">💬 ${escapeHtml(s.title || s.language || `Subtitle ${idx+1}`)} <span style="font-size:10px;opacity:0.6;">(VTT)</span></span>`,
            url: `${baseDirUrl}/${s.file || `subtitle_${s.index}_${s.language}.vtt`}`,
            sub: s,
        }));
        subItems.push({
            default: false,
            html: '<span>❌ Turn Off Subtitles</span>',
            url: '',
            sub: null,
        });

        customSettings.push({
            width: 240,
            html: 'Subtitles',
            name: 'subtitles',
            tooltip: meta.subtitles[0].title || meta.subtitles[0].language || 'English',
            selector: subItems,
            onSelect: function(item) {
                if (currentArtPlayer && currentArtPlayer.subtitle) {
                    if (item.url) {
                        currentArtPlayer.subtitle.show = true;
                        currentArtPlayer.subtitle.switch(item.url, { name: item.sub?.language || 'Sub' });
                    } else {
                        currentArtPlayer.subtitle.show = false;
                    }
                }
                return item.sub ? (item.sub.title || item.sub.language) : 'Off';
            }
        });
    }

    // Check Artplayer availability
    const ArtClass = window.Artplayer || window.ArtPlayer;
    if (!ArtClass) {
        console.warn('Artplayer library not loaded, using HTML5 fallback');
        mountFallbackVideo(container, streamUrl, job);
        return;
    }

    // Custom Controls bar items
    const customControls = [];
    if (hasMultiAudio) {
        customControls.push({
            position: 'right',
            html: `<span style="display:inline-flex;align-items:center;gap:4px;font-size:12px;font-weight:700;color:var(--orange-primary);cursor:pointer;padding:0 8px;">🎵 Audio</span>`,
            tooltip: 'Switch Audio Track',
            click: function() {
                if (currentArtPlayer && currentArtPlayer.setting) {
                    currentArtPlayer.setting.show = true;
                }
            }
        });
    }

    try {
        const artOptions = {
            container: '#artplayer-container',
            url: streamUrl,
            type: streamUrl.includes('.m3u8') ? 'm3u8' : 'mp4',
            title: filename,
            volume: 0.8,
            isLive: false,
            muted: false,
            autoplay: true,
            pip: true,
            autoSize: false,
            autoMini: true,
            screenshot: true,
            setting: true,
            loop: false,
            playbackRate: true,
            aspectRatio: true,
            fullscreen: true,
            fullscreenWeb: true,
            subtitleOffset: true,
            miniProgressBar: true,
            theme: '#ff7a18',
            moreVideoAttr: {
                crossOrigin: 'anonymous',
                playsInline: 'true',
            },
            settings: customSettings,
            controls: customControls,
            customType: {
                m3u8: function(video, url, art) {
                    if (window.Hls && Hls.isSupported()) {
                        if (art.hls) {
                            art.hls.destroy();
                        }
                        const hls = new Hls({
                            enableWorker: true,
                            lowLatencyMode: false,
                            backBufferLength: 90
                        });
                        hls.loadSource(url);
                        hls.attachMedia(video);
                        art.hls = hls;
                        art.on('destroy', () => hls.destroy());

                        hls.on(Hls.Events.MANIFEST_PARSED, () => {
                            console.log('[HLS] Manifest parsed, stream ready');
                            cinemaReady();
                        });

                        hls.on(Hls.Events.AUDIO_TRACK_SWITCHED, (event, data) => {
                            console.log('[HLS] Audio track switched to:', data.id);
                        });
                        hls.on(Hls.Events.ERROR, (event, data) => {
                            if (data && data.fatal) {
                                console.error('[HLS] Fatal error:', data);
                                switch (data.type) {
                                    case Hls.ErrorTypes.NETWORK_ERROR:
                                        hls.startLoad();
                                        break;
                                    case Hls.ErrorTypes.MEDIA_ERROR:
                                        hls.recoverMediaError();
                                        break;
                                    default:
                                        cinemaFail(job, 'HLS error: ' + data.details);
                                        break;
                                }
                            }
                        });
                    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
                        video.src = url;
                    }
                }
            }
        };

        if (subtitleOption) {
            artOptions.subtitle = subtitleOption;
        }

        currentArtPlayer = new ArtClass(artOptions);
        currentArtPlayer.on('ready', cinemaReady);
        currentArtPlayer.on('video:canplay', cinemaReady);
        currentArtPlayer.on('error', () => cinemaFail(job));
        clearTimeout(cinemaLoadTimer);
        cinemaLoadTimer = setTimeout(() => cinemaFail(job, 'Timed out while connecting to the storage node.'), 25000);

        const isHLS = streamUrl.includes('.m3u8');
        if (hasMultiAudio && !isHLS) {
            setupExternalAudioSync(currentArtPlayer, meta.audio_tracks, baseDirUrl);
        }
    } catch(err) {
        console.error('ArtPlayer init error:', err);
        mountFallbackVideo(container, streamUrl, job);
    }
}

// =============================================================================
// Lockstep Audio-Video Master-Slave Sync Engine
// =============================================================================
class LockstepAudioController {
    constructor(art, initialAudioUrl) {
        this.art = art;
        this.video = art.video;
        this.audio = new Audio();
        this.audio.crossOrigin = 'anonymous';
        this.audio.preload = 'auto';
        this.active = true;
        this.isSeeking = false;
        this.syncInterval = null;

        // Initialize audio source & mirror controls
        this.audio.src = initialAudioUrl;
        this.audio.volume = this.video.volume;
        this.audio.muted = this.video.muted;
        this.audio.load();

        this.bindEvents();
    }

    bindEvents() {
        const v = this.video;
        const a = this.audio;
        const art = this.art;

        // Volume & Mute mirror
        art.on('video:volumechange', () => {
            if (!this.active || !this.audio) return;
            a.volume = v.volume;
            a.muted = v.muted;
        });

        // 1. SEEKING: User drags timeline or jumps
        art.on('video:seeking', () => {
            if (!this.active || !this.audio) return;
            this.isSeeking = true;
            a.pause();
            a.currentTime = v.currentTime;
        });

        art.on('video:seeked', () => {
            if (!this.active || !this.audio) return;
            a.currentTime = v.currentTime;
            // Never play audio here! Audio MUST wait for video frame to render (video:playing)
        });

        // 2. BUFFERING: If video buffers, audio stops immediately
        art.on('video:waiting', () => {
            if (!this.active || !this.audio) return;
            a.pause();
        });

        art.on('video:pause', () => {
            if (!this.active || !this.audio) return;
            a.pause();
        });

        // 3. PLAYING: The golden event. Video has decoded and is moving on screen!
        art.on('video:playing', () => {
            if (!this.active || !this.audio || !this.audio.src) return;
            this.isSeeking = false;

            const startAudio = () => {
                if (!this.active || !this.audio || v.paused) return;
                const diff = Math.abs(v.currentTime - a.currentTime);
                if (diff > 0.03) {
                    a.currentTime = v.currentTime;
                }
                a.playbackRate = v.playbackRate;
                a.play().catch(() => {});
            };

            if (a.readyState >= 2) {
                startAudio();
            } else {
                a.addEventListener('canplay', startAudio, { once: true });
            }
        });

        art.on('video:ratechange', () => {
            if (!this.active || !this.audio) return;
            a.playbackRate = v.playbackRate;
        });

        // 4. Audio buffering guard: If audio buffers, pause video until audio catches up
        a.addEventListener('waiting', () => {
            if (!this.active || !this.video || v.paused) return;
            v.pause();
            const onAudioResume = () => {
                a.removeEventListener('canplay', onAudioResume);
                if (this.active && this.video && v.paused) {
                    v.play().catch(() => {});
                }
            };
            a.addEventListener('canplay', onAudioResume);
        });

        // 5. Continuous drift steering (checked every 200ms)
        this.syncInterval = setInterval(() => {
            if (!this.active || !this.audio || v.paused || v.seeking || this.isSeeking || a.paused) return;

            const diff = v.currentTime - a.currentTime;
            const absDiff = Math.abs(diff);

            if (absDiff > 0.3) {
                // Major drift -> snap directly
                a.currentTime = v.currentTime;
                a.playbackRate = v.playbackRate;
            } else if (diff > 0.03) {
                // Audio is slightly behind -> glide 3% faster to catch up smoothly
                a.playbackRate = v.playbackRate * 1.03;
            } else if (diff < -0.03) {
                // Audio is slightly ahead -> glide 3% slower to let video catch up
                a.playbackRate = v.playbackRate * 0.97;
            } else {
                // In sync within 30ms -> match video rate
                if (a.playbackRate !== v.playbackRate) {
                    a.playbackRate = v.playbackRate;
                }
            }
        }, 200);
    }

    switchTrack(newUrl) {
        if (!this.active || !this.audio) return;
        const v = this.video;
        const a = this.audio;
        const wasPlaying = !v.paused;

        // Pause both while switching
        v.pause();
        a.pause();

        a.src = newUrl;
        a.volume = v.volume;
        a.muted = v.muted;

        const onReady = () => {
            a.removeEventListener('canplay', onReady);
            if (!this.active || !this.audio) return;
            a.currentTime = v.currentTime;
            if (wasPlaying) {
                v.play().catch(() => {});
            }
        };
        a.addEventListener('canplay', onReady, { once: true });
        a.load();
    }

    destroy() {
        this.active = false;
        if (this.syncInterval) {
            clearInterval(this.syncInterval);
            this.syncInterval = null;
        }
        if (this.audio) {
            this.audio.pause();
            this.audio.removeAttribute('src');
            this.audio.load();
            this.audio = null;
        }
    }
}

function setupExternalAudioSync(art, audioTracks, baseDirUrl) {
    if (!audioTracks || audioTracks.length === 0) return;
    if (lockstepController) {
        lockstepController.destroy();
        lockstepController = null;
    }
    const firstTrack = audioTracks[0];
    const firstUrl = `${baseDirUrl}/${firstTrack.file || `audio_${firstTrack.index}_${firstTrack.language}.m4a`}`;
    lockstepController = new LockstepAudioController(art, firstUrl);
}

function switchArtPlayerAudio(trackIndex, url) {
    if (currentArtPlayer && currentArtPlayer.hls) {
        currentArtPlayer.hls.audioTrack = trackIndex;
        console.log('[HLS] Switched audio track index to:', trackIndex);
    } else if (lockstepController) {
        lockstepController.switchTrack(url);
    }
}

function closeArtPlayer() {
    if (lockstepController) {
        lockstepController.destroy();
        lockstepController = null;
    }
    if (currentArtPlayer) {
        if (currentArtPlayer.hls) {
            try { currentArtPlayer.hls.destroy(); } catch (_) {}
        }
        try { currentArtPlayer.destroy(false); } catch (_) {}
        currentArtPlayer = null;
    }
    const container = document.getElementById('artplayer-container');
    if (container) container.innerHTML = '';
    const errorBox = document.getElementById('cinema-error');
    if (errorBox) {
        errorBox.style.display = 'none';
        errorBox.innerHTML = '';
    }
    if (cinemaLoadTimer) {
        clearTimeout(cinemaLoadTimer);
        cinemaLoadTimer = null;
    }
    cinemaRetryCtx = null;
}

function closeStreamDetailsModal() {
    const modal = document.getElementById('modal-stream-details');
    if (modal) {
        modal.classList.remove('active');
        closeArtPlayer();
    }
}

function copyDirectStreamURL() {
    const input = document.getElementById('stream-direct-url');
    if (input) copyToClipboard(input.value, 'Direct Streaming URL copied!');
}

function copyToClipboard(text, alertMsg) {
    if (!text) return;
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
        navigator.clipboard.writeText(text).then(() => {
            alert(alertMsg || 'Copied to clipboard!');
        }).catch(() => {
            fallbackCopyText(text, alertMsg);
        });
    } else {
        fallbackCopyText(text, alertMsg);
    }
}

function fallbackCopyText(text, alertMsg) {
    try {
        const textArea = document.createElement("textarea");
        textArea.value = text;
        textArea.style.position = "fixed";
        textArea.style.left = "-999999px";
        textArea.style.top = "-999999px";
        textArea.setAttribute("readonly", "");
        document.body.appendChild(textArea);
        textArea.focus();
        textArea.select();
        const successful = document.execCommand('copy');
        document.body.removeChild(textArea);
        if (successful) {
            alert(alertMsg || 'Copied to clipboard!');
            return;
        }
    } catch (e) {
        // Fall back to prompt
    }
    prompt('Copy link:', text);
}

function formatDuration(sec) {
    if (!sec || isNaN(sec)) return '';
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = Math.floor(sec % 60);
    if (h > 0) {
        return `${h}h ${m}m ${s}s`;
    }
    return `${m}m ${s}s`;
}

function escapeHtml(str) {
    if (!str) return '';
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

async function deleteMediaConfirm(mediaID) {
    if (!confirm(`Delete media asset "${mediaID}"?`)) return;
    try {
        await fetch(`/api/v1/files/${encodeURIComponent(mediaID)}`, { method: 'DELETE' });
        fetchMediaList();
    } catch (_) {}
}

(function initRouter() {
    const pathToPage = {
        '/': 'dashboard',
        '/dashboard': 'dashboard',
        '/nodes': 'nodes',
        '/storage': 'storage',
        '/tiers': 'tiers',
        '/media': 'media',
        '/database': 'database',
        '/settings': 'settings',
        '/docs': 'docs'
    };
    const initialPage = pathToPage[window.location.pathname] || 'dashboard';
    navigateTo(initialPage, false);
    fetchClusterTelemetry();
    setInterval(fetchClusterTelemetry, 3000);
    window.addEventListener('popstate', (e) => {
        const page = (e.state && e.state.page) || pathToPage[window.location.pathname] || 'dashboard';
        navigateTo(page, false);
    });
})();

