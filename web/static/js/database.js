// Database Studio — BadgerDB table browser & CRUD console (phpMyAdmin style).
const DBStudio = (() => {
    let tables = [];
    let currentTable = null;
    let page = 1;
    let limit = 50;
    let search = '';
    let pageData = null;
    let editingKey = null;

    const esc = (s) => String(s ?? '').replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));

    const fmtBytes = (b) => {
        if (b == null) return '—';
        if (b < 1024) return b + ' B';
        if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB';
        return (b / 1024 / 1024).toFixed(2) + ' MB';
    };

    let selectedKeys = new Set();
    let pendingConfirmAction = null;
    let expectedConfirmInput = '';

    async function api(path, opts) {
        const options = opts || {};
        options.headers = Object.assign({
            'X-Cluster-Secret': window.CLUSTER_SECRET_TOKEN || 'cluster-secret-key-2026'
        }, options.headers || {});
        const res = await fetch(path, options);
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
        return data;
    }
    async function refresh() {
        await loadTables();
        if (currentTable) await loadRows();
    }

    async function loadTables() {
        try {
            const data = await api('/api/dbadmin/tables');
            tables = data.tables || [];
            const stats = data.stats || {};
            setText('db-stat-lsm', fmtBytes(stats.lsm_size));
            setText('db-stat-stored', fmtBytes(stats.stored_bytes));
            setText('db-stat-keys', stats.key_count ?? '—');
            setText('db-stat-tables', stats.table_count ?? '—');
            renderTableList();
            const dl = document.getElementById('db-table-datalist');
            if (dl) dl.innerHTML = tables.map(t => `<option value="${esc(t.name)}">`).join('');
        } catch (err) {
            const list = document.getElementById('db-table-list');
            if (list) list.innerHTML = `<div style="font-size:13px;color:#e5484d;padding:8px;">${esc(err.message)}</div>`;
        }
    }

    function renderTableList() {
        const list = document.getElementById('db-table-list');
        if (!list) return;
        if (!tables.length) {
            list.innerHTML = '<div style="font-size:13px;color:var(--text-secondary);padding:8px;">No tables found.</div>';
            return;
        }
        list.innerHTML = tables.map(t => `
            <div class="db-table-chip ${t.name === currentTable ? 'active' : ''}" onclick="DBStudio.selectTable('${esc(t.name)}')">
                <span class="db-table-name">${esc(t.name)}</span>
                <span class="db-table-count">${t.count}</span>
            </div>`).join('');
    }

    function selectTable(name) {
        currentTable = name;
        page = 1;
        search = '';
        selectedKeys.clear();
        const si = document.getElementById('db-search');
        if (si) si.value = '';
        const eb = document.getElementById('db-empty-table-btn');
        if (eb) eb.style.display = 'inline-flex';
        updateBatchBar();
        renderTableList();
        loadRows();
    }

    async function loadRows() {
        const body = document.getElementById('db-rows-body');
        const label = document.getElementById('db-current-table-label');
        if (!currentTable) return;

        label.textContent = `Table: ${currentTable}:*`;
        if (body) body.innerHTML = `<tr><td colspan="6" style="padding:22px;text-align:center;color:var(--text-secondary);">Loading rows…</td></tr>`;

        try {
            const q = new URLSearchParams({ table: currentTable, page, limit, search });
            pageData = await api('/api/dbadmin/rows?' + q);
            renderRows();
        } catch (err) {
            if (body) body.innerHTML = `<tr><td colspan="6" style="padding:22px;text-align:center;color:#e5484d;">${esc(err.message)}</td></tr>`;
            pageData = null;
            renderRows();
        }
    }

    function updateBatchBar() {
        const bar = document.getElementById('db-batch-actions');
        const cnt = document.getElementById('db-selected-count');
        if (!bar) return;
        if (selectedKeys.size > 0) {
            bar.style.display = 'inline-flex';
            if (cnt) cnt.textContent = `${selectedKeys.size} selected`;
        } else {
            bar.style.display = 'none';
        }
    }

    function toggleSelectAll(cb) {
        if (!pageData || !pageData.rows) return;
        if (cb.checked) {
            pageData.rows.forEach(r => selectedKeys.add(r.key));
        } else {
            pageData.rows.forEach(r => selectedKeys.delete(r.key));
        }
        renderRows();
        updateBatchBar();
    }

    function toggleRowSelection(key, checked) {
        if (checked) {
            selectedKeys.add(key);
        } else {
            selectedKeys.delete(key);
        }
        syncSelectAllCheckbox();
        updateBatchBar();
    }

    function clearSelection() {
        selectedKeys.clear();
        syncSelectAllCheckbox();
        updateBatchBar();
        renderRows();
    }

    function syncSelectAllCheckbox() {
        const sa = document.getElementById('db-select-all');
        if (!sa || !pageData || !pageData.rows || !pageData.rows.length) {
            if (sa) { sa.checked = false; sa.indeterminate = false; }
            return;
        }
        const allOnPage = pageData.rows.map(r => r.key);
        const allChecked = allOnPage.every(k => selectedKeys.has(k));
        const someChecked = allOnPage.some(k => selectedKeys.has(k));
        sa.checked = allChecked;
        sa.indeterminate = !allChecked && someChecked;
    }

    function renderRows() {
        const body = document.getElementById('db-rows-body');
        const info = document.getElementById('db-page-info');
        const pagContainer = document.getElementById('db-pagination-container');
        if (!body) return;

        if (!pageData || !pageData.rows || !pageData.rows.length) {
            const emptyMessage = currentTable 
                ? `<div style="font-weight:600;font-size:14px;color:var(--text-primary);margin-bottom:4px;">No rows found</div><div style="font-size:12px;color:var(--text-secondary);margin-bottom:12px;">This table contains no documents matching your search.</div><button type="button" class="btn btn-secondary" style="font-size:12px;padding:5px 12px;" onclick="DBStudio.openCreate()">Add first row</button>`
                : `<div style="font-weight:600;font-size:14px;color:var(--text-primary);margin-bottom:4px;">No table selected</div><div style="font-size:12px;color:var(--text-secondary);">Select a table namespace from the left to inspect records.</div>`;
            body.innerHTML = `<tr><td colspan="6" style="padding:32px 20px;text-align:center;">${emptyMessage}</td></tr>`;
            if (info) info.textContent = currentTable ? '0 rows' : '';
            if (pagContainer) pagContainer.innerHTML = '';
            syncSelectAllCheckbox();
            return;
        }

        body.innerHTML = pageData.rows.map(r => {
            const preview = previewOf(r.value);
            const isChecked = selectedKeys.has(r.key);
            return `<tr style="border-bottom:1px solid var(--border-light);" class="${isChecked ? 'row-selected' : ''}">
                <td style="text-align:center;padding:9px 6px;">
                    <input type="checkbox" class="db-row-checkbox" value="${esc(r.key)}" ${isChecked ? 'checked' : ''} onchange="DBStudio.toggleRowSelection('${esc(r.key)}', this.checked)" aria-label="Select row ${esc(r.key)}" style="cursor:pointer;accent-color:var(--orange-primary);">
                </td>
                <td data-label="Key" style="padding:9px 10px;font-family:'JetBrains Mono',monospace;font-size:12px;cursor:pointer;color:var(--orange-darker);font-weight:700;" onclick="DBStudio.viewRow('${esc(r.key)}')">${esc(r.key)}</td>
                <td data-label="Size" class="tabular-num" style="padding:9px 10px;color:var(--text-secondary);">${fmtBytes(r.size)}</td>
                <td data-label="Version" class="tabular-num" style="padding:9px 10px;color:var(--text-secondary);">v${r.version}</td>
                <td data-label="Value Preview" style="padding:9px 10px;color:var(--text-secondary);font-family:'JetBrains Mono',monospace;font-size:11.5px;max-width:420px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${esc(preview)}</td>
                <td data-label="Actions" style="padding:8px 10px;white-space:nowrap;text-align:right;">
                    <div style="display:inline-flex;gap:5px;align-items:center;justify-content:flex-end;">
                        <button type="button" class="db-row-action-btn" title="View JSON" aria-label="View JSON" onclick="DBStudio.viewRow('${esc(r.key)}')">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path><circle cx="12" cy="12" r="3"></circle></svg>
                        </button>
                        <button type="button" class="db-row-action-btn" title="Edit Row" aria-label="Edit Row" onclick="DBStudio.viewRow('${esc(r.key)}', true)">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
                        </button>
                        <button type="button" class="db-row-action-btn btn-delete" title="Delete Row" aria-label="Delete Row" onclick="DBStudio.deleteRow('${esc(r.key)}')">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                        </button>
                    </div>
                </td>
            </tr>`;
        }).join('');

        const tp = pageData.total_page || 1;
        const startIdx = pageData.total === 0 ? 0 : (pageData.page - 1) * limit + 1;
        const endIdx = Math.min(pageData.page * limit, pageData.total);
        if (info) {
            info.innerHTML = pageData.total > 0 
                ? `Showing <strong class="tabular-num">${startIdx}–${endIdx}</strong> of <strong class="tabular-num">${pageData.total}</strong> rows (Page ${pageData.page} of ${tp})` 
                : '0 rows';
        }
        if (window.UI && typeof window.UI.pagination === 'function') {
            window.UI.pagination('db-pagination-container', {
                currentPage: pageData.page,
                totalPages: tp,
                onPageChange: (newPage) => {
                    page = newPage;
                    loadRows();
                }
            });
        }
        syncSelectAllCheckbox();
        updateBatchBar();
    }

    function previewOf(value) {
        try {
            const obj = typeof value === 'string' ? JSON.parse(value) : value;
            const s = JSON.stringify(obj);
            return s.length > 160 ? s.slice(0, 160) + '…' : s;
        } catch {
            return String(value).slice(0, 160);
        }
    }

    function searchRows() {
        const si = document.getElementById('db-search');
        search = (si && si.value) || '';
        page = 1;
        loadRows();
    }

    function prevPage() { if (page > 1) { page--; loadRows(); } }
    function nextPage() { if (!pageData || page < pageData.total_page) { page++; loadRows(); } }

    let currentRawJSON = '';
    let rowEditor = null;
    let createEditor = null;

    function ensureRowEditor() {
        if (!rowEditor && window.UI && window.UI.codeEditor) {
            rowEditor = window.UI.codeEditor('db-row-editor', {
                wrap: 'db-row-editor-wrap',
                pre: 'db-row-editor-pre',
                code: 'db-row-editor-code'
            });
        }
        return rowEditor;
    }

    function ensureCreateEditor() {
        if (!createEditor && window.UI && window.UI.codeEditor) {
            createEditor = window.UI.codeEditor('db-create-value', {
                wrap: 'db-create-editor-wrap',
                pre: 'db-create-editor-pre',
                code: 'db-create-editor-code'
            });
        }
        return createEditor;
    }

    function setEditorMode(mode) {
        const prettyCard = document.getElementById('db-row-pretty-container');
        const wrap = document.getElementById('db-row-editor-wrap');
        const editor = document.getElementById('db-row-editor');
        const btnPretty = document.getElementById('db-view-mode-pretty');
        const btnRaw = document.getElementById('db-view-mode-raw');
        const saveBtn = document.getElementById('db-row-save-btn');
        const formatBtn = document.getElementById('db-format-json-btn');

        if (mode === 'raw') {
            if (prettyCard) prettyCard.style.display = 'none';
            if (wrap) wrap.style.display = 'block';
            else if (editor) editor.style.display = 'block';

            const ed = ensureRowEditor();
            if (ed) {
                ed.setValue(currentRawJSON);
                ed.focus();
            } else if (editor) {
                editor.value = currentRawJSON;
                editor.focus();
            }

            if (btnPretty) btnPretty.classList.remove('active');
            if (btnRaw) btnRaw.classList.add('active');
            if (saveBtn) saveBtn.style.display = 'inline-flex';
            if (formatBtn) formatBtn.style.display = 'inline-flex';
        } else {
            const ed = ensureRowEditor();
            if (ed) {
                currentRawJSON = ed.getValue();
            } else if (editor) {
                currentRawJSON = editor.value;
            }

            if (wrap) wrap.style.display = 'none';
            else if (editor) editor.style.display = 'none';

            const prettyBody = document.getElementById('db-row-pretty-body');
            if (prettyBody && window.UI && window.UI.highlightJSON) {
                prettyBody.innerHTML = window.UI.highlightJSON(currentRawJSON);
            }
            if (prettyCard) prettyCard.style.display = 'flex';
            if (btnPretty) btnPretty.classList.add('active');
            if (btnRaw) btnRaw.classList.remove('active');
            if (formatBtn) formatBtn.style.display = 'none';
        }
    }

    function formatCurrentJSON() {
        const ed = ensureRowEditor();
        const editor = document.getElementById('db-row-editor');
        const errEl = document.getElementById('db-row-editor-err');
        const raw = ed ? ed.getValue() : (editor ? editor.value : '');
        try {
            const parsed = JSON.parse(raw);
            const formatted = JSON.stringify(parsed, null, 2);
            if (ed) ed.setValue(formatted);
            else if (editor) editor.value = formatted;
            currentRawJSON = formatted;
            if (errEl) { errEl.style.display = 'none'; errEl.textContent = ''; }
        } catch (err) {
            if (errEl) {
                errEl.style.display = 'block';
                errEl.textContent = 'Invalid JSON: ' + err.message;
            }
        }
    }

    async function viewRow(key, focusEdit) {
        editingKey = key;
        try {
            const q = new URLSearchParams({ table: currentTable, key });
            const row = await api('/api/dbadmin/entry?' + q);
            const title = document.getElementById('db-row-modal-title');
            const sub = document.getElementById('db-row-modal-sub');
            const prettyBody = document.getElementById('db-row-pretty-body');
            const errEl = document.getElementById('db-row-editor-err');

            if (title) title.textContent = focusEdit ? 'Edit Row' : 'Row Viewer';
            if (sub) sub.textContent = row.full_key;

            const pretty = typeof row.value === 'string' ? row.value : JSON.stringify(row.value, null, 2);
            currentRawJSON = pretty;

            const ed = ensureRowEditor();
            if (ed) ed.setValue(pretty);

            if (prettyBody) {
                if (window.UI && window.UI.highlightJSON) {
                    prettyBody.innerHTML = window.UI.highlightJSON(pretty);
                } else {
                    prettyBody.textContent = pretty;
                }
            }

            setEditorMode(focusEdit ? 'raw' : 'pretty');

            if (errEl) { errEl.style.display = 'none'; errEl.textContent = ''; }
            openModal('db-row-modal');
        } catch (err) {
            alert('Failed to load row: ' + err.message);
        }
    }

    async function copyJSON(btn) {
        const ed = ensureRowEditor();
        const textToCopy = (ed ? ed.getValue() : (document.getElementById('db-row-editor') || {}).value) || currentRawJSON;
        try {
            await navigator.clipboard.writeText(textToCopy);
            if (btn) {
                const orig = btn.innerHTML;
                btn.innerHTML = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4"><polyline points="20 6 9 17 4 12"></polyline></svg><span>Copied!</span>`;
                setTimeout(() => { btn.innerHTML = orig; }, 1800);
            }
        } catch (_) {
            prompt('Copy JSON payload:', textToCopy);
        }
    }

    async function saveRow() {
        if (!editingKey || !currentTable) return;
        const ed = ensureRowEditor();
        const editor = document.getElementById('db-row-editor');
        const errEl = document.getElementById('db-row-editor-err');
        const val = ed ? ed.getValue() : (editor ? editor.value : '');

        try {
            JSON.parse(val); // validate before sending
        } catch (e) {
            if (errEl) { errEl.textContent = 'Invalid JSON: ' + e.message; errEl.style.display = 'block'; }
            return;
        }
        if (errEl) errEl.style.display = 'none';

        try {
            await api('/api/dbadmin/entry', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ table: currentTable, key: editingKey, value: JSON.parse(val) })
            });
            closeRowModal();
            await loadRows();
            await loadTables();
        } catch (err) {
            if (errEl) { errEl.textContent = err.message; errEl.style.display = 'block'; }
        }
    }

    function showConfirmModal(opts) {
        const titleEl = document.getElementById('db-confirm-title');
        const subEl = document.getElementById('db-confirm-sub');
        const msgEl = document.getElementById('db-confirm-message');
        const inputWrap = document.getElementById('db-confirm-input-wrap');
        const inputLabel = document.getElementById('db-confirm-input-label');
        const inputEl = document.getElementById('db-confirm-input');
        const btnEl = document.getElementById('db-confirm-action-btn');
        const errEl = document.getElementById('db-confirm-err');

        if (titleEl) titleEl.textContent = opts.title || 'Confirm Deletion';
        if (subEl) subEl.textContent = opts.sub || 'Irreversible database action';
        if (msgEl) msgEl.innerHTML = opts.message || 'Are you sure?';
        if (errEl) { errEl.textContent = ''; errEl.style.display = 'none'; }

        pendingConfirmAction = opts.onConfirm || null;
        expectedConfirmInput = opts.expectedInput || '';

        if (opts.requireInput) {
            if (inputWrap) inputWrap.style.display = 'block';
            if (inputLabel) inputLabel.textContent = opts.inputLabel || `Type "${opts.expectedInput}" to confirm:`;
            if (inputEl) {
                inputEl.value = '';
                inputEl.placeholder = opts.expectedInput;
            }
            if (btnEl) {
                btnEl.textContent = opts.actionBtnText || 'Delete';
                btnEl.disabled = true;
                btnEl.style.opacity = '0.5';
                btnEl.style.cursor = 'not-allowed';
            }
        } else {
            if (inputWrap) inputWrap.style.display = 'none';
            if (btnEl) {
                btnEl.textContent = opts.actionBtnText || 'Delete';
                btnEl.disabled = false;
                btnEl.style.opacity = '1';
                btnEl.style.cursor = 'pointer';
            }
        }

        openModal('db-confirm-modal');
        if (opts.requireInput && inputEl) setTimeout(() => inputEl.focus(), 100);
    }

    function onConfirmInputChange(val) {
        const btnEl = document.getElementById('db-confirm-action-btn');
        const matches = (val.trim() === expectedConfirmInput.trim());
        if (btnEl) {
            btnEl.disabled = !matches;
            btnEl.style.opacity = matches ? '1' : '0.5';
            btnEl.style.cursor = matches ? 'pointer' : 'not-allowed';
        }
    }

    async function executeConfirmedAction() {
        if (typeof pendingConfirmAction === 'function') {
            const errEl = document.getElementById('db-confirm-err');
            try {
                await pendingConfirmAction();
            } catch (err) {
                if (errEl) {
                    errEl.textContent = err.message || 'Action failed';
                    errEl.style.display = 'block';
                }
            }
        }
    }

    function closeConfirmModal() {
        const el = document.getElementById('db-confirm-modal');
        if (el) el.classList.remove('open');
        pendingConfirmAction = null;
        expectedConfirmInput = '';
        if (typeof window.setModalOpen === 'function') window.setModalOpen(false);
    }

    function deleteRow(key) {
        if (!currentTable) return;
        showConfirmModal({
            title: `Delete Row "${key}"?`,
            sub: `Table: ${currentTable}`,
            message: `Permanently delete key "<code>${esc(key)}</code>" from table "<strong>${esc(currentTable)}</strong>"? This action cannot be undone.`,
            actionBtnText: 'Delete Row',
            onConfirm: async () => {
                const q = new URLSearchParams({ table: currentTable, key });
                await api('/api/dbadmin/entry?' + q, { method: 'DELETE' });
                selectedKeys.delete(key);
                closeConfirmModal();
                await loadRows();
                await loadTables();
            }
        });
    }

    function deleteSelectedRows() {
        if (!currentTable || selectedKeys.size === 0) return;
        const count = selectedKeys.size;
        showConfirmModal({
            title: `Delete ${count} Selected Row${count === 1 ? '' : 's'}?`,
            sub: `Table: ${currentTable}`,
            message: `Permanently delete <strong>${count} selected record${count === 1 ? '' : 's'}</strong> from table "<strong>${esc(currentTable)}</strong>"? This action cannot be undone.`,
            actionBtnText: `Delete ${count} Row${count === 1 ? '' : 's'}`,
            onConfirm: async () => {
                await api('/api/dbadmin/batch-delete', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ table: currentTable, keys: Array.from(selectedKeys) })
                });
                selectedKeys.clear();
                closeConfirmModal();
                await loadRows();
                await loadTables();
            }
        });
    }

    function promptEmptyCurrentTable() {
        if (!currentTable) return;
        const count = pageData ? pageData.total : 0;
        showConfirmModal({
            title: `Empty Table "${currentTable}"?`,
            sub: `Drop all records in ${currentTable}`,
            message: `All <strong>${count} record${count === 1 ? '' : 's'}</strong> in table "<strong>${esc(currentTable)}</strong>" will be permanently deleted. This action cannot be undone.`,
            requireInput: true,
            expectedInput: currentTable,
            inputLabel: `Type "${currentTable}" to confirm:`,
            actionBtnText: 'Empty Table',
            onConfirm: async () => {
                await api('/api/dbadmin/truncate', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ table: currentTable })
                });
                selectedKeys.clear();
                closeConfirmModal();
                page = 1;
                await loadTables();
                await loadRows();
            }
        });
    }

    function promptClearAllDB() {
        showConfirmModal({
            title: 'Clear Entire Database?',
            sub: 'Total BadgerDB wipe',
            message: '⚠️ <strong>DANGER:</strong> All records across <strong>every table</strong> in the cluster will be dropped permanently. This action cannot be undone.',
            requireInput: true,
            expectedInput: 'CONFIRM',
            inputLabel: 'Type "CONFIRM" in capital letters to proceed:',
            actionBtnText: 'Clear Entire Database',
            onConfirm: async () => {
                await api('/api/dbadmin/truncate', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ table: '*' })
                });
                selectedKeys.clear();
                closeConfirmModal();
                currentTable = null;
                page = 1;
                await loadTables();
                await loadRows();
            }
        });
    }

    function openCreate() {
        const t = document.getElementById('db-create-table');
        if (t && currentTable) t.value = currentTable;
        const k = document.getElementById('db-create-key');
        if (k) k.value = '';
        const ed = ensureCreateEditor();
        if (ed) ed.setValue('{\n  \n}');
        else {
            const v = document.getElementById('db-create-value');
            if (v) v.value = '{\n  \n}';
        }
        openModal('db-create-modal');
    }

    async function createRow() {
        const table = (document.getElementById('db-create-table') || {}).value?.trim();
        const key = (document.getElementById('db-create-key') || {}).value?.trim();
        const ed = ensureCreateEditor();
        const value = ed ? ed.getValue() : ((document.getElementById('db-create-value') || {}).value || '');
        const errEl = document.getElementById('db-create-err');
        if (errEl) errEl.style.display = 'none';

        if (!table || !key) {
            if (errEl) { errEl.textContent = 'Both table and key are required.'; errEl.style.display = 'block'; }
            return;
        }
        try {
            JSON.parse(value);
        } catch (e) {
            if (errEl) { errEl.textContent = 'Invalid JSON: ' + e.message; errEl.style.display = 'block'; }
            return;
        }

        try {
            await api('/api/dbadmin/entry', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ table, key, value: JSON.parse(value) })
            });
            closeCreateModal();
            if (table === currentTable) await loadRows();
            await loadTables();
        } catch (err) {
            if (errEl) { errEl.textContent = err.message; errEl.style.display = 'block'; }
        }
    }

    function openModal(id) {
        const el = document.getElementById(id);
        if (el) el.classList.add('open');
        if (typeof window.setModalOpen === 'function') window.setModalOpen(true);
    }

    function closeRowModal() {
        const el = document.getElementById('db-row-modal');
        if (el) el.classList.remove('open');
        editingKey = null;
        if (typeof window.setModalOpen === 'function') window.setModalOpen(false);
    }

    function closeCreateModal() {
        const el = document.getElementById('db-create-modal');
        if (el) el.classList.remove('open');
        if (typeof window.setModalOpen === 'function') window.setModalOpen(false);
    }

    function setText(id, text) {
        const el = document.getElementById(id);
        if (el) el.textContent = text;
    }

    return {
        refresh, selectTable, search: searchRows, prevPage, nextPage,
        viewRow, saveRow, deleteRow,
        toggleSelectAll, toggleRowSelection, clearSelection, deleteSelectedRows,
        promptEmptyCurrentTable, promptClearAllDB,
        closeConfirmModal, onConfirmInputChange, executeConfirmedAction,
        openCreate, createRow, closeRowModal, closeCreateModal,
        setEditorMode, copyJSON, formatCurrentJSON
    };
})();

window.DBStudio = DBStudio;
if (window.App && window.App.pages) {
    window.App.pages.database = {
        init: () => DBStudio.refresh(),
        destroy: () => {}
    };
}
