// Dashboard page module: cluster overview metrics, score rings, fleet strip, and storage medium breakdown.
(function (global) {
    'use strict';

    const RING_CIRCUMFERENCE = 207.34;
    let pollTimer = null;

    function setRing(id, pct) {
        const ring = document.getElementById(id);
        if (!ring) return;
        const clamped = Math.max(0, Math.min(100, pct || 0));
        ring.style.strokeDashoffset = `${(RING_CIRCUMFERENCE * (1 - clamped / 100)).toFixed(2)}`;
    }

    function setRingPct(id, pct) {
        global.safeSetText(id, `${Math.max(0, Math.min(100, Math.round(pct || 0)))}%`);
    }

    function renderDashboard(data) {
        if (!data) return;
        const storageUsed = data.storage_used_percent || 0;
        const totalNodes = data.total_nodes || 0;
        const activeNodes = data.active_nodes || 0;
        const offlineNodes = data.offline_nodes || 0;

        global.safeSetText('dash-hero-storage', global.formatBytes(data.total_storage_bytes));
        global.safeSetText('dash-hero-free', global.formatBytes(data.free_storage_bytes));
        global.safeSetText('dash-hero-nodes', `${activeNodes}/${totalNodes || 0} Nodes`);
        global.safeSetText('dash-hero-drives', data.total_drives || 0);
        global.safeSetText('dash-hero-cores', data.total_cpu_cores || 0);

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

        const nvme = data.nvme || { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
        const ssd = data.ssd || { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
        const hdd = data.hdd || { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };

        // 1. Storage Pool Score Card
        global.safeSetText('dash-stat-storage', global.formatBytes(data.total_storage_bytes));
        global.safeSetText('dash-stat-storage-free-text', `${global.formatBytes(data.free_storage_bytes)} Free`);
        global.safeSetText('dash-stat-storage-sub', 'Used');
        setRing('dash-ring-storage', storageUsed);
        setRingPct('dash-ring-storage-pct', storageUsed);
        global.safeSetText('dash-score-nvme-val', `${global.formatBytes(nvme.total_bytes)} (${nvme.drive_count || 0}d)`);
        global.safeSetText('dash-score-ssd-val', `${global.formatBytes(ssd.total_bytes)} (${ssd.drive_count || 0}d)`);
        global.safeSetText('dash-score-hdd-val', `${global.formatBytes(hdd.total_bytes)} (${hdd.drive_count || 0}d)`);

        // 2. Memory Buffer Score Card
        global.safeSetText('dash-stat-ram', global.formatBytes(data.total_ram_bytes));
        global.safeSetText('dash-stat-ram-free-text', `${global.formatBytes(data.available_ram_bytes)} Free`);
        const usedRam = Math.max(0, (data.total_ram_bytes || 0) - (data.available_ram_bytes || 0));
        const ramPct = data.total_ram_bytes > 0 ? (usedRam / data.total_ram_bytes) * 100 : 0;
        setRing('dash-ring-ram', ramPct);
        setRingPct('dash-ring-ram-pct', ramPct);
        global.safeSetText('dash-score-ram-avail', global.formatBytes(data.available_ram_bytes));
        global.safeSetText('dash-score-ram-used', `${global.formatBytes(usedRam)} (${ramPct.toFixed(1)}%)`);

        // 3. CPU Compute Score Card
        const cores = data.total_cpu_cores || 0;
        global.safeSetText('dash-stat-cpu', cores);
        const cpuLoad = data.avg_cpu_load_percent || 0;
        global.safeSetText('dash-stat-cpu-sub', `${cpuLoad.toFixed(1)}% Avg`);
        setRing('dash-ring-cpu', cpuLoad);
        setRingPct('dash-ring-cpu-pct', cpuLoad);
        global.safeSetText('dash-score-cpu-cores', `${cores} ${cores === 1 ? 'Thread' : 'Threads'}`);
        global.safeSetText('dash-score-cpu-load', `${cpuLoad.toFixed(1)}%`);
        global.safeSetText('dash-score-cpu-idle', `${Math.max(0, 100 - cpuLoad).toFixed(1)}%`);

        // 4. Cluster Mesh Health Score Card
        global.safeSetText('dash-stat-nodes', totalNodes);
        global.safeSetText('dash-stat-nodes-sub', `${activeNodes} Active \u2022 ${offlineNodes} Offline`);
        const activePct = totalNodes > 0 ? (activeNodes / totalNodes) * 100 : 100;
        setRing('dash-ring-nodes', activePct);
        setRingPct('dash-ring-nodes-pct', activePct);
        global.safeSetText('dash-score-nodes-online', `${activeNodes} ${activeNodes === 1 ? 'Node' : 'Nodes'}`);
        global.safeSetText('dash-score-nodes-offline', `${offlineNodes} ${offlineNodes === 1 ? 'Node' : 'Nodes'}`);
        global.safeSetText('dash-score-nodes-drives', `${data.total_drives || 0} ${data.total_drives === 1 ? 'Drive' : 'Drives'}`);

        // Cluster Fleet Strip
        const fleetStrip = document.getElementById('dash-fleet-strip');
        if (fleetStrip) {
            const nodes = global.sortNodesDeterministic(data.nodes || []);
            if (nodes.length === 0) {
                fleetStrip.innerHTML = `
                    <div class="bui-tray-card" style="grid-column:1/-1;cursor:default;">
                        <div class="bui-inner-card" style="padding:28px;text-align:center;">
                            <div style="font-weight:700;font-size:14px;color:var(--text-primary);margin-bottom:4px;">No nodes in the mesh yet</div>
                            <div style="font-size:12.5px;color:var(--text-secondary);">Provision your first VPS to start pooling storage &amp; compute.</div>
                        </div>
                    </div>`;
            } else {
                fleetStrip.innerHTML = nodes.map(n => {
                    const m = n.metrics || {};
                    const status = (n.status || 'offline').toLowerCase();
                    const cpu = Math.round(m.cpu?.used_percent || 0);
                    const ram = Math.round(m.memory?.used_percent || 0);
                    const cpuColor = cpu >= 85 ? '#ef4444' : (cpu >= 60 ? '#f59e0b' : 'var(--chart-1)');
                    const ramColor = ram >= 85 ? '#ef4444' : (ram >= 60 ? '#f59e0b' : 'var(--chart-3)');
                    const driveCount = (m.disks || []).length;
                    const os = (m.os || 'linux').toLowerCase().slice(0, 7);
                    const version = m.capabilities?.version || 'v2.4';
                    const freeTotal = (m.disks || []).reduce((acc, d) => acc + (d.free_bytes || 0), 0);
                    const statusBadge = global.UI ? global.UI.badge({
                        text: status === 'online' ? 'Online' : status,
                        variant: status === 'online' ? 'emerald' : (status === 'stale' ? 'amber' : 'red'),
                        dot: true
                    }) : `<span class="fleet-status-dot ${global.escapeHtml(status)}"></span>`;

                    return `
                    <div class="bui-tray-card fleet-node-card" onclick="navigateTo('nodes')" title="Manage ${global.escapeHtml(m.node_id)}" style="cursor:pointer;">
                        <div class="bui-tray-head" style="padding:4px 6px 2px 6px;">
                            <div style="display:flex;align-items:center;gap:8px;min-width:0;">
                                <span class="fleet-node-name" style="font-size:14px;font-weight:700;color:var(--text-primary);">${global.escapeHtml(m.node_id)}</span>
                                <span class="ui-badge ui-badge-neutral ui-badge-rect ui-badge-mono">${global.escapeHtml(os)}</span>
                            </div>
                            ${statusBadge}
                        </div>
                        <div class="bui-inner-card bui-divided-list">
                            <div class="bui-metric-row">
                                <div class="bui-metric-left">
                                    <span class="bui-swatch-sq" style="background:${cpuColor};"></span>
                                    <span class="bui-metric-label">CPU Load</span>
                                </div>
                                <div class="bui-metric-right">
                                    <span class="bui-metric-val tabular-num">${cpu}%</span>
                                </div>
                            </div>
                            <div class="bui-metric-row">
                                <div class="bui-metric-left">
                                    <span class="bui-swatch-sq" style="background:${ramColor};"></span>
                                    <span class="bui-metric-label">Memory Use</span>
                                </div>
                                <div class="bui-metric-right">
                                    <span class="bui-metric-val tabular-num">${ram}%</span>
                                </div>
                            </div>
                            <div class="bui-metric-row" style="border-bottom:none;">
                                <div class="bui-metric-left">
                                    <span class="bui-swatch-sq" style="background:var(--chart-2);"></span>
                                    <span class="bui-metric-label">${driveCount} ${driveCount === 1 ? 'Drive' : 'Drives'}</span>
                                </div>
                                <div class="bui-metric-right">
                                    <span class="bui-metric-val tabular-num">${global.formatBytes(freeTotal)} free</span>
                                    <span class="ui-badge ui-badge-neutral ui-badge-rect ui-badge-mono" style="margin-left:4px;">${global.escapeHtml(version)}</span>
                                </div>
                            </div>
                        </div>
                    </div>`;
                }).join('');
            }
        }

        global.safeSetText('dash-total-drives-badge', `${data.total_drives || 0} Drives Active`);

        // Storage Medium Breakdown
        global.safeSetText('dash-medium-nvme-total', global.formatBytes(nvme.total_bytes));
        global.safeSetText('dash-medium-nvme-free-text', `${global.formatBytes(nvme.free_bytes)} Free`);
        global.safeSetText('dash-medium-nvme-sub', `${(nvme.used_percent || 0).toFixed(1)}% Used \u2022 ${nvme.drive_count} Drive${nvme.drive_count === 1 ? '' : 's'} Active`);
        const nvmeMeter = document.getElementById('dash-medium-nvme-meter');
        if (nvmeMeter) nvmeMeter.style.width = `${Math.min(100, nvme.used_percent || 0)}%`;
        const nvmeSharePct = data.total_storage_bytes > 0 ? ((nvme.total_bytes / data.total_storage_bytes) * 100).toFixed(1) : '0.0';
        global.safeSetText('dash-medium-nvme-pct', `${nvmeSharePct}% Share`);

        global.safeSetText('dash-medium-ssd-total', global.formatBytes(ssd.total_bytes));
        global.safeSetText('dash-medium-ssd-free-text', `${global.formatBytes(ssd.free_bytes)} Free`);
        global.safeSetText('dash-medium-ssd-sub', `${(ssd.used_percent || 0).toFixed(1)}% Used \u2022 ${ssd.drive_count} Drive${ssd.drive_count === 1 ? '' : 's'} Active`);
        const ssdMeter = document.getElementById('dash-medium-ssd-meter');
        if (ssdMeter) ssdMeter.style.width = `${Math.min(100, ssd.used_percent || 0)}%`;
        const ssdSharePct = data.total_storage_bytes > 0 ? ((ssd.total_bytes / data.total_storage_bytes) * 100).toFixed(1) : '0.0';
        global.safeSetText('dash-medium-ssd-pct', `${ssdSharePct}% Share`);

        global.safeSetText('dash-medium-hdd-total', global.formatBytes(hdd.total_bytes));
        global.safeSetText('dash-medium-hdd-free-text', `${global.formatBytes(hdd.free_bytes)} Free`);
        global.safeSetText('dash-medium-hdd-sub', `${(hdd.used_percent || 0).toFixed(1)}% Used \u2022 ${hdd.drive_count} Drive${hdd.drive_count === 1 ? '' : 's'} Active`);
        const hddMeter = document.getElementById('dash-medium-hdd-meter');
        if (hddMeter) hddMeter.style.width = `${Math.min(100, hdd.used_percent || 0)}%`;
        const hddSharePct = data.total_storage_bytes > 0 ? ((hdd.total_bytes / data.total_storage_bytes) * 100).toFixed(1) : '0.0';
        global.safeSetText('dash-medium-hdd-pct', `${hddSharePct}% Share`);

        // Composition Bar
        global.safeSetText('dash-storage-share-text', `Total Pooled: ${global.formatBytes(data.total_storage_bytes)}`);
        const segNvme = document.getElementById('dash-segment-nvme');
        if (segNvme) segNvme.style.width = `${nvmeSharePct}%`;
        const segSsd = document.getElementById('dash-segment-ssd');
        if (segSsd) segSsd.style.width = `${ssdSharePct}%`;
        const segHdd = document.getElementById('dash-segment-hdd');
        if (segHdd) segHdd.style.width = `${hddSharePct}%`;

        global.safeSetText('dash-legend-nvme', `${global.formatBytes(nvme.total_bytes)} (${nvmeSharePct}%)`);
        global.safeSetText('dash-legend-ssd', `${global.formatBytes(ssd.total_bytes)} (${ssdSharePct}%)`);
        global.safeSetText('dash-legend-hdd', `${global.formatBytes(hdd.total_bytes)} (${hddSharePct}%)`);
    }

    function init() {
        destroy();
        if (global.cachedPoolData) renderDashboard(global.cachedPoolData);
        global.fetchClusterTelemetry();
        pollTimer = setInterval(global.fetchClusterTelemetry, 2500);
    }

    function destroy() {
        if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
    }

    global.setRing = setRing;
    global.setRingPct = setRingPct;
    global.renderDashboard = renderDashboard;

    if (global.App && global.App.pages) {
        global.App.pages.dashboard = { init, destroy, render: renderDashboard };
    }
})(window);
