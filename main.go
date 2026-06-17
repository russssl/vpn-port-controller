package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/robfig/cron/v3"
)

const (
	DEBUG = "debug"
	INFO  = "info"
	ERROR = "error"
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

func logf(cfg Config, level string, msg string, v ...any) {
	shouldLog := false

	switch cfg.LogLevel {
	case DEBUG:
		shouldLog = true
	case INFO:
		shouldLog = level != DEBUG
	case ERROR:
		shouldLog = level == ERROR
	}

	if shouldLog {
		log.Printf("["+strings.ToUpper(level)+"] "+msg, v...)
	}
}

type Config struct {
	DelugePassword string
	DelugeUrl      string
	GluetunUrl     string
	GluetunApiKey  string
	LogLevel       string
	CronJob        string
}

type PortResponse struct {
	Port  int   `json:"port"`
	Ports []int `json:"ports"`
}

type DelugeBody struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
	Id     int         `json:"id"`
}

type DelugeResponse struct {
	Result interface{} `json:"result"`
	Error  interface{} `json:"error"`
	Id     int         `json:"id"`
}

func getenv(key string, required bool, missing *[]string) string {
	val := os.Getenv(key)
	if required && val == "" {
		*missing = append(*missing, key)
	}
	return val
}

func loadConfig() Config {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, reading from environment instead")
	}

	var missing []string

	cfg := Config{
		DelugePassword: getenv("DELUGE_PASSWORD", true, &missing),
		DelugeUrl:      getenv("DELUGE_URL", true, &missing),
		GluetunUrl:     getenv("GLUETUN_URL", true, &missing),
		GluetunApiKey:  getenv("GLUETUN_API_KEY", true, &missing),
		LogLevel:       getenv("LOG_LEVEL", false, &missing),
		CronJob:        getenv("CRON_JOB", false, &missing),
	}

	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	if cfg.LogLevel == "" {
		cfg.LogLevel = DEBUG
	}
	if cfg.LogLevel != DEBUG && cfg.LogLevel != INFO && cfg.LogLevel != ERROR {
		log.Printf("unrecognized LOG_LEVEL %q, defaulting to debug", cfg.LogLevel)
		cfg.LogLevel = DEBUG
	}

	if len(missing) > 0 {
		log.Fatalf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	return cfg
}

// ---------------- DELUGE ----------------

func delugeRPC(cfg Config, cookie string, body DelugeBody) (DelugeResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return DelugeResponse{}, err
	}

	req, err := http.NewRequest("POST", cfg.DelugeUrl, bytes.NewBuffer(jsonBody))
	if err != nil {
		return DelugeResponse{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", "_session_id="+cookie)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return DelugeResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return DelugeResponse{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return DelugeResponse{}, fmt.Errorf("deluge returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var dr DelugeResponse
	if err := json.Unmarshal(respBody, &dr); err != nil {
		return DelugeResponse{}, fmt.Errorf("failed to parse deluge response: %w", err)
	}

	if dr.Error != nil {
		return dr, fmt.Errorf("deluge RPC error: %v", dr.Error)
	}

	return dr, nil
}

func getDelugeCookie(cfg Config) (string, error) {
	logf(cfg, DEBUG, "starting deluge login")

	body := DelugeBody{
		Method: "auth.login",
		Params: []string{cfg.DelugePassword},
		Id:     1,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", cfg.DelugeUrl, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("deluge login returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var dr DelugeResponse
	if err := json.Unmarshal(respBody, &dr); err != nil {
		return "", fmt.Errorf("failed to parse deluge login response: %w", err)
	}
	if dr.Error != nil {
		return "", fmt.Errorf("deluge login error: %v", dr.Error)
	}

	for _, c := range resp.Cookies() {
		if c.Name == "_session_id" {
			logf(cfg, DEBUG, "deluge cookie received")
			return c.Value, nil
		}
	}

	return "", fmt.Errorf("session cookie not found")
}

// ---------------- GLUETUN ----------------

func getGluetunPort(cfg Config) (PortResponse, error) {
	url := cfg.GluetunUrl + "/v1/portforward"

	logf(cfg, DEBUG, "calling gluetun: %s", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return PortResponse{}, err
	}

	req.Header.Set("X-API-Key", cfg.GluetunApiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return PortResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return PortResponse{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return PortResponse{}, fmt.Errorf("gluetun returned status %d: %s", resp.StatusCode, string(body))
	}

	var pr PortResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return PortResponse{}, fmt.Errorf("failed to parse gluetun response: %w", err)
	}

	// Some Gluetun versions return a "ports" array instead of a single "port".
	if pr.Port == 0 && len(pr.Ports) > 0 {
		pr.Port = pr.Ports[0]
	}

	if pr.Port == 0 {
		return PortResponse{}, fmt.Errorf("gluetun did not return a forwarded port (is port forwarding ready?)")
	}

	return pr, nil
}

// ---------------- APPLY ----------------

func setDelugePort(cfg Config, cookie string, port int) error {
	logf(cfg, DEBUG, "setting deluge port to %d", port)

	body := DelugeBody{
		Method: "core.set_config",
		Params: []map[string]interface{}{
			{
				"listen_ports": []int{port, port},
			},
		},
		Id: 1,
	}

	dr, err := delugeRPC(cfg, cookie, body)
	if err != nil {
		return err
	}

	logf(cfg, DEBUG, "deluge response: %+v", dr.Result)

	return nil
}

// ---------------- TASK ----------------

func runTask(cfg Config) {
	logf(cfg, INFO, "=== starting task ===")

	cookie, err := getDelugeCookie(cfg)
	if err != nil {
		logf(cfg, ERROR, "deluge login failed: %v", err)
		return
	}

	logf(cfg, INFO, "Deluge cookie acquired")

	portResp, err := getGluetunPort(cfg)
	if err != nil {
		logf(cfg, ERROR, "gluetun failed: %v", err)
		return
	}

	logf(cfg, INFO, "VPN port: %d", portResp.Port)

	if err := setDelugePort(cfg, cookie, portResp.Port); err != nil {
		logf(cfg, ERROR, "failed setting deluge port: %v", err)
		return
	}

	logf(cfg, INFO, "=== done ===")
}

// ---------------- MAIN ----------------

func main() {
	cfg := loadConfig()

	if cfg.CronJob == "" {
		logf(cfg, INFO, "CRON_JOB not set, running once")
		runTask(cfg)
		return
	}

	logf(cfg, INFO, "scheduling task with cron expression: %s", cfg.CronJob)

	c := cron.New()
	_, err := c.AddFunc(cfg.CronJob, func() {
		runTask(cfg)
	})
	if err != nil {
		log.Fatalf("invalid CRON_JOB expression %q: %v", cfg.CronJob, err)
	}

	c.Start()
	logf(cfg, INFO, "cron scheduler started, waiting for next run")

	// Block forever; the cron scheduler runs in its own goroutine.
	select {}
}
