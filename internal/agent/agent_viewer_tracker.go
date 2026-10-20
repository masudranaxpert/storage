package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	agentDedupeMu    sync.Mutex
	agentDedupe      = make(map[string]time.Time)
	agentHeartbeatMu sync.Mutex
	agentHeartbeats  = make(map[string]time.Time)
)

func (a *Agent) recordViewerHeartbeat(key, clientIP string) {
	if a.CoordinatorURL == "" || key == "" {
		return
	}
	dedupeKey := clientIP + ":" + key
	agentHeartbeatMu.Lock()
	last, seen := agentHeartbeats[dedupeKey]
	now := time.Now()
	if seen && now.Sub(last) < 25*time.Second {
		agentHeartbeatMu.Unlock()
		return
	}
	agentHeartbeats[dedupeKey] = now
	if len(agentHeartbeats) > 10000 {
		for k, t := range agentHeartbeats {
			if now.Sub(t) > 60*time.Second {
				delete(agentHeartbeats, k)
			}
		}
	}
	agentHeartbeatMu.Unlock()

	go func() {
		payload, err := json.Marshal(map[string]string{
			"key":        key,
			"session_id": clientIP,
		})
		if err != nil {
			return
		}
		coord := strings.TrimRight(a.CoordinatorURL, "/")
		req, err := http.NewRequest(http.MethodPost, coord+"/api/stream/heartbeat", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if a.Secret != "" {
			req.Header.Set("X-Cluster-Secret", a.Secret)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}()
}

func (a *Agent) recordPlayHit(key, clientIP string) {
	if a.CoordinatorURL == "" || key == "" {
		return
	}
	dedupeKey := clientIP + ":" + key
	agentDedupeMu.Lock()
	last, seen := agentDedupe[dedupeKey]
	now := time.Now()
	if seen && now.Sub(last) < 30*time.Minute {
		agentDedupeMu.Unlock()
		return
	}
	agentDedupe[dedupeKey] = now
	if len(agentDedupe) > 10000 {
		for k, t := range agentDedupe {
			if now.Sub(t) > 30*time.Minute {
				delete(agentDedupe, k)
			}
		}
	}
	agentDedupeMu.Unlock()

	go func() {
		payload, err := json.Marshal(map[string]string{
			"key":       key,
			"client_ip": clientIP,
		})
		if err != nil {
			return
		}
		coord := strings.TrimRight(a.CoordinatorURL, "/")
		req, err := http.NewRequest(http.MethodPost, coord+"/api/lifecycle/hit", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if a.Secret != "" {
			req.Header.Set("Authorization", "Bearer "+a.Secret)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
}
