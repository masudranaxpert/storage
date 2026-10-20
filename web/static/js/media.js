// Media page module: asset table, adaptive polling, URL ingest, file upload, storage sync, and ArtPlayer cinema modal.
(function (global) {
    'use strict';

    let mediaList = [];
    let mediaCurrentPage = 1;
    let mediaPageSize = 10;
    let mediaSearchQuery = '';
    let mediaFilterStatus = 'all';
    let mediaFilterNode = '';
    let mediaFilterTier = '';
    let selectedMediaKeys = new Set();
    let mediaPollInterval = null;
    let selectedUploadFile = null;
    let lastMediaDataHash = '';
    let isMediaFetching = false;
    let hasFetchedMediaOnce = false;
    let activeLifecycleTasks = {}; // key -> task object
    let mediaHotMap = {}; // key -> hot file state { score, pinned_tier, pin_until, last_hit_at }

    // Instant Hydration: restore cached media files from sessionStorage
    try {
        const rawMedia = sessionStorage.getItem('stream_cached_media');
        if (rawMedia) {
            const parsed = JSON.parse(rawMedia);
            if (parsed && Array.isArray(parsed.files) && parsed.files.length > 0) {
                mediaList = parsed.files;
                hasFetchedMediaOnce = true;
            }
        }
    } catch (_) {}

    let currentArtPlayer = null;
    let lockstepController = null;
    let cinemaRetryCtx = null;
    let cinemaLoadTimer = null;

    // Helper: format past ISO timestamps as relative time (e.g. "1m ago", "2h ago", "3d ago")
    function formatRelativeTime(iso) {
        if (!iso || typeof iso !== 'string' || iso.startsWith('0001-01-01')) return '—';
        const t = new Date(iso).getTime();
        if (!t || isNaN(t)) return '—';
        const diffSec = Math.max(0, Math.floor((Date.now() - t) / 1000));
        if (diffSec < 60) return 'just now';
        const mins = Math.floor(diffSec / 60);
        if (mins < 60) return `${mins}m ago`;
        const hrs = Math.floor(mins / 60);
        if (hrs < 24) return `${hrs}h ago`;
        const days = Math.floor(hrs / 24);
        if (days < 30) return `${days}d ago`;
        const months = Math.floor(days / 30);
        if (months < 12) return `${months}mo ago`;
        return `${Math.floor(days / 365)}y ago`;
    }

    // Helper: format future ISO timestamps (e.g. "in 23h", "in 1d") or "expired"
    function formatRelativeFuture(iso) {
        if (!iso || typeof iso !== 'string' || iso.startsWith('0001-01-01')) return 'Permanent';
        const t = new Date(iso).getTime();
        if (!t || isNaN(t)) return 'Permanent';
        const diffSec = Math.floor((t - Date.now()) / 1000);
        if (diffSec <= 0) return 'expired';
        if (diffSec < 60) return `in ${diffSec}s`;
        const mins = Math.floor(diffSec / 60);
        if (mins < 60) return `in ${mins}m`;
        const hrs = Math.floor(mins / 60);
        if (hrs < 24) return `in ${hrs}h`;
        const days = Math.floor(hrs / 24);
        return `in ${days}d`;
    }

    // ISO 639-1 / 639-2 common audio & subtitle language name mapping
    const ISO_LANG_MAP = {
        hin: 'Hindi', hi: 'Hindi',
        ben: 'Bengali', bn: 'Bengali',
        ger: 'German', deu: 'German', de: 'German',
        eng: 'English', en: 'English',
        spa: 'Spanish', es: 'Spanish',
        fra: 'French', fre: 'French', fr: 'French',
        ita: 'Italian', it: 'Italian',
        jpn: 'Japanese', ja: 'Japanese',
        kor: 'Korean', ko: 'Korean',
        chi: 'Chinese', zho: 'Chinese', zh: 'Chinese',
        rus: 'Russian', ru: 'Russian',
        ara: 'Arabic', ar: 'Arabic',
        tam: 'Tamil', ta: 'Tamil',
        tel: 'Telugu', te: 'Telugu',
        mal: 'Malayalam', ml: 'Malayalam',
        kan: 'Kannada', kn: 'Kannada',
        mar: 'Marathi', mr: 'Marathi',
        pan: 'Punjabi', pa: 'Punjabi',
        guj: 'Gujarati', gu: 'Gujarati',
        urd: 'Urdu', ur: 'Urdu',
        por: 'Portuguese', pt: 'Portuguese',
        tur: 'Turkish', tr: 'Turkish',
        pol: 'Polish', pl: 'Polish',
        nld: 'Dutch', nl: 'Dutch',
        swe: 'Swedish', sv: 'Swedish',
        tha: 'Thai', th: 'Thai',
        vie: 'Vietnamese', vi: 'Vietnamese',
        ind: 'Indonesian', id: 'Indonesian'
    };

    function resolveLanguageName(code) {
        if (!code || typeof code !== 'string') return '';
        const lower = code.toLowerCase().trim();
        return ISO_LANG_MAP[lower] || (code.length <= 4 ? code.toUpperCase() : code);
    }

    // Helper: format user-friendly track titles instead of spam/promo tags (e.g. "Visit & Download - Fmxbd.com")
    function formatTrackTitle(track, fallbackDefault) {
        if (!track) return fallbackDefault;
        const langCode = (track.language || '').trim();
        const langName = resolveLanguageName(langCode);
        const rawTitle = (track.title || '').trim();

        // Check if raw title is promotional spam or generic noise
        const isSpammy = !rawTitle || /visit|download|fmxbd|fmovies|1337x|yts|psa|galaxytv|sub\s*\d+/i.test(rawTitle);

        if (!isSpammy) {
            return rawTitle;
        }
        if (langName) {
            return langName;
        }
        return fallbackDefault;
    }

    // Helper: determine the base directory URL for sub-tracks (audio, subtitles, HLS)
    // preserving signed tickets (t_...) and host/ports to prevent 403 Forbidden errors.
    function resolveBaseDirUrl(activeUrl, job) {
        const candidate = activeUrl || job?.stream_url;
        if (candidate && typeof candidate === 'string') {
            try {
                let clean = candidate.split('?')[0].replace(/\/+$/, '');
                if (/\.[a-z0-9]{2,5}$/i.test(clean)) {
                    clean = clean.substring(0, clean.lastIndexOf('/'));
                }
                return clean;
            } catch (_) {}
        }
        const key = job?.key || '';
        return `${window.location.origin}/stream/${encodeURIComponent(key)}`;
    }

    // Helper: clean text with ISO timestamps (e.g., "pinned to tier0 until 2026-10-12T13:08:03Z") into relative time
    function cleanReasonText(str) {
        if (!str || typeof str !== 'string') return '';
        // Match ISO 8601 timestamps inside text
        return str.replace(/until\s+(\d{4}-\d{2}-\d{2}T[^\s]+)/gi, (match, iso) => {
            const rel = formatRelativeFuture(iso);
            return `until ${rel} (${new Date(iso).toLocaleDateString()})`;
        });
    }

    async function fetchMediaList(force = false) {
        if (isMediaFetching) return;
        isMediaFetching = true;
        try {
            const [res, taskRes, hotRes] = await Promise.all([
                fetch('/api/v1/files'),
                fetch('/api/lifecycle/tasks?state=active&limit=100').catch(() => null),
                fetch('/api/lifecycle/hot?limit=250').catch(() => null)
            ]);
            if (!res.ok) return;
            const data = await res.json();
            const files = data.files || [];

            activeLifecycleTasks = {};
            if (taskRes && taskRes.ok) {
                try {
                    const taskData = await taskRes.json();
                    (taskData.tasks || []).forEach(t => {
                        if (t && t.key) activeLifecycleTasks[t.key] = t;
                    });
                } catch (_) {}
            }

            mediaHotMap = {};
            if (hotRes && hotRes.ok) {
                try {
                    const hotData = await hotRes.json();
                    (hotData.hot_files || []).forEach(hf => {
                        if (hf && hf.key) mediaHotMap[hf.key] = hf;
                    });
                } catch (_) {}
            }

            const currentHash = JSON.stringify(files.map(f => ({
                k: f.key,
                s: f.state,
                p: f.progress_percent,
                sp: f.speed,
                err: f.error,
                tb: f.transferred_bytes,
                hs: mediaHotMap[f.key] ? mediaHotMap[f.key].score : 0,
                lct: activeLifecycleTasks[f.key] ? activeLifecycleTasks[f.key].state + activeLifecycleTasks[f.key].progress : ''
            })));

            const dataChanged = (currentHash !== lastMediaDataHash);
            lastMediaDataHash = currentHash;

            mediaList = files.sort((a, b) => {
                const timeA = new Date(a.created_at).getTime() || 0;
                const timeB = new Date(b.created_at).getTime() || 0;
                if (timeA !== timeB) return timeB - timeA;
                return (b.key || '').localeCompare(a.key || '');
            });

            hasFetchedMediaOnce = true;
            try {
                sessionStorage.setItem('stream_cached_media', JSON.stringify({
                    files: files,
                    timestamp: Date.now()
                }));
            } catch (_) {}

            updateNodeFilterDropdown();

            const sel = window.getSelection();
            const hasSelection = sel && !sel.isCollapsed && document.getElementById('media-table-body')?.contains(sel.anchorNode);

            if (force || (dataChanged && !hasSelection)) {
                renderMediaTable();
            }
        } catch (err) {
            console.error('Failed to load media files:', err);
        } finally {
            isMediaFetching = false;
        }
    }

    function scheduleNextMediaPoll() {
        if (mediaPollInterval) {
            clearTimeout(mediaPollInterval);
            mediaPollInterval = null;
        }
        if (global.currentView !== 'media') return;

        const hasActiveJobs = mediaList.some(m =>
            ['downloading', 'processing', 'transferring', 'detected', 'awaiting_upload'].includes(m.state)
        );
        const delay = hasActiveJobs ? 2500 : 6000;

        mediaPollInterval = setTimeout(async () => {
            if (global.currentView === 'media') {
                await fetchMediaList();
                scheduleNextMediaPoll();
            }
        }, delay);
    }

    function setMediaFilter(status) {
        mediaFilterStatus = status;
        mediaCurrentPage = 1;
        document.querySelectorAll('.filter-btn[data-media-filter]').forEach(btn => {
            if (btn.getAttribute('data-media-filter') === status) {
                btn.classList.add('active');
            } else {
                btn.classList.remove('active');
            }
        });
        renderMediaTable();
    }

    function onMediaSearchChange() {
        const input = document.getElementById('media-search-input');
        mediaSearchQuery = (input ? input.value : '').toLowerCase().trim();
        mediaCurrentPage = 1;
        renderMediaTable();
    }

    function onMediaPageSizeChange() {
        const select = document.getElementById('media-page-size');
        if (select) {
            mediaPageSize = parseInt(select.value, 10) || 10;
            mediaCurrentPage = 1;
            renderMediaTable();
        }
    }

    function renderPipelineStepper(item) {
        const isUpload = item.source_type === 'upload';
        const hasTransfer = item.worker_node_id && item.placement?.node_id && item.worker_node_id !== item.placement?.node_id;

        let s1 = 'pending', s2 = 'pending', s3 = 'pending', s4 = 'pending';

        if (item.state === 'completed') {
            s1 = s2 = s3 = s4 = 'done';
        } else if (item.state === 'awaiting_upload' || item.state === 'uploading' || item.state === 'detected' || item.state === 'downloading') {
            s1 = 'active';
        } else if (item.state === 'processing') {
            s1 = 'done';
            s2 = 'active';
        } else if (item.state === 'transferring') {
            s1 = 'done';
            s2 = 'done';
            s3 = 'active';
        }

        const stepIcon = (st, label) => {
            let bg = 'rgba(255, 255, 255, 0.05)';
            let color = 'var(--text-muted)';
            let border = 'var(--border-light)';
            if (st === 'done') {
                bg = 'rgba(16, 185, 129, 0.12)';
                color = '#10b981';
                border = 'rgba(16, 185, 129, 0.3)';
            } else if (st === 'active') {
                bg = 'rgba(255, 122, 24, 0.15)';
                color = 'var(--orange-primary)';
                border = 'var(--orange-primary)';
            }
            return `
                <div style="display:inline-flex;align-items:center;gap:3px;padding:2px 7px;border-radius:6px;font-size:9.5px;font-weight:700;background:${bg};color:${color};border:1px solid ${border};white-space:nowrap;" title="${label}">
                    ${st === 'done' ? '<span>✓</span>' : ''}${label}
                </div>
            `;
        };
        const stepArrow = '<span style="font-size:9px;color:var(--text-muted);">&rarr;</span>';
        const stepPair = (arrow, chip) => `<span style="display:inline-flex;align-items:center;gap:3px;white-space:nowrap;">${arrow}${chip}</span>`;

        return `
            <div style="display:flex;align-items:center;gap:3px;margin-bottom:6px;flex-wrap:wrap;">
                ${stepIcon(s1, isUpload ? 'Upload' : 'Download')}
                ${stepPair(stepArrow, stepIcon(s2, 'Package'))}
                ${hasTransfer ? stepPair(stepArrow, stepIcon(s3, 'Sync')) : ''}
                ${stepPair(stepArrow, stepIcon(s4, 'Ready'))}
            </div>
        `;
    }

    function renderMediaSpecs(meta) {
        if (!meta || (!meta.video && (!meta.audio_tracks || meta.audio_tracks.length === 0))) {
            return `<span class="badge-video">MP4 Stream</span>`;
        }
        const v = meta.video || {};
        const resLabel = v.height ? `${v.height}p` : (v.width ? `${v.width}x${v.height}` : '');
        const fpsLabel = v.fps ? `${Math.round(v.fps)}fps` : '';
        const durLabel = meta.duration_sec ? global.formatDuration(meta.duration_sec) : '';

        let audioPills = '';
        if (meta.audio_tracks && meta.audio_tracks.length > 0) {
            audioPills = meta.audio_tracks.map((t, idx) => {
                const label = (t.language || '').trim().toUpperCase() || `A${idx + 1}`;
                const fullTitle = formatTrackTitle(t, `Audio Track ${idx + 1}`);
                return `<span class="badge-audio" title="${global.escapeHtml(fullTitle)} (${global.escapeHtml(t.codec || 'aac')})">${global.escapeHtml(label)}</span>`;
            }).join(' ');
        }

        let subPills = '';
        if (meta.subtitles && meta.subtitles.length > 0) {
            // Deduplicate sub labels or append track index if repeated
            const seenSubLangs = {};
            subPills = meta.subtitles.map((s, idx) => {
                const rawLang = (s.language || '').trim().toUpperCase() || 'SUB';
                seenSubLangs[rawLang] = (seenSubLangs[rawLang] || 0) + 1;
                const displayLang = seenSubLangs[rawLang] > 1 ? `${rawLang} ${seenSubLangs[rawLang]}` : rawLang;
                const fullTitle = formatTrackTitle(s, `Subtitle ${idx + 1}`);
                return `<span class="badge-sub" title="${global.escapeHtml(fullTitle)} (WebVTT)">${global.escapeHtml(displayLang)}</span>`;
            }).join(' ');
        }

        return `
            <div style="display:flex; flex-direction:column; gap:4px;">
                <div style="display:flex; align-items:center; gap:4px; flex-wrap:wrap;">
                    ${resLabel ? `<span class="badge-spec">${global.escapeHtml(resLabel)}</span>` : ''}
                    ${fpsLabel ? `<span class="badge-spec">${global.escapeHtml(fpsLabel)}</span>` : ''}
                    ${durLabel ? `<span style="font-size:11px; color:var(--text-secondary); font-weight:700; font-family:'JetBrains Mono',monospace; margin-left:2px;">${global.escapeHtml(durLabel)}</span>` : ''}
                </div>
                ${(audioPills || subPills) ? `
                    <div style="display:flex; align-items:center; gap:4px; flex-wrap:wrap; margin-top:2px;">
                        ${audioPills}
                        ${subPills}
                    </div>
                ` : ''}
            </div>
        `;
    }

    function renderPaginationButtons(totalPages) {
        const container = document.getElementById('media-pagination-buttons');
        if (!container) return;

        if (global.UI && typeof global.UI.pagination === 'function') {
            global.UI.pagination(container, {
                currentPage: mediaCurrentPage,
                totalPages: totalPages,
                onPageChange: (newPage) => {
                    goToMediaPage(newPage);
                }
            });
            return;
        }

        if (totalPages <= 1) {
            container.innerHTML = '';
            return;
        }

        let html = '';
        html += `<button class="page-btn" ${mediaCurrentPage === 1 ? 'disabled' : ''} onclick="goToMediaPage(${mediaCurrentPage - 1})">&laquo; Prev</button>`;

        for (let i = 1; i <= totalPages; i++) {
            if (i === 1 || i === totalPages || (i >= mediaCurrentPage - 1 && i <= mediaCurrentPage + 1)) {
                html += `<button class="page-btn ${i === mediaCurrentPage ? 'active' : ''}" onclick="goToMediaPage(${i})">${i}</button>`;
            } else if (i === mediaCurrentPage - 2 || i === mediaCurrentPage + 2) {
                html += `<span style="padding:0 4px;color:var(--text-muted);display:inline-flex;align-items:center;">...</span>`;
            }
        }

        html += `<button class="page-btn" ${mediaCurrentPage === totalPages ? 'disabled' : ''} onclick="goToMediaPage(${mediaCurrentPage + 1})">Next &raquo;</button>`;
        container.innerHTML = html;
    }

    function renderSkeletonMediaRows() {
        return `
            <tr class="skeleton-media-row">
                <td style="text-align:center;"><div class="skeleton-shimmer" style="width:16px;height:16px;margin:0 auto;border-radius:4px;"></div></td>
                <td>
                    <div style="display:flex;align-items:center;gap:12px;">
                        <div class="skeleton-shimmer" style="width:42px;height:42px;border-radius:8px;flex-shrink:0;"></div>
                        <div style="flex:1;display:flex;flex-direction:column;gap:6px;">
                            <div class="skeleton-shimmer skeleton-text" style="width:75%;height:14px;"></div>
                            <div class="skeleton-shimmer skeleton-text" style="width:40%;height:11px;"></div>
                        </div>
                    </div>
                </td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:80px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:90px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:110px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:55px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:45px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:80px;height:16px;border-radius:10px;"></div></td>
                <td style="text-align:right;"><div class="skeleton-shimmer" style="width:28px;height:28px;border-radius:6px;display:inline-block;"></div></td>
            </tr>
            <tr class="skeleton-media-row">
                <td style="text-align:center;"><div class="skeleton-shimmer" style="width:16px;height:16px;margin:0 auto;border-radius:4px;"></div></td>
                <td>
                    <div style="display:flex;align-items:center;gap:12px;">
                        <div class="skeleton-shimmer" style="width:42px;height:42px;border-radius:8px;flex-shrink:0;"></div>
                        <div style="flex:1;display:flex;flex-direction:column;gap:6px;">
                            <div class="skeleton-shimmer skeleton-text" style="width:65%;height:14px;"></div>
                            <div class="skeleton-shimmer skeleton-text" style="width:35%;height:11px;"></div>
                        </div>
                    </div>
                </td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:80px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:90px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:110px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:55px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:45px;height:12px;"></div></td>
                <td><div class="skeleton-shimmer skeleton-text" style="width:80px;height:16px;border-radius:10px;"></div></td>
                <td style="text-align:right;"><div class="skeleton-shimmer" style="width:28px;height:28px;border-radius:6px;display:inline-block;"></div></td>
            </tr>
        `;
    }

    function goToMediaPage(page) {
        mediaCurrentPage = page;
        renderMediaTable();
    }

    function renderMediaTable() {
        const tbody = document.getElementById('media-table-body');
        if (!tbody) return;

        const totalCount = mediaList.length;
        let totalBytes = 0;
        let readyCount = 0;
        let activeTasksCount = 0;

        mediaList.forEach(m => {
            if (m.size_bytes) totalBytes += m.size_bytes;
            if (m.state === 'completed') readyCount++;
            if (m.state === 'downloading' || m.state === 'processing' || m.state === 'transferring' || m.state === 'detected' || m.state === 'awaiting_upload') activeTasksCount++;
        });

        if (hasFetchedMediaOnce || totalCount > 0) {
            global.safeSetText('media-stat-count', totalCount);
            global.safeSetText('media-stat-size', global.formatBytes(totalBytes));
            global.safeSetText('media-stat-ready', readyCount);
            global.safeSetText('media-stat-tasks', activeTasksCount);
        }

        const filtered = mediaList.filter(item => {
            if (mediaFilterStatus !== 'all') {
                if (mediaFilterStatus === 'downloading') {
                    if (!['downloading', 'processing', 'transferring', 'detected', 'awaiting_upload'].includes(item.state)) return false;
                } else if (item.state !== mediaFilterStatus) {
                    return false;
                }
            }
            if (mediaFilterNode) {
                const itemNode = (item.placement?.node_id || item.worker_node_id || item.node_id || '').toLowerCase();
                if (itemNode !== mediaFilterNode.toLowerCase()) return false;
            }
            if (mediaFilterTier) {
                const itemTier = (item.placement?.tier_label || (item.placement?.tier_id !== undefined ? `tier${item.placement.tier_id}` : '')).toLowerCase();
                if (itemTier !== mediaFilterTier.toLowerCase()) return false;
            }
            if (mediaSearchQuery) {
                const q = mediaSearchQuery;
                const id = (item.key || '').toLowerCase();
                const name = (item.filename || '').toLowerCase();
                const node = (item.worker_node_id || item.placement?.node_id || item.node_id || '').toLowerCase();
                const tier = (item.placement?.tier_label || (item.placement?.tier_id !== undefined ? `tier${item.placement.tier_id}` : '')).toLowerCase();
                const path = (item.placement?.path || '').toLowerCase();
                const state = (item.state || '').toLowerCase();
                
                // Inspect nested video/audio/sub specs if present
                const meta = item.metadata || {};
                const res = (meta.video?.resolution || meta.video?.quality || '').toLowerCase();
                const codec = (meta.video?.codec || '').toLowerCase();
                const audioLangs = (meta.audio_tracks || []).map(a => (a.language || a.title || '').toLowerCase()).join(' ');
                const subLangs = (meta.subtitles || []).map(s => (s.language || s.title || '').toLowerCase()).join(' ');

                // Full-text document compilation
                const creator = (item.created_by || 'Dashboard').toLowerCase();
                const doc = `${name} ${id} ${creator} ${node} ${tier} ${path} ${state} ${res} ${codec} ${audioLangs} ${subLangs}`;
                
                // Support multi-term queries where each term must match
                const terms = q.split(/\s+/).filter(Boolean);
                const allMatch = terms.every(t => doc.includes(t));
                if (!allMatch) {
                    return false;
                }
            }
            return true;
        });

        let paginatedItems = [];
        if (global.UI && typeof global.UI.tablePagination === 'function') {
            const pg = global.UI.tablePagination({
                items: filtered,
                currentPage: mediaCurrentPage,
                pageSize: mediaPageSize,
                infoEl: 'media-pagination-info',
                buttonsEl: 'media-pagination-buttons',
                onPageChange: goToMediaPage
            });
            mediaCurrentPage = pg.currentPage;
            paginatedItems = pg.pageItems;
        } else {
            const totalItems = filtered.length;
            const totalPages = Math.max(1, Math.ceil(totalItems / mediaPageSize));
            if (mediaCurrentPage > totalPages) mediaCurrentPage = totalPages;
            const startIndex = (mediaCurrentPage - 1) * mediaPageSize;
            const endIndex = Math.min(startIndex + mediaPageSize, totalItems);
            paginatedItems = filtered.slice(startIndex, endIndex);
            renderPaginationButtons(totalPages);
        }

        if (paginatedItems.length === 0) {
            if (!hasFetchedMediaOnce && mediaList.length === 0) {
                tbody.innerHTML = renderSkeletonMediaRows();
                return;
            }
            tbody.innerHTML = (global.UI && typeof global.UI.emptyState === 'function')
                ? global.UI.emptyState({ colspan: 9, padding: '40px', color: 'var(--text-muted)', message: 'No media items match your search or filter.' })
                : `<tr><td colspan="9" style="text-align: center; padding: 40px; color: var(--text-muted);">No media items match your search or filter.</td></tr>`;
            return;
        }

        tbody.innerHTML = paginatedItems.map(item => {
            const isReady = item.state === 'completed';
            const isMissing = item.state === 'missing';
            const isFailed = item.state === 'failed';
            const filename = item.filename || item.key;
            const sizeStr = item.size_bytes ? global.formatBytes(item.size_bytes) : '-';
            const dateStr = item.created_at ? new Date(item.created_at).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' }) : '-';
            const meta = item.metadata;
            const specsHtml = renderMediaSpecs(meta);

            if (isReady && (!meta || !meta.video) && !item._fetchingMeta) {
                item._fetchingMeta = true;
                const metaUrl = item.stream_url ? (item.stream_url.replace(/\/+$/, '') + '/metadata.json') : (`/stream/${encodeURIComponent(item.key)}/metadata.json`);
                fetch(metaUrl)
                    .then(r => r.ok ? r.json() : null)
                    .then(m => {
                        if (m && (m.video || m.audio_tracks)) {
                            item.metadata = m;
                            const el = document.getElementById(`specs-cell-${item.key}`);
                            if (el) {
                                el.innerHTML = renderMediaSpecs(m);
                            }
                        }
                    })
                    .catch(() => {});
            }

            const placement = item.placement || {};
            const storageNode = placement.node_id || item.worker_node_id || item.node_id || 'Auto';
            const computeNode = item.node_id || item.worker_node_id || '';
            const tierLabel = placement.tier_label || (placement.tier_id === 2 ? 'tier2' : (placement.tier_id === 1 ? 'tier1' : 'storage'));
            const drivePath = placement.path || '/';
            const isDecoupled = computeNode && storageNode && computeNode !== storageNode;
            const hasCustomHost = !!placement.public_host;

            const locationHtml = `
                <div style="display:flex; flex-direction:column; gap:4px;">
                    ${isDecoupled ? `
                        <div style="display:inline-flex; align-items:center; gap:5px; flex-wrap:wrap;">
                            <span class="badge-compute" title="Processing Worker: Ingest & Remux performed here">${global.escapeHtml(computeNode)}</span>
                            <span style="display:inline-flex; align-items:center; gap:5px; white-space:nowrap;">
                                <span style="color:var(--text-muted);font-size:11px;font-weight:700;">&rarr;</span>
                                <span class="badge-location" title="Final Storage Node: Stored here">${global.escapeHtml(storageNode)}</span>
                            </span>
                        </div>
                    ` : `
                        <div style="display:inline-flex; align-items:center; gap:5px;">
                            <span class="badge-location" title="Direct Compute & Storage Node">${global.escapeHtml(storageNode)}</span>
                        </div>
                    `}
                    <div style="font-size:11px; color:var(--text-secondary); display:flex; align-items:center; gap:5px; flex-wrap:wrap;">
                        <span class="badge-drive" title="Disk mount point">${global.escapeHtml(drivePath)}</span>
                        <span class="badge-tier" title="Storage Tier">${global.escapeHtml(tierLabel)}</span>
                    </div>
                    ${hasCustomHost ? `<div style="font-size:10px; font-weight:700; color:var(--orange-primary);">Custom CDN / Host</div>` : ''}
                </div>
            `;

            // Lifecycle heat & demand state
            const hotInfo = mediaHotMap[item.key];
            const heatScoreVal = hotInfo ? (Number(hotInfo.score) || 0).toFixed(1) : (isReady ? '0.0' : '—');
            const isPinned = hotInfo && hotInfo.pinned_tier !== undefined && hotInfo.pinned_tier !== null;
            const lastHitRel = hotInfo && hotInfo.last_hit_at ? formatRelativeTime(hotInfo.last_hit_at) : '—';

            const heatHtml = `
                <div style="display:flex;flex-direction:column;gap:3px;align-items:flex-start;">
                    <div style="display:inline-flex;align-items:center;gap:6px;">
                        <span class="fl-score-pill tabular-num" style="font-size:12.5px;padding:2px 8px;" title="Real-time decayed heat score">${heatScoreVal}</span>
                        ${isPinned ? `<span class="badge-tier orange" style="font-size:9.5px;padding:1px 5px;" title="Pinned to Tier ${hotInfo.pinned_tier}">PINNED</span>` : ''}
                    </div>
                    ${lastHitRel !== '—' ? `<span style="font-size:10.5px;color:var(--text-muted);font-family:'JetBrains Mono',monospace;" title="Last playback hit">${lastHitRel}</span>` : ''}
                </div>
            `;

            const activeTask = activeLifecycleTasks[item.key];
            let relocationBadgeHtml = '';
            if (activeTask && !['done', 'failed', 'cancelled'].includes(activeTask.state)) {
                const targetTier = activeTask.to ? (activeTask.to.tier_label || 'Tier ' + activeTask.to.tier_id) : 'Target';
                const pPct = activeTask.progress ? activeTask.progress.toFixed(0) + '%' : (activeTask.state || 'moving');
                relocationBadgeHtml = `
                    <div style="margin-top:6px;display:inline-flex;align-items:center;gap:5px;padding:3px 8px;border-radius:9999px;background:var(--bg-orange-soft);border:1px solid var(--border-orange);font-size:11px;font-weight:700;color:var(--orange-dark);">
                        <span class="drawer-pulse-dot" style="width:6px;height:6px;background:var(--orange-primary);border-radius:50%;display:inline-block;"></span>
                        <span>Relocating &rarr; ${global.escapeHtml(targetTier)} (${pPct})</span>
                    </div>
                `;
            }

            let statusHtml = '';
            if (isMissing) {
                statusHtml = `
                    <div class="media-failed-alert" style="background:#fee2e2;border:1px solid #fca5a5;padding:6px 10px;border-radius:var(--radius-sm);">
                        <span style="color:#b91c1c;font-weight:700;font-size:11.5px;display:inline-flex;align-items:center;gap:4px;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
                            File Missing on Node
                        </span>
                        <div style="font-size:10.5px;color:#b91c1c;margin-top:2px;">File removed from storage disk</div>
                    </div>`;
            } else if (isReady) {
                statusHtml = `<span class="badge-cmaf">✓ Ready Stream</span>${relocationBadgeHtml}`;
            } else if (isFailed) {
                statusHtml = `
                    <div class="media-failed-alert">
                        <span class="media-failed-title" style="color:var(--status-offline);font-weight:700;font-size:12px;display:inline-flex;align-items:center;gap:4px;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"></circle><line x1="15" y1="9" x2="9" y2="15"></line><line x1="9" y1="9" x2="15" y2="15"></line></svg>
                            Failed
                        </span>
                        <div class="media-failed-msg" style="font-size:11px;color:var(--status-offline);margin-top:3px;line-height:1.35;word-break:break-word;font-family:system-ui,sans-serif;" title="${global.escapeHtml(item.error || 'Download or packaging error')}">
                            ${global.escapeHtml(item.error || 'Download error')}
                        </div>
                    </div>`;
            } else {
                const pct = Math.min(100, Math.max(0, item.progress_percent || 0)).toFixed(1);
                const stageLabel = item.stage_name || item.state.replace('_', ' ');
                const speedDisplay = item.speed || '';
                let byteProgress = '';
                if (item.transferred_bytes && item.total_bytes && item.total_bytes > 0) {
                    byteProgress = `${global.formatBytes(item.transferred_bytes)} / ${global.formatBytes(item.total_bytes)}`;
                }

                statusHtml = `
                    <div style="width:100%;max-width:300px;">
                        ${renderPipelineStepper(item)}
                        <div style="display:flex;justify-content:space-between;align-items:center;gap:8px;font-size:11px;font-weight:700;margin-bottom:4px;">
                            <span style="color:var(--orange-primary);flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;text-transform:capitalize;" title="${global.escapeHtml(stageLabel)}">
                                <svg class="spin-fast" style="vertical-align:-2px;" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"></circle><path d="M12 2a10 10 0 0 1 10 10"></path></svg>
                                ${global.escapeHtml(stageLabel)}
                            </span>
                            <span style="color:var(--text-primary);font-family:'JetBrains Mono',monospace;font-size:12px;flex-shrink:0;">${pct}%</span>
                        </div>
                        <div class="gauge-track" style="height:7px;background:var(--bg-body);border:1px solid var(--border-light);border-radius:5px;overflow:hidden;">
                            <div class="gauge-fill" style="width:${pct}%;background:linear-gradient(90deg, var(--orange-primary), #ff9f43);height:100%;transition:width 0.4s ease;box-shadow:0 0 8px rgba(255,122,24,0.4);"></div>
                        </div>
                        <div style="display:flex;justify-content:space-between;align-items:center;gap:8px;font-size:10.5px;color:var(--text-secondary);margin-top:4px;font-family:'JetBrains Mono',monospace;">
                            ${speedDisplay ? `<span style="flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${global.escapeHtml(speedDisplay)}</span>` : `<span></span>`}
                            ${byteProgress ? `<span style="font-weight:600;flex-shrink:0;">${global.escapeHtml(byteProgress)}</span>` : ''}
                        </div>
                        ${item.details ? `<div style="font-size:9.5px;color:var(--text-muted);margin-top:2px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${global.escapeHtml(item.details)}">${global.escapeHtml(item.details)}</div>` : ''}
                    </div>`;
            }

            const creator = item.created_by || 'Dashboard';
            const isAPIKey = creator !== 'Dashboard';
            const createdByHtml = `
                <div style="display:inline-flex;align-items:center;gap:5px;">
                    ${isAPIKey ? `
                        <span class="ui-badge ui-badge-orange ui-badge-pill" style="font-size:11px;font-weight:700;display:inline-flex;align-items:center;gap:4px;white-space:nowrap;" title="Created via API Key: ${global.escapeHtml(creator)}">
                            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3"><path d="M21 2l-2 2m-1.5 1.5L16 7l-1.5-1.5M7 11a5 5 0 1 0 7.07 7.07L22 10l-3-3L11 15z"></path></svg>
                            <span>${global.escapeHtml(creator)}</span>
                        </span>
                    ` : `
                        <span class="ui-badge ui-badge-neutral ui-badge-pill" style="font-size:11px;display:inline-flex;align-items:center;gap:4px;white-space:nowrap;" title="Created via Dashboard UI">
                            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><line x1="3" y1="9" x2="21" y2="9"></line><line x1="9" y1="21" x2="9" y2="9"></line></svg>
                            <span>Dashboard</span>
                        </span>
                    `}
                </div>
            `;

            const isChecked = selectedMediaKeys.has(item.key);
            return `
                <tr class="media-row-card ${isChecked ? 'row-selected' : ''}">
                    <td style="text-align:center;width:36px;vertical-align:middle;">
                        <input type="checkbox" class="media-row-checkbox" value="${global.escapeHtml(item.key)}" ${isChecked ? 'checked' : ''} onchange="onMediaRowCheckChange('${global.escapeHtml(item.key)}', this.checked)">
                    </td>
                    <td data-label="Media Asset" class="media-cell-asset">
                        <div>
                            <div class="media-card-title" title="${global.escapeHtml(filename)}">
                                ${global.escapeHtml(filename)}
                            </div>
                            <div class="media-card-meta">
                                <span class="media-card-id">${global.escapeHtml(item.key)}</span>
                                <span class="media-card-meta-dot">&bull;</span>
                                <span class="media-card-date">${dateStr}</span>
                                <span class="media-card-meta-dot mob-only">&bull;</span>
                                <span class="media-card-size-badge mob-only">${sizeStr}</span>
                            </div>
                        </div>
                    </td>
                    <td data-label="Created By" class="media-cell-creator">
                        ${createdByHtml}
                    </td>
                    <td data-label="Specs &amp; Tracks" class="media-cell-specs" id="specs-cell-${global.escapeHtml(item.key)}">
                        ${specsHtml}
                    </td>
                    <td data-label="Storage Location" class="media-cell-location">
                        ${locationHtml}
                    </td>
                    <td data-label="Size" class="media-cell-size">
                        <strong style="color:var(--text-primary);font-family:'JetBrains Mono',monospace;font-size:12px;">${sizeStr}</strong>
                    </td>
                    <td data-label="Heat Score" class="media-cell-heat">
                        ${heatHtml}
                    </td>
                    <td data-label="Status &amp; Pipeline" class="media-cell-status">
                        ${statusHtml}
                    </td>
                    <td data-label="Actions" class="media-cell-actions">
                        <div class="media-actions-inner">
                            ${isReady ? `
                                <button type="button" class="tbl-icon-btn" onclick="openFileLifecycleDrawer('${global.escapeHtml(item.key)}')" title="Inspect tier lifecycle &amp; placement" aria-label="Lifecycle">
                                    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                                        <circle cx="12" cy="12" r="10"></circle>
                                        <polyline points="12 6 12 12 16 14"></polyline>
                                    </svg>
                                </button>
                                <button type="button" class="tbl-icon-btn is-primary" onclick="openStreamDetailsModal('${global.escapeHtml(item.key)}')" title="Stream &amp; Play Video" aria-label="Play">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" style="margin-left:1px;">
                                        <polygon points="5 3 19 12 5 21 5 3"></polygon>
                                    </svg>
                                </button>
                            ` : ''}
                            ${isFailed ? `
                                <button type="button" class="tbl-icon-btn is-warning" onclick="retryMediaJob('${global.escapeHtml(item.key)}')" title="Retry failed transfer" aria-label="Retry">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                        <polyline points="23 4 23 10 17 10"></polyline>
                                        <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"></path>
                                    </svg>
                                </button>
                            ` : ''}
                            <button type="button" class="tbl-icon-btn is-danger" onclick="deleteMediaConfirm('${global.escapeHtml(item.key)}')" title="Delete from disk and database" aria-label="Delete">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                    <polyline points="3 6 5 6 21 6"></polyline>
                                    <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                                </svg>
                            </button>
                        </div>
                    </td>
                </tr>
            `;
        }).join('');
        updateBatchDeleteUI();
    }

    function updateNodeFilterDropdown() {
        const sel = document.getElementById('media-node-filter');
        if (!sel) return;
        const currentVal = sel.value;
        const nodes = new Set();
        mediaList.forEach(item => {
            const n = item.placement?.node_id || item.worker_node_id || item.node_id;
            if (n) nodes.add(n);
        });
        const sorted = Array.from(nodes).sort();
        let html = '<option value="">All VPS Nodes</option>';
        sorted.forEach(n => {
            html += `<option value="${global.escapeHtml(n)}" ${n === currentVal ? 'selected' : ''}>${global.escapeHtml(n)}</option>`;
        });
        sel.innerHTML = html;
    }

    function onMediaNodeFilterChange() {
        const sel = document.getElementById('media-node-filter');
        mediaFilterNode = sel ? sel.value : '';
        mediaCurrentPage = 1;
        renderMediaTable();
    }

    function onMediaTierFilterChange() {
        const sel = document.getElementById('media-tier-filter');
        mediaFilterTier = sel ? sel.value : '';
        mediaCurrentPage = 1;
        renderMediaTable();
    }

    function updateBatchDeleteUI() {
        const btn = document.getElementById('btn-batch-delete');
        const txt = document.getElementById('text-batch-delete');
        const selectAll = document.getElementById('media-select-all');
        if (!btn || !txt) return;
        const count = selectedMediaKeys.size;
        if (count > 0) {
            btn.style.display = 'inline-flex';
            txt.textContent = `Delete Selected (${count})`;
        } else {
            btn.style.display = 'none';
            if (selectAll) selectAll.checked = false;
        }
    }

    function onMediaRowCheckChange(key, checked) {
        if (checked) {
            selectedMediaKeys.add(key);
        } else {
            selectedMediaKeys.delete(key);
        }
        updateBatchDeleteUI();
    }

    function toggleSelectAllMedia(checked) {
        const checkboxes = document.querySelectorAll('.media-row-checkbox');
        checkboxes.forEach(cb => {
            cb.checked = checked;
            if (checked) {
                selectedMediaKeys.add(cb.value);
            } else {
                selectedMediaKeys.delete(cb.value);
            }
        });
        updateBatchDeleteUI();
    }

    async function deleteSelectedMedia() {
        const count = selectedMediaKeys.size;
        if (count === 0) return;
        if (!confirm(`Are you sure you want to permanently delete ${count} selected video file(s)?\n\nThis will remove them from the database and storage nodes immediately.`)) {
            return;
        }
        const btn = document.getElementById('btn-batch-delete');
        const txt = document.getElementById('text-batch-delete');
        if (btn) btn.disabled = true;
        if (txt) txt.textContent = 'Deleting...';

        try {
            const keys = Array.from(selectedMediaKeys);
            const res = await fetch('/api/v1/files', {
                method: 'DELETE',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ keys })
            });
            if (!res.ok) {
                const errText = await res.text();
                throw new Error(errText || 'Batch delete failed');
            }
            selectedMediaKeys.clear();
            updateBatchDeleteUI();
            await fetchMediaList(true);
            if (global.showToast) {
                global.showToast(`Successfully deleted ${count} media file(s).`, 'success');
            }
        } catch (err) {
            alert('Failed to delete selected files: ' + err.message);
        } finally {
            if (btn) btn.disabled = false;
            updateBatchDeleteUI();
        }
    }

    function openIngestURLModal() {
        const modal = document.getElementById('modal-ingest-url');
        if (!modal) return;

        const errorMsg = document.getElementById('ingest-error-msg');
        if (errorMsg) {
            errorMsg.style.display = 'none';
            errorMsg.innerText = '';
        }

        const input = document.getElementById('ingest-url-input');
        if (input) input.value = '';
        modal.classList.add('active', 'open');
        global.setModalOpen(true);
        setTimeout(() => { if (input) input.focus(); }, 100);
    }

    function closeIngestURLModal() {
        const modal = document.getElementById('modal-ingest-url');
        if (modal) modal.classList.remove('active', 'open');
        global.setModalOpen(false);
    }

    async function submitIngestURL() {
        const urlInput = document.getElementById('ingest-url-input');
        const submitBtn = document.getElementById('btn-submit-ingest');
        const errorMsg = document.getElementById('ingest-error-msg');

        if (errorMsg) {
            errorMsg.style.display = 'none';
            errorMsg.innerText = '';
        }

        const targetUrl = urlInput ? urlInput.value.trim() : '';
        if (!targetUrl) {
            if (errorMsg) {
                errorMsg.innerText = 'Please enter a valid video download URL';
                errorMsg.style.display = 'block';
            }
            return;
        }

        submitBtn.disabled = true;
        submitBtn.innerText = 'Probing & Dispatching...';

        try {
            const resp = await fetch('/api/v1/files', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ url: targetUrl })
            });

            if (resp.ok || resp.status === 201) {
                closeIngestURLModal();
                fetchMediaList();
            } else {
                let errDetail = 'Server returned an error';
                try {
                    const data = await resp.json();
                    errDetail = data.error || JSON.stringify(data);
                } catch (_) {
                    errDetail = await resp.text();
                }
                if (errorMsg) {
                    errorMsg.innerText = 'Ingest error: ' + errDetail;
                    errorMsg.style.display = 'block';
                } else {
                    alert('Ingest error: ' + errDetail);
                }
            }
        } catch (err) {
            if (errorMsg) {
                errorMsg.innerText = 'Network error: ' + err.message;
                errorMsg.style.display = 'block';
            } else {
                alert('Network error: ' + err.message);
            }
        } finally {
            submitBtn.disabled = false;
            submitBtn.innerText = 'Start Ingest';
        }
    }

    function openUploadModal() {
        const modal = document.getElementById('modal-upload-file');
        if (!modal) return;
        selectedUploadFile = null;
        document.getElementById('upload-filename-text').innerText = 'Click to choose or drag & drop video file';
        document.getElementById('upload-progress-container').style.display = 'none';
        document.getElementById('btn-submit-upload').disabled = true;
        modal.classList.add('active', 'open');
        global.setModalOpen(true);
    }

    function closeUploadModal() {
        const modal = document.getElementById('modal-upload-file');
        if (modal) modal.classList.remove('active', 'open');
        global.setModalOpen(false);
    }

    function onFileSelected(input) {
        if (input.files && input.files[0]) {
            selectedUploadFile = input.files[0];
            document.getElementById('upload-filename-text').innerText = `Selected: ${selectedUploadFile.name} (${global.formatBytes(selectedUploadFile.size)})`;
            document.getElementById('btn-submit-upload').disabled = false;
        }
    }

    async function startFileUpload() {
        if (!selectedUploadFile) return;

        const progressContainer = document.getElementById('upload-progress-container');
        const progressBar = document.getElementById('upload-progress-bar');
        const pctText = document.getElementById('upload-pct-text');
        const statusText = document.getElementById('upload-status-text');
        const submitBtn = document.getElementById('btn-submit-upload');

        progressContainer.style.display = 'block';
        submitBtn.disabled = true;
        statusText.innerText = 'Reserving upload slot...';

        let uploadURL = '';
        try {
            const reserve = await fetch('/api/v1/files-upload', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    filename: selectedUploadFile.name,
                    size_bytes: selectedUploadFile.size
                })
            });
            if (!reserve.ok) {
                alert('Reserve error: ' + await reserve.text());
                submitBtn.disabled = false;
                return;
            }
            const reserved = await reserve.json();
            uploadURL = reserved.upload_url;
            if (!uploadURL) {
                alert('No upload_url returned from coordinator');
                submitBtn.disabled = false;
                return;
            }
        } catch (err) {
            alert('Network error: ' + err.message);
            submitBtn.disabled = false;
            return;
        }

        const formData = new FormData();
        formData.append('file', selectedUploadFile);

        const xhr = new XMLHttpRequest();
        xhr.open('POST', uploadURL, true);

        xhr.upload.onprogress = (e) => {
            if (e.lengthComputable) {
                const pct = (e.loaded / e.total) * 100;
                progressBar.style.width = `${pct}%`;
                pctText.innerText = `${pct.toFixed(0)}%`;
                if (pct >= 100) {
                    statusText.innerText = 'Processing video & audio streams...';
                }
            }
        };

        xhr.onload = () => {
            if (xhr.status >= 200 && xhr.status < 300) {
                statusText.innerText = 'Upload complete! Processing in background...';
                setTimeout(() => {
                    closeUploadModal();
                    fetchMediaList();
                }, 500);
            } else {
                alert('Upload error: ' + xhr.responseText);
                submitBtn.disabled = false;
            }
        };

        xhr.onerror = () => {
            alert('Network error during upload');
            submitBtn.disabled = false;
        };

        statusText.innerText = 'Uploading to worker...';
        xhr.send(formData);
    }

    async function triggerStorageSync() {
        const btn = document.getElementById('btn-sync-storage');
        const icon = document.getElementById('icon-sync-storage');
        const text = document.getElementById('text-sync-storage');
        if (btn) btn.disabled = true;
        if (icon) icon.classList.add('spin-fast');
        if (text) text.innerText = 'Auditing nodes...';

        try {
            const resp = await fetch('/api/v1/files-sync?force=true', { method: 'POST' });
            const data = await resp.json();
            if (resp.ok) {
                await fetchMediaList();
                if (typeof global.fetchPoolMetrics === 'function') {
                    global.fetchPoolMetrics();
                }
                if (text) {
                    text.innerText = data.purged > 0 ? `Cleaned ${data.purged} ghost(s)` : 'In Sync (100%)';
                }
            } else {
                console.error('Storage sync error:', data.error);
                if (text) text.innerText = 'Sync Failed';
            }
        } catch (err) {
            console.error('Network error during storage audit:', err);
            if (text) text.innerText = 'Sync Error';
        } finally {
            if (icon) icon.classList.remove('spin-fast');
            setTimeout(() => {
                if (btn) btn.disabled = false;
                if (text) text.innerText = 'Sync Storage';
            }, 2000);
        }
    }

    async function openStreamDetailsModal(mediaID) {
        const modal = document.getElementById('modal-stream-details');
        if (!modal) return;

        const job = mediaList.find(m => m.key === mediaID);
        if (!job) return;

        const filename = job.filename || mediaID;
        global.safeSetText('stream-modal-title', filename);

        let streamUrl = job.stream_url;
        if (!streamUrl || (window.location.protocol === 'https:' && streamUrl.startsWith('http://'))) {
            try {
                const tokenRes = await fetch(`/api/v1/files/${encodeURIComponent(mediaID)}/token`);
                if (tokenRes.ok) {
                    const tokenData = await tokenRes.json();
                    if (tokenData && tokenData.video_url) {
                        streamUrl = tokenData.video_url.startsWith('http') ? tokenData.video_url : `${window.location.origin}${tokenData.video_url}`;
                    } else if (tokenData && tokenData.play_url) {
                        streamUrl = `${window.location.origin}${tokenData.play_url}`;
                    }
                }
            } catch (_) {}
            if (!streamUrl || streamUrl.startsWith('http://')) {
                streamUrl = `${window.location.origin}/stream/${encodeURIComponent(mediaID)}`;
            }
        }

        const directInput = document.getElementById('stream-direct-url');
        if (directInput) directInput.value = streamUrl;

        const badgesWrap = document.getElementById('stream-modal-badges');
        if (badgesWrap) {
            const computeNode = job.node_id || job.worker_node_id || '';
            const storageNode = job.placement?.node_id || computeNode || 'Auto';
            const drive = job.placement?.path || '/';
            const tier = job.placement?.tier_label || (job.placement?.tier_id === 2 ? 'tier2' : 'tier1');
            const isDecoupled = computeNode && storageNode && computeNode !== storageNode;

            let badges = `<span style="font-size:11px;color:var(--text-muted);font-family:'JetBrains Mono',monospace;">ID: ${global.escapeHtml(mediaID)}</span>`;
            if (isDecoupled) {
                badges += `<span class="badge-compute" title="Processed on ${global.escapeHtml(computeNode)}">${global.escapeHtml(computeNode)}</span>`;
                badges += `<span style="color:var(--text-muted);font-size:10px;">➔</span>`;
                badges += `<span class="badge-location" title="Stored on ${global.escapeHtml(storageNode)}">${global.escapeHtml(storageNode)}</span>`;
            } else {
                badges += `<span class="badge-location">${global.escapeHtml(storageNode)}</span>`;
            }
            badges += `<span class="badge-drive">${global.escapeHtml(drive)}</span>`;
            badges += `<span class="badge-tier">${global.escapeHtml(tier)}</span>`;
            badgesWrap.innerHTML = badges;
        }

        modal.classList.add('active', 'open');
        global.setModalOpen(true);

        let meta = job.metadata;
        if (!meta) {
            try {
                const metaUrl = `${window.location.origin}/stream/${encodeURIComponent(mediaID)}/metadata.json`;
                const r = await fetch(metaUrl);
                if (r.ok) {
                    meta = await r.json();
                    job.metadata = meta;
                }
            } catch (e) {
                console.warn('Could not fetch metadata.json:', e);
            }
        }

        let activeStreamUrl = streamUrl;
        let baseDirUrl = resolveBaseDirUrl(activeStreamUrl, job);
        if (meta && (meta.master_m3u8 || (meta.audio_tracks && meta.audio_tracks.length > 1))) {
            const candidateHlsUrl = `${baseDirUrl}/${meta.master_m3u8 || 'master.m3u8'}`;
            try {
                const check = await fetch(candidateHlsUrl, { method: 'HEAD' });
                if (check.ok) {
                    activeStreamUrl = candidateHlsUrl;
                }
            } catch (_) {}
        }

        if (directInput) directInput.value = activeStreamUrl;

        renderMediaModalDrawers(job, meta, activeStreamUrl);
        initArtPlayer(activeStreamUrl, job, meta);
    }

    function renderMediaModalDrawers(job, meta, activeStreamUrl) {
        const tracksInfo = document.getElementById('stream-modal-tracks-info');
        const audioDrawer = document.getElementById('stream-modal-audio-drawer');
        const audioList = document.getElementById('stream-modal-audio-list');
        const subDrawer = document.getElementById('stream-modal-sub-drawer');
        const subList = document.getElementById('stream-modal-sub-list');

        let baseDirUrl = resolveBaseDirUrl(activeStreamUrl, job);

        if (meta && meta.audio_tracks && meta.audio_tracks.length > 1) {
            if (tracksInfo) {
                const audioSummary = meta.audio_tracks.map((t, i) => formatTrackTitle(t, `Track ${i + 1}`)).join(', ');
                tracksInfo.style.display = 'block';
                tracksInfo.innerHTML = `
                    <div style="display:flex;align-items:center;gap:9px;font-weight:800;font-size:12.5px;color:var(--orange-dark);">
                        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"></path><circle cx="6" cy="18" r="3"></circle><circle cx="18" cy="16" r="3"></circle></svg>
                        <span>Studio Multi-Audio Engine Active (${meta.audio_tracks.length} Tracks)</span>
                    </div>
                    <div style="color:var(--text-secondary);margin-top:4px;font-size:11.5px;line-height:1.5;">
                        Switch languages seamlessly via ArtPlayer's <strong>Settings &rarr; Audio Track</strong> menu (${global.escapeHtml(audioSummary)}).
                    </div>
                `;
            }

            if (audioDrawer && audioList) {
                audioDrawer.style.display = 'block';
                audioList.innerHTML = meta.audio_tracks.map((t, idx) => {
                    const trackUrl = `${baseDirUrl}/${t.file || `audio_${t.index}_${t.language}.m4a`}`;
                    const title = formatTrackTitle(t, `Track ${idx + 1}`);
                    return `
                        <div class="track-row">
                            <span class="track-row-title">
                                <span class="track-icon"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"></path><circle cx="6" cy="18" r="3"></circle><circle cx="18" cy="16" r="3"></circle></svg></span>
                                ${global.escapeHtml(title)}
                                <span class="track-row-meta">${global.escapeHtml(t.codec || 'aac')} &middot; ${t.channels || 2}ch</span>
                            </span>
                            <span class="track-chip">#${idx + 1}</span>
                            <button class="btn btn-secondary" style="padding:4px 12px;font-size:11px;" onclick="copyToClipboard('${global.escapeHtml(trackUrl)}', 'Audio URL copied!')">Copy Link</button>
                        </div>
                    `;
                }).join('');
            }
        } else {
            if (tracksInfo) tracksInfo.style.display = 'none';
            if (audioDrawer) audioDrawer.style.display = 'none';
        }

        if (meta && meta.subtitles && meta.subtitles.length > 0) {
            if (subDrawer && subList) {
                subDrawer.style.display = 'block';
                const seenSubCount = {};
                subList.innerHTML = meta.subtitles.map((s, idx) => {
                    const subUrl = `${baseDirUrl}/${s.file || `subtitle_${s.index}_${s.language}.vtt`}`;
                    let title = formatTrackTitle(s, `Subtitle ${idx + 1}`);
                    seenSubCount[title] = (seenSubCount[title] || 0) + 1;
                    if (seenSubCount[title] > 1) {
                        title = `${title} #${seenSubCount[title]}`;
                    }
                    return `
                        <div class="track-row">
                            <span class="track-row-title">
                                <span class="track-icon"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="4" width="20" height="16" rx="2"></rect><path d="M6 16h4M14 16h4M4 12h2M9 12h6M18 12h2"></path></svg></span>
                                ${global.escapeHtml(title)}
                                <span class="track-row-meta">WebVTT</span>
                            </span>
                            <button class="btn btn-secondary" style="padding:4px 12px;font-size:11px;" onclick="copyToClipboard('${global.escapeHtml(subUrl)}', 'Subtitle URL copied!')">Copy VTT</button>
                        </div>
                    `;
                }).join('');
            }
        } else {
            if (subDrawer) subDrawer.style.display = 'none';
        }
    }

    function cinemaReady() {
        clearTimeout(cinemaLoadTimer);
        const errorBox = document.getElementById('cinema-error');
        if (errorBox) errorBox.style.display = 'none';
    }

    function cinemaFail(job, reason) {
        clearTimeout(cinemaLoadTimer);
        if (currentArtPlayer && currentArtPlayer.notice) {
            try { currentArtPlayer.notice.show = ''; } catch (_) {}
        }
        const box = document.getElementById('cinema-error');
        if (!box) return;
        box.style.display = 'flex';
        box.innerHTML = `
            <div class="cinema-error-icon">
                <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
            </div>
            <strong>Stream Unavailable</strong>
            <span>${global.escapeHtml(job.filename || job.key)} &mdash; ${global.escapeHtml(reason || 'The storage node did not respond. It may be offline, or the file was moved or deleted.')}</span>
            <button class="cinema-retry-btn" onclick="retryArtPlayer()">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"></path></svg>
                Retry
            </button>
        `;
    }

    function retryArtPlayer() {
        if (!cinemaRetryCtx) return;
        const ctx = cinemaRetryCtx;
        closeArtPlayer();
        initArtPlayer(ctx.streamUrl, ctx.job, ctx.meta);
    }

    function mountFallbackVideo(container, streamUrl, job) {
        const video = document.createElement('video');
        video.src = streamUrl;
        video.controls = true;
        video.autoplay = true;
        video.addEventListener('error', () => cinemaFail(job));
        container.appendChild(video);
    }

    class AudioBooster {
        constructor() {
            this.ctx = null;
            this.gainNode = null;
            this.compressor = null;
            this.boostLevel = 1.0;
            this.connectedElements = new WeakSet();
        }

        init(element) {
            if (!element) return;
            try {
                const AudioCtx = window.AudioContext || window.webkitAudioContext;
                if (!AudioCtx) return;

                if (!this.ctx || this.ctx.state === 'closed') {
                    this.ctx = new AudioCtx();

                    this.compressor = this.ctx.createDynamicsCompressor();
                    this.compressor.threshold.setValueAtTime(-6, this.ctx.currentTime);
                    this.compressor.knee.setValueAtTime(12, this.ctx.currentTime);
                    this.compressor.ratio.setValueAtTime(4, this.ctx.currentTime);
                    this.compressor.attack.setValueAtTime(0.003, this.ctx.currentTime);
                    this.compressor.release.setValueAtTime(0.25, this.ctx.currentTime);

                    this.gainNode = this.ctx.createGain();
                    this.gainNode.gain.setValueAtTime(this.boostLevel, this.ctx.currentTime);

                    this.compressor.connect(this.gainNode);
                    this.gainNode.connect(this.ctx.destination);
                }

                if (this.ctx.state === 'suspended') {
                    this.ctx.resume().catch(() => {});
                }

                if (!this.connectedElements.has(element)) {
                    const source = this.ctx.createMediaElementSource(element);
                    source.connect(this.compressor);
                    this.connectedElements.add(element);
                }
            } catch (err) {
                console.warn('AudioBooster init warning:', err);
            }
        }

        setBoost(level, element) {
            this.boostLevel = level;
            if (element) {
                this.init(element);
            }
            if (this.gainNode && this.ctx) {
                if (this.ctx.state === 'suspended') {
                    this.ctx.resume().catch(() => {});
                }
                this.gainNode.gain.setTargetAtTime(level, this.ctx.currentTime, 0.05);
            }
        }
    }

    let globalAudioBooster = null;

    function applyAudioBoost(level) {
        if (!globalAudioBooster) {
            globalAudioBooster = new AudioBooster();
        }
        const targetElement = (lockstepController && lockstepController.active && lockstepController.audio)
            ? lockstepController.audio
            : (currentArtPlayer ? currentArtPlayer.video : null);
        globalAudioBooster.setBoost(level, targetElement);
    }

    function initArtPlayer(streamUrl, job, meta) {
        closeArtPlayer();
        const container = document.getElementById('artplayer-container');
        if (!container) return;
        container.innerHTML = '';
        cinemaRetryCtx = { streamUrl, job, meta };
        const errorBox = document.getElementById('cinema-error');
        if (errorBox) {
            errorBox.style.display = 'none';
            errorBox.innerHTML = '';
        }

        const filename = job.filename || job.key;
        let baseDirUrl = resolveBaseDirUrl(streamUrl, job);
        const hasMultiAudio = meta && meta.audio_tracks && meta.audio_tracks.length > 1;
        const hasExternalAudio = meta && meta.audio_tracks && meta.audio_tracks.length > 0 &&
            (meta.audio_tracks.length > 1 || meta.audio_tracks[0].embedded === false || !!meta.audio_tracks[0].file);

        let subtitleOption = undefined;
        if (meta && meta.subtitles && meta.subtitles.length > 0) {
            const firstSub = meta.subtitles[0];
            if (firstSub && (firstSub.file || firstSub.language)) {
                subtitleOption = {
                    url: `${baseDirUrl}/${firstSub.file || `subtitle_${firstSub.index}_${firstSub.language}.vtt`}`,
                    type: 'vtt',
                    style: {
                        color: '#ffffff',
                        fontSize: '20px',
                        textShadow: '0 2px 4px rgba(0,0,0,0.8)'
                    },
                    encoding: 'utf-8'
                };
            }
        }

        const customSettings = [];

        if (hasMultiAudio) {
            const activeTrackTitle = formatTrackTitle(meta.audio_tracks[0], 'Default');
            const audioSelectorItems = meta.audio_tracks.map((t, idx) => {
                const title = formatTrackTitle(t, `Track ${idx + 1}`);
                return {
                    default: idx === 0,
                    html: `<span style="display:inline-flex;align-items:center;gap:6px;">🎵 ${global.escapeHtml(title)} <span style="font-size:10px;opacity:0.6;">(${global.escapeHtml(t.codec || 'aac')}, ${t.channels || 2}ch)</span></span>`,
                    url: `${baseDirUrl}/${t.file || `audio_${t.index}_${t.language}.m4a`}`,
                    track: t,
                    index: idx,
                    displayTitle: title
                };
            });

            customSettings.push({
                icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z"/></svg>',
                width: 240,
                html: 'Audio Track',
                name: 'audio_track',
                tooltip: activeTrackTitle,
                selector: audioSelectorItems,
                onSelect: function (item) {
                    switchArtPlayerAudio(item.index, item.url);
                    return item.displayTitle;
                }
            });
        }

        if (meta && meta.subtitles && meta.subtitles.length > 0) {
            const seenSubCount = {};
            const subItems = meta.subtitles.map((s, idx) => {
                let title = formatTrackTitle(s, `Subtitle ${idx + 1}`);
                seenSubCount[title] = (seenSubCount[title] || 0) + 1;
                if (seenSubCount[title] > 1) {
                    title = `${title} #${seenSubCount[title]}`;
                }
                return {
                    default: idx === 0,
                    html: `<span style="display:inline-flex;align-items:center;gap:6px;">💬 ${global.escapeHtml(title)} <span style="font-size:10px;opacity:0.6;">(VTT)</span></span>`,
                    url: `${baseDirUrl}/${s.file || `subtitle_${s.index}_${s.language}.vtt`}`,
                    sub: s,
                    displayTitle: title
                };
            });
            subItems.push({
                default: false,
                html: '<span>❌ Turn Off Subtitles</span>',
                url: '',
                sub: null,
                displayTitle: 'Off'
            });

            const firstSubTitle = subItems[0].displayTitle;
            customSettings.push({
                icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2m-9 5H5V7h6zm8 8H5v-2h14zm-4-4H5v-2h10zm4 0h-2v-2h2z"/></svg>',
                width: 240,
                html: 'Subtitles',
                name: 'subtitles',
                tooltip: firstSubTitle,
                selector: subItems,
                onSelect: function (item) {
                    if (currentArtPlayer && currentArtPlayer.subtitle) {
                        if (item.url) {
                            currentArtPlayer.subtitle.show = true;
                            currentArtPlayer.subtitle.switch(item.url, { name: item.sub?.language || 'Sub' });
                        } else {
                            currentArtPlayer.subtitle.show = false;
                        }
                    }
                    return item.displayTitle;
                }
            });
        }

        let savedBoost = 1.0;
        try {
            savedBoost = parseFloat(localStorage.getItem('stream_audio_boost') || '1.0');
            if (isNaN(savedBoost) || savedBoost < 1.0 || savedBoost > 2.0) savedBoost = 1.0;
        } catch (_) {}

        const boostItems = [
            {
                default: savedBoost === 1.0,
                html: '<span style="display:inline-flex;align-items:center;gap:6px;">🔊 100% <span style="font-size:10px;opacity:0.6;">(Normal)</span></span>',
                value: 1.0,
                displayTitle: '100%'
            },
            {
                default: Math.abs(savedBoost - 1.2) < 0.01,
                html: '<span style="display:inline-flex;align-items:center;gap:6px;">🔉 120% <span style="font-size:10px;opacity:0.6;">(Light Boost)</span></span>',
                value: 1.2,
                displayTitle: '120%'
            },
            {
                default: Math.abs(savedBoost - 1.5) < 0.01,
                html: '<span style="display:inline-flex;align-items:center;gap:6px;">⚡ 150% <span style="font-size:10px;opacity:0.6;">(Boosted)</span></span>',
                value: 1.5,
                displayTitle: '150%'
            },
            {
                default: Math.abs(savedBoost - 1.7) < 0.01,
                html: '<span style="display:inline-flex;align-items:center;gap:6px;">🔥 170% <span style="font-size:10px;opacity:0.6;">(High Boost)</span></span>',
                value: 1.7,
                displayTitle: '170%'
            },
            {
                default: Math.abs(savedBoost - 2.0) < 0.01,
                html: '<span style="display:inline-flex;align-items:center;gap:6px;">🚀 200% <span style="font-size:10px;opacity:0.6;">(Max Boost)</span></span>',
                value: 2.0,
                displayTitle: '200%'
            }
        ];

        const activeBoostItem = boostItems.find(b => b.default) || boostItems[0];

        customSettings.push({
            icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/></svg>',
            width: 220,
            html: 'Audio Boost',
            name: 'audio_boost',
            tooltip: activeBoostItem.displayTitle,
            selector: boostItems,
            onSelect: function (item) {
                try {
                    localStorage.setItem('stream_audio_boost', String(item.value));
                } catch (_) {}
                applyAudioBoost(item.value);
                if (currentArtPlayer && currentArtPlayer.notice) {
                    currentArtPlayer.notice.show = `Audio Boost: ${item.displayTitle}`;
                }
                return item.displayTitle;
            }
        });

        const ArtClass = window.Artplayer || window.ArtPlayer;
        if (!ArtClass) {
            console.warn('Artplayer library not loaded, using HTML5 fallback');
            mountFallbackVideo(container, streamUrl, job);
            return;
        }

        try {
            const isMobile = /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
            const videoAttrs = {
                playsInline: 'true',
                'webkit-playsinline': 'true',
                crossOrigin: 'anonymous'
            };

            const artOptions = {
                container: '#artplayer-container',
                url: streamUrl,
                type: streamUrl.includes('.m3u8') ? 'm3u8' : 'mp4',
                title: filename,
                volume: 0.8,
                isLive: false,
                muted: false,
                autoplay: false,
                pip: false,
                autoSize: false,
                autoMini: false,
                screenshot: false,
                setting: true,
                loop: false,
                flip: false,
                playbackRate: true,
                aspectRatio: false,
                fullscreen: true,
                fullscreenWeb: false,
                subtitleOffset: true,
                miniProgressBar: true,
                theme: '#ff7a18',
                moreVideoAttr: videoAttrs,
                settings: customSettings,
                controls: [],
                customType: {
                    m3u8: function (video, url, art) {
                        if (window.Hls && Hls.isSupported()) {
                            if (art.hls) {
                                art.hls.destroy();
                            }
                            const hls = new Hls({
                                enableWorker: true,
                                lowLatencyMode: false,
                                backBufferLength: 90
                            });
                            hls.loadSource(url);
                            hls.attachMedia(video);
                            art.hls = hls;
                            art.on('destroy', () => hls.destroy());

                            hls.on(Hls.Events.MANIFEST_PARSED, () => {
                                cinemaReady();
                            });

                            hls.on(Hls.Events.ERROR, (event, data) => {
                                if (data && data.fatal) {
                                    switch (data.type) {
                                        case Hls.ErrorTypes.NETWORK_ERROR:
                                            hls.startLoad();
                                            break;
                                        case Hls.ErrorTypes.MEDIA_ERROR:
                                            hls.recoverMediaError();
                                            break;
                                        default:
                                            cinemaFail(job, 'HLS error: ' + data.details);
                                            break;
                                    }
                                }
                            });
                        } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
                            video.src = url;
                        }
                    }
                }
            };

            if (subtitleOption) {
                artOptions.subtitle = subtitleOption;
            }

            currentArtPlayer = new ArtClass(artOptions);
            currentArtPlayer.muted = false;
            if (currentArtPlayer.video) {
                currentArtPlayer.video.muted = false;
            }
            currentArtPlayer.on('ready', () => {
                cinemaReady();
            });
            currentArtPlayer.on('video:canplay', cinemaReady);
            currentArtPlayer.on('error', () => cinemaFail(job));
            if (savedBoost > 1.0) {
                currentArtPlayer.on('video:play', () => {
                    applyAudioBoost(savedBoost);
                }, { once: true });
            }
            clearTimeout(cinemaLoadTimer);
            cinemaLoadTimer = setTimeout(() => cinemaFail(job, 'Timed out while connecting to the storage node.'), 25000);

            const isHLS = streamUrl.includes('.m3u8');
            if (hasExternalAudio && !isHLS) {
                setupExternalAudioSync(currentArtPlayer, meta.audio_tracks, baseDirUrl);
            }
        } catch (err) {
            console.error('ArtPlayer init error:', err);
            mountFallbackVideo(container, streamUrl, job);
        }
    }

    class LockstepAudioController {
        constructor(art, initialAudioUrl) {
            this.art = art;
            this.video = art.video;
            this.audio = new Audio();
            this.audio.crossOrigin = 'anonymous';
            this.audio.preload = 'auto';
            this.active = true;
            this.isSeeking = false;
            this.userPaused = this.video.paused;
            this.internalPauseExpected = 0;
            this.switchSeq = 0;
            this.isSwitchingTrack = false;
            this.syncInterval = null;

            this.video.muted = false;
            this.audio.muted = false;
            this.audio.volume = this.video.volume > 0 ? this.video.volume : 0.8;
            this.audio.src = initialAudioUrl;
            this.audio.load();

            this.bindEvents();
        }

        bindEvents() {
            const v = this.video;
            const a = this.audio;
            const art = this.art;

            art.on('video:volumechange', () => {
                if (!this.active || !this.audio) return;
                a.volume = v.volume;
                a.muted = v.muted;
            });

            art.on('video:seeking', () => {
                if (!this.active || !this.audio) return;
                this.isSeeking = true;
                a.pause();
                a.currentTime = v.currentTime;
            });

            art.on('video:seeked', () => {
                if (!this.active || !this.audio) return;
                a.currentTime = v.currentTime;
            });

            art.on('video:waiting', () => {
                if (!this.active || !this.audio) return;
                a.pause();
            });

            art.on('video:pause', () => {
                if (this.internalPauseExpected > 0) {
                    this.internalPauseExpected--;
                    if (this.active && this.audio) a.pause();
                    return;
                }
                this.userPaused = true;
                if (!this.active || !this.audio) return;
                a.pause();
            });

            const onPlayHandler = () => {
                this.userPaused = false;
                this.internalPauseExpected = 0;
                if (!this.active || !this.audio || !this.audio.src || this.isSwitchingTrack) return;
                this.isSeeking = false;

                const startAudio = () => {
                    if (!this.active || !this.audio || v.paused || this.userPaused || this.isSwitchingTrack) return;
                    const diff = Math.abs(v.currentTime - a.currentTime);
                    if (diff > 0.03) {
                        a.currentTime = v.currentTime;
                    }
                    a.playbackRate = v.playbackRate;
                    a.play().catch(err => console.warn('Lockstep audio play warning:', err));
                };

                if (a.readyState >= 2) {
                    startAudio();
                } else {
                    a.addEventListener('canplay', startAudio, { once: true });
                }
            };

            art.on('video:play', onPlayHandler);
            art.on('video:playing', onPlayHandler);
            if (!v.paused) {
                onPlayHandler();
            }

            art.on('video:ratechange', () => {
                if (!this.active || !this.audio) return;
                a.playbackRate = v.playbackRate;
            });

            a.addEventListener('waiting', () => {
                if (!this.active || !this.video || v.paused || this.userPaused || this.isSwitchingTrack) return;
                this.internalPauseExpected = (this.internalPauseExpected || 0) + 1;
                v.pause();
                const onAudioResume = () => {
                    a.removeEventListener('canplay', onAudioResume);
                    if (this.active && this.video && !this.userPaused && !this.isSwitchingTrack) {
                        v.play().catch(() => {});
                    }
                };
                a.addEventListener('canplay', onAudioResume, { once: true });
            });

            this.syncInterval = setInterval(() => {
                if (!this.active || !this.audio || v.paused || v.seeking || this.isSeeking || a.paused || this.isSwitchingTrack) return;

                const diff = v.currentTime - a.currentTime;
                const absDiff = Math.abs(diff);

                if (absDiff > 0.3) {
                    a.currentTime = v.currentTime;
                    a.playbackRate = v.playbackRate;
                } else if (diff > 0.03) {
                    a.playbackRate = v.playbackRate * 1.03;
                } else if (diff < -0.03) {
                    a.playbackRate = v.playbackRate * 0.97;
                } else {
                    if (a.playbackRate !== v.playbackRate) {
                        a.playbackRate = v.playbackRate;
                    }
                }
            }, 200);
        }

        switchTrack(newUrl) {
            if (!this.active || !this.audio) return;
            const v = this.video;
            const a = this.audio;

            this.switchSeq = (this.switchSeq || 0) + 1;
            const seq = this.switchSeq;
            this.isSwitchingTrack = true;

            // Stop previous audio immediately; keep video playing smoothly without pausing
            a.pause();

            let switchTimeout = setTimeout(() => {
                onSwitchError('Audio track load timed out');
            }, 8000);

            const cleanup = () => {
                if (switchTimeout) {
                    clearTimeout(switchTimeout);
                    switchTimeout = null;
                }
                a.removeEventListener('loadedmetadata', onMetadataOrCanPlay);
                a.removeEventListener('canplay', onMetadataOrCanPlay);
                a.removeEventListener('canplay', resumePlayback);
                a.removeEventListener('error', onAudioError);
            };

            const onAudioError = (e) => {
                if (this.switchSeq !== seq || !this.active) return;
                cleanup();
                this.isSwitchingTrack = false;
                console.warn('Audio track load failed (403/404 or network):', e);
                if (this.art && this.art.notice) {
                    this.art.notice.show = 'Failed to load audio track';
                }
            };

            const onSwitchError = (msg) => {
                if (this.switchSeq !== seq || !this.active) return;
                cleanup();
                this.isSwitchingTrack = false;
                console.warn(msg);
                if (this.art && this.art.notice) {
                    this.art.notice.show = msg;
                }
            };

            const resumePlayback = () => {
                if (this.switchSeq !== seq || !this.active || !this.audio) return;
                cleanup();
                this.isSwitchingTrack = false;

                // Live alignment to exact video frame as video kept playing
                const liveTime = v.currentTime;
                if (Math.abs(liveTime - a.currentTime) > 0.03) {
                    a.currentTime = liveTime;
                }
                a.playbackRate = v.playbackRate;

                // Play audio seamlessly if video is active
                if (!this.userPaused && !v.paused) {
                    a.play().catch(() => {});
                }
            };

            const onMetadataOrCanPlay = () => {
                a.removeEventListener('loadedmetadata', onMetadataOrCanPlay);
                a.removeEventListener('canplay', onMetadataOrCanPlay);
                if (this.switchSeq !== seq || !this.active || !this.audio) return;

                const targetTime = v.currentTime;
                if (targetTime < 0.05) {
                    if (a.readyState >= 3) {
                        resumePlayback();
                    } else {
                        a.addEventListener('canplay', resumePlayback, { once: true });
                    }
                    return;
                }

                const onSeeked = () => {
                    a.removeEventListener('seeked', onSeeked);
                    if (this.switchSeq !== seq || !this.active || !this.audio) return;
                    if (a.readyState >= 3) {
                        resumePlayback();
                    } else {
                        a.addEventListener('canplay', resumePlayback, { once: true });
                    }
                };

                a.addEventListener('seeked', onSeeked, { once: true });
                a.currentTime = targetTime;
            };

            a.addEventListener('loadedmetadata', onMetadataOrCanPlay, { once: true });
            a.addEventListener('canplay', onMetadataOrCanPlay, { once: true });
            a.addEventListener('error', onAudioError, { once: true });

            a.src = newUrl;
            a.volume = v.volume > 0 ? v.volume : 0.8;
            a.muted = false;
            v.muted = false;
            a.load();
        }

        destroy() {
            this.active = false;
            this.switchSeq = (this.switchSeq || 0) + 1;
            if (this.syncInterval) {
                clearInterval(this.syncInterval);
                this.syncInterval = null;
            }
            if (this.audio) {
                this.audio.pause();
                this.audio.removeAttribute('src');
                this.audio.load();
                this.audio = null;
            }
        }
    }

    function setupExternalAudioSync(art, audioTracks, baseDirUrl) {
        if (!audioTracks || audioTracks.length === 0) return;
        if (lockstepController) {
            lockstepController.destroy();
            lockstepController = null;
        }
        const firstTrack = audioTracks[0];
        const firstUrl = `${baseDirUrl}/${firstTrack.file || `audio_${firstTrack.index}_${firstTrack.language}.m4a`}`;
        lockstepController = new LockstepAudioController(art, firstUrl);

        if (globalAudioBooster && globalAudioBooster.boostLevel > 1.0) {
            art.on('video:play', () => {
                applyAudioBoost(globalAudioBooster.boostLevel);
            }, { once: true });
        }
    }

    function switchArtPlayerAudio(trackIndex, url) {
        if (currentArtPlayer && currentArtPlayer.hls) {
            currentArtPlayer.hls.audioTrack = trackIndex;
        } else if (lockstepController) {
            lockstepController.switchTrack(url);
        }
    }

    function closeArtPlayer() {
        if (lockstepController) {
            lockstepController.destroy();
            lockstepController = null;
        }
        if (currentArtPlayer) {
            if (currentArtPlayer.hls) {
                try { currentArtPlayer.hls.destroy(); } catch (_) {}
            }
            try { currentArtPlayer.destroy(false); } catch (_) {}
            currentArtPlayer = null;
        }
        const container = document.getElementById('artplayer-container');
        if (container) container.innerHTML = '';
        const errorBox = document.getElementById('cinema-error');
        if (errorBox) {
            errorBox.style.display = 'none';
            errorBox.innerHTML = '';
        }
        if (cinemaLoadTimer) {
            clearTimeout(cinemaLoadTimer);
            cinemaLoadTimer = null;
        }
        cinemaRetryCtx = null;
    }

    function closeStreamDetailsModal() {
        const modal = document.getElementById('modal-stream-details');
        if (modal) {
            modal.classList.remove('active', 'open');
            closeArtPlayer();
        }
        global.setModalOpen(false);
    }

    function copyDirectStreamURL() {
        const input = document.getElementById('stream-direct-url');
        if (input) global.copyToClipboard(input.value, 'Direct Streaming URL copied!');
    }

    async function deleteMediaConfirm(mediaID) {
        if (!confirm(`Delete media asset "${mediaID}"?`)) return;
        try {
            await fetch(`/api/v1/files/${encodeURIComponent(mediaID)}`, { method: 'DELETE' });
            fetchMediaList(true);
        } catch (_) {}
    }

    async function retryMediaJob(key) {
        try {
            const res = await fetch(`/api/v1/files/${encodeURIComponent(key)}/retry`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' }
            });
            if (!res.ok) {
                const data = await res.json().catch(() => ({}));
                throw new Error(data.error || 'Retry request failed');
            }
            global.showToast('Job restarted successfully', 'success');
            fetchMediaList(true);
        } catch (err) {
            global.showToast('Retry error: ' + err.message, 'error');
        }
    }

    let activeLifecycleKey = null;
    let cachedDrawerTiers = [];
    let drawerPollTimer = null;

    function stopDrawerPolling() {
        if (drawerPollTimer) {
            clearTimeout(drawerPollTimer);
            drawerPollTimer = null;
        }
    }

    function getDrawerTierLabel(tierId, fallback) {
        if (tierId === -1) return 'Trash Sink';
        const t = cachedDrawerTiers.find(x => x.id === tierId);
        if (t && t.name) return t.name;
        return fallback || ('Tier ' + tierId);
    }

    async function cancelDrawerRelocationTask(taskId) {
        if (!confirm('Cancel this lifecycle relocation task?')) return;
        try {
            const res = await fetch(`/api/lifecycle/tasks/${encodeURIComponent(taskId)}/cancel`, {
                method: 'POST'
            });
            if (!res.ok) {
                const errData = await res.json().catch(() => ({}));
                throw new Error(errData.error || 'HTTP ' + res.status);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Relocation task cancelled');
            }
            if (activeLifecycleKey) {
                openFileLifecycleDrawer(activeLifecycleKey);
            }
        } catch (err) {
            console.error('Failed to cancel task:', err);
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Failed to cancel task: ' + err.message, 'error');
            }
        }
    }
    global.cancelDrawerRelocationTask = cancelDrawerRelocationTask;

    function renderDrawerTransitionCard(data) {
        const card = document.getElementById('drawer-transition-card');
        const labelEl = document.getElementById('drawer-transition-label');
        const badgeEl = document.getElementById('drawer-transition-status-badge');
        const bodyEl = document.getElementById('drawer-transition-body');
        if (!card || !bodyEl) return;

        const task = data.active_task;
        const next = data.next || {};
        const isActiveTask = task && !['done', 'failed', 'cancelled'].includes(task.state);

        // Disable manual relocate controls if an active task is running
        const moveSelect = document.getElementById('drawer-move-tier-select');
        const moveBtn = document.getElementById('btn-drawer-move');
        const moveNotice = document.getElementById('drawer-move-busy-notice');
        if (moveSelect) moveSelect.disabled = !!isActiveTask;
        if (moveBtn) moveBtn.disabled = !!isActiveTask;
        if (moveNotice) moveNotice.style.display = isActiveTask ? 'block' : 'none';

        if (isActiveTask) {
            card.classList.add('is-active-task');
            if (labelEl) labelEl.textContent = 'Active Relocation';
            if (badgeEl) {
                badgeEl.innerHTML = `<span class="drawer-pulse-pill is-busy"><span class="drawer-pulse-dot"></span> ${(task.state || 'active').toUpperCase()}</span>`;
            }

            const fromTier = getDrawerTierLabel(task.from ? task.from.tier_id : undefined, task.from ? task.from.tier_label : 'Source');
            const toTier = getDrawerTierLabel(task.to ? task.to.tier_id : undefined, task.to ? task.to.tier_label : 'Target');
            const kindLabel = (task.kind || 'manual').toUpperCase();

            let progressHtml = '';
            if (task.progress !== undefined && task.progress > 0) {
                progressHtml = `
                    <div style="margin-top:8px;">
                        <div style="height:6px;border-radius:9999px;background:var(--bg-subtle);overflow:hidden;">
                            <span style="display:block;height:100%;background:var(--orange-primary);width:${Math.min(100, Math.max(4, task.progress))}%;"></span>
                        </div>
                        <div style="display:flex;justify-content:space-between;align-items:center;font-size:11px;color:var(--text-muted);margin-top:4px;">
                            <span class="tabular-num">${task.progress.toFixed(0)}% completed</span>
                            <span class="tabular-num">${task.speed || 'Transferring'}</span>
                        </div>
                    </div>
                `;
            }

            let errorHtml = '';
            if (task.error) {
                errorHtml = `
                    <div class="drawer-transition-error">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" style="flex:none;margin-top:2px;"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>
                        <span><strong>Retry ${task.attempts || 1}:</strong> ${global.App ? global.App.escapeHtml(task.error) : task.error}</span>
                    </div>
                `;
            }

            bodyEl.innerHTML = `
                <div class="drawer-route-flow">
                    <div class="drawer-route-node">
                        <span class="drawer-route-tier" title="${global.App ? global.App.escapeHtml(fromTier) : fromTier}">${global.App ? global.App.escapeHtml(fromTier) : fromTier}</span>
                        <span class="drawer-route-sub">${global.App ? global.App.escapeHtml((task.from && task.from.node_id) || '-') : '-'}</span>
                    </div>
                    <div class="drawer-route-arrow">
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                            <line x1="5" y1="12" x2="19" y2="12"></line>
                            <polyline points="12 5 19 12 12 19"></polyline>
                        </svg>
                        <span class="drawer-route-kind">${kindLabel}</span>
                    </div>
                    <div class="drawer-route-node">
                        <span class="drawer-route-tier is-target" title="${global.App ? global.App.escapeHtml(toTier) : toTier}">${global.App ? global.App.escapeHtml(toTier) : toTier}</span>
                        <span class="drawer-route-sub">${global.App ? global.App.escapeHtml((task.to && task.to.node_id) || '-') : '-'}</span>
                    </div>
                </div>
                <div class="drawer-transition-reason">
                    <strong>Reason:</strong> ${global.App ? global.App.escapeHtml(task.reason || 'Relocation in progress') : task.reason}
                </div>
                ${errorHtml}
                ${progressHtml}
                <div style="display:flex;justify-content:space-between;align-items:center;margin-top:4px;padding-top:8px;border-top:1px solid var(--border-light);">
                    <span class="mono" style="font-size:10.5px;color:var(--text-muted);">${task.id || ''}</span>
                    <button type="button" class="btn btn-secondary" onclick="cancelDrawerRelocationTask('${task.id}')" style="padding:4px 10px;font-size:11.5px;color:var(--status-offline);border-color:rgba(220,38,38,0.25);background:var(--bg-surface);">
                        <span>Cancel Task</span>
                    </button>
                </div>
            `;
            return;
        }

        const act = (next.action || 'none').toLowerCase();
        if (act !== 'none') {
            card.classList.add('is-planned');
            if (labelEl) labelEl.textContent = 'Next Lifecycle Transition';
            if (badgeEl) {
                badgeEl.innerHTML = `<span class="drawer-pulse-pill is-muted">Policy Trigger</span>`;
            }

            const kindClass = act === 'promote' ? 'orange' : (act === 'expire' ? 'red' : 'neutral');
            const edgeDesc = next.edge_id ? `<span class="mono" style="font-size:11px;color:var(--text-muted);">Rule: ${next.edge_id}</span>` : '';
            const relEta = next.eta ? formatRelativeFuture(next.eta) : 'Evaluating';
            const cleanedTrigger = cleanReasonText(next.reason || 'Policy rule conditions matched');

            bodyEl.innerHTML = `
                <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;">
                    <div style="display:flex;align-items:center;gap:6px;">
                        <span class="badge-tier ${kindClass}">${act.toUpperCase()}</span>
                        ${edgeDesc}
                    </div>
                    <span class="mono" style="font-size:11px;color:var(--text-secondary);" title="${next.eta || ''}">${relEta}</span>
                </div>
                <div class="drawer-transition-reason" style="margin-top:6px;">
                    <strong>Trigger:</strong> ${global.App ? global.App.escapeHtml(cleanedTrigger) : cleanedTrigger}
                </div>
            `;
            return;
        }

        // Steady State
        card.classList.add('is-steady');
        if (labelEl) labelEl.textContent = 'Lifecycle Policy Status';
        if (badgeEl) {
            badgeEl.innerHTML = `<span class="drawer-pulse-pill is-ok">✓ Steady</span>`;
        }

        bodyEl.innerHTML = `
            <div class="drawer-steady-banner">
                <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="var(--status-online)" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" style="flex:none;margin-top:2px;">
                    <circle cx="12" cy="12" r="10"></circle>
                    <polyline points="9 12 12 15 16 10"></polyline>
                </svg>
                <div>
                    <div style="font-size:12.5px;font-weight:700;color:var(--text-primary);">Satisfies Lifecycle Policy</div>
                    <div style="font-size:11.5px;color:var(--text-secondary);margin-top:2px;">${global.App ? global.App.escapeHtml(next.reason || 'Current placement satisfies all tier rules and watermark thresholds.') : next.reason}</div>
                </div>
            </div>
        `;
    }

    async function openFileLifecycleDrawer(key) {
        activeLifecycleKey = key;
        const modal = document.getElementById('modal-file-lifecycle') || document.getElementById('drawer-file-lifecycle');
        if (!modal) return;

        modal.classList.add('open', 'active');
        modal.setAttribute('aria-hidden', 'false');
        if (global.App && typeof global.App.setModalOpen === 'function') {
            global.App.setModalOpen(true);
        }

        const titleEl = document.getElementById('drawer-lifecycle-filename');
        const keyEl = document.getElementById('drawer-lifecycle-key');
        if (titleEl) titleEl.textContent = key;
        if (keyEl) keyEl.textContent = 'Key: ' + key;

        const setT = (id, val) => { const el = document.getElementById(id); if (el) el.textContent = val; };
        setT('drawer-prop-tier', 'Loading...');
        setT('drawer-prop-node', 'Loading...');
        setT('drawer-prop-path', 'Loading...');
        setT('drawer-prop-score', '-');

        const transCard = document.getElementById('drawer-transition-card');
        if (transCard) transCard.className = 'drawer-card drawer-transition-card';
        const transLabel = document.getElementById('drawer-transition-label');
        if (transLabel) transLabel.textContent = 'Next Lifecycle Transition';
        const transBadge = document.getElementById('drawer-transition-status-badge');
        if (transBadge) transBadge.innerHTML = '';
        const transBody = document.getElementById('drawer-transition-body');
        if (transBody) transBody.innerHTML = `<div style="font-size:12px;color:var(--text-muted);">Evaluating placement...</div>`;

        const pinBadge = document.getElementById('drawer-prop-pin-badge');
        if (pinBadge) pinBadge.innerHTML = '';
        const pinDetails = document.getElementById('drawer-prop-pin-details');
        if (pinDetails) pinDetails.textContent = '';

        populateDrawerTierSelect();

        try {
            const res = await fetch(`/api/v1/files/${encodeURIComponent(key)}/placement`);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();

            const item = mediaList.find(m => m.key === key);
            if (item && item.filename && titleEl) {
                titleEl.textContent = item.filename;
            }

            const pl = data.placement || {};
            setT('drawer-prop-tier', pl.tier_label || (pl.tier_id !== undefined ? 'Tier ' + pl.tier_id : '-'));
            setT('drawer-prop-node', pl.node_id || '-');
            setT('drawer-prop-path', pl.path || '-');

            const st = data.state || {};
            const score = Number(st.score) || 0;
            setT('drawer-prop-score', score.toFixed(2));

            const isPinned = st.pinned_tier !== undefined && st.pinned_tier !== null && st.pinned_tier >= 0;
            if (pinBadge) {
                if (isPinned) {
                    pinBadge.innerHTML = `<span class="badge-tier orange" style="font-size:11px;font-weight:700;">PINNED TO TIER ${st.pinned_tier}</span>`;
                } else {
                    pinBadge.innerHTML = `<span class="badge-tier neutral" style="font-size:11px;">AUTO DYNAMIC</span>`;
                }
            }
            if (pinDetails) {
                if (isPinned) {
                    const untilRel = formatRelativeFuture(st.pin_until);
                    const exactDate = st.pin_until && !st.pin_until.startsWith('0001-01-01') ? new Date(st.pin_until).toLocaleString() : '';
                    pinDetails.textContent = `Pinned until ${untilRel}${exactDate ? ` (${exactDate})` : ''}. Idle and watermark demotions are suppressed.`;
                } else {
                    const lastHitRel = formatRelativeTime(st.last_hit_at);
                    const exactHit = st.last_hit_at && !st.last_hit_at.startsWith('0001-01-01') ? new Date(st.last_hit_at).toLocaleString() : '';
                    pinDetails.textContent = `Last play hit recorded: ${lastHitRel}${exactHit ? ` (${exactHit})` : ''}`;
                }
            }

            // Render modern transition & active task card
            renderDrawerTransitionCard(data);

            // Auto-refresh active relocation tasks while drawer is open
            stopDrawerPolling();
            if (activeLifecycleKey && data.active_task && !['done', 'failed', 'cancelled'].includes(data.active_task.state)) {
                drawerPollTimer = setTimeout(() => {
                    if (activeLifecycleKey) openFileLifecycleDrawer(activeLifecycleKey);
                }, 2500);
            }

            const histEl = document.getElementById('drawer-history-list');
            if (histEl) {
                const history = st.history || [];
                if (history.length === 0) {
                    histEl.innerHTML = `<div class="ml-empty-state">No recorded relocations.</div>`;
                } else {
                    histEl.innerHTML = history.slice().reverse().map(m => {
                        const relTime = m.moved_at ? formatRelativeTime(m.moved_at) : '—';
                        const exactTime = m.moved_at ? new Date(m.moved_at).toLocaleString() : '';
                        const cleanedReason = cleanReasonText(m.reason || '');
                        return `
                            <div class="ml-history-item">
                                <div class="ml-hist-top">
                                    <span class="badge-tier ${m.kind === 'promote' ? 'orange' : (m.kind === 'expire' ? 'red' : 'neutral')}">${(m.kind || 'move').toUpperCase()}</span>
                                    <span class="tabular-num mono ml-hist-time" title="${exactTime}">${relTime}</span>
                                </div>
                                <div class="ml-hist-route">Tier ${m.from} &rarr; ${m.to === -1 ? 'Trash Sink' : 'Tier ' + m.to}</div>
                                <div class="ml-hist-reason">${global.App ? global.App.escapeHtml(cleanedReason) : cleanedReason}</div>
                            </div>
                        `;
                    }).join('');
                }
            }
        } catch (err) {
            console.error('Failed to load file placement view:', err);
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Failed to load placement: ' + err.message, 'error');
            }
        }
    }

    async function populateDrawerTierSelect() {
        const sel = document.getElementById('drawer-move-tier-select');
        if (!sel) return;
        try {
            const res = await fetch('/api/flows');
            if (res.ok) {
                const data = await res.json();
                cachedDrawerTiers = data.tiers || [];
                sel.innerHTML = cachedDrawerTiers.map(t => `<option value="${t.id}">${global.App ? global.App.escapeHtml(t.name) : t.name} (Tier ${t.id})</option>`).join('');
            }
        } catch (_) {}
    }

    function closeFileLifecycleDrawer() {
        stopDrawerPolling();
        activeLifecycleKey = null;
        const modal = document.getElementById('modal-file-lifecycle') || document.getElementById('drawer-file-lifecycle');
        const backdrop = document.getElementById('drawer-file-lifecycle-backdrop');
        if (backdrop) backdrop.classList.remove('open', 'active');
        if (modal) {
            modal.classList.remove('open', 'active');
            modal.setAttribute('aria-hidden', 'true');
        }
        if (global.App && typeof global.App.setModalOpen === 'function') {
            global.App.setModalOpen(false);
        }
    }

    async function submitDrawerMove() {
        if (!activeLifecycleKey) return;
        const sel = document.getElementById('drawer-move-tier-select');
        if (!sel) return;
        const tierId = parseInt(sel.value, 10);
        if (isNaN(tierId)) return;

        try {
            const res = await fetch(`/api/v1/files/${encodeURIComponent(activeLifecycleKey)}/move`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ tier_id: tierId })
            });
            if (!res.ok) {
                const errData = await res.json().catch(() => ({}));
                throw new Error(errData.error || 'HTTP ' + res.status);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Relocation task queued');
            }
            openFileLifecycleDrawer(activeLifecycleKey);
        } catch (err) {
            console.error('Failed to queue move:', err);
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Failed to move: ' + err.message, 'error');
            }
        }
    }

    async function setDrawerDemand(level) {
        if (!activeLifecycleKey) return;
        try {
            const res = await fetch(`/api/v1/files/${encodeURIComponent(activeLifecycleKey)}/demand`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ level: level, ttl_hours: 24 })
            });
            if (!res.ok) {
                const errData = await res.json().catch(() => ({}));
                throw new Error(errData.error || 'HTTP ' + res.status);
            }
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast(level === 'high' ? 'High demand pinned' : 'File unpinned');
            }
            openFileLifecycleDrawer(activeLifecycleKey);
        } catch (err) {
            console.error('Failed to set demand:', err);
            if (global.App && typeof global.App.showToast === 'function') {
                global.App.showToast('Failed to set demand: ' + err.message, 'error');
            }
        }
    }

    function init() {
        destroy();
        if (mediaList && mediaList.length > 0) {
            renderMediaTable();
        }
        fetchMediaList(true).then(() => {
            scheduleNextMediaPoll();
        });
    }

    function destroy() {
        if (mediaPollInterval) {
            clearTimeout(mediaPollInterval);
            mediaPollInterval = null;
        }
        closeArtPlayer();
        closeFileLifecycleDrawer();
    }

    global.fetchMediaList = fetchMediaList;
    global.scheduleNextMediaPoll = scheduleNextMediaPoll;
    global.setMediaFilter = setMediaFilter;
    global.onMediaNodeFilterChange = onMediaNodeFilterChange;
    global.onMediaTierFilterChange = onMediaTierFilterChange;
    global.toggleSelectAllMedia = toggleSelectAllMedia;
    global.onMediaRowCheckChange = onMediaRowCheckChange;
    global.deleteSelectedMedia = deleteSelectedMedia;
    global.onMediaSearchChange = onMediaSearchChange;
    global.onMediaPageSizeChange = onMediaPageSizeChange;
    global.renderPipelineStepper = renderPipelineStepper;
    global.renderMediaSpecs = renderMediaSpecs;
    global.renderMediaTable = renderMediaTable;
    global.renderPaginationButtons = renderPaginationButtons;
    global.goToMediaPage = goToMediaPage;
    global.openIngestURLModal = openIngestURLModal;
    global.closeIngestURLModal = closeIngestURLModal;
    global.submitIngestURL = submitIngestURL;
    global.openUploadModal = openUploadModal;
    global.closeUploadModal = closeUploadModal;
    global.onFileSelected = onFileSelected;
    global.startFileUpload = startFileUpload;
    global.triggerStorageSync = triggerStorageSync;
    global.openStreamDetailsModal = openStreamDetailsModal;
    global.renderMediaModalDrawers = renderMediaModalDrawers;
    global.cinemaReady = cinemaReady;
    global.cinemaFail = cinemaFail;
    global.retryArtPlayer = retryArtPlayer;
    global.initArtPlayer = initArtPlayer;
    global.switchArtPlayerAudio = switchArtPlayerAudio;
    global.closeArtPlayer = closeArtPlayer;
    global.closeStreamDetailsModal = closeStreamDetailsModal;
    global.copyDirectStreamURL = copyDirectStreamURL;
    global.deleteMediaConfirm = deleteMediaConfirm;
    global.retryMediaJob = retryMediaJob;
    global.openFileLifecycleDrawer = openFileLifecycleDrawer;
    global.closeFileLifecycleDrawer = closeFileLifecycleDrawer;
    global.submitDrawerMove = submitDrawerMove;
    global.setDrawerDemand = setDrawerDemand;

    if (global.App && global.App.pages) {
        global.App.pages.media = { init, destroy };
    }

    // Instant Hydration: if mediaList was restored from cache, render table immediately
    if (mediaList && mediaList.length > 0) {
        if (document.readyState === 'loading') {
            document.addEventListener('DOMContentLoaded', () => {
                if (global.currentView === 'media') renderMediaTable();
            });
        } else if (global.currentView === 'media') {
            renderMediaTable();
        }
    }
})(window);
