// Shared application core: formatters, telemetry fetchers, modal/toast helpers, and page lifecycle router.
(function (global) {
    'use strict';

    const pageTitles = {
        dashboard: { title: 'Dashboard', sub: '' },
        nodes: { title: 'Nodes', sub: '' },
        storage: { title: 'Storage & Disks', sub: '' },
        tiers: { title: 'Tiers & Workers', sub: '' },
        flows: { title: 'Policy Canvas', sub: '' },
        'flows-queue': { title: 'Task Queue', sub: '' },
        'flows-hot': { title: 'Hot Files', sub: '' },
        'flows-audit': { title: 'Audit Log', sub: '' },
        media: { title: 'Media & Streams', sub: '' },
        database: { title: 'Database Studio', sub: '' },
        settings: { title: 'Settings', sub: '' },
        apikeys: { title: 'API Keys', sub: '' },
        docs: { title: 'API Docs', sub: '' }
    };

    const pageRoutes = {
        dashboard: '/',
        nodes: '/nodes',
        storage: '/storage',
        tiers: '/tiers',
        flows: '/flows',
        'flows-queue': '/flows-queue',
        'flows-hot': '/flows-hot',
        'flows-audit': '/flows-audit',
        media: '/media',
        database: '/database',
        settings: '/settings',
        apikeys: '/apikeys',
        docs: '/docs'
    };

    const pathToPage = {
        '/': 'dashboard',
        '/dashboard': 'dashboard',
        '/nodes': 'nodes',
        '/storage': 'storage',
        '/tiers': 'tiers',
        '/flows': 'flows',
        '/flows-queue': 'flows-queue',
        '/flows-hot': 'flows-hot',
        '/flows-audit': 'flows-audit',
        '/media': 'media',
        '/database': 'database',
        '/settings': 'settings',
        '/apikeys': 'apikeys',
        '/docs': 'docs'
    };

    function formatBytes(bytes, decimals = 2) {
        if (!bytes || bytes === 0) return '0 B';
        const k = 1024;
        const dm = decimals < 0 ? 0 : decimals;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
    }

    function formatDuration(sec) {
        if (!sec || isNaN(sec)) return '';
        const h = Math.floor(sec / 3600);
        const m = Math.floor((sec % 3600) / 60);
        const s = Math.floor(sec % 60);
        if (h > 0) return `${h}h ${m}m ${s}s`;
        return `${m}m ${s}s`;
    }

    function escapeHtml(str) {
        if (global.UI && typeof global.UI.esc === 'function') {
            return global.UI.esc(str);
        }
        if (!str) return '';
        return String(str)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;');
    }

    function safeSetText(id, text) {
        const el = document.getElementById(id);
        if (el) el.innerText = text;
    }

    function cleanNodeIPs(rawIPs) {
        if (!rawIPs || !Array.isArray(rawIPs)) return [];

        const isPublic = ip => !/^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[0-1])\.|127\.|169\.254\.)/.test(ip);
        const hasPublic = rawIPs.some(isPublic);

        let filtered = rawIPs.filter(ip => {
            if (!ip || typeof ip !== 'string') return false;
            ip = ip.trim();
            if (ip === '127.0.0.1' || ip === 'localhost' || ip === '::1') return false;
            if (hasPublic) {
                if (/^172\.(1[6-9]|2[0-9]|3[0-1])\./.test(ip)) return false;
                if (/^192\.168\.\d+\.1$/.test(ip)) return false;
            }
            return true;
        });

        filtered = Array.from(new Set(filtered));
        filtered.sort((a, b) => {
            const aPub = isPublic(a) ? 0 : 1;
            const bPub = isPublic(b) ? 0 : 1;
            return aPub - bPub;
        });

        return filtered.length > 0 ? filtered : (rawIPs.slice(0, 1));
    }

    function sortNodesDeterministic(nodes) {
        if (!Array.isArray(nodes)) return [];
        return nodes.slice().sort((a, b) => {
            const idA = (a && a.metrics && a.metrics.node_id) ? a.metrics.node_id : '';
            const idB = (b && b.metrics && b.metrics.node_id) ? b.metrics.node_id : '';
            return idA.localeCompare(idB, undefined, { numeric: true, sensitivity: 'base' });
        });
    }

    function setModalOpen(open) {
        if (open) {
            document.body.classList.add('modal-open');
        } else {
            const anyOpen = document.querySelector('.modal-overlay.open, .modal-overlay.active, .tier-modal-backdrop.open');
            if (!anyOpen) {
                document.body.classList.remove('modal-open');
            }
        }
    }

    function showToast(message, type) {
        let container = document.getElementById('app-toast-container');
        if (!container) {
            container = document.createElement('div');
            container.id = 'app-toast-container';
            container.style.cssText = 'position:fixed;bottom:20px;right:20px;z-index:9999;display:flex;flex-direction:column;gap:8px;pointer-events:none;';
            document.body.appendChild(container);
        }
        const toast = document.createElement('div');
        const isErr = type === 'error';
        toast.style.cssText = `padding:10px 14px;border-radius:8px;font-size:12.5px;font-weight:600;color:#fff;background:${isErr ? '#dc2626' : '#18181b'};box-shadow:0 8px 20px rgba(0,0,0,0.18);pointer-events:auto;transition:opacity 0.25s ease;`;
        toast.textContent = message;
        container.appendChild(toast);
        setTimeout(() => {
            toast.style.opacity = '0';
            setTimeout(() => toast.remove(), 260);
        }, 2600);
    }

    function fallbackCopyText(text, alertMsg) {
        try {
            const textArea = document.createElement('textarea');
            textArea.value = text;
            textArea.style.position = 'fixed';
            textArea.style.left = '-999999px';
            textArea.style.top = '-999999px';
            textArea.setAttribute('readonly', '');
            document.body.appendChild(textArea);
            textArea.focus();
            textArea.select();
            const successful = document.execCommand('copy');
            document.body.removeChild(textArea);
            if (successful) {
                alert(alertMsg || 'Copied to clipboard!');
                return;
            }
        } catch (_) {}
        prompt('Copy link:', text);
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

    function toggleMobileSidebar(forceState) {
        const sidebar = document.querySelector('.app-sidebar') || document.querySelector('.sidebar');
        const backdrop = document.getElementById('sidebar-backdrop');
        if (!sidebar) return;

        const isOpen = typeof forceState === 'boolean' ? forceState : (!sidebar.classList.contains('open') && !sidebar.classList.contains('mobile-open'));
        if (isOpen) {
            sidebar.classList.add('open', 'mobile-open');
            if (backdrop) {
                backdrop.style.display = 'block';
                setTimeout(() => backdrop.classList.add('visible'), 10);
            }
            document.body.classList.add('modal-open');
        } else {
            sidebar.classList.remove('open', 'mobile-open');
            if (backdrop) {
                backdrop.classList.remove('visible');
                setTimeout(() => {
                    if (!sidebar.classList.contains('open') && !sidebar.classList.contains('mobile-open')) {
                        backdrop.style.display = 'none';
                    }
                }, 250);
            }
            setModalOpen(false);
        }
    }

    async function fetchClusterTelemetry() {
        if (document.body && document.body.classList.contains('auth-required')) {
            return;
        }
        try {
            const res = await fetch('/api/pool');

            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            if (data && Array.isArray(data.nodes)) {
                data.nodes = sortNodesDeterministic(data.nodes);
            }
            global.cachedPoolData = data;
            App.cachedPoolData = data;
            renderAllViews(data);
        } catch (err) {
            console.error('Failed to fetch cluster telemetry:', err);
            const hdrBadge = document.getElementById('hdr-cluster-status');
            const hdrText = document.getElementById('hdr-cluster-text');
            if (hdrBadge) hdrBadge.className = 'cluster-badge status-offline';
            if (hdrText) hdrText.innerText = 'Coordinator Unreachable';
        }
    }

    function renderAllViews(data) {
        if (!data) return;
        const hdrBadge = document.getElementById('hdr-cluster-status');
        const hdrText = document.getElementById('hdr-cluster-text');
        if (hdrBadge && hdrText) {
            if (data.online_nodes > 0) {
                hdrBadge.className = 'cluster-badge status-online';
                hdrText.innerText = `${data.online_nodes} / ${data.total_nodes} Nodes Online`;
            } else {
                hdrBadge.className = 'cluster-badge status-offline';
                hdrText.innerText = '0 Nodes Connected';
            }
        }
        if (typeof global.renderDashboard === 'function') global.renderDashboard(data);
        if (typeof global.renderNodes === 'function') global.renderNodes(data);
        if (typeof global.renderStorage === 'function') global.renderStorage(data);
        if (typeof global.renderSettings === 'function') global.renderSettings(data);
    }

    let activePageId = null;

    function navigateTo(pageId, pushHistory = true) {
        if (!pageId) pageId = 'dashboard';
        if (pageId === 'settings-keys') pageId = 'apikeys';
        let targetSubTab = null;
        if (pageId === 'settings-users') {
            pageId = 'settings';
            targetSubTab = 'users';
        }

        if (activePageId && activePageId !== pageId) {
            const prevMod = App.pages[activePageId];
            if (prevMod && typeof prevMod.destroy === 'function') {
                prevMod.destroy();
            }
        }

        global.currentView = pageId;
        App.currentView = pageId;
        activePageId = pageId;

        document.querySelectorAll('.page-view').forEach(el => el.classList.remove('active'));
        const target = document.getElementById(`view-${pageId}`);
        if (target) target.classList.add('active');

        const isFlowPage = pageId === 'flows' || pageId.startsWith('flows-');
        const isSettingsPage = pageId === 'settings' || pageId === 'apikeys';

        document.querySelectorAll('.nav-link').forEach(el => {
            const dp = el.getAttribute('data-page');
            if (dp === pageId || (isFlowPage && dp === 'flows') || (isSettingsPage && dp === 'settings')) {
                el.classList.add('active');
            } else {
                el.classList.remove('active');
            }
        });

        document.querySelectorAll('.sidebar-sublink').forEach(el => {
            const dp = el.getAttribute('data-page');
            if (dp === pageId) {
                el.classList.add('active');
            } else {
                el.classList.remove('active');
            }
        });

        document.querySelectorAll('.mob-nav-item[data-page]').forEach(el => {
            const dp = el.getAttribute('data-page');
            if (dp === pageId || (isFlowPage && dp === 'flows') || (isSettingsPage && dp === 'settings')) {
                el.classList.add('active');
            } else {
                el.classList.remove('active');
            }
        });

        const flowsGroup = document.getElementById('sidebar-group-flows');
        if (flowsGroup) {
            flowsGroup.classList.toggle('is-expanded', isFlowPage);
        }

        const settingsGroup = document.getElementById('sidebar-group-settings');
        if (settingsGroup) {
            settingsGroup.classList.toggle('is-expanded', isSettingsPage);
        }

        const info = pageTitles[pageId] || pageTitles.dashboard;
        const titleEl = document.getElementById('header-page-title');
        const subEl = document.getElementById('header-page-subtitle');
        if (titleEl) titleEl.innerText = info.title;
        if (subEl) subEl.innerText = info.sub;

        toggleMobileSidebar(false);

        if (pushHistory && pageRoutes[pageId]) {
            window.history.pushState({ page: pageId }, info.title, pageRoutes[pageId]);
        }

        const nextMod = App.pages[pageId];
        if (nextMod && typeof nextMod.init === 'function') {
            nextMod.init();
        }
        if (targetSubTab && typeof global.switchSettingsTab === 'function') {
            global.switchSettingsTab(targetSubTab);
        } else {
            if (pageId === 'tiers' && typeof global.fetchTiersView === 'function') {
                global.fetchTiersView(true);
            }
            if (pageId === 'database' && global.DBStudio && typeof global.DBStudio.refresh === 'function') {
                global.DBStudio.refresh();
            }
            if (global.cachedPoolData) {
                renderAllViews(global.cachedPoolData);
            }
        }
    }

    function detectInitialPage() {
        if (window.location.pathname === '/settings' && window.location.hash === '#apikeys') {
            return 'apikeys';
        }
        const pathPage = pathToPage[window.location.pathname];
        if (pathPage) return pathPage;
        const activeDom = document.querySelector('.page-view.active');
        if (activeDom && activeDom.id && activeDom.id.startsWith('view-')) {
            const domPage = activeDom.id.slice(5);
            return domPage;
        }
        return global.currentView || 'dashboard';
    }

    const App = {
        pages: {},
        currentView: global.currentView || 'dashboard',
        cachedPoolData: null,
        formatBytes: formatBytes,
        formatDuration: formatDuration,
        escapeHtml: escapeHtml,
        safeSetText: safeSetText,
        cleanNodeIPs: cleanNodeIPs,
        sortNodesDeterministic: sortNodesDeterministic,
        setModalOpen: setModalOpen,
        showToast: showToast,
        copyToClipboard: copyToClipboard,
        fallbackCopyText: fallbackCopyText,
        toggleMobileSidebar: toggleMobileSidebar,
        fetchClusterTelemetry: fetchClusterTelemetry,
        fetchPoolMetrics: fetchClusterTelemetry,
        renderAllViews: renderAllViews,
        navigateTo: navigateTo
    };

    // Expose globals for inline template onclick handlers and existing modules.
    global.App = App;
    global.cachedPoolData = null;
    global.formatBytes = formatBytes;
    global.formatDuration = formatDuration;
    global.escapeHtml = escapeHtml;
    global.safeSetText = safeSetText;
    global.cleanNodeIPs = cleanNodeIPs;
    global.sortNodesDeterministic = sortNodesDeterministic;
    global.setModalOpen = setModalOpen;
    global.showToast = showToast;
    global.copyToClipboard = copyToClipboard;
    global.fallbackCopyText = fallbackCopyText;
    global.toggleMobileSidebar = toggleMobileSidebar;
    global.fetchClusterTelemetry = fetchClusterTelemetry;
    global.fetchPoolMetrics = fetchClusterTelemetry;
    global.renderAllViews = renderAllViews;
    global.navigateTo = navigateTo;

    document.addEventListener('DOMContentLoaded', () => {
        document.querySelectorAll('.nav-link').forEach(link => {
            link.addEventListener('click', (e) => {
                const page = link.getAttribute('data-page');
                if (page) {
                    e.preventDefault();
                    navigateTo(page, true);
                }
            });
        });

        document.querySelectorAll('.sidebar-sublink').forEach(sublink => {
            sublink.addEventListener('click', (e) => {
                e.preventDefault();
                const page = sublink.getAttribute('data-page');
                if (page) {
                    navigateTo(page, true);
                }
            });
        });

        window.addEventListener('popstate', (e) => {
            const page = (e.state && e.state.page) || pathToPage[window.location.pathname] || 'dashboard';
            navigateTo(page, false);
        });

        const initialPage = detectInitialPage();
        navigateTo(initialPage, false);

        // Check authentication state & setup requirement
        checkAuthStatus();
    });

    let authNeedsSetup = false;

    async function checkAuthStatus() {
        try {
            const res = await fetch('/api/auth/status');
            if (!res.ok) return;
            const data = await res.json();
            authNeedsSetup = !!data.needs_setup;

            const userWrap = document.getElementById('topbar-user-wrap');
            const usernameEl = document.getElementById('topbar-username');

            if (data.authenticated && data.username) {
                document.body.classList.remove('auth-required');
                if (userWrap) userWrap.style.display = 'inline-flex';
                if (usernameEl) usernameEl.innerText = data.username;
                hideAuthModal();
                fetchClusterTelemetry();

            } else if (data.needs_setup) {
                document.body.classList.add('auth-required');
                showAuthModal(true);
            } else {
                document.body.classList.add('auth-required');
                showAuthModal(false);
            }
        } catch (e) {
            console.error('Failed to check auth status:', e);
        }
    }

    function showAuthModal(isSetup) {
        const modal = document.getElementById('modal-auth-overlay');
        if (!modal) return;
        const title = document.getElementById('auth-modal-title');
        const sub = document.getElementById('auth-modal-subtitle');
        const confirmGroup = document.getElementById('auth-confirm-group');
        const submitText = document.getElementById('auth-submit-text');
        const errorAlert = document.getElementById('auth-error-alert');
        if (errorAlert) errorAlert.style.display = 'none';

        if (isSetup) {
            if (title) title.innerText = 'Create Administrator Account';
            if (sub) sub.innerText = 'Set up administrator credentials to continue.';
            if (confirmGroup) confirmGroup.style.display = 'flex';
            if (submitText) submitText.innerText = 'Create Account & Sign In';
        } else {
            if (title) title.innerText = 'Sign In';
            if (sub) sub.innerText = 'Enter your credentials to continue.';
            if (confirmGroup) confirmGroup.style.display = 'none';
            if (submitText) submitText.innerText = 'Sign In';
        }
        modal.classList.add('open', 'active');
        setModalOpen(true);
    }

    function hideAuthModal() {
        const modal = document.getElementById('modal-auth-overlay');
        if (modal) modal.classList.remove('open', 'active');
        setModalOpen(false);
    }

    async function handleAuthSubmit(event) {
        event.preventDefault();
        const usernameInput = document.getElementById('auth-username');
        const passwordInput = document.getElementById('auth-password');
        const confirmInput = document.getElementById('auth-confirm-password');
        const errorAlert = document.getElementById('auth-error-alert');
        const errorText = document.getElementById('auth-error-text');
        const submitBtn = document.getElementById('btn-auth-submit');

        if (errorAlert) errorAlert.style.display = 'none';

        const username = usernameInput ? usernameInput.value.trim() : '';
        const password = passwordInput ? passwordInput.value : '';

        if (!username || !password) return;

        if (authNeedsSetup) {
            const confirmVal = confirmInput ? confirmInput.value : '';
            if (password !== confirmVal) {
                if (errorAlert && errorText) {
                    errorText.innerText = 'Passwords do not match.';
                    errorAlert.style.display = 'block';
                }
                return;
            }
        }

        if (submitBtn) submitBtn.disabled = true;

        try {
            const endpoint = authNeedsSetup ? '/api/auth/setup' : '/api/auth/login';
            const res = await fetch(endpoint, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password })
            });
            const data = await res.json();
            if (!res.ok) {
                throw new Error(data.error || 'Authentication failed');
            }

            document.body.classList.remove('auth-required');
            hideAuthModal();
            showToast({ title: 'Welcome back!', message: `Signed in as ${data.username}`, type: 'success' });
            if (!document.querySelector('.page-view')) {
                window.location.reload();
                return;
            }
            checkAuthStatus();
        } catch (err) {
            if (errorAlert && errorText) {
                errorText.innerText = err.message || 'Authentication error';
                errorAlert.style.display = 'block';
            }
        } finally {
            if (submitBtn) submitBtn.disabled = false;
        }
    }

    async function performLogout() {
        if (!confirm('Are you sure you want to log out?')) return;
        try {
            await fetch('/api/auth/logout', { method: 'POST' });
            document.body.classList.add('auth-required');
            showToast({ title: 'Logged out', message: 'You have been signed out.', type: 'info' });
            checkAuthStatus();
        } catch (err) {
            window.location.reload();
        }
    }

    global.checkAuthStatus = checkAuthStatus;
    global.handleAuthSubmit = handleAuthSubmit;
    global.performLogout = performLogout;

    // Global backdrop click dismissal for all modals.
    document.addEventListener('click', (e) => {
        if (e.target && e.target.classList && e.target.classList.contains('modal-overlay')) {
            const id = e.target.id;
            if (id === 'modal-auth-overlay') return; // Auth modal is mandatory and cannot be dismissed by clicking outside
            if (id === 'db-row-modal' && global.DBStudio) global.DBStudio.closeRowModal();
            else if (id === 'db-create-modal' && global.DBStudio) global.DBStudio.closeCreateModal();
            else if (id === 'db-confirm-modal' && global.DBStudio) global.DBStudio.closeConfirmModal();
            else if (id === 'node-detail-modal' && typeof global.closeNodeDetailModal === 'function') global.closeNodeDetailModal();
            else if (id === 'modal-add-vps' && typeof global.closeAddVPSModal === 'function') global.closeAddVPSModal();
            else if (id === 'modal-upload-file' && typeof global.closeUploadModal === 'function') global.closeUploadModal();
            else if (id === 'modal-ingest-url' && typeof global.closeIngestURLModal === 'function') global.closeIngestURLModal();
            else if (id === 'modal-stream-details' && typeof global.closeStreamDetailsModal === 'function') global.closeStreamDetailsModal();
            else if (id === 'storage-clean-modal' && typeof global.closeStorageCleanModal === 'function') global.closeStorageCleanModal();
            else {
                e.target.classList.remove('open', 'active');
                setModalOpen(false);
            }
        }
    });

    // Global Escape key dismissal for all modals.
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            const openModalEl = document.querySelector('.modal-overlay.open, .modal-overlay.active, .tier-modal-backdrop.open');
            if (openModalEl) {
                const id = openModalEl.id;
                if (id === 'modal-auth-overlay') return; // Auth modal is mandatory and cannot be dismissed with Escape
                if (id === 'db-row-modal' && global.DBStudio) global.DBStudio.closeRowModal();
                else if (id === 'db-create-modal' && global.DBStudio) global.DBStudio.closeCreateModal();
                else if (id === 'db-confirm-modal' && global.DBStudio) global.DBStudio.closeConfirmModal();
                else if (id === 'node-detail-modal' && typeof global.closeNodeDetailModal === 'function') global.closeNodeDetailModal();
                else if (id === 'modal-add-vps' && typeof global.closeAddVPSModal === 'function') global.closeAddVPSModal();
                else if (id === 'modal-upload-file' && typeof global.closeUploadModal === 'function') global.closeUploadModal();
                else if (id === 'modal-ingest-url' && typeof global.closeIngestURLModal === 'function') global.closeIngestURLModal();
                else if (id === 'modal-stream-details' && typeof global.closeStreamDetailsModal === 'function') global.closeStreamDetailsModal();
                else if (id === 'storage-clean-modal' && typeof global.closeStorageCleanModal === 'function') global.closeStorageCleanModal();
                else if (id === 'tier-action-modal' && typeof global.closeTierModal === 'function') global.closeTierModal();
                else {
                    openModalEl.classList.remove('open', 'active');
                    setModalOpen(false);
                }
            }
        }
    });

    // Theme Switcher (better-ui: suppresses transitions to prevent color smearing, forces style flush)
    function toggleTheme() {
        const css = document.createElement('style');
        css.type = 'text/css';
        css.appendChild(
            document.createTextNode(
                `*, *::before, *::after {
                    -webkit-transition: none !important;
                    -moz-transition: none !important;
                    -o-transition: none !important;
                    -ms-transition: none !important;
                    transition: none !important;
                }`
            )
        );
        document.head.appendChild(css);

        const current = document.documentElement.getAttribute('data-theme') || 'light';
        const next = current === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        localStorage.setItem('stream_theme', next);

        const metaTheme = document.querySelector('meta[name="theme-color"]');
        if (metaTheme) metaTheme.setAttribute('content', next === 'dark' ? '#090a0d' : '#fbfaf8');

        // Notify embedded Swagger UI iframe
        const swaggerIframe = document.getElementById('swagger-frame');
        if (swaggerIframe && swaggerIframe.contentWindow) {
            swaggerIframe.contentWindow.postMessage({ type: 'theme-change', theme: next }, '*');
        }

        // Force style flush
        window.getComputedStyle(css).opacity;

        requestAnimationFrame(() => {
            requestAnimationFrame(() => {
                if (css.parentNode) css.parentNode.removeChild(css);
            });
        });
    }

    global.toggleTheme = toggleTheme;
})(window);
