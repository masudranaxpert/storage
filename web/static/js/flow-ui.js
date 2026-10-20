// Data Flow UI components (window.UI.flow): tier graph, compact pipeline list,
// inspector forms and list rows. Components render markup and emit callbacks;
// the page controller (flows.js) owns all state and API calls.
(function (global) {
    'use strict';

    const UI = global.UI || (global.UI = {});
    const TRASH = -1;

    function esc(v) {
        return UI.esc ? UI.esc(v) : String(v == null ? '' : v);
    }

    function bytes(n) {
        return typeof global.formatBytes === 'function' ? global.formatBytes(n || 0, 1) : (n || 0) + ' B';
    }

    function num(v) {
        const n = Number(v) || 0;
        return Number.isInteger(n) ? String(n) : n.toFixed(1);
    }

    function timeAgo(iso) {
        if (!iso) return '-';
        const t = new Date(iso).getTime();
        if (!t || t < 0) return '-';
        const s = Math.max(0, (Date.now() - t) / 1000);
        if (s < 60) return 'just now';
        if (s < 3600) return Math.floor(s / 60) + 'm ago';
        if (s < 86400) return Math.floor(s / 3600) + 'h ago';
        return Math.floor(s / 86400) + 'd ago';
    }

    const ICON = {
        upload: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>',
        trash: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>',
        up: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></svg>',
        down: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><polyline points="19 12 12 19 5 12"/></svg>',
        x: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>',
        plus: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>',
        arrow: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="5" y1="12" x2="19" y2="12"/><polyline points="12 5 19 12 12 19"/></svg>',
        chevron: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"/></svg>'
    };

    const KIND = {
        promote: { label: 'Promote', icon: ICON.up },
        demote: { label: 'Demote', icon: ICON.down },
        expire: { label: 'Expire', icon: ICON.trash }
    };

    // ---- Model helpers ---------------------------------------------------

    function rankOf(t) {
        if (!t) return 0;
        const lc = t.lifecycle || {};
        return lc.speed_rank !== undefined && lc.speed_rank !== null ? lc.speed_rank : t.id;
    }

    function sortTiers(tiers) {
        return (tiers || []).slice().sort((a, b) => (rankOf(a) - rankOf(b)) || (a.id - b.id));
    }

    function usedPct(t) {
        if (!t || !(t.usable_bytes > 0)) return 0;
        return Math.min(100, Math.max(0, (t.used_bytes || 0) / t.usable_bytes * 100));
    }

    // Tier names follow "Tier 0 · Hot (NVMe)"; split into title and descriptor.
    function splitName(t) {
        const name = (t && t.name) || ('Tier ' + (t ? t.id : '?'));
        const i = name.indexOf('·');
        if (i === -1) return { title: name.trim(), sub: '' };
        return { title: name.slice(0, i).trim(), sub: name.slice(i + 1).trim() };
    }

    function tierLabel(tiers, id) {
        if (id === TRASH) return 'Delete';
        const t = (tiers || []).find(x => x.id === id);
        return t ? splitName(t).title : 'Tier ' + id;
    }

    // Kind follows speed direction, mirroring server-side validation.
    function inferKind(tiers, from, to) {
        if (to === TRASH) return 'expire';
        const a = (tiers || []).find(t => t.id === from);
        const b = (tiers || []).find(t => t.id === to);
        if (!a || !b || from === to) return null;
        const ra = rankOf(a), rb = rankOf(b);
        if (rb < ra) return 'promote';
        if (rb > ra) return 'demote';
        return null;
    }

    function triggerShort(edge) {
        const tr = edge.trigger || {};
        if (edge.kind === 'promote') return 'heat ≥ ' + num(tr.heat_at_least);
        const parts = [];
        if (tr.age_days > 0) parts.push(tr.age_days + 'd old');
        if (tr.idle_days > 0) parts.push('idle ' + tr.idle_days + 'd');
        if (tr.on_watermark) parts.push('when full');
        return parts.join(' · ') || 'no trigger';
    }

    function describe(edge, tiers) {
        const tr = edge.trigger || {};
        const from = tierLabel(tiers, edge.from);
        const to = tierLabel(tiers, edge.to);
        if (edge.kind === 'promote') {
            return `Move files from ${from} up to ${to} once their heat score reaches ${num(tr.heat_at_least)}.`;
        }
        if (edge.kind === 'demote') {
            const c = [];
            if (tr.idle_days > 0) c.push(`nobody has played them for ${tr.idle_days} day${tr.idle_days === 1 ? '' : 's'}`);
            if (tr.heat_below > 0) c.push(`their heat is below ${num(tr.heat_below)}`);
            let s = `Move files from ${from} down to ${to}`;
            if (c.length) s += ' when ' + c.join(' and ');
            if (tr.on_watermark) s += `${c.length ? ',' : ''} while ${from} is above its high watermark, until it falls to the low watermark`;
            if (!c.length && !tr.on_watermark) s += ' — add a trigger, this rule never fires';
            return s + '.';
        }
        const c = [];
        if (tr.age_days > 0) c.push(`have been on ${from} for ${tr.age_days} days`);
        if (tr.idle_days > 0) c.push(`have not been played for ${tr.idle_days} days`);
        let s = `Permanently delete files that ` + (c.length ? c.join(' and ') : '(no trigger set)');
        if (tr.on_watermark) s += `, only while ${from} is above its high watermark and nothing can be demoted`;
        return s + '. This cannot be undone.';
    }

    // ---- Small building blocks ------------------------------------------

    function kindChip(kind) {
        const k = KIND[kind] || { label: kind, icon: '' };
        return `<span class="fl-chip fl-tone-${esc(kind)}">${k.icon}<span>${esc(k.label)}</span></span>`;
    }

    function capacity(t, opts) {
        const o = opts || {};
        const lc = t.lifecycle || {};
        const hi = lc.high_watermark || 90;
        const lo = lc.low_watermark || 75;
        const pct = usedPct(t);
        const level = pct >= hi ? 'danger' : (pct >= lo ? 'warn' : 'ok');
        const free = Math.max(0, (t.usable_bytes || 0) - (t.used_bytes || 0));
        return `<div class="fl-cap ${o.compact ? 'is-compact' : ''}">` +
            `<div class="fl-cap-track" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct.toFixed(1)}" aria-label="${esc(splitName(t).title)} capacity used">` +
            `<span class="fl-cap-fill is-${level}" style="width:${Math.max(pct, 1.5).toFixed(2)}%"></span>` +
            `<i class="fl-cap-mark is-lo" style="left:${lo}%" title="Low watermark ${lo}%"></i>` +
            `<i class="fl-cap-mark is-hi" style="left:${hi}%" title="High watermark ${hi}%"></i>` +
            `</div>` +
            `<div class="fl-cap-meta tabular-num"><span>${pct.toFixed(1)}% used</span><span>${t.usable_bytes > 0 ? esc(bytes(free)) + ' free' : 'no capacity'}</span></div>` +
            `</div>`;
    }

    function tierCard(t, stats, opts) {
        const o = opts || {};
        const n = splitName(t);
        const s = stats || {};
        const blocks = t.blocks || [];
        const online = blocks.filter(b => b.online).length;
        const ingestIdx = o.ingestIndex;
        return `<div class="fl-node fl-tier ${o.selected ? 'is-selected' : ''} ${o.droppable ? 'is-drop' : ''}" data-node="tier" data-tier="${t.id}" data-drop="${t.id}" tabindex="0" role="button" aria-pressed="${o.selected ? 'true' : 'false'}" aria-label="${esc(t.name)}, ${usedPct(t).toFixed(0)}% used, ${s.file_count || 0} files">` +
            `<div class="fl-tier-head">` +
            `<span class="fl-rank" title="Speed rank (0 is fastest)">R${esc(rankOf(t))}</span>` +
            `<span class="fl-tier-title"><strong>${esc(n.title)}</strong>${n.sub ? `<span>${esc(n.sub)}</span>` : ''}</span>` +
            (ingestIdx >= 0 ? `<span class="fl-chip fl-tone-ingest" title="Ingest priority">#${ingestIdx + 1}</span>` : '') +
            `</div>` +
            capacity(t, { compact: true }) +
            `<div class="fl-tier-stats tabular-num">` +
            `<span><b>${s.file_count || 0}</b> files</span>` +
            `<span><b>${s.hot_count || 0}</b> hot</span>` +
            `<span class="${blocks.length && online < blocks.length ? 'is-warn' : ''}"><b>${online}/${blocks.length}</b> blocks</span>` +
            `</div>` +
            (o.port ? `<span class="fl-port" data-port="${t.id}" title="Drag to another tier to add a rule" aria-hidden="true"></span>` : '') +
            `</div>`;
    }

    function sourceCard(selected, port) {
        return `<div class="fl-node fl-src ${selected ? 'is-selected' : ''}" data-node="source" tabindex="0" role="button" aria-pressed="${selected ? 'true' : 'false'}" aria-label="New files: edit ingest order">` +
            `<span class="fl-src-icon">${ICON.upload}</span>` +
            `<strong>New files</strong>` +
            `<span>Upload &amp; URL ingest</span>` +
            (port ? `<span class="fl-port" data-port="source" title="Drag to a tier to add it to the ingest order" aria-hidden="true"></span>` : '') +
            `</div>`;
    }

    function trashCard(active) {
        return `<div class="fl-node fl-trash ${active ? 'is-active' : ''}" data-node="trash" data-drop="${TRASH}" aria-label="Delete permanently: drop a tier here to add an expire rule">` +
            `<span class="fl-trash-icon">${ICON.trash}</span>` +
            `<span class="fl-trash-text"><strong>Delete permanently</strong><span>Expire rules end here</span></span>` +
            `</div>`;
    }

    function empty(title, text, action) {
        return `<div class="fl-empty"><strong>${esc(title)}</strong>${text ? `<p>${esc(text)}</p>` : ''}${action || ''}</div>`;
    }

    // ---- Graph canvas ----------------------------------------------------

    // graph(el, handlers) → { update(model), destroy() }.
    // Nodes are HTML (crisp text at any width); wires are an SVG overlay
    // recomputed from live DOM geometry on every layout change.
    function graph(el, handlers) {
        const h = handlers || {};
        let model = null;
        let drag = null;
        let raf = 0;

        el.innerHTML = '';
        const root = document.createElement('div');
        root.className = 'fl-graph';
        el.appendChild(root);

        const ro = typeof ResizeObserver === 'function' ? new ResizeObserver(() => schedule()) : null;
        if (ro) ro.observe(root);

        function schedule() {
            cancelAnimationFrame(raf);
            raf = requestAnimationFrame(drawWires);
        }

        function lanesFor(edges) {
            // Short spans take inner lanes so long arcs wrap around them.
            const tiers = sortTiers(model.tiers);
            const pos = {};
            tiers.forEach((t, i) => { pos[t.id] = i; });
            pos[TRASH] = tiers.length;
            return edges
                .filter(e => pos[e.from] !== undefined && pos[e.to] !== undefined)
                .map(e => ({ e, span: Math.abs(pos[e.to] - pos[e.from]) }))
                .sort((a, b) => a.span - b.span || String(a.e.id).localeCompare(String(b.e.id)))
                .map((x, i) => ({ edge: x.e, lane: i }));
        }

        function render() {
            const m = model;
            const tiers = sortTiers(m.tiers);
            const edges = (m.policy.edges || []).filter(Boolean);
            const lanes = lanesFor(edges);
            const hasExpire = edges.some(e => e.kind === 'expire');
            const ingest = m.policy.ingest || [];
            const sel = m.selection || {};

            root.style.setProperty('--lanes', String(Math.max(1, lanes.length)));
            root.innerHTML =
                `<svg class="fl-wires" aria-hidden="true">` +
                `<defs>` +
                ['ingest', 'promote', 'demote', 'expire', 'error'].map(k =>
                    `<marker id="fl-arrow-${k}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 1 L10 5 L0 9 z" class="fl-arrow-${k}"/></marker>`).join('') +
                `</defs><g class="fl-wire-layer"></g><path class="fl-wire-temp" d=""/></svg>` +
                `<div class="fl-graph-src">${sourceCard(sel.type === 'source', true)}</div>` +
                `<div class="fl-graph-tiers">` +
                tiers.map(t => tierCard(t, (m.stats || {})[t.id], {
                    selected: sel.type === 'tier' && sel.id === t.id,
                    ingestIndex: ingest.indexOf(t.id),
                    port: true
                })).join('') +
                trashCard(hasExpire) +
                `</div>` +
                `<div class="fl-graph-labels"></div>`;
            root._lanes = lanes;
            schedule();
        }

        function rel(node, box) {
            const r = node.getBoundingClientRect();
            return { x: r.left - box.left, y: r.top - box.top, w: r.width, h: r.height };
        }

        function drawWires() {
            if (!model) return;
            const layer = root.querySelector('.fl-wire-layer');
            const labels = root.querySelector('.fl-graph-labels');
            const svg = root.querySelector('.fl-wires');
            if (!layer || !labels || !svg) return;

            const box = root.getBoundingClientRect();
            svg.setAttribute('width', box.width);
            svg.setAttribute('height', box.height);
            svg.setAttribute('viewBox', `0 0 ${box.width} ${box.height}`);

            const sel = model.selection || {};
            const errors = model.errors || {};
            const nodeOf = id => root.querySelector(id === TRASH ? '.fl-trash' : `.fl-tier[data-tier="${id}"]`);
            const src = root.querySelector('.fl-src');
            let paths = '';
            let pills = '';

            // Ingest wires: source right edge → tier left edge.
            if (src) {
                const s = rel(src, box);
                (model.policy.ingest || []).forEach((tid, i) => {
                    const n = nodeOf(tid);
                    if (!n) return;
                    const t = rel(n, box);
                    const x1 = s.x + s.w, y1 = s.y + s.h / 2;
                    const x2 = t.x - 2, y2 = t.y + t.h / 2;
                    const dx = Math.max(24, (x2 - x1) / 2);
                    paths += `<path class="fl-wire is-ingest" d="M${x1} ${y1} C${x1 + dx} ${y1} ${x2 - dx} ${y2} ${x2} ${y2}" marker-end="url(#fl-arrow-ingest)"/>`;
                    pills += `<button type="button" class="fl-wire-pill is-ingest" data-select="source" style="left:${(x1 + x2) / 2}px;top:${(y1 + y2) / 2}px" aria-label="Ingest priority ${i + 1}: ${esc(tierLabel(model.tiers, tid))}">#${i + 1}</button>`;
                });
            }

            // Lifecycle wires: rounded orthogonal arcs in the right-hand gutter.
            const lanes = root._lanes || [];
            const tierCol = root.querySelector('.fl-graph-tiers');
            const colRight = tierCol ? rel(tierCol, box).x + rel(tierCol, box).w : box.width - 80;
            const ends = {};
            lanes.forEach(({ edge }) => {
                (ends[edge.from] = ends[edge.from] || []).push(edge.id + ':out');
                (ends[edge.to] = ends[edge.to] || []).push(edge.id + ':in');
            });
            const anchorY = (id, tag) => {
                const n = nodeOf(id);
                if (!n) return null;
                const r = rel(n, box);
                const list = ends[id] || [tag];
                const k = list.indexOf(tag);
                return { x: r.x + r.w, y: r.y + r.h * (k + 1) / (list.length + 1) };
            };

            lanes.forEach(({ edge, lane }) => {
                const a = anchorY(edge.from, edge.id + ':out');
                const b = anchorY(edge.to, edge.id + ':in');
                if (!a || !b) return;
                const lx = colRight + 34 + lane * 40;
                const dir = b.y >= a.y ? 1 : -1;
                const r = Math.min(10, Math.abs(b.y - a.y) / 2);
                // Start and end just outside the port circle so arrowheads stay visible.
                const d = `M${a.x + 9} ${a.y} H${lx - r} Q${lx} ${a.y} ${lx} ${a.y + dir * r} V${b.y - dir * r} Q${lx} ${b.y} ${lx - r} ${b.y} H${b.x + 10}`;
                const err = !!errors[edge.id];
                const tone = err ? 'error' : edge.kind;
                const cls = `fl-wire is-${tone}${edge.enabled ? '' : ' is-off'}${sel.type === 'edge' && sel.id === edge.id ? ' is-selected' : ''}`;
                paths += `<path class="${cls}" d="${d}" marker-end="url(#fl-arrow-${tone})"/>`;
                paths += `<path class="fl-wire-hit" data-edge="${esc(edge.id)}" d="${d}"/>`;
                pills += `<button type="button" class="fl-wire-pill is-${tone}${edge.enabled ? '' : ' is-off'}${sel.type === 'edge' && sel.id === edge.id ? ' is-selected' : ''}" data-edge="${esc(edge.id)}" style="left:${lx}px;top:${(a.y + b.y) / 2}px" title="${esc(describe(edge, model.tiers))}" aria-label="${esc((KIND[edge.kind] || {}).label || edge.kind)} rule ${esc(tierLabel(model.tiers, edge.from))} to ${esc(tierLabel(model.tiers, edge.to))}: ${esc(triggerShort(edge))}">${(KIND[edge.kind] || {}).icon || ''}<span>${esc(triggerShort(edge))}</span></button>`;
            });

            layer.innerHTML = paths;
            labels.innerHTML = pills;
        }

        // Delegated selection: nodes, wire pills and wire hit-paths.
        function onClick(e) {
            if (drag && drag.moved) return;
            const pill = e.target.closest('[data-edge]');
            if (pill) { h.onSelect && h.onSelect({ type: 'edge', id: pill.getAttribute('data-edge') }); return; }
            const toSrc = e.target.closest('[data-select="source"]');
            if (toSrc) { h.onSelect && h.onSelect({ type: 'source' }); return; }
            if (e.target.closest('.fl-port')) return;
            const node = e.target.closest('.fl-node');
            if (node) {
                const type = node.getAttribute('data-node');
                if (type === 'tier') h.onSelect && h.onSelect({ type: 'tier', id: Number(node.getAttribute('data-tier')) });
                else if (type === 'source') h.onSelect && h.onSelect({ type: 'source' });
                else if (type === 'trash') h.onSelect && h.onSelect({ type: 'new', to: TRASH });
                return;
            }
            h.onSelect && h.onSelect({ type: 'none' });
        }

        function onKey(e) {
            if (e.key !== 'Enter' && e.key !== ' ') return;
            const node = e.target.closest && e.target.closest('.fl-node[tabindex]');
            if (!node) return;
            e.preventDefault();
            node.click();
        }

        // Pointer-based connect works for mouse, pen and touch alike.
        function onPointerDown(e) {
            const port = e.target.closest('.fl-port');
            if (!port || e.button > 0) return;
            e.preventDefault();
            const raw = port.getAttribute('data-port');
            const from = raw === 'source' ? 'source' : Number(raw);
            const box = root.getBoundingClientRect();
            const pr = port.getBoundingClientRect();
            drag = {
                from,
                x0: pr.left + pr.width / 2 - box.left,
                y0: pr.top + pr.height / 2 - box.top,
                moved: false,
                pointerId: e.pointerId
            };
            root.classList.add('is-connecting', from === 'source' ? 'from-source' : 'from-tier');
            const fromNode = from === 'source' ? root.querySelector('.fl-src') : root.querySelector(`.fl-tier[data-tier="${from}"]`);
            if (fromNode) fromNode.classList.add('is-origin');
            window.addEventListener('pointermove', onPointerMove);
            window.addEventListener('pointerup', onPointerUp);
            window.addEventListener('pointercancel', endDrag);
            window.addEventListener('keydown', onDragKey);
        }

        function onPointerMove(e) {
            if (!drag || e.pointerId !== drag.pointerId) return;
            const box = root.getBoundingClientRect();
            const x = e.clientX - box.left, y = e.clientY - box.top;
            if (Math.abs(x - drag.x0) + Math.abs(y - drag.y0) > 4) drag.moved = true;
            const dx = Math.max(30, Math.abs(x - drag.x0) / 2);
            const temp = root.querySelector('.fl-wire-temp');
            if (temp) temp.setAttribute('d', `M${drag.x0} ${drag.y0} C${drag.x0 + dx} ${drag.y0} ${x - dx} ${y} ${x} ${y}`);
            root.querySelectorAll('.is-hover').forEach(n => n.classList.remove('is-hover'));
            const over = document.elementFromPoint(e.clientX, e.clientY);
            const target = over && over.closest('[data-drop]');
            if (target && root.contains(target)) target.classList.add('is-hover');
        }

        function onPointerUp(e) {
            if (!drag || e.pointerId !== drag.pointerId) return;
            const over = document.elementFromPoint(e.clientX, e.clientY);
            const target = over && over.closest('[data-drop]');
            const from = drag.from;
            const moved = drag.moved;
            endDrag();
            if (!moved || !target || !root.contains(target)) return;
            const to = Number(target.getAttribute('data-drop'));
            if (from === to) return;
            h.onConnect && h.onConnect({ from, to });
        }

        function onDragKey(e) {
            if (e.key === 'Escape') endDrag();
        }

        function endDrag() {
            const wasMoved = drag && drag.moved;
            drag = null;
            root.classList.remove('is-connecting', 'from-source', 'from-tier');
            root.querySelectorAll('.is-hover, .is-origin').forEach(n => n.classList.remove('is-hover', 'is-origin'));
            const temp = root.querySelector('.fl-wire-temp');
            if (temp) temp.setAttribute('d', '');
            window.removeEventListener('pointermove', onPointerMove);
            window.removeEventListener('pointerup', onPointerUp);
            window.removeEventListener('pointercancel', endDrag);
            window.removeEventListener('keydown', onDragKey);
            // Swallow the click that follows a drag release.
            if (wasMoved) {
                drag = { moved: true };
                setTimeout(() => { drag = null; }, 0);
            }
        }

        root.addEventListener('click', onClick);
        root.addEventListener('keydown', onKey);
        root.addEventListener('pointerdown', onPointerDown);

        return {
            update(next) {
                model = next;
                render();
            },
            redraw: schedule,
            destroy() {
                endDrag();
                cancelAnimationFrame(raf);
                if (ro) ro.disconnect();
                root.removeEventListener('click', onClick);
                root.removeEventListener('keydown', onKey);
                root.removeEventListener('pointerdown', onPointerDown);
                el.innerHTML = '';
            }
        };
    }

    // ---- Pipeline list (narrow screens) -----------------------------------

    function ruleRow(edge, tiers, opts) {
        const o = opts || {};
        const k = KIND[edge.kind] || { label: edge.kind, icon: '' };
        return `<button type="button" class="fl-rule-row fl-tone-${esc(edge.kind)} ${o.selected ? 'is-selected' : ''} ${edge.enabled ? '' : 'is-off'} ${o.error ? 'has-error' : ''}" data-edge="${esc(edge.id)}">` +
            `<span class="fl-rule-icon">${k.icon}</span>` +
            `<span class="fl-rule-main"><strong>${esc(k.label)} ${ICON.arrow} ${esc(tierLabel(tiers, edge.to))}</strong>` +
            `<span>${esc(o.incoming ? 'from ' + tierLabel(tiers, edge.from) + ' · ' : '')}${esc(triggerShort(edge))}${edge.enabled ? '' : ' · off'}</span></span>` +
            (o.error ? `<span class="fl-rule-err" title="${esc(o.error)}">!</span>` : `<span class="fl-rule-go">${ICON.chevron}</span>`) +
            `</button>`;
    }

    function pipeline(el, m, h) {
        const tiers = sortTiers(m.tiers);
        const edges = (m.policy.edges || []).filter(Boolean);
        const ingest = m.policy.ingest || [];
        const sel = m.selection || {};
        const errors = m.errors || {};

        let html = `<ol class="fl-pipe">`;
        html += `<li class="fl-pipe-step">` +
            `<button type="button" class="fl-pipe-src ${sel.type === 'source' ? 'is-selected' : ''}" data-select="source">` +
            `<span class="fl-src-icon">${ICON.upload}</span>` +
            `<span class="fl-pipe-src-main"><strong>New files land on</strong>` +
            `<span class="fl-pipe-order">${ingest.length ? ingest.map((id, i) => `<span class="fl-chip fl-tone-ingest">#${i + 1} ${esc(tierLabel(tiers, id))}</span>`).join('') : '<span class="fl-muted">No ingest tier set</span>'}</span></span>` +
            `<span class="fl-rule-go">${ICON.chevron}</span>` +
            `</button></li>`;

        tiers.forEach(t => {
            const out = edges.filter(e => e.from === t.id);
            html += `<li class="fl-pipe-step">` +
                tierCard(t, (m.stats || {})[t.id], { selected: sel.type === 'tier' && sel.id === t.id, ingestIndex: ingest.indexOf(t.id) }) +
                `<div class="fl-pipe-rules">` +
                (out.length ? out.map(e => ruleRow(e, tiers, { selected: sel.type === 'edge' && sel.id === e.id, error: errors[e.id] })).join('')
                    : `<button type="button" class="fl-pipe-add" data-add-from="${t.id}">${ICON.plus}<span>Add rule from ${esc(splitName(t).title)}</span></button>`) +
                `</div></li>`;
        });
        html += `</ol>`;
        el.innerHTML = html;

        el.onclick = (e) => {
            const edge = e.target.closest('[data-edge]');
            if (edge) return h.onSelect && h.onSelect({ type: 'edge', id: edge.getAttribute('data-edge') });
            if (e.target.closest('[data-select="source"]')) return h.onSelect && h.onSelect({ type: 'source' });
            const add = e.target.closest('[data-add-from]');
            if (add) return h.onSelect && h.onSelect({ type: 'new', from: Number(add.getAttribute('data-add-from')) });
            const node = e.target.closest('.fl-tier');
            if (node) return h.onSelect && h.onSelect({ type: 'tier', id: Number(node.getAttribute('data-tier')) });
        };
        el.onkeydown = (e) => {
            if ((e.key === 'Enter' || e.key === ' ') && e.target.classList && e.target.classList.contains('fl-tier')) {
                e.preventDefault();
                e.target.click();
            }
        };
    }

    // ---- Inspector ---------------------------------------------------------

    function field(opts) {
        return `<label class="fl-field">` +
            `<span class="fl-field-label">${esc(opts.label)}</span>` +
            `<span class="fl-input-wrap"><input class="fl-input tabular-num" type="number" inputmode="decimal" data-field="${esc(opts.name)}" value="${esc(opts.value)}" min="${opts.min != null ? opts.min : 0}" ${opts.max != null ? `max="${opts.max}"` : ''} step="${opts.step || 1}">${opts.unit ? `<span class="fl-input-unit">${esc(opts.unit)}</span>` : ''}</span>` +
            (opts.hint ? `<span class="fl-field-hint">${esc(opts.hint)}</span>` : '') +
            `</label>`;
    }

    function toggle(name, checked, label, hint) {
        return `<label class="fl-toggle">` +
            `<input type="checkbox" data-field="${esc(name)}" ${checked ? 'checked' : ''}>` +
            `<span class="fl-switch" aria-hidden="true"></span>` +
            `<span class="fl-toggle-text"><strong>${esc(label)}</strong>${hint ? `<span>${esc(hint)}</span>` : ''}</span>` +
            `</label>`;
    }

    function inspectorHead(title, sub, extra) {
        return `<div class="fl-insp-head"><div><h3>${title}</h3>${sub ? `<p>${sub}</p>` : ''}</div>${extra || ''}</div>`;
    }

    function edgeForm(edge, ctx) {
        const tiers = ctx.tiers;
        const tr = edge.trigger || {};
        let fields = '';
        if (edge.kind === 'promote') {
            fields = field({ label: 'Heat score at least', name: 'heat_at_least', value: tr.heat_at_least || 0, step: 0.5, hint: 'Each play adds 1; the score halves every half-life.' });
        } else if (edge.kind === 'demote') {
            fields = `<div class="fl-field-grid">` +
                field({ label: 'Idle for', name: 'idle_days', value: tr.idle_days || 0, unit: 'days', hint: '0 = ignore idle time' }) +
                field({ label: 'Heat below', name: 'heat_below', value: tr.heat_below || 0, step: 0.5, hint: 'Keeps a gap to the promote threshold' }) +
                `</div>` +
                toggle('on_watermark', tr.on_watermark, 'Also run when the tier is full', 'Starts at the high watermark, stops at the low one');
        } else {
            fields = `<div class="fl-field-grid">` +
                field({ label: 'On this tier for', name: 'age_days', value: tr.age_days || 0, unit: 'days' }) +
                field({ label: 'Idle for', name: 'idle_days', value: tr.idle_days || 0, unit: 'days' }) +
                `</div>` +
                toggle('on_watermark', tr.on_watermark, 'Only under capacity pressure', 'Delete only while the tier is above its high watermark');
        }
        const err = (ctx.errors || {})[edge.id];
        return inspectorHead(
            `${kindChip(edge.kind)}<span>${esc(tierLabel(tiers, edge.from))}</span>${ICON.arrow}<span>${esc(tierLabel(tiers, edge.to))}</span>`,
            '', '') +
            (err ? `<div class="fl-alert is-error" role="alert">${esc(err)}</div>` : '') +
            `<div class="fl-insp-body">` +
            toggle('enabled', edge.enabled, 'Rule enabled', edge.enabled ? 'Runs on every evaluation tick' : 'Kept in the policy but skipped') +
            fields +
            `<div class="fl-summary ${edge.kind === 'expire' ? 'is-danger' : ''}"><span class="fl-summary-label">What this rule does</span><p data-summary>${esc(describe(edge, tiers))}</p></div>` +
            `</div>` +
            `<div class="fl-insp-foot">` +
            `<button type="button" class="fl-btn is-danger-ghost" data-action="delete-edge">${ICON.trash}<span>Delete rule</span></button>` +
            `<button type="button" class="fl-btn is-ghost is-small" data-action="open-docs" title="Documentation">Docs</button>` +
            `<span class="fl-foot-note">Changes stay in the draft until you save.</span></div>`;
    }

    function sourceForm(ctx) {
        const tiers = sortTiers(ctx.tiers);
        const ingest = ctx.policy.ingest || [];
        const rest = tiers.filter(t => ingest.indexOf(t.id) === -1);
        return inspectorHead('Ingest order', 'New files go to the first tier with room, then the next.') +
            `<div class="fl-insp-body">` +
            (ingest.length ? `<ol class="fl-order">` + ingest.map((id, i) => {
                const t = tiers.find(x => x.id === id);
                return `<li class="fl-order-item">` +
                    `<span class="fl-order-num tabular-num">${i + 1}</span>` +
                    `<span class="fl-order-main"><strong>${esc(tierLabel(tiers, id))}</strong>${t ? capacity(t, { compact: true }) : ''}</span>` +
                    `<span class="fl-order-actions">` +
                    `<button type="button" class="fl-icon-btn" data-action="ingest-up" data-index="${i}" ${i === 0 ? 'disabled' : ''} aria-label="Move up">${ICON.up}</button>` +
                    `<button type="button" class="fl-icon-btn" data-action="ingest-down" data-index="${i}" ${i === ingest.length - 1 ? 'disabled' : ''} aria-label="Move down">${ICON.down}</button>` +
                    `<button type="button" class="fl-icon-btn is-danger" data-action="ingest-remove" data-index="${i}" ${ingest.length === 1 ? 'disabled title="At least one ingest tier is required"' : ''} aria-label="Remove">${ICON.x}</button>` +
                    `</span></li>`;
            }).join('') + `</ol>` : `<div class="fl-alert is-error">No ingest tier: uploads will fail until you add one.</div>`) +
            (rest.length ? `<div class="fl-inline-add"><select class="fl-select" data-role="ingest-add" aria-label="Tier to add">` +
                rest.map(t => `<option value="${t.id}">${esc(t.name)}</option>`).join('') +
                `</select><button type="button" class="fl-btn" data-action="ingest-add">${ICON.plus}<span>Add</span></button></div>` : '') +
            `</div>`;
    }

    function tierForm(t, ctx) {
        const lc = t.lifecycle || {};
        const n = splitName(t);
        const edges = (ctx.policy.edges || []).filter(Boolean);
        const out = edges.filter(e => e.from === t.id);
        const inc = edges.filter(e => e.to === t.id);
        const s = (ctx.stats || {})[t.id] || {};
        const blocks = t.blocks || [];
        return inspectorHead(esc(n.title), esc(n.sub || t.name), `<span class="fl-rank">R${esc(rankOf(t))}</span>`) +
            `<div class="fl-insp-body">` +
            capacity(t) +
            `<div class="fl-kv tabular-num">` +
            `<div><span>Files</span><strong>${s.file_count || 0}</strong></div>` +
            `<div><span>Hot</span><strong>${s.hot_count || 0}</strong></div>` +
            `<div><span>Blocks online</span><strong>${blocks.filter(b => b.online).length}/${blocks.length}</strong></div>` +
            `<div><span>Pooled</span><strong>${esc(bytes(t.usable_bytes))}</strong></div>` +
            `</div>` +
            `<div class="fl-section-title">Tier settings <span class="fl-muted">saved immediately</span></div>` +
            `<div class="fl-field-grid is-3">` +
            field({ label: 'Speed rank', name: 'speed_rank', value: lc.speed_rank != null ? lc.speed_rank : t.id, hint: '0 = fastest' }) +
            field({ label: 'Low mark', name: 'low_watermark', value: lc.low_watermark || 75, min: 1, max: 99, unit: '%' }) +
            field({ label: 'High mark', name: 'high_watermark', value: lc.high_watermark || 90, min: 2, max: 100, unit: '%' }) +
            `</div>` +
            `<div class="fl-alert is-error" data-role="tier-error" hidden></div>` +
            `<div class="fl-row-end"><button type="button" class="fl-btn is-primary" data-action="save-tier">Save tier settings</button></div>` +
            `<div class="fl-section-title">Rules</div>` +
            `<div class="fl-rule-list">` +
            out.map(e => ruleRow(e, ctx.tiers, { error: (ctx.errors || {})[e.id] })).join('') +
            inc.map(e => ruleRow(e, ctx.tiers, { incoming: true, error: (ctx.errors || {})[e.id] })).join('') +
            `<button type="button" class="fl-pipe-add" data-action="new-from" data-from="${t.id}">${ICON.plus}<span>Add rule from ${esc(n.title)}</span></button>` +
            `</div></div>`;
    }

    function newRuleForm(sel, ctx) {
        const tiers = sortTiers(ctx.tiers);
        const from = sel.from != null ? sel.from : (tiers.length ? tiers[tiers.length - 1].id : 0);
        const to = sel.to != null ? sel.to : (tiers.find(t => t.id !== from) || { id: TRASH }).id;
        const opt = (id, cur) => `<option value="${id}" ${id === cur ? 'selected' : ''}>${esc(id === TRASH ? 'Delete permanently' : (tiers.find(t => t.id === id) || {}).name || 'Tier ' + id)}</option>`;
        return inspectorHead('New rule', 'Pick where files move from and to. Faster target = promote, slower = demote.') +
            `<div class="fl-insp-body">` +
            `<div class="fl-field-grid">` +
            `<label class="fl-field"><span class="fl-field-label">From</span><select class="fl-select" data-role="new-from">${tiers.map(t => opt(t.id, from)).join('')}</select></label>` +
            `<label class="fl-field"><span class="fl-field-label">To</span><select class="fl-select" data-role="new-to">${tiers.map(t => opt(t.id, to)).join('')}${opt(TRASH, to)}</select></label>` +
            `</div>` +
            `<div class="fl-summary" data-role="new-kind"></div>` +
            `<div class="fl-row-end"><button type="button" class="fl-btn" data-action="cancel-new">Cancel</button><button type="button" class="fl-btn is-primary" data-action="create-edge">Create rule</button></div>` +
            `</div>`;
    }

    function overview(ctx) {
        const edges = (ctx.policy.edges || []).filter(Boolean);
        return inspectorHead('How it works', 'Select a tier, wire or rule to edit it.') +
            `<div class="fl-insp-body">` +
            `<ol class="fl-steps">` +
            `<li><strong>Ingest</strong><span>New files land on the first tier in the ingest order that has room.</span></li>` +
            `<li><strong>Promote</strong><span>Hot files move up to a faster tier. Drag a tier's dot onto a faster tier.</span></li>` +
            `<li><strong>Demote</strong><span>Idle files, or files on a full tier, move down. Nothing is deleted.</span></li>` +
            `<li><strong>Expire</strong><span>Optional. Drop a tier on “Delete permanently” to remove old files.</span></li>` +
            `</ol>` +
            `<button type="button" class="fl-btn is-primary is-block" data-action="new-rule">${ICON.plus}<span>Add rule</span></button>` +
            (edges.length ? `<div class="fl-section-title">All rules</div><div class="fl-rule-list">` +
                edges.map(e => ruleRow(e, ctx.tiers, { incoming: true, error: (ctx.errors || {})[e.id] })).join('') + `</div>` : '') +
            `</div>`;
    }

    // inspector(el, ctx): ctx = { selection, policy, tiers, stats, errors, on: {...} }.
    function inspector(el, ctx) {
        const sel = ctx.selection || { type: 'none' };
        const on = ctx.on || {};
        const edge = sel.type === 'edge' ? (ctx.policy.edges || []).find(e => e && e.id === sel.id) : null;
        const tier = sel.type === 'tier' ? (ctx.tiers || []).find(t => t.id === sel.id) : null;

        if (edge) el.innerHTML = edgeForm(edge, ctx);
        else if (tier) el.innerHTML = tierForm(tier, ctx);
        else if (sel.type === 'source') el.innerHTML = sourceForm(ctx);
        else if (sel.type === 'new') el.innerHTML = newRuleForm(sel, ctx);
        else el.innerHTML = overview(ctx);

        if (sel.type === 'new') {
            const f = el.querySelector('[data-role="new-from"]');
            const t = el.querySelector('[data-role="new-to"]');
            const out = el.querySelector('[data-role="new-kind"]');
            const btn = el.querySelector('[data-action="create-edge"]');
            const sync = () => {
                const kind = inferKind(ctx.tiers, Number(f.value), Number(t.value));
                btn.disabled = !kind;
                out.className = 'fl-summary' + (kind === 'expire' ? ' is-danger' : '');
                out.innerHTML = kind
                    ? `<span class="fl-summary-label">Rule type</span>${kindChip(kind)}<p>${kind === 'promote' ? 'Moves hot files to the faster tier.' : kind === 'demote' ? 'Moves idle files to the slower tier. Nothing is deleted.' : 'Permanently deletes matching files. Saving requires typed confirmation.'}</p>`
                    : `<span class="fl-summary-label">Rule type</span><p>Pick two tiers with different speed ranks.</p>`;
            };
            f.onchange = sync;
            t.onchange = sync;
            sync();
        }

        el.oninput = (e) => {
            const input = e.target.closest('[data-field]');
            if (!input || !edge) return;
            const name = input.getAttribute('data-field');
            if (name === 'enabled') {
                edge.enabled = input.checked;
            } else {
                edge.trigger = edge.trigger || {};
                if (input.type === 'checkbox') edge.trigger[name] = input.checked;
                else if (/_days$/.test(name)) edge.trigger[name] = Math.max(0, parseInt(input.value, 10) || 0);
                else edge.trigger[name] = Math.max(0, parseFloat(input.value) || 0);
            }
            const sum = el.querySelector('[data-summary]');
            if (sum) sum.textContent = describe(edge, ctx.tiers);
            on.edgeChange && on.edgeChange(edge);
        };

        el.onclick = (e) => {
            const rule = e.target.closest('.fl-rule-row[data-edge]');
            if (rule) return on.select && on.select({ type: 'edge', id: rule.getAttribute('data-edge') });
            const btn = e.target.closest('[data-action]');
            if (!btn || btn.disabled) return;
            const action = btn.getAttribute('data-action');
            const idx = Number(btn.getAttribute('data-index'));
            const ingest = (ctx.policy.ingest || []).slice();
            switch (action) {
                case 'delete-edge':
                    return edge && on.deleteEdge && on.deleteEdge(edge.id);
                case 'ingest-up':
                    [ingest[idx - 1], ingest[idx]] = [ingest[idx], ingest[idx - 1]];
                    return on.ingestChange && on.ingestChange(ingest);
                case 'ingest-down':
                    [ingest[idx + 1], ingest[idx]] = [ingest[idx], ingest[idx + 1]];
                    return on.ingestChange && on.ingestChange(ingest);
                case 'ingest-remove':
                    ingest.splice(idx, 1);
                    return on.ingestChange && on.ingestChange(ingest);
                case 'ingest-add': {
                    const s = el.querySelector('[data-role="ingest-add"]');
                    if (s) ingest.push(Number(s.value));
                    return on.ingestChange && on.ingestChange(ingest);
                }
                case 'save-tier': {
                    const val = n => Number((el.querySelector(`[data-field="${n}"]`) || {}).value);
                    const lc = { speed_rank: Math.max(0, Math.round(val('speed_rank'))), high_watermark: val('high_watermark'), low_watermark: val('low_watermark') };
                    const err = el.querySelector('[data-role="tier-error"]');
                    const bad = !(lc.low_watermark > 0 && lc.low_watermark < lc.high_watermark && lc.high_watermark <= 100);
                    if (err) {
                        err.hidden = !bad;
                        err.textContent = bad ? 'Low mark must be above 0 and below the high mark (max 100%).' : '';
                    }
                    if (!bad && tier) on.saveTier && on.saveTier(tier.id, lc, btn);
                    return;
                }
                case 'new-rule':
                    return on.select && on.select({ type: 'new' });
                case 'new-from':
                    return on.select && on.select({ type: 'new', from: Number(btn.getAttribute('data-from')) });
                case 'cancel-new':
                    return on.select && on.select({ type: 'none' });
                case 'create-edge': {
                    const f = Number(el.querySelector('[data-role="new-from"]').value);
                    const t = Number(el.querySelector('[data-role="new-to"]').value);
                    return on.connect && on.connect({ from: f, to: t });
                }
            }
        };
    }

    // ---- List rows -------------------------------------------------------

    function stateChip(state) {
        const tone = state === 'done' ? 'ok' : (state === 'failed' ? 'error' : (state === 'cancelled' ? 'muted' : 'busy'));
        return `<span class="fl-state is-${tone}">${esc(state)}</span>`;
    }

    function hop(from, to, tiers) {
        const f = from ? tierLabel(tiers, from.tier_id) : '-';
        const t = to ? tierLabel(tiers, to.tier_id) : '-';
        return `<span class="fl-hop"><span>${esc(f)}</span>${ICON.arrow}<span>${esc(t)}</span></span>`;
    }

    function taskRow(t, tiers) {
        const active = ['queued', 'copying', 'verifying', 'switching', 'cleanup'].indexOf(t.state) !== -1;
        const cancellable = ['queued', 'copying', 'verifying'].indexOf(t.state) !== -1;
        const pct = Math.min(100, Math.max(0, Number(t.progress) || 0));
        return `<div class="fl-item">` +
            `<div class="fl-item-top">${kindChip(t.kind === 'manual' ? 'promote' : t.kind)}<code class="fl-key" title="${esc(t.key)}">${esc(t.key)}</code>${stateChip(t.state)}</div>` +
            `<div class="fl-item-mid">${t.kind === 'expire' ? `<span class="fl-hop"><span>${esc(tierLabel(tiers, t.from && t.from.tier_id))}</span>${ICON.arrow}<span>Delete</span></span>` : hop(t.from, t.to, tiers)}<span class="fl-muted">${esc(t.to && t.to.node_id ? t.to.node_id : '')}</span></div>` +
            (active ? `<div class="fl-progress"><span style="width:${pct}%"></span></div><div class="fl-item-meta tabular-num"><span>${pct.toFixed(0)}%${t.speed ? ' · ' + esc(t.speed) : ''}${t.bytes ? ' · ' + esc(bytes(t.bytes)) : ''}</span>${cancellable ? `<button type="button" class="fl-link is-danger" data-cancel="${esc(t.id)}">Cancel</button>` : ''}</div>` : '') +
            (t.error ? `<div class="fl-item-error">${esc(t.error)}</div>` : '') +
            (t.reason ? `<div class="fl-item-reason">${esc(t.reason)}${t.attempts > 1 ? ` · attempt ${t.attempts}` : ''} · ${esc(timeAgo(t.updated_at))}</div>` : '') +
            `</div>`;
    }

    function previewRow(t, tiers) {
        return `<div class="fl-item">` +
            `<div class="fl-item-top">${kindChip(t.kind === 'manual' ? 'promote' : t.kind)}<code class="fl-key" title="${esc(t.key)}">${esc(t.key)}</code></div>` +
            `<div class="fl-item-mid">${t.kind === 'expire' ? `<span class="fl-hop"><span>${esc(tierLabel(tiers, t.from && t.from.tier_id))}</span>${ICON.arrow}<span>Delete</span></span>` : hop(t.from, t.to, tiers)}</div>` +
            (t.reason ? `<div class="fl-item-reason">${esc(t.reason)}</div>` : '') +
            `</div>`;
    }

    function skippedRow(s) {
        return `<div class="fl-item is-quiet"><div class="fl-item-top"><code class="fl-key">${esc(s.key)}</code>${s.edge_id ? `<span class="fl-muted mono">${esc(s.edge_id)}</span>` : ''}</div><div class="fl-item-reason">${esc(s.reason)}</div></div>`;
    }

    function hotRow(f, tiers, rank) {
        const pinned = f.pinned_tier !== undefined && f.pinned_tier !== null;
        return `<div class="fl-item fl-hot">` +
            `<span class="fl-hot-rank tabular-num">${rank}</span>` +
            `<div class="fl-hot-main"><strong title="${esc(f.filename || f.key)}">${esc(f.filename || f.key)}</strong>` +
            `<span class="fl-muted"><code>${esc(f.key)}</code> · ${esc(tierLabel(tiers, f.tier_id))} · ${esc(bytes(f.size_bytes))} · ${esc(timeAgo(f.last_hit_at))}</span></div>` +
            `<span class="fl-hot-score tabular-num" title="Decayed heat score">${(Number(f.score) || 0).toFixed(1)}</span>` +
            (pinned ? `<span class="fl-chip fl-tone-ingest" title="Pinned until ${esc(f.pin_until || '')}">Pinned ${esc(tierLabel(tiers, f.pinned_tier))}</span>`
                : `<button type="button" class="fl-btn is-small" data-pin="${esc(f.key)}">Pin hot</button>`) +
            `</div>`;
    }

    function auditRow(a, tiers) {
        return `<div class="fl-item">` +
            `<div class="fl-item-top">${kindChip('expire')}<strong class="fl-audit-name" title="${esc(a.filename || a.key)}">${esc(a.filename || a.key)}</strong><span class="fl-muted tabular-num">${esc(a.at ? new Date(a.at).toLocaleString() : '-')}</span></div>` +
            `<div class="fl-item-mid"><code class="fl-key">${esc(a.key)}</code><span class="fl-muted">${esc(tierLabel(tiers, a.tier))} · ${esc(a.node || '-')} · ${esc(bytes(a.size))}</span></div>` +
            (a.reason ? `<div class="fl-item-reason">${esc(a.reason)}</div>` : '') +
            `</div>`;
    }

    function statTile(label, value, sub, tone) {
        return `<div class="fl-stat ${tone ? 'is-' + tone : ''}"><span class="fl-stat-label">${esc(label)}</span><strong class="tabular-num">${esc(value)}</strong>${sub ? `<span class="fl-stat-sub">${esc(sub)}</span>` : ''}</div>`;
    }

    UI.flow = {
        TRASH: TRASH,
        sortTiers: sortTiers,
        tierLabel: tierLabel,
        inferKind: inferKind,
        triggerShort: triggerShort,
        describe: describe,
        capacity: capacity,
        graph: graph,
        pipeline: pipeline,
        inspector: inspector,
        taskRow: taskRow,
        previewRow: previewRow,
        skippedRow: skippedRow,
        hotRow: hotRow,
        auditRow: auditRow,
        statTile: statTile,
        empty: empty,
        kindChip: kindChip
    };
})(window);
