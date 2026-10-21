// Data Flow: Hot Files component with decayed heat score rankings, manual pinning and pagination.
(function (global) {
    'use strict';

    const UI = global.UI || (global.UI = {});
    const FlowHot = {};

    let currentPage = 1;
    const pageSize = 15;
    let cachedHotFiles = [];

    FlowHot.init = function (containerEl) {
        currentPage = 1;
        FlowHot.render(containerEl);
        FlowHot.fetchHot(containerEl);
    };

    FlowHot.fetchHot = async function (containerEl) {
        const listEl = containerEl ? containerEl.querySelector('[data-role="hot-list"]') : null;
        if (listEl && !listEl.children.length) {
            listEl.innerHTML = '<div class="fl-loading">Ranking files…</div>';
        }

        try {
            const res = await fetch('/api/lifecycle/hot?limit=200');
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            cachedHotFiles = data.hot_files || [];
            FlowHot.renderList(containerEl);
        } catch (err) {
            if (listEl) {
                listEl.innerHTML = (UI.flow && UI.flow.empty)
                    ? UI.flow.empty('Could not load hot files', err.message)
                    : `<div class="fl-empty"><strong>Could not load hot files</strong><p>${err.message}</p></div>`;
            }
        }
    };

    FlowHot.render = function (containerEl) {
        if (!containerEl) return;
        containerEl.innerHTML = `
            <div class="fl-subview-card">
                <div class="fl-subview-header">
                    <div class="fl-subview-title-group">
                        <div class="fl-subview-icon-badge">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M8.5 14.5A2.5 2.5 0 0 0 11 12c0-1.38-.5-2-1-3-1.072-2.143-.224-4.054 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.153.433-2.294 1-3a2.5 2.5 0 0 0 2.5 2.5z"/>
                            </svg>
                        </div>
                        <div>
                            <h3 class="fl-subview-title">Demand & Hot Files Ranking</h3>
                            <p class="fl-subview-subtitle">Ranked by real-time playback heat score. High demand pins keep files in Tier 0 hot storage.</p>
                        </div>
                    </div>
                    <div class="fl-subview-actions">
                        <button type="button" class="fl-btn is-small" data-action="refresh-hot">
                            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
                            <span>Refresh</span>
                        </button>
                    </div>
                </div>
                <div class="fl-table-responsive" data-role="hot-table-wrapper">
                    <table class="fl-table" data-role="hot-table">
                        <thead>
                            <tr>
                                <th style="width:68px;text-align:center;">Rank</th>
                                <th>File & Key</th>
                                <th style="width:140px;">Current Tier</th>
                                <th style="width:110px;">Size</th>
                                <th style="width:130px;">Heat Score</th>
                                <th style="width:120px;">Last Hit</th>
                                <th style="width:170px;text-align:right;">Actions</th>
                            </tr>
                        </thead>
                        <tbody data-role="hot-tbody">
                            <tr>
                                <td colspan="7" class="fl-table-empty">
                                    <div class="fl-loading">Loading ranked files…</div>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                <div class="fl-subview-footer" data-role="hot-pagination"></div>
            </div>
        `;

        const refreshBtn = containerEl.querySelector('[data-action="refresh-hot"]');
        if (refreshBtn) {
            refreshBtn.onclick = () => FlowHot.fetchHot(containerEl);
        }
    };

    FlowHot.renderList = function (containerEl) {
        if (!containerEl) return;
        const tbody = containerEl.querySelector('[data-role="hot-tbody"]');
        const pagEl = containerEl.querySelector('[data-role="hot-pagination"]');
        if (!tbody) return;

        const total = cachedHotFiles.length;
        const totalPages = Math.max(1, Math.ceil(total / pageSize));
        if (currentPage > totalPages) currentPage = totalPages;

        const startIdx = (currentPage - 1) * pageSize;
        const endIdx = Math.min(total, startIdx + pageSize);
        const paged = cachedHotFiles.slice(startIdx, endIdx);

        if (!total) {
            tbody.innerHTML = `
                <tr>
                    <td colspan="7">
                        <div class="fl-empty">
                            <div class="fl-empty-icon">
                                <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>
                            </div>
                            <h4>No plays recorded yet</h4>
                            <p>Plays raise a file's heat score automatically in real time.</p>
                        </div>
                    </td>
                </tr>
            `;
            if (pagEl) pagEl.innerHTML = '';
            return;
        }

        const F = UI.flow;
        const tiers = (global.App && global.App.pages && global.App.pages.flows && global.App.pages.flows.getTiers)
            ? global.App.pages.flows.getTiers()
            : [];

        const formatBytes = typeof global.formatBytes === 'function' ? global.formatBytes : (n) => (n || 0) + ' B';
        const esc = UI.esc ? UI.esc : (s) => String(s == null ? '' : s);

        const rowsHtml = paged.map((f, i) => {
            const rank = startIdx + i + 1;
            const pinned = f.pinned_tier !== undefined && f.pinned_tier !== null;
            const tierName = F && F.tierLabel ? F.tierLabel(tiers, f.tier_id) : ('Tier ' + (f.tier_id ?? 0));
            const scoreVal = (Number(f.score) || 0).toFixed(1);
            const sizeStr = formatBytes(f.size_bytes, 1);

            let timeAgoStr = '—';
            if (f.last_hit_at && !f.last_hit_at.startsWith('0001-01-01')) {
                const t = new Date(f.last_hit_at).getTime();
                if (t && t > 0) {
                    const diff = Math.max(0, (Date.now() - t) / 1000);
                    if (diff < 60) timeAgoStr = 'just now';
                    else if (diff < 3600) timeAgoStr = Math.floor(diff / 60) + 'm ago';
                    else if (diff < 86400) timeAgoStr = Math.floor(diff / 3600) + 'h ago';
                    else timeAgoStr = Math.floor(diff / 86400) + 'd ago';
                }
            }

            const rankBadgeClass = rank === 1 ? 'rank-1' : (rank === 2 ? 'rank-2' : (rank === 3 ? 'rank-3' : 'rank-other'));

            return `
                <tr>
                    <td style="text-align:center;">
                        <span class="fl-rank-badge ${rankBadgeClass} tabular-num">${rank}</span>
                    </td>
                    <td>
                        <div class="fl-table-file">
                            <span class="fl-file-name" title="${esc(f.filename || f.key)}">${esc(f.filename || f.key)}</span>
                            <span class="fl-file-meta mono">${esc(f.key)}</span>
                        </div>
                    </td>
                    <td>
                        <span class="badge-tier orange" style="font-size:11px;">${esc(tierName)}</span>
                    </td>
                    <td>
                        <span class="mono tabular-num" style="font-size:12px;color:var(--text-secondary);">${esc(sizeStr)}</span>
                    </td>
                    <td>
                        <span class="fl-score-pill tabular-num" title="Decayed heat score">${scoreVal}</span>
                    </td>
                    <td>
                        <span class="tabular-num" style="font-size:12px;color:var(--text-muted);">${esc(timeAgoStr)}</span>
                    </td>
                    <td style="text-align:right;">
                        <div style="display:inline-flex;align-items:center;justify-content:flex-end;gap:6px;">
                            ${pinned
                                ? `<span class="fl-chip fl-tone-ingest" title="Pinned until ${esc(f.pin_until || '')}" style="font-size:11px;">Pinned T${f.pinned_tier}</span>` +
                                  `<button type="button" class="btn btn-secondary" data-unpin="${esc(f.key)}" title="Unpin file (restore auto dynamic lifecycle)" style="font-size:11px;padding:3px 8px;">Unpin</button>`
                                : `<button type="button" class="btn btn-secondary" data-pin="${esc(f.key)}" title="Pin to Tier 0 for 24h" style="font-size:11.5px;padding:4px 10px;display:inline-flex;align-items:center;gap:4px;">` +
                                  `<span>Pin Hot</span></button>`
                            }
                        </div>
                    </td>
                </tr>
            `;
        }).join('');

        tbody.innerHTML = rowsHtml;

        // Wire pin buttons
        tbody.querySelectorAll('[data-pin]').forEach(btn => {
            btn.onclick = async function () {
                const key = this.getAttribute('data-pin');
                this.disabled = true;
                try {
                    const res = await fetch(`/api/v1/files/${encodeURIComponent(key)}/demand`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ level: 'high', ttl_hours: 24 })
                    });
                    if (res.ok) {
                        if (global.App && global.App.showToast) global.App.showToast('Pinned to Tier 0 for 24h');
                        FlowHot.fetchHot(containerEl);
                    } else {
                        const err = await res.text();
                        if (global.App && global.App.showToast) global.App.showToast('Pin failed: ' + err, 'error');
                        this.disabled = false;
                    }
                } catch (e) {
                    if (global.App && global.App.showToast) global.App.showToast('Pin failed: ' + e.message, 'error');
                    this.disabled = false;
                }
            };
        });

        // Wire unpin buttons
        tbody.querySelectorAll('[data-unpin]').forEach(btn => {
            btn.onclick = async function () {
                const key = this.getAttribute('data-unpin');
                this.disabled = true;
                try {
                    const res = await fetch(`/api/v1/files/${encodeURIComponent(key)}/demand`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ level: 'normal' })
                    });
                    if (res.ok) {
                        if (global.App && global.App.showToast) global.App.showToast('Unpinned: auto dynamic lifecycle restored');
                        FlowHot.fetchHot(containerEl);
                    } else {
                        const err = await res.text();
                        if (global.App && global.App.showToast) global.App.showToast('Unpin failed: ' + err, 'error');
                        this.disabled = false;
                    }
                } catch (e) {
                    if (global.App && global.App.showToast) global.App.showToast('Unpin failed: ' + e.message, 'error');
                    this.disabled = false;
                }
            };
        });

        // Render pagination using standard component
        if (pagEl) {
            if (totalPages > 1 && UI.pagination) {
                pagEl.innerHTML = `
                    <div class="fl-subview-page-info">Showing ${startIdx + 1}–${endIdx} of ${total} files</div>
                    <div id="hot-pagination-buttons"></div>
                `;
                UI.pagination('hot-pagination-buttons', {
                    currentPage: currentPage,
                    totalPages: totalPages,
                    onPageChange: (newPage) => {
                        currentPage = newPage;
                        FlowHot.renderList(containerEl);
                    }
                });
            } else {
                pagEl.innerHTML = `
                    <div class="fl-subview-page-info">Showing ${total} ranked file${total === 1 ? '' : 's'}</div>
                    <div style="font-size:11.5px;color:var(--text-muted);display:flex;align-items:center;gap:6px;">
                        <span class="status-indicator-dot online" style="width:6px;height:6px;"></span>
                        <span>Heat scores decayed in real time</span>
                    </div>
                `;
            }
        }
    };

    global.FlowHot = FlowHot;

    global.App = global.App || { pages: {} };
    global.App.pages = global.App.pages || {};
    global.App.pages['flows-hot'] = {
        init() {
            const el = document.getElementById('view-flows-hot');
            if (el) FlowHot.init(el);
        }
    };
})(window);
