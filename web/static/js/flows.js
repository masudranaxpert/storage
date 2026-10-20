// Data Flow page controller: owns policy draft state, API calls and polling;
// rendering is delegated to the UI.flow components (flow-ui.js).
(function (global) {
    'use strict';

    const F = () => global.UI.flow;
    const GRAPH_MIN_WIDTH = 720; // below this the board switches to the pipeline list
    const WIDE_LAYOUT = 1280;    // matches the CSS breakpoint for the side panel

    const state = {
        active: null,        // last saved policy
        draft: null,         // working copy edited by the UI
        tiers: [],
        stats: {},
        errors: {},          // edge id -> validation message
        selection: { type: 'none' },
        tab: 'details',
        queueFilter: 'active',
        activeTasks: 0,
        hotCount: 0,
        mode: null,          // 'graph' | 'list'
        loaded: false
    };

    let root = null;
    let graph = null;
    let boardObserver = null;
    let queueTimer = null;
    let countTimer = null;
    let mounted = false;

    const $ = (sel) => root.querySelector(sel);
    const clone = (o) => JSON.parse(JSON.stringify(o || {}));
    const toast = (msg, type) => global.App && global.App.showToast && global.App.showToast(msg, type);

    async function api(url, opts) {
        const res = await fetch(url, opts);
        let body = null;
        try { body = await res.json(); } catch (_) { body = null; }
        return { ok: res.ok, status: res.status, body };
    }

    function errText(r) {
        return (r.body && (r.body.error || r.body.message)) || ('HTTP ' + r.status);
    }

    function normalizePolicy(p) {
        const out = clone(p);
        out.ingest = Array.isArray(out.ingest) ? out.ingest : [];
        out.edges = Array.isArray(out.edges) ? out.edges.filter(Boolean) : [];
        out.edges.forEach(e => { e.trigger = e.trigger || {}; });
        return out;
    }

    // Only the user-editable parts decide dirtiness; server bumps the rest.
    function signature(p) {
        return p ? JSON.stringify({ ingest: p.ingest, edges: p.edges }) : '';
    }

    function isDirty() {
        return !!state.draft && signature(state.draft) !== signature(state.active);
    }

    // ---- Loading -------------------------------------------------------

    async function load(opts) {
        const keepDraft = opts && opts.keepDraft;
        const r = await api('/api/flows');
        if (!r.ok || !r.body) {
            $('#fl-board').innerHTML = F().empty('Could not load the flow policy', errText(r),
                '<button type="button" class="fl-btn" data-action="refresh">Try again</button>');
            return;
        }
        state.active = normalizePolicy(r.body.policy);
        if (!keepDraft || !state.draft) {
            state.draft = clone(state.active);
            state.errors = {};
        }
        state.tiers = r.body.tiers || [];
        state.stats = r.body.stats || {};
        state.hotCount = Object.values(state.stats).reduce((n, s) => n + ((s && s.hot_count) || 0), 0);
        state.loaded = true;

        // Drop a selection that no longer exists.
        const sel = state.selection;
        if (sel.type === 'edge' && !state.draft.edges.some(e => e.id === sel.id)) state.selection = { type: 'none' };
        if (sel.type === 'tier' && !state.tiers.some(t => t.id === sel.id)) state.selection = { type: 'none' };

        renderAll();
        refreshCounts();
    }

    async function refreshCounts() {
        const r = await api('/api/lifecycle/tasks?state=active&limit=200');
        const tasks = r.ok && r.body ? (r.body.tasks || []) : [];
        state.activeTasks = tasks.length;
        const badge = $('[data-role="queue-count"]');
        if (badge) {
            badge.hidden = tasks.length === 0;
            badge.textContent = String(tasks.length);
        }
        const subnavBadge = $('[data-role="subnav-queue-count"]');
        if (subnavBadge) {
            subnavBadge.hidden = tasks.length === 0;
            subnavBadge.textContent = String(tasks.length);
        }
        const sidebarSublinkCount = document.querySelector('[data-role="sidebar-queue-count"]');
        if (sidebarSublinkCount) {
            sidebarSublinkCount.hidden = tasks.length === 0;
            sidebarSublinkCount.textContent = String(tasks.length);
        }
        renderStats();
    }

    // ---- Rendering -------------------------------------------------------

    function renderAll() {
        renderHeader();
        renderStats();
        renderBoard();
        renderDetails();
    }

    function renderHeader() {
        const dirty = isDirty();
        const pill = $('#fl-save-state');
        pill.classList.toggle('is-dirty', dirty);
        pill.querySelector('[data-role="text"]').textContent = dirty
            ? 'Unsaved changes'
            : 'Saved · v' + ((state.active && state.active.version) || 1);
        root.querySelectorAll('[data-action="save"]').forEach(b => { b.disabled = !dirty; });
        root.querySelectorAll('.fl-head [data-action="discard"]').forEach(b => { b.hidden = !dirty; });
        const bar = $('[data-role="dirtybar"]');
        if (bar) bar.hidden = !dirty;
    }

    function renderStats() {
        const el = $('#fl-stats');
        if (!el || !state.loaded) return;
        const edges = state.draft.edges;
        const enabled = edges.filter(e => e.enabled).length;
        const files = Object.values(state.stats).reduce((n, s) => n + ((s && s.file_count) || 0), 0);
        const online = state.tiers.reduce((n, t) => n + (t.blocks || []).filter(b => b.online).length, 0);
        const blocks = state.tiers.reduce((n, t) => n + (t.blocks || []).length, 0);
        el.innerHTML =
            F().statTile('Files placed', files, `${state.tiers.length} tiers · ${online}/${blocks} blocks online`, online < blocks ? 'warn' : '') +
            F().statTile('Rules on', `${enabled}/${edges.length}`, edges.some(e => e.kind === 'expire' && e.enabled) ? 'Expire is on' : 'No deletion rules', edges.some(e => e.kind === 'expire' && e.enabled) ? 'danger' : '') +
            F().statTile('Moving now', state.activeTasks, state.activeTasks ? 'Open the Queue tab' : 'Queue is idle', state.activeTasks ? 'busy' : '') +
            F().statTile('Hot files', state.hotCount, 'Above a promote threshold', '');
    }

    function boardModel() {
        return {
            policy: state.draft,
            tiers: state.tiers,
            stats: state.stats,
            selection: state.selection,
            errors: state.errors
        };
    }

    function renderBoard() {
        const board = $('#fl-board');
        if (!board || !state.loaded) return;
        const mode = board.clientWidth >= GRAPH_MIN_WIDTH ? 'graph' : 'list';
        if (mode !== state.mode) {
            if (graph) { graph.destroy(); graph = null; }
            board.onclick = null;
            board.innerHTML = '';
            state.mode = mode;
            const hint = $('[data-role="hint"]');
            if (hint) hint.textContent = mode === 'graph'
                ? 'Drag the dot on a tier onto another tier to add a rule. Click anything to edit it.'
                : 'Tap a tier or rule to edit it.';
        }
        if (!state.tiers.length) {
            board.innerHTML = F().empty('No storage tiers yet', 'Create tiers and assign disks on the Tiers page first.',
                '<a class="fl-btn" href="/tiers" data-nav="tiers">Open Tiers</a>');
            return;
        }
        if (mode === 'graph') {
            if (!graph) graph = F().graph(board, { onSelect: select, onConnect: connect });
            graph.update(boardModel());
        } else {
            F().pipeline(board, boardModel(), { onSelect: select });
        }
    }

    function renderDetails() {
        const pane = $('#fl-pane-details');
        if (!pane || !state.loaded) return;
        F().inspector(pane, {
            selection: state.selection,
            policy: state.draft,
            tiers: state.tiers,
            stats: state.stats,
            errors: state.errors,
            on: {
                select: select,
                connect: connect,
                edgeChange: onEdgeChange,
                deleteEdge: deleteEdge,
                ingestChange: (ingest) => {
                    state.draft.ingest = ingest;
                    changed({ details: true });
                },
                saveTier: saveTier
            }
        });
    }

    // Light re-render after a draft edit; the inspector keeps focus.
    function changed(opts) {
        renderHeader();
        renderStats();
        renderBoard();
        if (opts && opts.details) renderDetails();
    }

    // ---- Selection & editing -------------------------------------------

    function select(sel) {
        state.selection = sel || { type: 'none' };
        switchTab('details');
        renderBoard();
        renderDetails();
        // Single-column layouts: bring the editor into view only if out of viewport.
        if (state.selection.type !== 'none' && global.innerWidth < WIDE_LAYOUT) {
            const panel = $('#fl-panel');
            if (panel) {
                const rect = panel.getBoundingClientRect();
                if (rect.top > global.innerHeight || rect.bottom < 0) {
                    panel.scrollIntoView({ behavior: 'auto', block: 'nearest' });
                }
            }
        }
    }

    function newEdgeId(kind) {
        const rnd = Math.random().toString(36).slice(2, 10);
        return 'e_' + kind.charAt(0) + rnd;
    }

    function defaultTrigger(kind) {
        if (kind === 'promote') return { heat_at_least: 5 };
        if (kind === 'demote') return { idle_days: 7, heat_below: 2 };
        return { age_days: 30 };
    }

    function connect(spec) {
        const tiers = state.tiers;
        if (spec.from === 'source') {
            if (spec.to === F().TRASH) return;
            const ingest = state.draft.ingest;
            if (ingest.indexOf(spec.to) === -1) {
                ingest.push(spec.to);
                toast(`${F().tierLabel(tiers, spec.to)} added to the ingest order`);
            }
            state.selection = { type: 'source' };
            changed({ details: true });
            return;
        }
        const kind = F().inferKind(tiers, spec.from, spec.to);
        if (!kind) {
            toast('Those tiers have the same speed rank. Change one rank first.', 'error');
            return;
        }
        const existing = state.draft.edges.find(e => e.from === spec.from && e.to === spec.to);
        if (existing) {
            select({ type: 'edge', id: existing.id });
            return;
        }
        const edge = { id: newEdgeId(kind), kind, from: spec.from, to: spec.to, trigger: defaultTrigger(kind), enabled: true };
        state.draft.edges.push(edge);
        state.selection = { type: 'edge', id: edge.id };
        switchTab('details');
        changed({ details: true });
    }

    function onEdgeChange(edge) {
        delete state.errors[edge.id];
        changed();
    }

    function deleteEdge(id) {
        state.draft.edges = state.draft.edges.filter(e => e.id !== id);
        delete state.errors[id];
        if (state.selection.type === 'edge' && state.selection.id === id) state.selection = { type: 'none' };
        changed({ details: true });
    }

    function discard() {
        state.draft = clone(state.active);
        state.errors = {};
        state.selection = { type: 'none' };
        changed({ details: true });
    }

    async function saveTier(tierId, lifecycle, btn) {
        if (btn) btn.disabled = true;
        const r = await api(`/api/tiers/${tierId}/lifecycle`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(lifecycle)
        });
        if (btn) btn.disabled = false;
        if (!r.ok) {
            toast('Could not save tier settings: ' + errText(r), 'error');
            return;
        }
        toast('Tier settings saved');
        await load({ keepDraft: true });
    }

    // ---- Saving & expire confirmation -------------------------------------

    // The server demands the phrase whenever an expire edge is new, re-enabled
    // or made more aggressive; mirror that so the modal appears up front.
    function needsExpireConfirm() {
        const before = {};
        (state.active.edges || []).forEach(e => { before[e.id] = e; });
        return state.draft.edges.some(e => {
            if (e.kind !== 'expire' || !e.enabled) return false;
            const prev = before[e.id];
            if (!prev || !prev.enabled) return true;
            const a = prev.trigger || {}, b = e.trigger || {};
            const tighter = (x, y) => (y > 0 && (!(x > 0) || y < x));
            return tighter(a.age_days, b.age_days) || tighter(a.idle_days, b.idle_days) || (a.on_watermark && !b.on_watermark);
        });
    }

    async function save(confirmPhrase) {
        if (!isDirty()) return;
        if (!confirmPhrase && needsExpireConfirm()) {
            openExpireModal();
            return;
        }
        root.querySelectorAll('[data-action="save"]').forEach(b => { b.disabled = true; });
        const payload = Object.assign({}, state.active, {
            ingest: state.draft.ingest,
            edges: state.draft.edges,
            version: state.active.version,
            expire_confirm: confirmPhrase || ''
        });
        const r = await api('/api/flows', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });

        if (r.status === 409) {
            renderHeader();
            toast('Someone else saved a newer policy. Reloading it; your draft was kept.', 'error');
            await load({ keepDraft: true });
            return;
        }
        if (r.status === 422) {
            state.errors = {};
            const errs = (r.body && r.body.errors) || [];
            errs.forEach(e => { if (e.edge_id) state.errors[e.edge_id] = e.message; });
            const first = errs[0] ? errs[0].message : 'Validation failed';
            if (/DELETE FILES|confirm/i.test(first) && !confirmPhrase) {
                renderHeader();
                openExpireModal();
                return;
            }
            const bad = errs.find(e => e.edge_id);
            if (bad) state.selection = { type: 'edge', id: bad.edge_id };
            changed({ details: true });
            toast(errs.length > 1 ? `${first} (+${errs.length - 1} more)` : first, 'error');
            return;
        }
        if (!r.ok) {
            renderHeader();
            toast('Save failed: ' + errText(r), 'error');
            return;
        }

        closeExpireModal();
        state.active = normalizePolicy(r.body);
        state.draft = clone(state.active);
        state.errors = {};
        changed({ details: true });
        toast('Policy saved');
    }

    async function openExpireModal() {
        const modal = root.querySelector('#flow-expire-modal');
        const input = modal.querySelector('[data-role="expire-input"]');
        const count = modal.querySelector('[data-role="expire-count"]');
        input.value = '';
        modal.querySelector('[data-action="expire-confirm"]').disabled = true;
        count.textContent = 'Counting matching files…';
        modal.classList.add('open', 'active');
        modal.setAttribute('aria-hidden', 'false');
        if (global.App && global.App.setModalOpen) global.App.setModalOpen(true);
        setTimeout(() => input.focus(), 50);

        const r = await api('/api/flows/preview', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(Object.assign({}, state.active, { ingest: state.draft.ingest, edges: state.draft.edges, expire_acknowledged: true }))
        });
        if (r.ok && r.body) {
            const n = (r.body.tasks || []).filter(t => t.kind === 'expire').length;
            count.textContent = n === 0
                ? 'No files match right now. Files that match later will be deleted automatically.'
                : `${n} file${n === 1 ? '' : 's'} would be deleted on the next run.`;
        } else {
            count.textContent = 'The dry run failed, so the impact is unknown. Review the rule carefully.';
        }
    }

    function closeExpireModal() {
        const modal = root && root.querySelector('#flow-expire-modal');
        if (!modal || !modal.classList.contains('open')) return;
        modal.classList.remove('open', 'active');
        modal.setAttribute('aria-hidden', 'true');
        if (global.App && global.App.setModalOpen) global.App.setModalOpen(false);
    }

    function openDocsModal() {
        const modal = root && root.querySelector('#flow-docs-modal');
        if (!modal) return;
        modal.classList.add('open', 'active');
        modal.setAttribute('aria-hidden', 'false');
        if (global.App && global.App.setModalOpen) global.App.setModalOpen(true);
    }

    function closeDocsModal() {
        const modal = root && root.querySelector('#flow-docs-modal');
        if (!modal || !modal.classList.contains('open')) return;
        modal.classList.remove('open', 'active');
        modal.setAttribute('aria-hidden', 'true');
        if (global.App && global.App.setModalOpen) global.App.setModalOpen(false);
    }

    // ---- Sub-View switching (Topology vs Dedicated Full-Width Views) ----

    function switchSubView(view) {
        if (!view || view === 'topology') {
            renderBoard();
            return;
        }
        const targetPage = 'flows-' + view;
        if (global.navigateTo) {
            global.navigateTo(targetPage, true);
        }
    }

    // ---- Side panel tabs ---------------------------------------------------

    function switchTab(tab) {
        state.tab = tab;
        root.querySelectorAll('.fl-tab').forEach(b => {
            const on = b.getAttribute('data-tab') === tab;
            b.setAttribute('aria-selected', on ? 'true' : 'false');
            b.tabIndex = on ? 0 : -1;
        });
        root.querySelectorAll('.fl-pane').forEach(p => { p.hidden = p.id !== 'fl-pane-' + tab; });
        if (tab === 'preview') loadPreview();
    }

    function stopQueuePolling() {
        if (queueTimer) { clearInterval(queueTimer); queueTimer = null; }
    }

    function loading(el, text) {
        el.innerHTML = `<div class="fl-loading">${text}</div>`;
    }

    async function loadPreview() {
        const list = $('[data-role="preview-list"]');
        const summary = $('[data-role="preview-summary"]');
        loading(list, 'Evaluating rules…');
        const dirty = isDirty();
        const r = dirty
            ? await api('/api/flows/preview', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(Object.assign({}, state.active, { ingest: state.draft.ingest, edges: state.draft.edges }))
            })
            : await api('/api/flows/preview');
        if (!r.ok || !r.body) {
            list.innerHTML = F().empty('Preview failed', errText(r));
            return;
        }
        const tasks = r.body.tasks || [];
        const skipped = r.body.skipped || [];
        summary.textContent = `${dirty ? 'Draft' : 'Saved'} policy · ${tasks.length} move${tasks.length === 1 ? '' : 's'}, ${skipped.length} skipped`;
        if (!tasks.length && !skipped.length) {
            list.innerHTML = F().empty('Nothing to do', 'Every file already sits where the rules want it.');
            return;
        }
        list.innerHTML =
            (tasks.length ? `<h4 class="fl-list-title">Would run</h4>` + tasks.map(t => F().previewRow(t, state.tiers)).join('') : '') +
            (skipped.length ? `<h4 class="fl-list-title">Skipped</h4>` + skipped.map(s => F().skippedRow(s)).join('') : '');
    }

    async function loadQueue() {
        const list = $('[data-role="queue-list"]');
        if (!list.childElementCount) loading(list, 'Loading tasks…');
        const r = await api(`/api/lifecycle/tasks?state=${state.queueFilter}&limit=50`);
        if (!r.ok || !r.body) {
            list.innerHTML = F().empty('Could not load tasks', errText(r));
            return;
        }
        const tasks = r.body.tasks || [];
        if (state.queueFilter === 'active') {
            state.activeTasks = tasks.length;
            const badge = $('[data-role="queue-count"]');
            badge.hidden = tasks.length === 0;
            badge.textContent = String(tasks.length);
            renderStats();
        }
        list.innerHTML = tasks.length
            ? tasks.map(t => F().taskRow(t, state.tiers)).join('')
            : F().empty(`No ${state.queueFilter} tasks`, state.queueFilter === 'active' ? 'Moves appear here while they run.' : '');
    }

    async function cancelTask(id) {
        if (!global.confirm('Cancel this move? The partial copy on the destination is removed; the original stays.')) return;
        const r = await api(`/api/lifecycle/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
        toast(r.ok ? 'Task cancelled' : 'Cancel failed: ' + errText(r), r.ok ? undefined : 'error');
        loadQueue();
    }

    async function loadHot() {
        const list = $('[data-role="hot-list"]');
        loading(list, 'Ranking files…');
        const r = await api('/api/lifecycle/hot?limit=50');
        if (!r.ok || !r.body) {
            list.innerHTML = F().empty('Could not load hot files', errText(r));
            return;
        }
        const files = r.body.hot_files || [];
        list.innerHTML = files.length
            ? files.map((f, i) => F().hotRow(f, state.tiers, i + 1)).join('')
            : F().empty('No plays recorded yet', 'Plays through play_url raise a file\'s heat score.');
    }

    async function pin(key, btn) {
        if (btn) btn.disabled = true;
        const r = await api(`/api/v1/files/${encodeURIComponent(key)}/demand`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ level: 'high', ttl_hours: 24 })
        });
        toast(r.ok ? 'Marked as high demand' : 'Pin failed: ' + errText(r), r.ok ? undefined : 'error');
        loadHot();
        refreshCounts();
    }

    async function simulate() {
        const out = $('[data-role="sim-out"]');
        const size = parseFloat($('[data-role="sim-size"]').value);
        const unit = Number($('[data-role="sim-unit"]').value);
        if (!(size > 0)) {
            out.innerHTML = `<div class="fl-alert is-error">Enter a size above zero.</div>`;
            return;
        }
        loading(out, 'Checking placement…');
        const r = await api('/api/flows/simulate?size_bytes=' + Math.floor(size * unit));
        if (r.status === 507) {
            out.innerHTML = `<div class="fl-result is-error"><strong>No tier has room</strong><p>Every tier in the ingest order is full for this size. Add disks or demote files first.</p></div>`;
            return;
        }
        if (!r.ok || !r.body) {
            out.innerHTML = F().empty('Simulation failed', errText(r));
            return;
        }
        const s = r.body;
        const tier = state.tiers.find(t => t.id === s.tier_id);
        out.innerHTML = `<div class="fl-result">` +
            `<span class="fl-summary-label">Lands on</span>` +
            `<strong class="fl-result-tier">${global.UI.esc(tier ? tier.name : 'Tier ' + s.tier_id)}</strong>` +
            `<dl class="fl-kv is-2"><div><dt>Node</dt><dd class="mono">${global.UI.esc(s.node_id || '-')}</dd></div><div><dt>Path</dt><dd class="mono">${global.UI.esc(s.path || '/')}</dd></div></dl>` +
            (s.reason ? `<p class="fl-muted">${global.UI.esc(s.reason)}</p>` : '') +
            (tier ? F().capacity(tier) : '') +
            `</div>`;
    }

    async function loadAudit() {
        const list = $('[data-role="audit-list"]');
        loading(list, 'Loading audit log…');
        const r = await api('/api/lifecycle/audit?limit=50');
        if (!r.ok || !r.body) {
            list.innerHTML = F().empty('Could not load the audit log', errText(r));
            return;
        }
        const rows = r.body.audits || [];
        list.innerHTML = rows.length
            ? rows.map(a => F().auditRow(a, state.tiers)).join('')
            : F().empty('No deletions', 'Nothing has been removed by an expire rule.');
    }

    // ---- Events ------------------------------------------------------------

    function onClick(e) {
        const nav = e.target.closest('[data-nav]');
        if (nav && global.navigateTo) {
            e.preventDefault();
            global.navigateTo(nav.getAttribute('data-nav'), true);
            return;
        }
        const subnav = e.target.closest('.fl-subnav-btn');
        if (subnav) return switchSubView(subnav.getAttribute('data-view'));
        const tab = e.target.closest('.fl-tab');
        if (tab) return switchTab(tab.getAttribute('data-tab'));
        const q = e.target.closest('[data-queue]');
        if (q) {
            state.queueFilter = q.getAttribute('data-queue');
            root.querySelectorAll('[data-queue]').forEach(b => {
                const on = b === q;
                b.classList.toggle('is-active', on);
                b.setAttribute('aria-pressed', on ? 'true' : 'false');
            });
            $('[data-role="queue-list"]').innerHTML = '';
            return loadQueue();
        }
        const cancel = e.target.closest('[data-cancel]');
        if (cancel) return cancelTask(cancel.getAttribute('data-cancel'));
        const pinBtn = e.target.closest('[data-pin]');
        if (pinBtn) return pin(pinBtn.getAttribute('data-pin'), pinBtn);
        const preset = e.target.closest('[data-preset]');
        if (preset) {
            $('[data-role="sim-size"]').value = preset.getAttribute('data-preset');
            $('[data-role="sim-unit"]').value = preset.getAttribute('data-unit');
            return simulate();
        }
        if (e.target.classList && e.target.classList.contains('modal-overlay')) {
            closeExpireModal();
            closeDocsModal();
            return;
        }

        const btn = e.target.closest('[data-action]');
        if (!btn || btn.disabled) return;
        const action = btn.getAttribute('data-action');
        if (action === 'open-docs') return openDocsModal();
        if (action === 'docs-close') return closeDocsModal();
        // Inspector actions are handled by the inspector component itself.
        if (btn.closest('#fl-pane-details')) return;
        switch (action) {
            case 'open-docs': return openDocsModal();
            case 'docs-close': return closeDocsModal();
            case 'save': return save();
            case 'discard': return discard();
            case 'refresh': return load({ keepDraft: true });
            case 'new-rule': return select({ type: 'new' });
            case 'preview': return loadPreview();
            case 'hot': return loadHot();
            case 'audit': return loadAudit();
            case 'expire-cancel': return closeExpireModal();
            case 'expire-confirm': return save('DELETE FILES');
        }
    }

    function onInput(e) {
        if (e.target.matches && e.target.matches('[data-role="expire-input"]')) {
            root.querySelector('[data-action="expire-confirm"]').disabled = e.target.value.trim() !== 'DELETE FILES';
        }
    }

    function onKeydown(e) {
        // Roving focus across tabs (WAI-ARIA tabs pattern).
        if (e.target.classList && e.target.classList.contains('fl-tab') && (e.key === 'ArrowRight' || e.key === 'ArrowLeft')) {
            const tabs = Array.from(root.querySelectorAll('.fl-tab'));
            const i = tabs.indexOf(e.target);
            const next = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
            next.focus();
            switchTab(next.getAttribute('data-tab'));
            e.preventDefault();
            return;
        }
        if (e.key === 'Escape') {
            closeExpireModal();
            closeDocsModal();
        }
        const typing = /^(INPUT|SELECT|TEXTAREA)$/.test(e.target.tagName);
        if (!typing && (e.key === 'Delete' || e.key === 'Backspace') && state.selection.type === 'edge' && e.target.closest('#fl-board')) {
            e.preventDefault();
            deleteEdge(state.selection.id);
        }
        if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's' && isDirty()) {
            e.preventDefault();
            save();
        }
    }

    function onSubmit(e) {
        if (e.target.matches('[data-role="sim-form"]')) {
            e.preventDefault();
            simulate();
        }
    }

    function onBeforeUnload(e) {
        if (isDirty()) {
            e.preventDefault();
            e.returnValue = '';
        }
    }

    function mount() {
        root = document.getElementById('view-flows');
        if (!root) return false;
        root.addEventListener('click', onClick);
        root.addEventListener('input', onInput);
        root.addEventListener('keydown', onKeydown);
        root.addEventListener('submit', onSubmit);
        const board = $('#fl-board');
        if (typeof ResizeObserver === 'function') {
            let last = 0;
            boardObserver = new ResizeObserver(entries => {
                const w = Math.round(entries[0].contentRect.width);
                if (Math.abs(w - last) < 2) return;
                last = w;
                if (state.loaded && (w >= GRAPH_MIN_WIDTH) !== (state.mode === 'graph')) renderBoard();
            });
            boardObserver.observe(board);
        }
        mounted = true;
        return true;
    }

    global.App = global.App || { pages: {} };
    global.App.pages = global.App.pages || {};
    global.App.pages.flows = {
        getTiers() {
            return state.tiers || [];
        },
        switchSubView(view) {
            switchSubView(view);
        },
        init() {
            if (!mounted && !mount()) return;
            global.addEventListener('beforeunload', onBeforeUnload);
            load({ keepDraft: isDirty() });
            countTimer = setInterval(refreshCounts, 15000);
            renderBoard();
        },
        destroy() {
            if (countTimer) { clearInterval(countTimer); countTimer = null; }
            closeExpireModal();
            closeDocsModal();
            global.removeEventListener('beforeunload', onBeforeUnload);
        }
    };
})(window);
