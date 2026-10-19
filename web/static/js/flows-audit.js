// Data Flow: Audit Log component displaying history of files removed by expire rules with pagination.
(function (global) {
    'use strict';

    const UI = global.UI || (global.UI = {});
    const FlowAudit = {};

    let currentPage = 1;
    const pageSize = 15;
    let cachedAudits = [];

    FlowAudit.init = function (containerEl) {
        currentPage = 1;
        FlowAudit.render(containerEl);
        FlowAudit.fetchAudit(containerEl);
    };

    FlowAudit.fetchAudit = async function (containerEl) {
        const listEl = containerEl ? containerEl.querySelector('[data-role="audit-list"]') : null;
        if (listEl && !listEl.children.length) {
            listEl.innerHTML = '<div class="fl-loading">Loading audit records…</div>';
        }

        try {
            const res = await fetch('/api/lifecycle/audit?limit=200');
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            cachedAudits = data.audits || [];
            FlowAudit.renderList(containerEl);
        } catch (err) {
            if (listEl) {
                listEl.innerHTML = (UI.flow && UI.flow.empty)
                    ? UI.flow.empty('Could not load audit log', err.message)
                    : `<div class="fl-empty"><strong>Could not load audit log</strong><p>${err.message}</p></div>`;
            }
        }
    };

    FlowAudit.render = function (containerEl) {
        if (!containerEl) return;
        containerEl.innerHTML = `
            <div class="fl-subview-card">
                <div class="fl-subview-header">
                    <div class="fl-subview-title-group">
                        <div class="fl-subview-icon-badge">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"></path>
                            </svg>
                        </div>
                        <div>
                            <h3 class="fl-subview-title">Lifecycle Expire Audit Log</h3>
                            <p class="fl-subview-subtitle">Immutable audit trail of files permanently deleted by configured expire policies</p>
                        </div>
                    </div>
                    <div class="fl-subview-actions">
                        <button type="button" class="fl-btn is-small" data-action="refresh-audit">
                            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
                            <span>Refresh</span>
                        </button>
                    </div>
                </div>
                <div class="fl-list" data-role="audit-list"></div>
                <div class="fl-subview-footer" data-role="audit-pagination"></div>
            </div>
        `;

        const refreshBtn = containerEl.querySelector('[data-action="refresh-audit"]');
        if (refreshBtn) {
            refreshBtn.onclick = () => FlowAudit.fetchAudit(containerEl);
        }
    };

    FlowAudit.renderList = function (containerEl) {
        if (!containerEl) return;
        const listEl = containerEl.querySelector('[data-role="audit-list"]');
        const pagEl = containerEl.querySelector('[data-role="audit-pagination"]');
        if (!listEl) return;

        const total = cachedAudits.length;
        const totalPages = Math.max(1, Math.ceil(total / pageSize));
        if (currentPage > totalPages) currentPage = totalPages;

        const startIdx = (currentPage - 1) * pageSize;
        const endIdx = Math.min(total, startIdx + pageSize);
        const paged = cachedAudits.slice(startIdx, endIdx);

        if (!total) {
            listEl.innerHTML = (UI.flow && UI.flow.empty)
                ? UI.flow.empty('No deletions recorded', 'No files have been deleted by expire rules yet.')
                : `<div class="fl-empty"><strong>No deletions recorded</strong></div>`;
            if (pagEl) pagEl.innerHTML = '';
            return;
        }

        const F = UI.flow;
        const tiers = (global.App && global.App.pages && global.App.pages.flows && global.App.pages.flows.getTiers)
            ? global.App.pages.flows.getTiers()
            : [];

        listEl.innerHTML = paged.map(a => F && F.auditRow ? F.auditRow(a, tiers) : '').join('');

        // Render pagination using standard component
        if (pagEl) {
            if (totalPages > 1 && UI.pagination) {
                pagEl.innerHTML = `
                    <div class="fl-subview-page-info">Showing ${startIdx + 1}–${endIdx} of ${total} records</div>
                    <div id="audit-pagination-buttons"></div>
                `;
                UI.pagination('audit-pagination-buttons', {
                    currentPage: currentPage,
                    totalPages: totalPages,
                    onPageChange: (newPage) => {
                        currentPage = newPage;
                        FlowAudit.renderList(containerEl);
                    }
                });
            } else {
                pagEl.innerHTML = `
                    <div class="fl-subview-page-info">Showing ${total} deletion record${total === 1 ? '' : 's'}</div>
                    <div style="font-size:11.5px;color:var(--text-muted);display:flex;align-items:center;gap:6px;">
                        <span class="status-indicator-dot online" style="width:6px;height:6px;"></span>
                        <span>Immutable audit ledger</span>
                    </div>
                `;
            }
        }
    };

    global.FlowAudit = FlowAudit;

    global.App = global.App || { pages: {} };
    global.App.pages = global.App.pages || {};
    global.App.pages['flows-audit'] = {
        init() {
            const el = document.getElementById('view-flows-audit');
            if (el) FlowAudit.init(el);
        }
    };
})(window);
