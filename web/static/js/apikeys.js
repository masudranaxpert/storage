// Developer Authentication: API Access Tokens management and interactive Integration Guide.
(function (global) {
    'use strict';

    function formatDateTime(isoStr) {
        if (!isoStr || isoStr.startsWith('0001')) return 'Never';
        const d = new Date(isoStr);
        if (isNaN(d.getTime())) return 'Never';
        return d.toLocaleDateString(undefined, {
            month: 'short',
            day: 'numeric',
            year: 'numeric',
            hour: '2-digit',
            minute: '2-digit'
        });
    }

    function loadAPIKeys() {
        const mount = document.getElementById('api-keys-table-mount');
        if (!mount) return;

        fetch('/api/keys')
            .then(res => {
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                return res.json();
            })
            .then(data => {
                const keys = data.keys || [];

                if (keys.length === 0) {
                    mount.innerHTML = `
                        <div style="padding:48px 24px;text-align:center;display:flex;flex-direction:column;align-items:center;gap:12px;">
                            <div class="bui-squircle-icon grad-orange" style="width:48px;height:48px;margin-bottom:4px;">
                                <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2">
                                    <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"></path>
                                </svg>
                            </div>
                            <h4 style="font-size:15px;font-weight:700;color:var(--text-primary);margin:0;">No API Keys Generated</h4>
                            <p style="font-size:13px;color:var(--text-secondary);max-width:380px;margin:0;line-height:1.5;">
                                Create an API key to securely ingest videos, poll playback status, and trigger deletions programmatically.
                            </p>
                            <button type="button" class="btn btn-primary" onclick="openCreateAPIKeyModal()" style="margin-top:6px;display:inline-flex;align-items:center;gap:6px;font-size:13px;padding:8px 16px;">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3"><line x1="12" y1="5" x2="12" y2="19"></line><line x1="5" y1="12" x2="19" y2="12"></line></svg>
                                <span>Generate Your First API Key</span>
                            </button>
                        </div>
                    `;
                    return;
                }

                mount.innerHTML = `
                    <table class="api-keys-table">
                        <thead>
                            <tr>
                                <th style="width:28%;">Name</th>
                                <th style="width:28%;">Key Prefix</th>
                                <th style="width:18%;">Created</th>
                                <th style="width:16%;">Last Used</th>
                                <th style="width:10%;text-align:right;">Actions</th>
                            </tr>
                        </thead>
                        <tbody>
                            ${keys.map(k => `
                                <tr>
                                    <td>
                                        <div style="font-weight:700;color:var(--text-primary);font-size:13px;">${escapeHtml(k.name || 'API Key')}</div>
                                    </td>
                                    <td>
                                        <code class="ui-badge ui-badge-neutral ui-badge-mono ui-badge-rect" style="font-size:11.5px;letter-spacing:0.3px;">
                                            ${escapeHtml(k.prefix || 'sm_live_••••')}
                                        </code>
                                    </td>
                                    <td>
                                        <span class="tabular-num" style="font-size:12px;color:var(--text-secondary);">
                                            ${formatDateTime(k.created_at)}
                                        </span>
                                    </td>
                                    <td>
                                        <span class="tabular-num" style="font-size:12px;color:var(--text-secondary);">
                                            ${formatDateTime(k.last_used)}
                                        </span>
                                    </td>
                                    <td style="text-align:right;">
                                        <div style="display:inline-flex;align-items:center;gap:6px;justify-content:flex-end;">
                                            <button type="button" class="tbl-icon-btn" onclick="copyRawAPIKey('${escapeHtml(k.key)}', this)" title="Copy API Key" aria-label="Copy API Key">
                                                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                                    <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
                                                    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
                                                </svg>
                                            </button>
                                            <button type="button" class="tbl-icon-btn is-danger" onclick="deleteAPIKey('${escapeHtml(k.key)}', '${escapeHtml(k.name || 'Key')}')" title="Revoke API Key" aria-label="Revoke API Key">
                                                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                                    <polyline points="3 6 5 6 21 6"></polyline>
                                                    <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                                                </svg>
                                            </button>
                                        </div>
                                    </td>
                                </tr>
                            `).join('')}
                        </tbody>
                    </table>
                `;
            })
            .catch(err => {
                mount.innerHTML = `
                    <div style="padding:24px;text-align:center;color:#ef4444;font-size:13px;">
                        Failed to load API keys: ${err.message}
                    </div>
                `;
            });
    }

    function openCreateAPIKeyModal() {
        const modal = document.getElementById('modal-create-api-key');
        const inputStep = document.getElementById('modal-apikey-step-input');
        const resultStep = document.getElementById('modal-apikey-step-result');
        const nameInput = document.getElementById('apikey-name-input');
        const btnSubmit = document.getElementById('btn-submit-create-key');

        if (!modal) return;
        if (inputStep) inputStep.style.display = 'flex';
        if (resultStep) resultStep.style.display = 'none';
        if (nameInput) {
            nameInput.value = '';
            setTimeout(() => nameInput.focus(), 80);
        }
        if (btnSubmit) {
            btnSubmit.disabled = false;
            btnSubmit.innerText = 'Generate Key';
        }

        modal.style.display = 'flex';
        modal.classList.add('open');
    }

    function closeCreateAPIKeyModal() {
        const modal = document.getElementById('modal-create-api-key');
        if (modal) {
            modal.style.display = 'none';
            modal.classList.remove('open');
        }
        loadAPIKeys();
    }

    function submitCreateAPIKey() {
        const nameInput = document.getElementById('apikey-name-input');
        const btnSubmit = document.getElementById('btn-submit-create-key');
        const inputStep = document.getElementById('modal-apikey-step-input');
        const resultStep = document.getElementById('modal-apikey-step-result');
        const secretInput = document.getElementById('apikey-secret-result');

        const name = (nameInput && nameInput.value.trim()) || 'Production Pipeline';
        if (btnSubmit) {
            btnSubmit.disabled = true;
            btnSubmit.innerText = 'Generating…';
        }

        fetch('/api/keys', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name })
        })
            .then(res => {
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                return res.json();
            })
            .then(rec => {
                if (inputStep) inputStep.style.display = 'none';
                if (resultStep) resultStep.style.display = 'flex';
                if (secretInput) secretInput.value = rec.key;
            })
            .catch(err => {
                alert('Failed to generate key: ' + err.message);
                if (btnSubmit) {
                    btnSubmit.disabled = false;
                    btnSubmit.innerText = 'Generate Key';
                }
            });
    }

    function copyGeneratedKey(btnEl) {
        const secretInput = document.getElementById('apikey-secret-result');
        if (!secretInput || !secretInput.value) return;
        navigator.clipboard.writeText(secretInput.value).then(() => {
            if (btnEl) {
                const origHtml = btnEl.innerHTML;
                btnEl.innerHTML = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>`;
                btnEl.classList.add('is-copied');
                setTimeout(() => {
                    btnEl.innerHTML = origHtml;
                    btnEl.classList.remove('is-copied');
                }, 1400);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('API Key copied to clipboard', 'success');
            }
        }).catch(err => {
            alert('Could not copy to clipboard: ' + err);
        });
    }

    function deleteAPIKey(key, name) {
        if (!confirm(`Are you sure you want to revoke "${name}"?\n\nAny external client using this key will immediately lose access.`)) {
            return;
        }

        fetch(`/api/keys/${encodeURIComponent(key)}`, {
            method: 'DELETE'
        })
            .then(res => {
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                return res.json();
            })
            .then(() => {
                if (global.App && typeof global.App.showToast === 'function') {
                    global.App.showToast('API Key revoked successfully', 'info');
                }
                loadAPIKeys();
            })
            .catch(err => {
                alert('Failed to revoke API key: ' + err.message);
            });
    }

    function copyRawAPIKey(key, btnEl) {
        if (!key) return;
        navigator.clipboard.writeText(key).then(() => {
            if (btnEl) {
                const origHtml = btnEl.innerHTML;
                btnEl.innerHTML = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>`;
                btnEl.classList.add('is-copied');
                setTimeout(() => {
                    btnEl.innerHTML = origHtml;
                    btnEl.classList.remove('is-copied');
                }, 1400);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('API Key copied to clipboard', 'success');
            }
        }).catch(err => {
            alert('Could not copy to clipboard: ' + err);
        });
    }

    function openIntegrationGuideModal() {
        const modal = document.getElementById('modal-integration-guide');
        if (modal) {
            const origin = window.location.origin;
            const postEl = document.getElementById('curl-ex-post');
            if (postEl) {
                postEl.textContent = `curl -X POST ${origin}/api/v1/files \\\n  -H "X-API-Key: YOUR_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"url": "https://cdn.example.com/video.mp4"}'`;
            }
            const getEl = document.getElementById('curl-ex-get');
            if (getEl) {
                getEl.textContent = `curl -X GET ${origin}/api/v1/files/{key} \\\n  -H "X-API-Key: YOUR_API_KEY"`;
            }
            const delEl = document.getElementById('curl-ex-del');
            if (delEl) {
                delEl.textContent = `# Single key or comma-separated batch delete:\ncurl -X DELETE "${origin}/api/v1/files/key1,key2" \\\n  -H "X-API-Key: YOUR_API_KEY"`;
            }
            modal.style.display = 'flex';
            modal.classList.add('open');
        }
    }

    function closeIntegrationGuideModal() {
        const modal = document.getElementById('modal-integration-guide');
        if (modal) {
            modal.style.display = 'none';
            modal.classList.remove('open');
        }
    }

    function copyCurlExample(elementId, btnEl) {
        const el = document.getElementById(elementId);
        if (!el) return;
        navigator.clipboard.writeText(el.innerText).then(() => {
            if (btnEl) {
                const origHtml = btnEl.innerHTML;
                btnEl.innerHTML = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>`;
                btnEl.classList.add('is-copied');
                setTimeout(() => {
                    btnEl.innerHTML = origHtml;
                    btnEl.classList.remove('is-copied');
                }, 1400);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('cURL command copied to clipboard', 'success');
            }
        }).catch(() => {});
    }

    function escapeHtml(str) {
        if (!str) return '';
        return String(str)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;');
    }

    function init() {
        loadAPIKeys();
    }

    function destroy() {}

    global.loadAPIKeys = loadAPIKeys;
    global.openCreateAPIKeyModal = openCreateAPIKeyModal;
    global.closeCreateAPIKeyModal = closeCreateAPIKeyModal;
    global.submitCreateAPIKey = submitCreateAPIKey;
    global.copyGeneratedKey = copyGeneratedKey;
    global.deleteAPIKey = deleteAPIKey;
    global.copyRawAPIKey = copyRawAPIKey;
    global.openIntegrationGuideModal = openIntegrationGuideModal;
    global.closeIntegrationGuideModal = closeIntegrationGuideModal;
    global.copyCurlExample = copyCurlExample;

    if (global.App && global.App.pages) {
        global.App.pages.apikeys = { init, destroy };
    }
})(window);
