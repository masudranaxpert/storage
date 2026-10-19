// Data Flow: Task Queue component with status filter, pagination and task cancellation.
(function (global) {
    'use strict';

    const UI = global.UI || (global.UI = {});
    const FlowQueue = {};

    let currentFilter = 'active';
    let currentPage = 1;
    const pageSize = 15;
    let cachedTasks = [];
    let pollTimer = null;
    let onCountsUpdated = null;

    FlowQueue.init = function (containerEl, options) {
        options = options || {};
        onCountsUpdated = options.onCountsUpdated || null;
        currentFilter = options.initialFilter || 'active';
        currentPage = 1;
        FlowQueue.render(containerEl);
        FlowQueue.fetchTasks(containerEl);

        FlowQueue.stopPolling();
        pollTimer = setInterval(() => {
            FlowQueue.fetchTasks(containerEl, true);
        }, 3500);
    };

    FlowQueue.stopPolling = function () {
        if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
    };

    FlowQueue.setFilter = function (containerEl, filter) {
        currentFilter = filter;
        currentPage = 1;
        FlowQueue.fetchTasks(containerEl);
    };

    FlowQueue.fetchTasks = async function (containerEl, isBackground = false) {
        const listEl = containerEl ? containerEl.querySelector('[data-role="queue-list"]') : null;
        if (!isBackground && listEl && !listEl.children.length) {
            listEl.innerHTML = '<div class="fl-loading">Loading tasks…</div>';
        }

        try {
            const res = await fetch(`/api/lifecycle/tasks?state=${encodeURIComponent(currentFilter)}&limit=200`);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            cachedTasks = data.tasks || [];

            if (currentFilter === 'active' && typeof onCountsUpdated === 'function') {
                onCountsUpdated(cachedTasks.length);
            }

            FlowQueue.renderList(containerEl);
        } catch (err) {
            if (listEl) {
                listEl.innerHTML = (UI.flow && UI.flow.empty)
                    ? UI.flow.empty('Could not load tasks', err.message)
                    : `<div class="fl-empty"><strong>Could not load tasks</strong><p>${err.message}</p></div>`;
            }
        }
    };

    FlowQueue.render = function (containerEl) {
        if (!containerEl) return;
        containerEl.innerHTML = `
            <div class="fl-subview-card">
                <div class="fl-subview-header">
                    <div class="fl-subview-title-group">
                        <div class="fl-subview-icon-badge">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                <rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect>
                                <line x1="8" y1="12" x2="16" y2="12"></line>
                                <line x1="12" y1="8" x2="12" y2="16"></line>
                            </svg>
                        </div>
                        <div>
                            <h3 class="fl-subview-title">Lifecycle Task Queue</h3>
                            <p class="fl-subview-subtitle">Active and completed tier migrations, promos, demotes and deletions</p>
                        </div>
                    </div>
                    <div class="fl-seg" role="group" aria-label="Task state">
                        <button type="button" class="${currentFilter === 'active' ? 'is-active' : ''}" data-queue-filter="active">Active</button>
                        <button type="button" class="${currentFilter === 'done' ? 'is-active' : ''}" data-queue-filter="done">Done</button>
                        <button type="button" class="${currentFilter === 'failed' ? 'is-active' : ''}" data-queue-filter="failed">Failed</button>
                    </div>
                </div>
                <div class="fl-list" data-role="queue-list"></div>
                <div class="fl-subview-footer" data-role="queue-pagination"></div>
            </div>
        `;

        containerEl.querySelectorAll('[data-queue-filter]').forEach(btn => {
            btn.onclick = function () {
                const f = this.getAttribute('data-queue-filter');
                containerEl.querySelectorAll('[data-queue-filter]').forEach(b => b.classList.toggle('is-active', b === this));
                FlowQueue.setFilter(containerEl, f);
            };
        });
    };

    FlowQueue.renderList = function (containerEl) {
        if (!containerEl) return;
        const listEl = containerEl.querySelector('[data-role="queue-list"]');
        const pagEl = containerEl.querySelector('[data-role="queue-pagination"]');
        if (!listEl) return;

        const total = cachedTasks.length;
        const totalPages = Math.max(1, Math.ceil(total / pageSize));
        if (currentPage > totalPages) currentPage = totalPages;

        const startIdx = (currentPage - 1) * pageSize;
        const endIdx = Math.min(total, startIdx + pageSize);
        const paged = cachedTasks.slice(startIdx, endIdx);

        if (!total) {
            listEl.innerHTML = (UI.flow && UI.flow.empty)
                ? UI.flow.empty(`No ${currentFilter} tasks`, currentFilter === 'active' ? 'Moves appear here while they run.' : 'No recorded tasks in this state.')
                : `<div class="fl-empty"><strong>No ${currentFilter} tasks</strong></div>`;
            if (pagEl) pagEl.innerHTML = '';
            return;
        }

        const F = UI.flow;
        const tiers = (global.App && global.App.pages && global.App.pages.flows && global.App.pages.flows.getTiers)
            ? global.App.pages.flows.getTiers()
            : [];

        listEl.innerHTML = paged.map(t => F && F.taskRow ? F.taskRow(t, tiers) : '').join('');

        // Wire cancel buttons inside list
        listEl.querySelectorAll('[data-cancel]').forEach(btn => {
            btn.onclick = async function () {
                const id = this.getAttribute('data-cancel');
                if (!global.confirm('Cancel this move? The partial copy on the destination is removed; the original stays.')) return;
                try {
                    const res = await fetch(`/api/lifecycle/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
                    if (res.ok) {
                        if (global.App && global.App.showToast) global.App.showToast('Task cancelled');
                        FlowQueue.fetchTasks(containerEl);
                    } else {
                        const err = await res.text();
                        if (global.App && global.App.showToast) global.App.showToast('Cancel failed: ' + err, 'error');
                    }
                } catch (e) {
                    if (global.App && global.App.showToast) global.App.showToast('Cancel failed: ' + e.message, 'error');
                }
            };
        });

        // Render pagination using standard component
        if (pagEl) {
            if (totalPages > 1 && UI.pagination) {
                pagEl.innerHTML = `
                    <div class="fl-subview-page-info">Showing ${startIdx + 1}–${endIdx} of ${total} tasks</div>
                    <div id="queue-pagination-buttons"></div>
                `;
                UI.pagination('queue-pagination-buttons', {
                    currentPage: currentPage,
                    totalPages: totalPages,
                    onPageChange: (newPage) => {
                        currentPage = newPage;
                        FlowQueue.renderList(containerEl);
                    }
                });
            } else {
                pagEl.innerHTML = `
                    <div class="fl-subview-page-info">Showing ${total} ${currentFilter} task${total === 1 ? '' : 's'}</div>
                    <div style="font-size:11.5px;color:var(--text-muted);display:flex;align-items:center;gap:6px;">
                        <span class="status-indicator-dot online" style="width:6px;height:6px;"></span>
                        <span>Auto-refreshing every 3.5s</span>
                    </div>
                `;
            }
        }
    };

    global.FlowQueue = FlowQueue;

    global.App = global.App || { pages: {} };
    global.App.pages = global.App.pages || {};
    global.App.pages['flows-queue'] = {
        init() {
            const el = document.getElementById('view-flows-queue');
            if (el) {
                FlowQueue.init(el, {
                    onCountsUpdated: (count) => {
                        const sb = document.querySelector('[data-role="sidebar-queue-count"]');
                        if (sb) {
                            sb.hidden = count === 0;
                            sb.textContent = String(count);
                        }
                    }
                });
            }
        },
        destroy() {
            FlowQueue.stopPolling();
        }
    };
})(window);
