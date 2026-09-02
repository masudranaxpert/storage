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

    async function api(path, opts) {
        const res = await fetch(path, opts);
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
        return data;
    }

    // ---------- tables & stats ----------

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
        const si = document.getElementById('db-search');
        if (si) si.value = '';
        renderTableList();
        loadRows();
    }

    // ---------- rows ----------

    async function loadRows() {
        const body = document.getElementById('db-rows-body');
        const label = document.getElementById('db-current-table-label');
        if (!currentTable) return;

        label.textContent = `Table: ${currentTable}:*`;
        if (body) body.innerHTML = `<tr><td colspan="5" style="padding:22px;text-align:center;color:var(--text-secondary);">Loading rows…</td></tr>`;

        try {
            const q = new URLSearchParams({ table: currentTable, page, limit, search });
            pageData = await api('/api/dbadmin/rows?' + q);
            renderRows();
        } catch (err) {
            if (body) body.innerHTML = `<tr><td colspan="5" style="padding:22px;text-align:center;color:#e5484d;">${esc(err.message)}</td></tr>`;
            pageData = null;
            renderRows();
        }
    }

    function renderRows() {
        const body = document.getElementById('db-rows-body');
        const info = document.getElementById('db-page-info');
        const prev = document.getElementById('db-prev-btn');
        const next = document.getElementById('db-next-btn');
        const trunc = document.getElementById('db-truncate-btn');
        if (!body) return;

        if (trunc) trunc.disabled = !currentTable || !(pageData && pageData.total > 0);

        if (!pageData || !pageData.rows || !pageData.rows.length) {
            body.innerHTML = `<tr><td colspan="5" style="padding:22px;text-align:center;color:var(--text-secondary);">${currentTable ? 'No rows found.' : 'No table selected — pick one from the left.'}</td></tr>`;
            if (info) info.textContent = currentTable ? '0 rows' : '';
            if (prev) prev.disabled = true;
            if (next) next.disabled = true;
            return;
        }

        body.innerHTML = pageData.rows.map(r => {
            const preview = previewOf(r.value);
            return `<tr style="border-bottom:1px solid var(--border-light);">
                <td style="padding:9px 10px;font-family:'JetBrains Mono',monospace;font-size:12px;cursor:pointer;color:var(--orange-darker);" onclick="DBStudio.viewRow('${esc(r.key)}')">${esc(r.key)}</td>
                <td style="padding:9px 10px;color:var(--text-secondary);">${fmtBytes(r.size)}</td>
                <td style="padding:9px 10px;color:var(--text-secondary);">${r.version}</td>
                <td style="padding:9px 10px;color:var(--text-secondary);font-family:'JetBrains Mono',monospace;font-size:11.5px;max-width:420px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${esc(preview)}</td>
                <td style="padding:9px 10px;white-space:nowrap;">
                    <button class="btn btn-secondary" style="padding:4px 10px;font-size:11px;" onclick="DBStudio.viewRow('${esc(r.key)}')">View</button>
                    <button class="btn btn-secondary" style="padding:4px 10px;font-size:11px;" onclick="DBStudio.viewRow('${esc(r.key)}', true)">Edit</button>
                    <button class="btn btn-secondary" style="padding:4px 10px;font-size:11px;color:#e5484d;border-color:#e5484d55;" onclick="DBStudio.deleteRow('${esc(r.key)}')">Delete</button>
                </td>
            </tr>`;
        }).join('');

        const tp = pageData.total_page || 1;
        if (info) info.textContent = `${pageData.total} row${pageData.total === 1 ? '' : 's'} — page ${pageData.page}/${tp}`;
        if (prev) prev.disabled = pageData.page <= 1;
        if (next) next.disabled = pageData.page >= tp;
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

    // ---------- row CRUD ----------

    async function viewRow(key, focusEdit) {
        editingKey = key;
        try {
            const q = new URLSearchParams({ table: currentTable, key });
            const row = await api('/api/dbadmin/entry?' + q);
            const title = document.getElementById('db-row-modal-title');
            const sub = document.getElementById('db-row-modal-sub');
            const editor = document.getElementById('db-row-editor');
            const errEl = document.getElementById('db-row-editor-err');
            if (title) title.textContent = focusEdit ? 'Edit Row' : 'Row Viewer';
            if (sub) sub.textContent = row.full_key;
            if (editor) {
                const pretty = typeof row.value === 'string' ? row.value : JSON.stringify(row.value, null, 2);
                editor.value = pretty;
                editor.readOnly = false;
                editor.focus();
            }
            if (errEl) { errEl.style.display = 'none'; errEl.textContent = ''; }
            openModal('db-row-modal');
        } catch (err) {
            alert('Failed to load row: ' + err.message);
        }
    }

    async function saveRow() {
        if (!editingKey || !currentTable) return;
        const editor = document.getElementById('db-row-editor');
        const errEl = document.getElementById('db-row-editor-err');
        try {
            JSON.parse(editor.value); // validate before sending
        } catch (e) {
            if (errEl) { errEl.textContent = 'Invalid JSON: ' + e.message; errEl.style.display = 'block'; }
            return;
        }
        if (errEl) errEl.style.display = 'none';

        try {
            await api('/api/dbadmin/entry', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ table: currentTable, key: editingKey, value: editor.value })
            });
            closeRowModal();
            await loadRows();
            await loadTables();
        } catch (err) {
            if (errEl) { errEl.textContent = err.message; errEl.style.display = 'block'; }
        }
    }

    async function deleteRow(key) {
        if (!currentTable) return;
        if (!confirm(`Delete key "${key}" from table "${currentTable}"?\n\nThis cannot be undone.`)) return;
        try {
            const q = new URLSearchParams({ table: currentTable, key });
            await api('/api/dbadmin/entry?' + q, { method: 'DELETE' });
            await loadRows();
            await loadTables();
        } catch (err) {
            alert('Delete failed: ' + err.message);
        }
    }

    async function truncate() {
        if (!currentTable) return;
        const count = pageData ? pageData.total : 0;
        if (!confirm(`TRUNCATE table "${currentTable}"?\n\nAll ${count} row(s) will be permanently deleted.`)) return;
        if (prompt(`Type the table name "${currentTable}" to confirm:`) !== currentTable) {
            alert('Confirmation mismatch — aborting.');
            return;
        }
        try {
            const res = await api('/api/dbadmin/truncate', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ table: currentTable })
            });
            alert(`Removed ${res.removed} row(s).`);
            page = 1;
            await loadTables();
            await loadRows();
        } catch (err) {
            alert('Truncate failed: ' + err.message);
        }
    }

    // ---------- create ----------

    function openCreate() {
        const t = document.getElementById('db-create-table');
        if (t && currentTable) t.value = currentTable;
        openModal('db-create-modal');
    }

    async function createRow() {
        const table = (document.getElementById('db-create-table') || {}).value?.trim();
        const key = (document.getElementById('db-create-key') || {}).value?.trim();
        const value = (document.getElementById('db-create-value') || {}).value;
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
                body: JSON.stringify({ table, key, value })
            });
            closeCreateModal();
            if (table === currentTable) await loadRows();
            await loadTables();
        } catch (err) {
            if (errEl) { errEl.textContent = err.message; errEl.style.display = 'block'; }
        }
    }

    // ---------- modal helpers ----------

    function openModal(id) {
        const el = document.getElementById(id);
        if (el) el.classList.add('open');
    }

    function closeRowModal() {
        const el = document.getElementById('db-row-modal');
        if (el) el.classList.remove('open');
        editingKey = null;
    }

    function closeCreateModal() {
        const el = document.getElementById('db-create-modal');
        if (el) el.classList.remove('open');
    }

    function setText(id, text) {
        const el = document.getElementById(id);
        if (el) el.textContent = text;
    }

    return {
        refresh, selectTable, search: searchRows, prevPage, nextPage,
        viewRow, saveRow, deleteRow, truncate,
        openCreate, createRow, closeRowModal, closeCreateModal
    };
})();

window.DBStudio = DBStudio;
