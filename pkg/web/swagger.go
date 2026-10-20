package web

import (
	"encoding/json"
	"net/http"
)

// SwaggerUIHTML returns a modern, responsive Swagger UI embedded page.
const SwaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>StreamMesh API Documentation & Explorer</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css" />
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@500;600;700&display=swap" rel="stylesheet">
    <style>
        body {
            margin: 0;
            padding: 0;
            background: #fbfaf8;
            font-family: 'Plus Jakarta Sans', -apple-system, BlinkMacSystemFont, sans-serif;
        }
        .topbar {
            display: none;
        }
        .swagger-ui .info {
            margin: 24px 0;
        }
        .swagger-ui .info .title {
            color: #1c1917;
            font-weight: 800;
            font-size: 28px;
        }
        .swagger-ui .btn.execute {
            background-color: #ea580c;
            border-color: #ea580c;
            color: #ffffff;
            font-weight: 700;
            border-radius: 8px;
        }
        .swagger-ui .btn.execute:hover {
            background-color: #c2410c;
        }
        .swagger-ui .opblock.opblock-post {
            border-color: #f97316;
            background: rgba(249, 115, 22, 0.05);
        }
        .swagger-ui .opblock.opblock-post .opblock-summary-method {
            background: #ea580c;
        }
        .swagger-ui .opblock.opblock-get {
            border-color: #16a34a;
            background: rgba(22, 163, 74, 0.05);
        }
        .swagger-ui .opblock.opblock-get .opblock-summary-method {
            background: #16a34a;
        }
        .swagger-ui .opblock.opblock-delete {
            border-color: #dc2626;
            background: rgba(220, 38, 38, 0.05);
        }
        .swagger-ui .opblock.opblock-delete .opblock-summary-method {
            background: #dc2626;
        }

        /* Dark Mode Overrides */
        html.dark, body.dark {
            background: #111317 !important;
            color: #f3f4f6 !important;
        }
        html.dark .swagger-ui {
            color: #f3f4f6;
        }
        html.dark .swagger-ui .info .title,
        html.dark .swagger-ui .info li,
        html.dark .swagger-ui .info p,
        html.dark .swagger-ui .info table,
        html.dark .swagger-ui .opblock .opblock-summary-operation-id,
        html.dark .swagger-ui .opblock .opblock-summary-path,
        html.dark .swagger-ui .opblock .opblock-summary-path__deprecated,
        html.dark .swagger-ui .opblock-description-wrapper p,
        html.dark .swagger-ui .opblock-external-docs-wrapper p,
        html.dark .swagger-ui .opblock-title_normal p,
        html.dark .swagger-ui .response-col_status,
        html.dark .swagger-ui table thead tr td,
        html.dark .swagger-ui table thead tr th,
        html.dark .swagger-ui .parameter__name,
        html.dark .swagger-ui .parameter__type,
        html.dark .swagger-ui .tab li button.tablinks {
            color: #f3f4f6 !important;
        }
        html.dark .swagger-ui .opblock .opblock-summary-description {
            color: #9ca3af !important;
        }
        html.dark .swagger-ui .scheme-container {
            background: #17191f !important;
            box-shadow: none !important;
            border-bottom: 1px solid #23262f !important;
        }
        html.dark .swagger-ui select {
            background-color: #1a1d24 !important;
            color: #f3f4f6 !important;
            border-color: #23262f !important;
        }
        html.dark .swagger-ui input[type=text] {
            background: #1a1d24 !important;
            color: #f3f4f6 !important;
            border-color: #23262f !important;
        }
        html.dark .swagger-ui .opblock-body {
            background: #111317 !important;
        }
        html.dark .swagger-ui .opblock-section-header {
            background: #17191f !important;
        }
        html.dark .swagger-ui .opblock.opblock-post {
            background: rgba(249, 115, 22, 0.08) !important;
            border-color: rgba(249, 115, 22, 0.4) !important;
        }
        html.dark .swagger-ui .opblock.opblock-get {
            background: rgba(16, 185, 129, 0.08) !important;
            border-color: rgba(16, 185, 129, 0.4) !important;
        }
        html.dark .swagger-ui .opblock.opblock-delete {
            background: rgba(239, 68, 68, 0.08) !important;
            border-color: rgba(239, 68, 68, 0.4) !important;
        }
        html.dark .swagger-ui section.models {
            border-color: #23262f !important;
        }
        html.dark .swagger-ui section.models .model-container {
            background: #17191f !important;
        }
        html.dark .swagger-ui .model-box {
            background: #17191f !important;
        }
        html.dark .swagger-ui .responses-inner {
            background: #111317 !important;
        }
        html.dark .swagger-ui .response-col_links {
            color: #9ca3af !important;
        }
        html.dark .swagger-ui textarea {
            background: #090a0d !important;
            color: #f3f4f6 !important;
            border-color: #23262f !important;
        }
        html.dark .swagger-ui .highlight-code {
            background: #090a0d !important;
        }
        html.dark .swagger-ui .microlight {
            background: #090a0d !important;
            color: #f3f4f6 !important;
        }
        html.dark .swagger-ui .btn.authorize {
            color: #fb923c !important;
            border-color: #fb923c !important;
        }
        html.dark .swagger-ui .btn.authorize svg {
            fill: #fb923c !important;
        }
        html.dark .swagger-ui .model-title {
            color: #f3f4f6 !important;
        }
        html.dark .swagger-ui .prop-type {
            color: #60a5fa !important;
        }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js"></script>
    <script>
        function applyTheme(theme) {
            if (theme === 'dark') {
                document.documentElement.classList.add('dark');
                document.body.classList.add('dark');
            } else {
                document.documentElement.classList.remove('dark');
                document.body.classList.remove('dark');
            }
        }
        try {
            var theme = localStorage.getItem('stream_theme') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
            applyTheme(theme);
        } catch(e) {}
        window.addEventListener('message', (e) => {
            if (e.data && e.data.type === 'theme-change') {
                applyTheme(e.data.theme);
            }
        });

        window.onload = () => {
            window.ui = SwaggerUIBundle({
                url: '/api/openapi.json',
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIBundle.SwaggerUIStandalonePreset
                ],
                layout: "BaseLayout"
            });
        };
    </script>
</body>
</html>`

// jsonSchema builds a JSON-schema object for a body type.
func jsonSchema(objectSchema map[string]interface{}, required []string) map[string]interface{} {
	s := map[string]interface{}{"type": "object", "properties": objectSchema}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// jsonResponse is a shortcut for a JSON response entry.
func jsonResponse(description string) map[string]interface{} {
	return map[string]interface{}{"description": description}
}

// GetOpenAPISpec returns the clean OpenAPI 3.0.0 specification for the public Video Ingest & Management API.
func GetOpenAPISpec() map[string]interface{} {
	return map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":   "Stream Mesh // Video API (v1)",
			"version": "1.0.0",
			"description": "Distributed video ingest, fast-start streaming, and storage lifecycle.\n\n" +
				"### Core Endpoints:\n" +
				"1. **POST /api/v1/files** — Ingest video by URL. The coordinator automatically probes headers, downloads, validates codecs, remuxes to fast-start MP4/CMAF, and streams to storage.\n" +
				"2. **GET /api/v1/files/{key}** — Player streaming URLs (video, multi-track audio, subtitles) masked with an opaque ticket (t_...) and anti-leech token.\n" +
				"3. **GET /api/v1/files/{key}/info** — Live lifecycle progress (`downloading` → `processing` → `transferring` → `completed`), node placement, and transfer speed.\n" +
				"4. **GET /api/v1/files/{key}/metadata** — Technical media metadata (codecs, bitrates, audio channels, subtitle tracks).\n" +
				"5. **DELETE /api/v1/files/{key}** — Delete a file or batch delete multiple files (single key or comma-separated `key1,key2,key3`).\n" +
				"6. **DELETE /api/v1/files** — Batch delete multiple files with JSON body `{\"keys\": [\"key1\", \"key2\"]}` or `?keys=key1,key2`.\n" +
				"7. **POST /api/v1/files/{key}/heartbeat** — Live playback heartbeat to track real-time concurrent viewers (`active_viewers`).",
		},
		"servers": []map[string]interface{}{
			{
				"url":         "/",
				"description": "Cluster Coordinator",
			},
		},
		"tags": []map[string]interface{}{
			{"name": "Files v1", "description": "Public video ingest, status, and deletion APIs"},
		},
		"paths": map[string]interface{}{
			"/api/v1/files": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Ingest a video by URL",
					"description": "Send only the URL — everything else is automatic. The master HEAD-probes the source: filename comes from Content-Disposition (URL path as fallback), size from Content-Length, MIME from headers. It then reserves a tier block, picks a processing worker, downloads, validates magic bytes, remuxes to CMAF, and streams the result to the storage node.",
					"tags":        []string{"Files v1"},
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": jsonSchema(map[string]interface{}{
									"url": map[string]interface{}{
										"type":        "string",
										"description": "Direct HTTP/HTTPS video link — the only required field",
										"example":     "https://cdn.example.com/movie.mkv",
									},
								}, []string{"url"}),
							},
						},
					},
					"responses": map[string]interface{}{
						"201": jsonResponse("Accepted — job created and dispatched"),
						"400": jsonResponse("Invalid request body"),
						"422": jsonResponse("Not a video / size cannot be determined"),
						"503": jsonResponse("No processing worker online"),
						"507": jsonResponse("No storage: tier blocks full"),
					},
				},
				"delete": map[string]interface{}{
					"summary":     "Batch delete files from the cluster",
					"description": "Delete multiple files at once. Send a JSON body with keys array or query parameter `?keys=key1,key2`.",
					"tags":        []string{"Files v1"},
					"requestBody": map[string]interface{}{
						"required": false,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": jsonSchema(map[string]interface{}{
									"keys": map[string]interface{}{
										"type":        "array",
										"items":       map[string]interface{}{"type": "string"},
										"description": "List of file keys to delete",
										"example":     []string{"3hjMG311oM4LVqYm", "luTS1LxGvGSGkPCm"},
									},
								}, []string{"keys"}),
							},
						},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Batch deletion result with deleted array and failed map"),
						"400": jsonResponse("No keys provided"),
					},
				},
			},
			"/api/v1/files/{key}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Player streaming URLs & anti-leech token",
					"description": "Returns direct multi-track playback URLs for video players (video_url, audio_tracks, subtitles) masked with an opaque playback ticket (t_...) and a 4-hour anti-leech expiry token.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{
							"name":        "key",
							"in":          "path",
							"required":    true,
							"description": "16-character file key",
							"schema":      map[string]interface{}{"type": "string"},
							"example":     "HELH5QjXyETgEwUW",
						},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Player streaming payload"),
						"404": jsonResponse("Unknown key"),
					},
				},
				"delete": map[string]interface{}{
					"summary":     "Delete file(s) from the cluster",
					"description": "Removes the media folder from the owning worker node and drops database records. Supports deleting a single key or multiple comma-separated keys for batch deletion (e.g. key1,key2,key3).",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{
							"name":        "key",
							"in":          "path",
							"required":    true,
							"description": "Single key (e.g. HELH5QjXyETgEwUW) or multiple comma-separated keys (e.g. key1,key2,key3)",
							"schema":      map[string]interface{}{"type": "string"},
							"example":     "HELH5QjXyETgEwUW",
						},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Deleted successfully"),
						"404": jsonResponse("Unknown key"),
						"502": jsonResponse("Worker unreachable"),
					},
				},
			},
			"/api/v1/files/{key}/info": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Full file lifecycle, progress & placement info",
					"description": "Live lifecycle status for one file: downloading → processing → transferring → completed. Details node placement, byte progress, transfer speeds, and storage tier.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{
							"name":        "key",
							"in":          "path",
							"required":    true,
							"description": "16-character file key",
							"schema":      map[string]interface{}{"type": "string"},
							"example":     "HELH5QjXyETgEwUW",
						},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Full file status and placement info"),
						"404": jsonResponse("Unknown key"),
					},
				},
			},
			"/api/v1/files/{key}/metadata": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "File audio, video & subtitle metadata",
					"description": "Returns probed technical metadata: video codec/bitrate/resolution, multi-language audio streams, and extracted subtitle tracks.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{
							"name":        "key",
							"in":          "path",
							"required":    true,
							"description": "16-character file key",
							"schema":      map[string]interface{}{"type": "string"},
							"example":     "HELH5QjXyETgEwUW",
						},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Probed media metadata"),
						"404": jsonResponse("Unknown key"),
					},
				},
			},
			"/api/v1/files/{key}/heartbeat": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Live playback heartbeat & active viewer ping",
					"description": "Sends a lightweight periodic heartbeat (every 20–30s) while a user is actively playing a video. The coordinator maintains this session in memory for 60 seconds to accurately report real-time concurrent active viewers on the dashboard.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{
							"name":        "key",
							"in":          "path",
							"required":    true,
							"description": "16-character file key",
							"schema":      map[string]interface{}{"type": "string"},
							"example":     "HELH5QjXyETgEwUW",
						},
					},
					"requestBody": map[string]interface{}{
						"description": "Optional session identifier for deduplication (falls back to client IP)",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"session_id": map[string]interface{}{"type": "string", "example": "viewer_x9a8b7"},
									},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Heartbeat recorded, returns current active viewers"),
					},
				},
			},
		},
	}
}

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(GetOpenAPISpec())
}

func (s *Server) handleSwaggerDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(SwaggerUIHTML))
}
