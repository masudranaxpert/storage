package api

const embedPlayerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
    <title>{{.Title}}</title>
    <style>
        * { box-sizing: border-box; }
        html, body {
            margin: 0;
            padding: 0;
            width: 100%;
            height: 100%;
            overflow: hidden;
            background: #000;
            color: #fff;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
        }
        #artplayer {
            width: 100%;
            height: 100%;
            position: absolute;
            inset: 0;
        }
    </style>
    <script src="/static/js/vendor/artplayer.js?v=2"></script>
    <script>if (typeof Artplayer === 'undefined') { document.write('<script src="https://cdn.jsdelivr.net/npm/artplayer@5.2.3/dist/artplayer.js"><\/script>'); }</script>
    <script src="/static/js/vendor/hls.min.js?v=2"></script>
    <script>if (typeof Hls === 'undefined') { document.write('<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.8/dist/hls.min.js"><\/script>'); }</script>
    <script src="https://cdn.jsdelivr.net/npm/dashjs@4.7.4/dist/dash.all.min.js"></script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/shaka-player/4.3.5/shaka-player.compiled.js"></script>
</head>
<body>
    <div id="artplayer"></div>
    <script>
    (function () {
        const TITLE = {{.Title}};
        const VIDEO_URL = {{.VideoURL}};
        const SUBTITLES = {{.SubtitlesJSON}};
        const AUDIO_TRACKS = {{.AudioTracksJSON}};

        const customSettings = [];

        // 1. Multi-Audio Selector
        if (AUDIO_TRACKS && AUDIO_TRACKS.length > 1) {
            var seenAudioCount = {};
            var audioItems = AUDIO_TRACKS.map(function (t, idx) {
                var title = t.title || ('Track ' + (idx + 1));
                seenAudioCount[title] = (seenAudioCount[title] || 0) + 1;
                if (seenAudioCount[title] > 1) {
                    title = title + ' #' + seenAudioCount[title];
                }
                var metaDetail = (t.codec || 'aac') + (t.channels ? (', ' + t.channels + 'ch') : '');
                return {
                    default: idx === 0,
                    html: '<span style="display:inline-flex;align-items:center;gap:6px;">🎵 ' + title + ' <span style="font-size:10px;opacity:0.6;">(' + metaDetail + ')</span></span>',
                    url: t.url,
                    index: idx,
                    displayTitle: title
                };
            });

            customSettings.push({
                icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z"/></svg>',
                width: 240,
                html: 'Audio Track',
                name: 'audio_track',
                tooltip: audioItems[0] ? audioItems[0].displayTitle : 'Default',
                selector: audioItems,
                onSelect: function (item) {
                    if (window.artAudioCtrl) {
                        window.artAudioCtrl.switchAudio(item.url);
                    }
                    return item.displayTitle;
                }
            });
        }

        // 2. Subtitles Selector
        var defaultSubtitle = undefined;
        if (SUBTITLES && SUBTITLES.length > 0) {
            var firstSub = SUBTITLES[0];
            defaultSubtitle = {
                url: firstSub.url,
                type: 'vtt',
                style: {
                    color: '#ffffff',
                    fontSize: '20px',
                    textShadow: '0 2px 4px rgba(0,0,0,0.85)',
                    fontWeight: '600'
                },
                encoding: 'utf-8'
            };

            var seenSubCount = {};
            var subItems = SUBTITLES.map(function (s, idx) {
                var title = s.title || ('Subtitle ' + (idx + 1));
                seenSubCount[title] = (seenSubCount[title] || 0) + 1;
                if (seenSubCount[title] > 1) {
                    title = title + ' #' + seenSubCount[title];
                }
                return {
                    default: idx === 0,
                    html: '<span style="display:inline-flex;align-items:center;gap:6px;">💬 ' + title + ' <span style="font-size:10px;opacity:0.6;">(VTT)</span></span>',
                    url: s.url,
                    displayTitle: title
                };
            });
            subItems.push({
                default: false,
                html: '<span>❌ Turn Off Subtitles</span>',
                url: '',
                displayTitle: 'Off'
            });

            customSettings.push({
                icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2m-9 5H5V7h6zm8 8H5v-2h14zm-4-4H5v-2h10zm4 0h-2v-2h2z"/></svg>',
                width: 240,
                html: 'Subtitles',
                name: 'subtitles',
                tooltip: subItems[0] ? subItems[0].displayTitle : 'Default',
                selector: subItems,
                onSelect: function (item) {
                    if (window.art && window.art.subtitle) {
                        if (item.url) {
                            window.art.subtitle.show = true;
                            window.art.subtitle.switch(item.url, { name: item.displayTitle });
                        } else {
                            window.art.subtitle.show = false;
                        }
                    }
                    return item.displayTitle;
                }
            });
        }

        function AudioBooster() {
            this.ctx = null;
            this.gainNode = null;
            this.compressor = null;
            this.boostLevel = 1.0;
            this.connectedElements = [];
        }
        AudioBooster.prototype.init = function (element) {
            if (!element) return;
            try {
                var AudioCtx = window.AudioContext || window.webkitAudioContext;
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
                    this.ctx.resume().catch(function () {});
                }
                if (this.connectedElements.indexOf(element) === -1) {
                    var source = this.ctx.createMediaElementSource(element);
                    source.connect(this.compressor);
                    this.connectedElements.push(element);
                }
            } catch (e) {
                console.warn('AudioBooster init warning:', e);
            }
        };
        AudioBooster.prototype.setBoost = function (level, element) {
            this.boostLevel = level;
            if (element) {
                this.init(element);
            }
            if (this.gainNode && this.ctx) {
                if (this.ctx.state === 'suspended') {
                    this.ctx.resume().catch(function () {});
                }
                this.gainNode.gain.setTargetAtTime(level, this.ctx.currentTime, 0.05);
            }
        };

        var globalAudioBooster = new AudioBooster();
        function applyAudioBoost(level) {
            var targetElement = (window.artAudioCtrl && window.artAudioCtrl.audio && window.artAudioCtrl.audio.src)
                ? window.artAudioCtrl.audio
                : (window.art ? window.art.video : null);
            globalAudioBooster.setBoost(level, targetElement);
        }

        var savedBoost = 1.0;
        try {
            savedBoost = parseFloat(localStorage.getItem('stream_audio_boost') || '1.0');
            if (isNaN(savedBoost) || savedBoost < 1.0 || savedBoost > 2.0) savedBoost = 1.0;
        } catch (_) {}

        var boostItems = [
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

        var activeBoostItem = boostItems.find(function (b) { return b.default; }) || boostItems[0];

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
                if (window.art && window.art.notice) {
                    window.art.notice.show = 'Audio Boost: ' + item.displayTitle;
                }
                return item.displayTitle;
            }
        });

        var isMobile = /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
        var artOptions = {
            container: '#artplayer',
            url: VIDEO_URL,
            title: TITLE,
            type: VIDEO_URL.indexOf('.m3u8') !== -1 ? 'm3u8' : 'mp4',
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
            playsInline: true,
            autoPlayback: true,
            airplay: true,
            hotkey: true,
            fastForward: true,
            lock: true,
            moreVideoAttr: {
                'playsinline': 'true',
                'webkit-playsinline': 'true',
                'crossorigin': 'anonymous'
            },
            settings: customSettings,
            controls: [],
            customType: {
                m3u8: function (video, url, art) {
                    if (window.Hls && Hls.isSupported()) {
                        if (art.hls) art.hls.destroy();
                        var hls = new Hls({ enableWorker: true, backBufferLength: 90 });
                        hls.loadSource(url);
                        hls.attachMedia(video);
                        art.hls = hls;
                        art.on('destroy', function () { hls.destroy(); });
                    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
                        video.src = url;
                    }
                },
                mpd: function (video, url, art) {
                    if (window.dashjs) {
                        if (art.dash) art.dash.reset();
                        var player = dashjs.MediaPlayer().create();
                        player.initialize(video, url, art.option.autoplay);
                        art.dash = player;
                        art.on('destroy', function () { player.reset(); });
                    }
                },
                mp4: function (video, url, art) {
                    if (window.shaka && shaka.Player.isBrowserSupported()) {
                        if (art.shaka) {
                            try { art.shaka.destroy(); } catch (_) {}
                            art.shaka = null;
                        }
                        var player = new shaka.Player();
                        player.attach(video).then(function() {
                            return player.load(url, null, 'video/mp4');
                        }).then(function() {
                            art.shaka = player;
                            art.on('destroy', function() { player.destroy(); });
                        }).catch(function(err) {
                            console.warn('Shaka player error, falling back to native video.src:', err);
                            video.src = url;
                        });
                    } else {
                        video.src = url;
                    }
                }
            },
            contextmenu: [
                {
                    html: 'Video Info / Stats',
                    click: function (c) {
                        var dur = window.art ? Math.round(window.art.duration) : 0;
                        if (window.art) window.art.notice.show = TITLE + ' (' + dur + 's)';
                        c.show = false;
                    }
                },
                {
                    html: 'Copy Embed Code',
                    click: function (c) {
                        var embedCode = '<iframe src="' + window.location.href + '" width="100%" height="100%" frameborder="0" allowfullscreen><\/iframe>';
                        navigator.clipboard.writeText(embedCode).then(function () {
                            if (window.art) window.art.notice.show = 'Embed code copied to clipboard!';
                        }).catch(function () {});
                        c.show = false;
                    }
                },
                {
                    html: 'Copy Stream Link',
                    click: function (c) {
                        navigator.clipboard.writeText(window.location.href).then(function () {
                            if (window.art) window.art.notice.show = 'Stream link copied to clipboard!';
                        }).catch(function () {});
                        c.show = false;
                    }
                }
            ]
        };

        if (defaultSubtitle) {
            artOptions.subtitle = defaultSubtitle;
        }

        var ArtClass = window.Artplayer || window.ArtPlayer;
        if (!ArtClass) {
            document.getElementById('artplayer').innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:#fff;"><video src="' + VIDEO_URL + '" controls style="max-width:100%;max-height:100%;"></video></div>';
            return;
        }

        var art = new ArtClass(artOptions);
        window.art = art;
        art.muted = false;
        if (art.video) art.video.muted = false;

        function LockstepAudio(artInstance) {
            this.art = artInstance;
            this.video = artInstance.video;
            this.audio = new Audio();
            this.audio.crossOrigin = 'anonymous';
            this.audio.preload = 'auto';
            this.video.muted = false;
            this.audio.muted = false;
            this.audio.volume = this.video.volume > 0 ? this.video.volume : 0.8;
            this.syncTimer = null;
            this.userPaused = this.video.paused;
            this.internalPauseExpected = 0;
            this.switchSeq = 0;
            this.isSwitchingTrack = false;
            this.bindVideoEvents();
        }

        LockstepAudio.prototype.bindVideoEvents = function () {
            var self = this;
            var v = this.video;

            var playAudio = function () {
                self.userPaused = false;
                self.internalPauseExpected = 0;
                if (!self.audio || !self.audio.src || self.isSwitchingTrack) return;

                var startA = function () {
                    if (!self.audio || v.paused || self.userPaused || self.isSwitchingTrack) return;
                    if (Math.abs(v.currentTime - self.audio.currentTime) > 0.03) {
                        self.audio.currentTime = v.currentTime;
                    }
                    self.audio.playbackRate = v.playbackRate;
                    self.audio.play().catch(function () {});
                };

                if (self.audio.readyState >= 2) {
                    startA();
                } else {
                    self.audio.addEventListener('canplay', startA, { once: true });
                }
            };

            v.addEventListener('play', playAudio);
            v.addEventListener('playing', playAudio);

            v.addEventListener('pause', function () {
                if (self.internalPauseExpected > 0) {
                    self.internalPauseExpected--;
                    if (self.audio) self.audio.pause();
                    return;
                }
                self.userPaused = true;
                if (self.audio) self.audio.pause();
            });

            v.addEventListener('seeking', function () {
                if (self.audio) {
                    self.audio.pause();
                    self.audio.currentTime = v.currentTime;
                }
            });

            v.addEventListener('seeked', function () {
                if (self.audio) self.audio.currentTime = v.currentTime;
            });

            v.addEventListener('ratechange', function () {
                if (self.audio) self.audio.playbackRate = v.playbackRate;
            });

            v.addEventListener('volumechange', function () {
                if (self.audio) {
                    self.audio.volume = v.volume;
                    self.audio.muted = v.muted;
                }
            });
        };

        LockstepAudio.prototype.switchAudio = function (audioUrl) {
            this.switchSeq = (this.switchSeq || 0) + 1;
            var seq = this.switchSeq;
            this.isSwitchingTrack = true;

            if (this.syncTimer) {
                clearInterval(this.syncTimer);
                this.syncTimer = null;
            }

            if (!this.audio) {
                this.audio = new Audio();
                this.audio.crossOrigin = 'anonymous';
                this.audio.preload = 'auto';
            }
            this.audio.pause();

            if (!audioUrl) {
                this.isSwitchingTrack = false;
                this.video.muted = false;
                this.audio.removeAttribute('src');
                return;
            }

            var v = this.video;

            // Stop previous audio immediately; keep video playing smoothly without pausing
            v.muted = false;
            var a = this.audio;
            a.pause();
            a.volume = v.volume > 0 ? v.volume : 0.8;
            a.muted = false;

            var self = this;

            var switchTimeout = setTimeout(function () {
                onSwitchError('Audio track load timed out');
            }, 8000);

            var cleanup = function () {
                if (switchTimeout) {
                    clearTimeout(switchTimeout);
                    switchTimeout = null;
                }
                a.removeEventListener('loadedmetadata', onMetadataOrCanPlay);
                a.removeEventListener('canplay', onMetadataOrCanPlay);
                a.removeEventListener('canplay', resumePlayback);
                a.removeEventListener('error', onAudioError);
            };

            var onAudioError = function (e) {
                if (self.switchSeq !== seq || !self.audio) return;
                cleanup();
                self.isSwitchingTrack = false;
                console.warn('Audio track load failed (403/404 or network):', e);
                if (self.art && self.art.notice) {
                    self.art.notice.show = 'Failed to load audio track';
                }
            };

            var onSwitchError = function (msg) {
                if (self.switchSeq !== seq || !self.audio) return;
                cleanup();
                self.isSwitchingTrack = false;
                console.warn(msg);
                if (self.art && self.art.notice) {
                    self.art.notice.show = msg;
                }
            };

            var resumePlayback = function () {
                if (self.switchSeq !== seq || !self.audio) return;
                cleanup();
                self.isSwitchingTrack = false;

                // Live alignment to exact video frame as video kept playing
                var liveTime = v.currentTime;
                if (Math.abs(liveTime - a.currentTime) > 0.03) {
                    a.currentTime = liveTime;
                }
                a.playbackRate = v.playbackRate;

                // Play audio seamlessly if video is active
                if (!self.userPaused && !v.paused) {
                    a.play().catch(function () {});
                }
            };

            var onMetadataOrCanPlay = function () {
                a.removeEventListener('loadedmetadata', onMetadataOrCanPlay);
                a.removeEventListener('canplay', onMetadataOrCanPlay);
                if (self.switchSeq !== seq || !self.audio) return;

                var targetTime = v.currentTime;
                if (targetTime < 0.05) {
                    if (a.readyState >= 3) {
                        resumePlayback();
                    } else {
                        a.addEventListener('canplay', resumePlayback, { once: true });
                    }
                    return;
                }

                var onSeeked = function () {
                    a.removeEventListener('seeked', onSeeked);
                    if (self.switchSeq !== seq || !self.audio) return;
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

            a.addEventListener('waiting', function () {
                if (v.paused || self.userPaused || self.isSwitchingTrack) return;
                self.internalPauseExpected = (self.internalPauseExpected || 0) + 1;
                v.pause();
                var onAudioResume = function () {
                    a.removeEventListener('canplay', onAudioResume);
                    if (!self.userPaused && !self.isSwitchingTrack) {
                        v.play().catch(function () {});
                    }
                };
                a.addEventListener('canplay', onAudioResume, { once: true });
            });

            this.syncTimer = setInterval(function () {
                if (self.audio && !v.paused && !self.userPaused && !self.isSwitchingTrack && !self.audio.paused) {
                    var diff = v.currentTime - self.audio.currentTime;
                    var absDiff = Math.abs(diff);
                    if (absDiff > 0.3) {
                        self.audio.currentTime = v.currentTime;
                        self.audio.playbackRate = v.playbackRate;
                    } else if (diff > 0.03) {
                        self.audio.playbackRate = v.playbackRate * 1.03;
                    } else if (diff < -0.03) {
                        self.audio.playbackRate = v.playbackRate * 0.97;
                    } else {
                        if (self.audio.playbackRate !== v.playbackRate) {
                            self.audio.playbackRate = v.playbackRate;
                        }
                    }
                }
            }, 200);

            a.src = audioUrl;
            a.load();
        };

        window.artAudioCtrl = new LockstepAudio(art);
        if (AUDIO_TRACKS && AUDIO_TRACKS.length > 0) {
            window.artAudioCtrl.switchAudio(AUDIO_TRACKS[0].url);
        }
        if (savedBoost > 1.0) {
            art.on('video:play', function () {
                applyAudioBoost(savedBoost);
            }, { once: true });
        }

        var hbTimer = null;
        var viewerSessionId = Math.random().toString(36).slice(2) + Date.now().toString(36);
        function pingHeartbeat() {
            fetch('/api/stream/heartbeat', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ key: '{{ .Key }}', session_id: viewerSessionId })
            }).catch(function () {});
        }
        art.on('video:play', function () {
            pingHeartbeat();
            if (!hbTimer) hbTimer = setInterval(pingHeartbeat, 25000);
        });
        art.on('video:pause', function () {
            if (hbTimer) { clearInterval(hbTimer); hbTimer = null; }
        });
        art.on('destroy', function () {
            if (hbTimer) { clearInterval(hbTimer); hbTimer = null; }
        });
    })();
    </script>
</body>
</html>`

