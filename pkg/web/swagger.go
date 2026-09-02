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
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js"></script>
    <script>
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

// GetOpenAPISpec returns the complete OpenAPI 3.0.0 JSON specification for the
// Stream Mesh API. It documents the current v1 system end to end:
// cluster control plane, file ingest (URL + upload), job lifecycle,
// streaming, and node administration.
func GetOpenAPISpec() map[string]interface{} {
	return map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":   "Stream Mesh // Distributed Cluster & File Ingest API (v1)",
			"version": "3.0.0",
			"description": "Distributed video ingest, multi-tier storage pooling, and CMAF/fMP4 packaging.\n\n" +
				"Two ingest modes:\n" +
				"1. URL mode — POST /api/v1/files {url} — just the URL; filename, size, and MIME are auto-detected from the source. The cluster downloads, validates, remuxes to CMAF, places the file on a tier block, and streams it.\n" +
				"2. Upload mode — reserve a slot, then PUT raw bytes (or multipart) to the returned upload_url.",
		},
		"servers": []map[string]interface{}{
			{
				"url":         "/",
				"description": "Current Cluster Coordinator",
			},
		},
		"tags": []map[string]interface{}{
			{"name": "Files v1", "description": "Primary file ingest + lifecycle API"},
			{"name": "Cluster Control Plane", "description": "Node inventory, resource pool, provisioning"},
			{"name": "Streaming", "description": "Byte-range HLS/CMAF media delivery"},
		},
		"paths": map[string]interface{}{
			// ------------------------------------------------ ----------
			// FILES v1 — the primary system
			// ----------------------------------------------------------
			"/api/v1/files": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Ingest a video by URL",
					"description": "Send only the URL — everything else is automatic. The master HEAD-probes the source: filename comes from Content-Disposition (URL path as fallback), size from Content-Length, MIME from headers. It then reserves a tier block, picks a processing worker, downloads, validates magic bytes, remuxes to CMAF, and streams the result to the storage node.",
					"tags":     []string{"Files v1"},
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
				"get": map[string]interface{}{
					"summary": "List all files in the library",
					"description": "Returns every file record with live state, progress, speed, placement, and stream URL — newest first.",
					"tags":  []string{"Files v1"},
					"responses": map[string]interface{}{
						"200": jsonResponse("Array of file statuses under files[]"),
					},
				},
			},
			"/api/v1/files/{key}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "File status & stream URL",
					"description": "Live lifecycle status for one file: downloading → processing → transferring → completed. worker_node_id = the node running the download/remux; placement.node_id = the storage node holding the file. Stream URL appears once completed.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{"name": "key", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}, "example": "3hjMG311oM4LVqYm"},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("File status"),
						"404": jsonResponse("Unknown key"),
					},
				},
				"delete": map[string]interface{}{
					"summary":     "Delete a file from the cluster",
					"description": "Removes the media folder from the owning worker node and drops both DB rows.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{"name": "key", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("Deleted"),
						"404": jsonResponse("Unknown key"),
						"502": jsonResponse("Worker unreachable"),
					},
				},
			},
			"/api/v1/files-upload": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Reserve an upload slot",
					"description": "Creates an awaiting_upload job and returns a one-shot upload_url. Body: {filename, size_bytes}.",
					"tags":        []string{"Files v1"},
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": jsonSchema(map[string]interface{}{
									"filename":   map[string]interface{}{"type": "string", "example": "movie.mkv"},
									"size_bytes": map[string]interface{}{"type": "integer", "format": "int64"},
								}, []string{"filename"}),
							},
						},
					},
					"responses": map[string]interface{}{
						"201": jsonResponse("Slot reserved — upload_url returned"),
						"400": jsonResponse("Invalid body"),
					},
				},
			},
			"/api/v1/files/{key}/upload": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Upload bytes for a reserved slot",
					"description": "Streams the client body (raw octet-stream or multipart 'file' part) to the assigned worker, which validates magic bytes and remuxes to CMAF.",
					"tags":        []string{"Files v1"},
					"parameters": []map[string]interface{}{
						{"name": "key", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/octet-stream": map[string]interface{}{
								"schema": map[string]interface{}{"type": "string", "format": "binary"},
							},
							"multipart/form-data": map[string]interface{}{
								"schema": jsonSchema(map[string]interface{}{
									"file": map[string]interface{}{"type": "string", "format": "binary"},
								}, nil),
							},
						},
					},
					"responses": map[string]interface{}{
						"202": jsonResponse("Upload accepted — processing started"),
						"404": jsonResponse("Unknown key"),
						"409": jsonResponse("Slot not awaiting upload"),
					},
				},
			},
			// ------------------------------------------------ ----------
			// CLUSTER CONTROL PLANE
			// ----------------------------------------------------------
			"/api/nodes": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "List cluster nodes & live telemetry",
					"description": "All registered nodes with CPU/RAM/disk stats, capabilities (aria2c/ffmpeg), agent port, media path, version, and online status.",
					"tags":        []string{"Cluster Control Plane"},
					"responses": map[string]interface{}{
						"200": jsonResponse("Array of node records"),
					},
				},
			},
			"/api/pool": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Aggregated cluster resource pool",
					"description": "Total/active/offline nodes, summed storage, RAM, and CPU cores across the fleet.",
					"tags":        []string{"Cluster Control Plane"},
					"responses": map[string]interface{}{
						"200": jsonResponse("Pool summary"),
					},
				},
			},
			"/api/tiers": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Storage tiers & blocks",
					"description": "Resolved tier definitions (NVMe/SSD/HDD) with their blocks, owners, quotas, and live usage.",
					"tags":        []string{"Cluster Control Plane"},
					"responses": map[string]interface{}{
						"200": jsonResponse("Tier list"),
					},
				},
			},
			"/api/nodes/provision": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "1-Click remote VPS provisioning",
					"description": "Provisions a remote VPS over SSH: installs the agent binary, aria2c, ffmpeg, and registers systemd. Agent advertises the VPS public/VPC IP for SeaweedFS-style node peering (ports 2052 + coordinator 1212/9090 must be reachable).",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"host":            map[string]interface{}{"type": "string", "example": "203.0.113.10"},
										"user":            map[string]interface{}{"type": "string", "example": "root"},
										"password":        map[string]interface{}{"type": "string"},
										"node_name":       map[string]interface{}{"type": "string", "example": "vps-01"},
										"advertise_addr":  map[string]interface{}{"type": "string", "example": "203.0.113.10"},
										"coordinator_url": map[string]interface{}{"type": "string", "example": "http://198.51.100.1:1212"},
									},
								},
							},
						},
					},
					"tags": []string{"Cluster Control Plane"},
					"responses": map[string]interface{}{
						"200": jsonResponse("Provisioning successful"),
						"502": jsonResponse("Provisioning error"),
					},
				},
			},
			"/api/health": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Coordinator health probe",
					"tags":        []string{"Cluster Control Plane"},
					"responses": map[string]interface{}{
						"200": jsonResponse(`{"status":"ok"}`),
					},
				},
			},
			// ------------------------------------------------ ----------
			// STREAMING
			// ----------------------------------------------------------
			"/stream/{key}/master.m3u8": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "HLS manifest for a completed file",
					"description": "Served by the owning worker's agent (see stream_url in file status). Supports byte-range requests for fMP4 segments.",
					"tags":        []string{"Streaming"},
					"parameters": []map[string]interface{}{
						{"name": "key", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{
						"200": jsonResponse("HLS playlist"),
						"404": jsonResponse("Not placed on this node"),
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
