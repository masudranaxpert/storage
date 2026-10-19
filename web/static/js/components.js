// Reusable UI component primitives modeled after BoardUI, Kobra Systems, and Bencho.
// Provides a deep module interface (window.UI) that encapsulates double-surface trays,
// multi-segment SVG score rings, stage bar cards, semantic badges, and OTP slot groups.
(function (global) {
    'use strict';

    function esc(str) {
        if (str === null || str === undefined) return '';
        return String(str).replace(/[&<>"']/g, function (m) {
            return {
                '&': '&amp;',
                '<': '&lt;',
                '>': '&gt;',
                '"': '&quot;',
                "'": '&#39;'
            }[m];
        });
    }

    // Renders a Kobra / BoardUI semantic badge with optional dot, 4px square swatch, or icon.
    function badge(opts) {
        if (!opts) return '';
        const text = opts.text || '';
        const variant = opts.variant || 'neutral'; // emerald, amber, red, blue, violet, orange, neutral, dark
        const pill = opts.pill !== false; // default rounded-full
        const mono = !!opts.mono;
        const dot = !!opts.dot;
        const swatch = opts.swatch || ''; // CSS color for 4px rounded square
        const icon = opts.icon || '';
        const title = opts.title ? ` title="${esc(opts.title)}"` : '';
        const idAttr = opts.id ? ` id="${esc(opts.id)}"` : '';
        const extraClass = opts.className ? ` ${esc(opts.className)}` : '';

        const shapeClass = pill ? 'ui-badge-pill' : 'ui-badge-rect';
        const monoClass = mono ? ' ui-badge-mono' : '';

        let indicatorHtml = '';
        if (swatch) {
            indicatorHtml = `<span class="ui-badge-swatch" style="background:${esc(swatch)};"></span>`;
        } else if (dot) {
            indicatorHtml = `<span class="ui-badge-dot"></span>`;
        }

        return `<span data-slot="badge" class="ui-badge ui-badge-${esc(variant)} ${shapeClass}${monoClass}${extraClass}"${idAttr}${title}>` +
            indicatorHtml +
            (icon ? `<span class="ui-badge-icon">${icon}</span>` : '') +
            `<span>${esc(text)}</span>` +
            `</span>`;
    }

    // Renders a divided metric row inside a BoardUI inner white card.
    // Includes a 12x12px 4px-radius color swatch square on the left and tabular value on the right.
    function metricRow(opts) {
        const label = opts.label || '';
        const value = opts.value || '';
        const swatch = opts.swatch || '';
        const sub = opts.sub || '';
        const idVal = opts.valueId ? ` id="${esc(opts.valueId)}"` : '';
        const badgeHtml = opts.badgeHtml || '';

        return `<div class="bui-metric-row">` +
            `<div class="bui-metric-left">` +
            (swatch ? `<span class="bui-swatch-sq" style="background:${esc(swatch)};"></span>` : '') +
            `<span class="bui-metric-label">${esc(label)}</span>` +
            (sub ? `<span class="bui-metric-sub">${esc(sub)}</span>` : '') +
            `</div>` +
            `<div class="bui-metric-right">` +
            badgeHtml +
            `<span class="bui-metric-val tabular-num"${idVal}>${esc(value)}</span>` +
            `</div>` +
            `</div>`;
    }

    // Computes multi-segment SVG circle stroke-dasharray & stroke-dashoffset for BoardUI Sleep Score Card.
    function renderSegmentedRingSVG(segments, centerScore, centerLabel, ringIdPrefix) {
        const radius = 52;
        const circumference = 2 * Math.PI * radius; // ~326.73
        const validSegments = (segments || []).filter(s => (s.pct || 0) > 0);
        const gapDeg = validSegments.length > 1 ? 5 : 0;
        const gapLen = (gapDeg / 360) * circumference;

        let offset = 0;
        const circlesHtml = validSegments.map((seg, idx) => {
            const rawLen = (Math.max(0, Math.min(100, seg.pct)) / 100) * circumference;
            const segLen = Math.max(2, rawLen - gapLen);
            const dashArray = `${segLen.toFixed(2)} ${(circumference - segLen).toFixed(2)}`;
            const dashOffset = (-offset).toFixed(2);
            offset += rawLen;
            const idAttr = ringIdPrefix ? ` id="${esc(ringIdPrefix)}-seg-${idx}"` : '';
            return `<circle class="bui-score-seg"${idAttr} cx="64" cy="64" r="${radius}" fill="none" stroke="${esc(seg.color || 'var(--chart-1)')}" stroke-width="11" stroke-linecap="round" stroke-dasharray="${dashArray}" stroke-dashoffset="${dashOffset}"></circle>`;
        }).join('');

        const scoreId = ringIdPrefix ? ` id="${esc(ringIdPrefix)}-score"` : '';
        const capId = ringIdPrefix ? ` id="${esc(ringIdPrefix)}-cap"` : '';

        return `<div class="bui-score-ring-wrap">` +
            `<svg class="bui-score-ring-svg" viewBox="0 0 128 128" aria-hidden="true">` +
            `<circle class="bui-score-track" cx="64" cy="64" r="${radius}" fill="none" stroke="var(--chart-track)" stroke-width="11"></circle>` +
            `<g transform="rotate(-90 64 64)">${circlesHtml}</g>` +
            `</svg>` +
            `<div class="bui-score-ring-center">` +
            `<span class="bui-score-numeral tabular-num"${scoreId}>${esc(centerScore)}</span>` +
            (centerLabel ? `<span class="bui-score-caption"${capId}>${esc(centerLabel)}</span>` : '') +
            `</div>` +
            `</div>`;
    }

    // Renders a BoardUI Sleep Score Card:
    // Outer #f7f7f7 20px tray + header with white chip + segmented ring gauge + inner #ffffff 12px divided metric card.
    function scoreCard(opts) {
        const title = opts.title || '';
        const status = opts.status || '';
        const statusId = opts.statusId ? ` id="${esc(opts.statusId)}"` : '';
        const chipText = opts.chipText || '';
        const chipId = opts.chipId ? ` id="${esc(opts.chipId)}"` : '';
        const chipIcon = opts.chipIcon || '';
        const ringHtml = renderSegmentedRingSVG(opts.segments || [], opts.score || '0%', opts.unit || 'Used', opts.ringIdPrefix);
        const rowsHtml = (opts.metrics || []).map(metricRow).join('');
        const cardId = opts.id ? ` id="${esc(opts.id)}"` : '';

        return `<section class="bui-tray-card bui-score-card"${cardId}>` +
            `<div class="bui-tray-head">` +
            `<div class="bui-tray-titles">` +
            `<span class="bui-tray-eyebrow">${esc(title)}</span>` +
            `<span class="bui-tray-headline tabular-num"${statusId}>${esc(status)}</span>` +
            `</div>` +
            (chipText ? `<div class="bui-head-chip"${chipId}>${chipIcon}<span>${esc(chipText)}</span></div>` : '') +
            `</div>` +
            `<div class="bui-score-visual">${ringHtml}</div>` +
            `<div class="bui-inner-card bui-divided-list">${rowsHtml}</div>` +
            `</section>`;
    }

    // Renders a BoardUI Stat Card:
    // Outer #f7f7f7 20px tray + 40x40 gradient squircle icon + Display 4 numeral + inner #ffffff 12px footer bar.
    function statCard(opts) {
        const label = opts.label || '';
        const value = opts.value || '';
        const valueId = opts.valueId ? ` id="${esc(opts.valueId)}"` : '';
        const subId = opts.subId ? ` id="${esc(opts.subId)}"` : '';
        const gradient = opts.gradient || 'orange'; // orange, blue, emerald, violet
        const icon = opts.icon || '';
        const footerLabel = opts.footerLabel || '';
        const footerBadge = opts.footerBadge ? badge(opts.footerBadge) : '';
        const actionsHtml = opts.actionsHtml || '';

        return `<section class="bui-tray-card bui-stat-card">` +
            `<div class="bui-stat-top">` +
            `<div class="bui-stat-meta">` +
            `<span class="bui-tray-eyebrow">${esc(label)}</span>` +
            `<div class="bui-stat-figure tabular-num"${valueId}>${esc(value)}</div>` +
            `</div>` +
            (icon ? `<div class="bui-squircle-icon grad-${esc(gradient)}">${icon}</div>` : '') +
            `</div>` +
            `<div class="bui-inner-card bui-stat-footer">` +
            (actionsHtml ? actionsHtml : (
                `<span class="bui-stat-footer-text"${subId}>${esc(footerLabel)}</span>` +
                footerBadge
            )) +
            `</div>` +
            `</section>`;
    }

    // Renders a BoardUI Stage Bars Card:
    // Outer #f7f7f7 20px tray + header + inner #ffffff 12px card with 20px rounded-full horizontal pill bars.
    function stageBarsCard(opts) {
        const title = opts.title || '';
        const headline = opts.headline || '';
        const headlineId = opts.headlineId ? ` id="${esc(opts.headlineId)}"` : '';
        const chipText = opts.chipText || '';
        const chipId = opts.chipId ? ` id="${esc(opts.chipId)}"` : '';
        const rows = opts.rows || [];

        const barsHtml = rows.map(r => {
            const pct = Math.max(0, Math.min(100, Number(r.pct) || 0));
            const barId = r.barId ? ` id="${esc(r.barId)}"` : '';
            const valId = r.valId ? ` id="${esc(r.valId)}"` : '';
            const subId = r.subId ? ` id="${esc(r.subId)}"` : '';
            return `<div class="bui-stage-item">` +
                `<div class="bui-stage-meta">` +
                `<div class="bui-stage-title-group">` +
                `<span class="bui-swatch-sq" style="background:${esc(r.color || 'var(--chart-1)')};"></span>` +
                `<span class="bui-stage-name">${esc(r.label)}</span>` +
                (r.tag ? badge({ text: r.tag, variant: r.tagVariant || 'neutral', pill: false, mono: true }) : '') +
                `</div>` +
                `<div class="bui-stage-Right">` +
                `<strong class="bui-stage-val tabular-num"${valId}>${esc(r.value)}</strong>` +
                (r.sub ? `<span class="bui-stage-sub tabular-num"${subId}>${esc(r.sub)}</span>` : '') +
                `</div>` +
                `</div>` +
                `<div class="bui-stage-pill-track">` +
                `<div class="bui-stage-pill-fill"${barId} style="width:${pct}%;background:${esc(r.color || 'var(--chart-1)')};"></div>` +
                `</div>` +
                `</div>`;
        }).join('');

        return `<section class="bui-tray-card bui-stage-card">` +
            `<div class="bui-tray-head">` +
            `<div class="bui-tray-titles">` +
            `<span class="bui-tray-eyebrow">${esc(title)}</span>` +
            `<span class="bui-tray-headline tabular-num"${headlineId}>${esc(headline)}</span>` +
            `</div>` +
            (chipText ? `<div class="bui-head-chip"${chipId}><span>${esc(chipText)}</span></div>` : '') +
            `</div>` +
            `<div class="bui-inner-card bui-stage-list">${barsHtml}</div>` +
            `</section>`;
    }

    // Renders Kobra Systems input-otp style segmented character slots for tokens/secrets.
    function otpSlots(text, masked) {
        const clean = String(text || 'cluster-secret-2026');
        const groups = clean.split('-');
        return `<div data-slot="input-otp" class="kobra-otp-root">` +
            groups.map((grp, gIdx) => {
                const chars = grp.split('').slice(0, 8);
                const slots = chars.map((ch, idx) => {
                    const displayChar = masked ? '•' : ch;
                    return `<div data-slot="input-otp-slot" class="kobra-otp-slot tabular-num">${esc(displayChar)}</div>`;
                }).join('');
                const sep = gIdx < groups.length - 1
                    ? `<div data-slot="input-otp-separator" class="kobra-otp-sep">-</div>`
                    : '';
                return `<div data-slot="input-otp-group" class="kobra-otp-group">${slots}</div>${sep}`;
            }).join('') +
            `</div>`;
    }

    // Formats and syntax-highlights JSON text or JS objects with semantic color tokens.
    function highlightJSON(val, format) {
        let str = '';
        if (typeof val === 'string') {
            if (format !== false) {
                try {
                    const parsed = JSON.parse(val);
                    str = JSON.stringify(parsed, null, 2);
                } catch (_) {
                    str = val;
                }
            } else {
                str = val;
            }
        } else if (val !== null && val !== undefined) {
            str = JSON.stringify(val, null, 2);
        } else {
            str = 'null';
        }

        const re = /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?|[{}[\],:])/g;
        let last = 0;
        let html = '';
        let match;
        while ((match = re.exec(str)) !== null) {
            if (match.index > last) {
                html += esc(str.slice(last, match.index));
            }
            last = re.lastIndex;
            const tok = match[0];
            let cls = 'json-number';
            if (tok.charCodeAt(0) === 34 /* '"' */) {
                cls = tok.trim().endsWith(':') ? 'json-key' : 'json-string';
            } else if (tok === 'true' || tok === 'false') {
                cls = 'json-boolean';
            } else if (tok === 'null') {
                cls = 'json-null';
            } else if (/^[{}[\],:]$/.test(tok)) {
                cls = 'json-punct';
            }
            html += `<span class="${cls}">${esc(tok)}</span>`;
        }
        if (last < str.length) {
            html += esc(str.slice(last));
        }
        return html;
    }

    // Lightweight live syntax-highlighted code editor with scroll sync, tab indent, and token coloring.
    function codeEditor(target, opts) {
        const textarea = typeof target === 'string' ? document.getElementById(target) : target;
        if (!textarea) return null;
        if (textarea._codeEditor) return textarea._codeEditor;

        const o = opts || {};
        let wrap = o.wrap ? (typeof o.wrap === 'string' ? document.getElementById(o.wrap) : o.wrap) : null;
        let pre = o.pre ? (typeof o.pre === 'string' ? document.getElementById(o.pre) : o.pre) : null;
        let code = o.code ? (typeof o.code === 'string' ? document.getElementById(o.code) : o.code) : null;

        if (!wrap) {
            wrap = document.createElement('div');
            wrap.className = 'bui-code-editor-wrap' + (o.className ? ' ' + o.className : '');
            if (o.height) wrap.style.height = o.height;
            textarea.parentNode.insertBefore(wrap, textarea);

            pre = document.createElement('pre');
            pre.className = 'bui-code-editor-pre';
            pre.setAttribute('aria-hidden', 'true');

            code = document.createElement('code');
            code.className = 'bui-code-editor-code';
            pre.appendChild(code);

            wrap.appendChild(pre);
            wrap.appendChild(textarea);
        }

        textarea.classList.add('bui-code-editor-textarea');
        textarea.setAttribute('spellcheck', 'false');
        textarea.setAttribute('autocomplete', 'off');
        textarea.setAttribute('autocapitalize', 'off');
        textarea.setAttribute('wrap', 'off');

        function update() {
            const val = textarea.value || '';
            const html = highlightJSON(val, false);
            if (code) code.innerHTML = html + (val.endsWith('\n') ? ' ' : '');
            syncScroll();
            if (typeof o.onInput === 'function') o.onInput(val);
        }

        function syncScroll() {
            if (pre) {
                pre.scrollTop = textarea.scrollTop;
                pre.scrollLeft = textarea.scrollLeft;
            }
        }

        textarea.addEventListener('input', update);
        textarea.addEventListener('scroll', syncScroll);

        textarea.addEventListener('keydown', function (e) {
            if (e.key === 'Tab') {
                e.preventDefault();
                const start = textarea.selectionStart;
                const end = textarea.selectionEnd;
                const val = textarea.value;
                textarea.value = val.substring(0, start) + '  ' + val.substring(end);
                textarea.selectionStart = textarea.selectionEnd = start + 2;
                update();
            }
        });

        const instance = {
            wrap: wrap,
            pre: pre,
            code: code,
            textarea: textarea,
            setValue: function (val) {
                textarea.value = val;
                update();
            },
            getValue: function () {
                return textarea.value;
            },
            refresh: function () {
                update();
            },
            focus: function () {
                textarea.focus();
            }
        };

        textarea._codeEditor = instance;
        update();
        return instance;
    }

    // Renders an accessible, responsive, interactive pagination component.
    function pagination(container, opts) {
        const el = typeof container === 'string' ? document.getElementById(container) : container;
        if (!el) return;
        const o = opts || {};
        const current = Math.max(1, parseInt(o.currentPage, 10) || 1);
        const total = Math.max(1, parseInt(o.totalPages, 10) || 1);
        const onPage = typeof o.onPageChange === 'function' ? o.onPageChange : function () {};

        if (total <= 1 && !o.alwaysShow) {
            el.innerHTML = '';
            return;
        }

        // Build windowed page items
        const items = [];
        if (total <= 7) {
            for (let i = 1; i <= total; i++) items.push(i);
        } else {
            if (current <= 4) {
                items.push(1, 2, 3, 4, 5, '…', total);
            } else if (current >= total - 3) {
                items.push(1, '…', total - 4, total - 3, total - 2, total - 1, total);
            } else {
                items.push(1, '…', current - 1, current, current + 1, '…', total);
            }
        }

        let html = '<nav class="bui-pagination" aria-label="Pagination">';

        // Prev button
        const prevDisabled = current <= 1;
        html += `<button type="button" class="page-btn page-btn-prev" ${prevDisabled ? 'disabled aria-disabled="true"' : `data-page="${current - 1}"`} aria-label="Previous page">` +
            `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3" aria-hidden="true"><polyline points="15 18 9 12 15 6"></polyline></svg>` +
            `</button>`;

        // Page buttons & ellipsis
        items.forEach(item => {
            if (item === '…') {
                html += `<span class="page-btn-ellipsis" aria-hidden="true">…</span>`;
            } else {
                const isActive = item === current;
                html += `<button type="button" class="page-btn ${isActive ? 'active' : ''}" ${isActive ? 'aria-current="page"' : `data-page="${item}"`} aria-label="Page ${item}">${item}</button>`;
            }
        });

        // Next button
        const nextDisabled = current >= total;
        html += `<button type="button" class="page-btn page-btn-next" ${nextDisabled ? 'disabled aria-disabled="true"' : `data-page="${current + 1}"`} aria-label="Next page">` +
            `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3" aria-hidden="true"><polyline points="9 18 15 12 9 6"></polyline></svg>` +
            `</button>`;

        html += '</nav>';
        el.innerHTML = html;

        // Wire click handler via delegation
        el.querySelectorAll('button[data-page]').forEach(btn => {
            btn.onclick = function (e) {
                e.preventDefault();
                const p = parseInt(this.getAttribute('data-page'), 10);
                if (p && p !== current) {
                    onPage(p);
                }
            };
        });
    }

    // Renders a status pill for nodes or storage/processing rows.
    function statusPill(status, label, variant) {
        const s = String(status || 'offline').toLowerCase();
        const isOnline = s === 'online';
        const isStale = s === 'stale';
        const statusClass = isOnline ? 'online' : (isStale ? 'stale' : 'offline');
        const text = label || (isOnline ? 'Online' : (isStale ? 'Stale' : (status || 'Offline')));
        if (variant === 'proc') {
            return `<span class="proc-status-badge ${isOnline ? 'eligible' : 'not-eligible'}"><span class="badge-dot"></span><span>${esc(text)}</span></span>`;
        }
        return `<div class="node-status-pill ${esc(statusClass)}"><span class="status-indicator-dot ${esc(statusClass)}"></span><span>${esc(text)}</span></div>`;
    }

    // Renders a capacity/utilization progress bar with optional label and watermark ticks.
    function capacityBar(opts) {
        const o = opts || {};
        const pct = Math.min(100, Math.max(0, Number(o.percent) || 0));
        const color = o.color || (pct > 92 ? 'var(--status-offline)' : (pct > 80 ? 'var(--status-stale)' : 'var(--orange-primary)'));
        const height = o.height || '10px';
        const subtext = o.subtext !== undefined ? o.subtext : `${pct.toFixed(1)}% used`;
        const highTick = o.highWatermark ? `<span style="position:absolute;left:${Math.min(100, Math.max(0, Number(o.highWatermark)))}%;top:0;bottom:0;width:2px;background:var(--status-offline);opacity:0.75;" title="High watermark ${esc(o.highWatermark)}%"></span>` : '';
        const lowTick = o.lowWatermark ? `<span style="position:absolute;left:${Math.min(100, Math.max(0, Number(o.lowWatermark)))}%;top:0;bottom:0;width:2px;background:var(--status-online);opacity:0.75;" title="Low watermark ${esc(o.lowWatermark)}%"></span>` : '';
        return `<div style="min-width:${esc(o.minWidth || '120px')};">` +
            `<div class="bui-stage-pill-track" style="position:relative;height:${esc(height)};margin:0 0 5px 0;">` +
            `<div class="bui-stage-pill-fill" style="width:${pct}%;background:${esc(color)}"></div>` +
            highTick + lowTick +
            `</div>` +
            (subtext ? `<div class="tabular-num" style="font-size:11.5px;color:var(--text-secondary);font-weight:500;">${esc(subtext)}</div>` : '') +
            `</div>`;
    }

    // Renders a node drive row item with path, disk medium pill, free/total bytes, and progress bar.
    function driveBar(d, isPrimary, tierBadgeHtml, fmtFn) {
        const fmt = typeof fmtFn === 'function' ? fmtFn : (typeof global.formatBytes === 'function' ? global.formatBytes : (b => (b || 0) + ' B'));
        const pct = Math.min(100, Math.max(0, Number(d.used_percent) || 0));
        const fillColor = pct > 92 ? 'var(--status-offline)' : (pct > 80 ? 'var(--status-stale)' : 'var(--orange-primary)');
        const dtype = d.disk_type || 'SSD';
        const dtypeClass = dtype.toLowerCase().includes('nvme') ? 'nvme' : (dtype.toLowerCase().includes('hdd') ? 'hdd' : 'ssd');
        return `<div class="drive-item ${isPrimary ? 'primary-drive' : 'secondary-drive'}">` +
            `<div class="drive-row-top">` +
            `<div class="drive-path-wrap">` +
            `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="color:var(--text-muted);flex-shrink:0;">` +
            `<ellipse cx="12" cy="5" rx="9" ry="3"></ellipse>` +
            `<path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"></path>` +
            `<path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"></path>` +
            `</svg>` +
            `<span class="drive-path" title="${esc(d.path)}">${esc(d.path)}</span>` +
            `<span class="type-pill ${esc(dtypeClass)}">${esc(dtype)}</span>` +
            (tierBadgeHtml || '') +
            `</div>` +
            `<div class="drive-stats tabular-num">` +
            `<strong style="color:#047857;">${esc(fmt(d.free_bytes))} free</strong>` +
            `<span style="color:var(--text-muted);"> / ${esc(fmt(d.total_bytes))}</span>` +
            `</div>` +
            `</div>` +
            `<div class="bui-stage-pill-track" style="height:8px;margin-top:7px;">` +
            `<div class="bui-stage-pill-fill" style="width:${pct}%;background:${esc(fillColor)};"></div>` +
            `</div>` +
            `</div>`;
    }

    // Renders an empty state block or table row with escaped text.
    function emptyState(opts) {
        const o = opts || {};
        if (o.colspan) {
            const pad = o.padding || '24px';
            const color = o.color || 'var(--text-secondary)';
            return `<tr><td colspan="${Number(o.colspan)}" style="text-align:center;padding:${esc(pad)};color:${esc(color)};">${esc(o.message || o.title || 'No items found.')}</td></tr>`;
        }
        return `<div class="empty-state">` +
            (o.iconHtml || '') +
            (o.title ? `<h3>${esc(o.title)}</h3>` : '') +
            (o.message ? `<p>${esc(o.message)}</p>` : '') +
            `</div>`;
    }

    // Slices an array for pagination, updates the "Showing X to Y of Z entries" label, and renders pagination buttons.
    function tablePagination(opts) {
        const o = opts || {};
        const items = Array.isArray(o.items) ? o.items : [];
        const pageSize = Math.max(1, parseInt(o.pageSize, 10) || 10);
        const totalItems = items.length;
        const totalPages = Math.max(1, Math.ceil(totalItems / pageSize));
        const currentPage = Math.min(totalPages, Math.max(1, parseInt(o.currentPage, 10) || 1));
        const startIndex = (currentPage - 1) * pageSize;
        const endIndex = Math.min(startIndex + pageSize, totalItems);
        const pageItems = items.slice(startIndex, endIndex);

        const infoEl = typeof o.infoEl === 'string' ? document.getElementById(o.infoEl) : o.infoEl;
        if (infoEl) {
            infoEl.innerText = totalItems === 0
                ? 'Showing 0 to 0 of 0 entries'
                : `Showing ${startIndex + 1} to ${endIndex} of ${totalItems} entries`;
        }

        if (o.buttonsEl) {
            pagination(o.buttonsEl, {
                currentPage: currentPage,
                totalPages: totalPages,
                onPageChange: o.onPageChange
            });
        }

        return {
            pageItems: pageItems,
            currentPage: currentPage,
            totalPages: totalPages,
            totalItems: totalItems,
            startIndex: startIndex,
            endIndex: endIndex
        };
    }

    // Configures and opens the shared confirmation modal (#db-confirm-modal or #tier-action-modal fallback).
    function confirmModal(opts) {
        const o = opts || {};
        if (typeof global.openTierModal === 'function' && document.getElementById('tier-action-modal') && !o.requireInput) {
            global.openTierModal({
                mode: 'confirm',
                title: esc(o.title || 'Confirm Action'),
                subtitle: esc(o.subtitle || ''),
                description: o.htmlDescription || esc(o.description || 'Are you sure you want to proceed?'),
                submitLabel: esc(o.confirmText || 'Confirm'),
                danger: !!o.danger,
                iconTheme: o.danger ? 'theme-red' : 'theme-orange',
                onSubmit: o.onConfirm
            });
            return;
        }
        if (global.confirm(o.description || o.title || 'Are you sure?')) {
            if (typeof o.onConfirm === 'function') o.onConfirm();
        }
    }

    global.UI = {
        esc: esc,
        badge: badge,
        metricRow: metricRow,
        scoreCard: scoreCard,
        statCard: statCard,
        stageBarsCard: stageBarsCard,
        otpSlots: otpSlots,
        renderSegmentedRingSVG: renderSegmentedRingSVG,
        highlightJSON: highlightJSON,
        codeEditor: codeEditor,
        pagination: pagination,
        statusPill: statusPill,
        capacityBar: capacityBar,
        driveBar: driveBar,
        emptyState: emptyState,
        tablePagination: tablePagination,
        confirmModal: confirmModal
    };
})(window);
