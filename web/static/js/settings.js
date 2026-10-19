// Cluster & Engine settings: live mesh telemetry, BadgerDB status, secret management, and cache budgeting.
(function (global) {
    'use strict';

    let pollTimer = null;
    let pollCacheTimer = null;
    let isSecretRevealed = false;

    const CLUSTER_SECRET_TOKEN = 'stream-control-plane-session-2026';

    function renderClusterSecretOTP(masked = true) {
        const mount = document.getElementById('settings-otp-slots-mount');
        if (!mount) return;

        if (global.UI && typeof global.UI.otpSlots === 'function') {
            mount.innerHTML = global.UI.otpSlots(CLUSTER_SECRET_TOKEN, masked);
            return;
        }

        const chars = CLUSTER_SECRET_TOKEN.split('');
        mount.innerHTML = `
            <div style="display:flex;flex-wrap:wrap;gap:6px;align-items:center;">
                ${chars.map(c => `
                    <div style="width:26px;height:32px;display:inline-flex;align-items:center;justify-content:center;background:var(--bg-body);border:1px solid var(--border-light);border-radius:var(--radius-sm);font-family:'JetBrains Mono',monospace;font-size:12px;font-weight:700;color:var(--text-primary);">
                        ${masked ? '•' : c}
                    </div>
                `).join('')}
            </div>
        `;
    }

    function toggleClusterSecretMask() {
        isSecretRevealed = !isSecretRevealed;
        renderClusterSecretOTP(!isSecretRevealed);
        const label = document.getElementById('label-toggle-secret-mask');
        if (label) {
            label.innerText = isSecretRevealed ? 'Mask Key' : 'Reveal Key';
        }
    }

    function copyClusterJoinFlag(btnEl) {
        const flagText = `--secret="${CLUSTER_SECRET_TOKEN}"`;
        navigator.clipboard.writeText(flagText).then(() => {
            if (btnEl) {
                const orig = btnEl.innerHTML;
                btnEl.innerHTML = `
                    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" style="color:#10b981;"><polyline points="20 6 9 17 4 12"></polyline></svg>
                    <span style="color:#10b981;font-weight:700;">Copied!</span>
                `;
                setTimeout(() => { btnEl.innerHTML = orig; }, 1600);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Join flag copied to clipboard', 'success');
            }
        }).catch(() => {});
    }

    function renderSettings(pool) {
        renderClusterSecretOTP(!isSecretRevealed);

        if (!pool) return;
        const nodes = Array.isArray(pool.nodes) ? pool.nodes : [];
        const nodeCountEl = document.getElementById('settings-node-count');
        if (nodeCountEl) {
            nodeCountEl.innerText = `${nodes.length} Registered Node${nodes.length === 1 ? '' : 's'}`;
        }
    }

    function renderCacheCard() {
        fetch('/api/system/cache').then(r => r.json()).then(data => {
            const list = data.caches || [];
            const mount = document.getElementById('settings-cache-mount');
            const totalLbl = document.getElementById('cache-total-label');
            const bar = document.getElementById('cache-total-bar');

            const totalBytes = data.total_bytes || 0;
            const maxBytes = 50 * 1024 * 1024; // 50MB
            const pct = Math.min(100, (totalBytes / maxBytes) * 100).toFixed(1);

            if (totalLbl) totalLbl.innerText = `${global.formatBytes(totalBytes)} / 50 MB`;
            if (bar) bar.style.width = `${pct}%`;

            if (!mount) return;
            mount.innerHTML = list.map(c => `
                <div style="display:flex;justify-content:space-between;align-items:center;padding:8px 0;border-bottom:1px solid var(--border-light);font-size:12.5px;">
                    <span style="font-weight:600;color:var(--text-primary);display:flex;align-items:center;gap:6px;">
                        <span class="bui-swatch-sq" style="width:8px;height:8px;background:var(--orange-primary);border-radius:2px;"></span>
                        <code style="font-family:'JetBrains Mono',monospace;font-size:11px;color:var(--text-secondary);">${c.name}</code>
                    </span>
                    <span class="tabular-num" style="color:var(--text-secondary);white-space:nowrap;">${c.entries} entries · ${global.formatBytes(c.bytes || 0)}${c.evicted ? ` · ${c.evicted} evicted` : ''}</span>
                </div>`).join('');
        }).catch(() => {});
    }

    function init() {
        destroy();
        renderSettings(global.cachedPoolData);
        global.fetchClusterTelemetry();
        pollTimer = setInterval(global.fetchClusterTelemetry, 5000);
        renderCacheCard();
        pollCacheTimer = setInterval(renderCacheCard, 5000);
        if (currentSettingsTab === 'users') {
            loadUsers();
        }
    }

    function destroy() {
        if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
        if (pollCacheTimer) {
            clearInterval(pollCacheTimer);
            pollCacheTimer = null;
        }
    }

    async function handleSettingsPasswordChange(event) {
        event.preventDefault();
        const oldPass = document.getElementById('settings-old-pass').value;
        const newPass = document.getElementById('settings-new-pass').value;
        const btn = document.getElementById('btn-update-admin-pass');
        if (btn) btn.disabled = true;

        try {
            const res = await fetch('/api/auth/password', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ old_password: oldPass, new_password: newPass })
            });
            const data = await res.json();
            if (!res.ok) throw new Error(data.error || 'Failed to update password');

            global.showToast({ title: 'Success', message: 'Administrator password updated successfully.', type: 'success' });
            document.getElementById('settings-old-pass').value = '';
            document.getElementById('settings-new-pass').value = '';
        } catch (err) {
            global.showToast({ title: 'Error', message: err.message, type: 'error' });
        } finally {
            if (btn) btn.disabled = false;
        }
    }

    let currentSettingsTab = 'cluster';

    function switchSettingsTab(tab) {
        currentSettingsTab = tab;
        document.querySelectorAll('#view-settings .storage-tab-btn').forEach(btn => btn.classList.remove('active'));
        document.querySelectorAll('#view-settings .settings-tab-content').forEach(c => c.style.display = 'none');

        const btn = document.getElementById(`tab-btn-${tab}`);
        const content = document.getElementById(`settings-tab-${tab}`);
        if (btn) btn.classList.add('active');
        if (content) content.style.display = 'block';

        // Update sidebar sublink highlight if active
        document.querySelectorAll('#sidebar-submenu-settings .sidebar-sublink').forEach(el => {
            const dp = el.getAttribute('data-page');
            if ((tab === 'cluster' && dp === 'settings') || (tab === 'users' && dp === 'settings-users')) {
                el.classList.add('active');
            } else if (dp !== 'apikeys') {
                el.classList.remove('active');
            }
        });

        if (tab === 'users') {
            loadUsers();
        } else if (tab === 'cluster') {
            renderClusterSecretOTP(!isSecretRevealed);
        }
    }

    async function loadUsers() {
        const mount = document.getElementById('settings-users-table-mount');
        if (!mount) return;

        try {
            const res = await fetch('/api/auth/users');
            if (!res.ok) throw new Error(await res.text());
            const data = await res.json();
            renderUsers(data.users || []);
        } catch (err) {
            mount.innerHTML = `
                <div style="padding:28px 20px;text-align:center;color:var(--status-offline);font-size:13px;">
                    Failed to load users: ${global.escapeHtml ? global.escapeHtml(err.message) : err.message}
                </div>
            `;
        }
    }

    function renderUsers(users) {
        const mount = document.getElementById('settings-users-table-mount');
        if (!mount) return;

        if (!Array.isArray(users) || users.length === 0) {
            mount.innerHTML = `
                <div style="padding:32px 20px;text-align:center;color:var(--text-secondary);font-size:13px;">
                    No administrative accounts found.
                </div>
            `;
            return;
        }

        const rows = users.map(u => {
            const dateStr = u.created_at ? new Date(u.created_at).toLocaleString() : '—';
            const isSingle = users.length <= 1;
            const deleteBtn = isSingle 
                ? `<span style="font-size:11.5px;color:var(--text-muted);font-style:italic;" title="Cannot delete the sole administrator account">Required Account</span>`
                : `<button type="button" class="btn btn-secondary" onclick="confirmDeleteUser('${global.escapeHtml ? global.escapeHtml(u.username) : u.username}')" style="padding:4px 10px;font-size:12px;color:var(--status-offline,#ef4444);border-color:rgba(239,68,68,0.25);display:inline-flex;align-items:center;gap:4px;">
                    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                    <span>Delete</span>
                   </button>`;

            return `
                <tr style="border-bottom:1px solid var(--border-light);">
                    <td style="padding:14px 16px;">
                        <div style="display:flex;align-items:center;gap:10px;">
                            <div style="width:32px;height:32px;border-radius:50%;background:linear-gradient(135deg,var(--orange-primary),var(--orange-dark));display:flex;align-items:center;justify-content:center;color:#fff;font-weight:700;font-size:13px;text-transform:uppercase;">
                                ${(u.username || 'A')[0]}
                            </div>
                            <div>
                                <span style="font-weight:700;color:var(--text-primary);font-size:13.5px;">${global.escapeHtml ? global.escapeHtml(u.username) : u.username}</span>
                            </div>
                        </div>
                    </td>
                    <td style="padding:14px 16px;">
                        <span data-slot="badge" class="ui-badge ui-badge-emerald ui-badge-pill" style="font-size:11px;">Administrator</span>
                    </td>
                    <td style="padding:14px 16px;color:var(--text-secondary);font-size:12.5px;font-variant-numeric:tabular-nums;">
                        ${dateStr}
                    </td>
                    <td style="padding:14px 16px;text-align:right;">
                        ${deleteBtn}
                    </td>
                </tr>
            `;
        }).join('');

        mount.innerHTML = `
            <table class="custom-table responsive-card-table" style="width:100%;border-collapse:collapse;font-size:13px;">
                <thead>
                    <tr style="border-bottom:1px solid var(--border-light);text-align:left;font-size:11.5px;color:var(--text-secondary);text-transform:uppercase;letter-spacing:0.5px;">
                        <th style="padding:10px 16px;font-weight:700;">User</th>
                        <th style="padding:10px 16px;font-weight:700;">Role</th>
                        <th style="padding:10px 16px;font-weight:700;">Created</th>
                        <th style="padding:10px 16px;font-weight:700;text-align:right;">Action</th>
                    </tr>
                </thead>
                <tbody>
                    ${rows}
                </tbody>
            </table>
        `;
    }

    function openAddUserModal() {
        const modal = document.getElementById('modal-add-user');
        if (modal) {
            modal.style.display = 'flex';
            const input = document.getElementById('new-user-username');
            if (input) input.focus();
        }
    }

    function closeAddUserModal() {
        const modal = document.getElementById('modal-add-user');
        if (modal) modal.style.display = 'none';
        const form = document.getElementById('form-add-user');
        if (form) form.reset();
    }

    async function handleCreateUserSubmit(event) {
        event.preventDefault();
        const username = document.getElementById('new-user-username').value.trim();
        const pass = document.getElementById('new-user-password').value;
        const confirmPass = document.getElementById('new-user-confirm-password').value;
        const btn = document.getElementById('btn-submit-add-user');

        if (pass !== confirmPass) {
            global.showToast({ title: 'Validation Error', message: 'Passwords do not match.', type: 'error' });
            return;
        }

        if (btn) btn.disabled = true;

        try {
            const res = await fetch('/api/auth/users', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password: pass })
            });
            const data = await res.json();
            if (!res.ok) throw new Error(data.error || 'Failed to create user');

            global.showToast({ title: 'Success', message: `Administrator "${username}" created successfully.`, type: 'success' });
            closeAddUserModal();
            loadUsers();
        } catch (err) {
            global.showToast({ title: 'Error', message: err.message, type: 'error' });
        } finally {
            if (btn) btn.disabled = false;
        }
    }

    async function confirmDeleteUser(username) {
        if (!confirm(`Are you sure you want to permanently delete administrator "${username}"? This cannot be undone.`)) {
            return;
        }

        try {
            const res = await fetch(`/api/auth/users/${encodeURIComponent(username)}`, {
                method: 'DELETE'
            });
            const data = await res.json();
            if (!res.ok) throw new Error(data.error || 'Failed to delete user');

            global.showToast({ title: 'User Deleted', message: `User "${username}" was removed.`, type: 'success' });
            loadUsers();
        } catch (err) {
            global.showToast({ title: 'Error', message: err.message, type: 'error' });
        }
    }

    global.CLUSTER_SECRET_TOKEN = CLUSTER_SECRET_TOKEN;
    global.renderClusterSecretOTP = renderClusterSecretOTP;
    global.toggleClusterSecretMask = toggleClusterSecretMask;
    global.copyClusterJoinFlag = copyClusterJoinFlag;
    global.renderSettings = renderSettings;
    global.handleSettingsPasswordChange = handleSettingsPasswordChange;
    global.switchSettingsTab = switchSettingsTab;
    global.loadUsers = loadUsers;
    global.openAddUserModal = openAddUserModal;
    global.closeAddUserModal = closeAddUserModal;
    global.handleCreateUserSubmit = handleCreateUserSubmit;
    global.confirmDeleteUser = confirmDeleteUser;

    if (global.App && global.App.pages) {
        global.App.pages.settings = { init, destroy, render: renderSettings };
    }
})(window);
