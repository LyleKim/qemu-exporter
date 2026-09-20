// live-migration-webhook receives Alertmanager webhook POSTs for the
// VMCPUPressureHigh alert and triggers a Nova block live-migration for the
// affected VM. Separate binary from qemu-exporter (CLAUDE.md: webhook 리시버
// 코드 규칙). Read-only/no-Nova-API-calls rules in CLAUDE.md apply to the
// exporter's metadata path only -- this binary's whole job is calling Nova's
// live-migration action, nothing else.
package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var instanceUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type alertmanagerWebhook struct {
	Alerts []struct {
		Status string            `json:"status"`
		Labels map[string]string `json:"labels"`
	} `json:"alerts"`
}

type migrator struct {
	authURL       string
	username      string
	password      string
	userDomain    string
	projectName   string
	projectDomain string
	novaURL       string
	nodeHosts     [2]string
	webhookSecret string
	httpClient    *http.Client

	mu       sync.Mutex
	inFlight map[string]bool
}

func main() {
	nodeHosts := strings.Split(os.Getenv("NODE_HOSTS"), ",")
	if len(nodeHosts) != 2 || nodeHosts[0] == "" || nodeHosts[1] == "" {
		slog.Error("NODE_HOSTS must be exactly two comma-separated hypervisor hostnames")
		os.Exit(1)
	}

	m := &migrator{
		authURL:       os.Getenv("OS_AUTH_URL"),
		username:      os.Getenv("OS_USERNAME"),
		password:      os.Getenv("OS_PASSWORD"),
		userDomain:    os.Getenv("OS_USER_DOMAIN_NAME"),
		projectName:   os.Getenv("OS_PROJECT_NAME"),
		projectDomain: os.Getenv("OS_PROJECT_DOMAIN_NAME"),
		novaURL:       strings.TrimRight(os.Getenv("NOVA_URL"), "/"),
		nodeHosts:     [2]string{nodeHosts[0], nodeHosts[1]},
		webhookSecret: os.Getenv("WEBHOOK_SECRET"),
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		inFlight:      map[string]bool{},
	}
	if m.webhookSecret == "" {
		slog.Error("WEBHOOK_SECRET must be set so /webhook can authenticate Alertmanager")
		os.Exit(1)
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	http.HandleFunc("/webhook", m.handleWebhook)
	slog.Info("live-migration-webhook listening", "addr", listenAddr, "nova_url", m.novaURL, "node_hosts", m.nodeHosts)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func (m *migrator) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if !constantTimeBearerMatch(r.Header.Get("Authorization"), m.webhookSecret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload alertmanagerWebhook
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)

	for _, a := range payload.Alerts {
		if a.Status != "firing" {
			continue
		}
		uuid := a.Labels["instance_uuid"]
		if uuid == "" {
			slog.Warn("alert missing instance_uuid label, skipping")
			continue
		}
		if !instanceUUIDRe.MatchString(uuid) {
			slog.Warn("alert instance_uuid is not a well-formed UUID, skipping", "instance_uuid", uuid)
			continue
		}

		m.mu.Lock()
		if m.inFlight[uuid] {
			m.mu.Unlock()
			slog.Info("migration already in progress, skipping duplicate trigger", "instance_uuid", uuid)
			continue
		}
		m.inFlight[uuid] = true
		m.mu.Unlock()

		go func(uuid string) {
			defer func() {
				m.mu.Lock()
				delete(m.inFlight, uuid)
				m.mu.Unlock()
			}()
			if err := m.triggerLiveMigration(uuid); err != nil {
				slog.Error("live migration trigger failed", "instance_uuid", uuid, "err", err)
			} else {
				slog.Info("live migration triggered", "instance_uuid", uuid)
			}
		}(uuid)
	}
}

func (m *migrator) triggerLiveMigration(instanceUUID string) error {
	token, err := m.keystoneToken()
	if err != nil {
		return fmt.Errorf("keystone auth: %w", err)
	}

	currentHost, err := m.serverHost(token, instanceUUID)
	if err != nil {
		return fmt.Errorf("get server host: %w", err)
	}
	destHost := m.otherHost(currentHost)
	if destHost == "" {
		return fmt.Errorf("current host %q does not match either configured NODE_HOSTS entry %v", currentHost, m.nodeHosts)
	}

	body, _ := json.Marshal(map[string]any{
		"os-migrateLive": map[string]any{
			"host":             destHost,
			"block_migration":  true,
			"disk_over_commit": false,
		},
	})
	req, err := http.NewRequest(http.MethodPost, m.novaURL+"/servers/"+instanceUUID+"/action", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", token)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("nova migrate action returned %d: %s", resp.StatusCode, string(b))
	}
	slog.Info("migrate action accepted", "instance_uuid", instanceUUID, "from_host", currentHost, "to_host", destHost)
	return nil
}

func constantTimeBearerMatch(authHeader, secret string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	got := authHeader[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

func (m *migrator) otherHost(current string) string {
	switch {
	case strings.EqualFold(current, m.nodeHosts[0]):
		return m.nodeHosts[1]
	case strings.EqualFold(current, m.nodeHosts[1]):
		return m.nodeHosts[0]
	default:
		return ""
	}
}

func (m *migrator) serverHost(token, instanceUUID string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, m.novaURL+"/servers/"+instanceUUID, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Token", token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("get server returned %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Server struct {
			Host string `json:"OS-EXT-SRV-ATTR:host"`
		} `json:"server"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Server.Host == "" {
		return "", fmt.Errorf("server response missing OS-EXT-SRV-ATTR:host")
	}
	return out.Server.Host, nil
}

func (m *migrator) keystoneToken() (string, error) {
	body, _ := json.Marshal(map[string]any{
		"auth": map[string]any{
			"identity": map[string]any{
				"methods": []string{"password"},
				"password": map[string]any{
					"user": map[string]any{
						"name":     m.username,
						"domain":   map[string]any{"name": m.userDomain},
						"password": m.password,
					},
				},
			},
			"scope": map[string]any{
				"project": map[string]any{
					"name":   m.projectName,
					"domain": map[string]any{"name": m.projectDomain},
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, m.authURL+"/auth/tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("keystone token request returned %d: %s", resp.StatusCode, string(b))
	}
	token := resp.Header.Get("X-Subject-Token")
	if token == "" {
		return "", fmt.Errorf("keystone response missing X-Subject-Token header")
	}
	return token, nil
}
