package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// APIConfig holds configuration for the API server.
type APIConfig struct {
	Port    string
	Regions []RegionConfig
}

// ControllerConfig holds configuration for the controller service.
type ControllerConfig struct {
	ReconcileInterval   time.Duration
	ProvisioningTimeout time.Duration
	PendingExpiryDays   int
	MaxRetries          int
	BGP                 BGPConfig
}

// BGPConfig holds configuration for the BGP service.
type BGPConfig struct {
	LocalASN        uint32
	RouterID        string
	ListenPort      int
	RouteReflectors []string
}

// AgentConfig holds configuration for the per-host agent.
type AgentConfig struct {
	ReconcileInterval time.Duration
	ControllerAddr    string
}

// RegionConfig maps a region name to an ID.
type RegionConfig struct {
	Name string
	ID   uint32
}

// LoadAPIConfig reads API config from environment variables with defaults.
func LoadAPIConfig() APIConfig {
	return APIConfig{
		Port: envOr("PORT", "8080"),
		Regions: []RegionConfig{
			{Name: "us-east-1", ID: 0},
			{Name: "us-west-1", ID: 1},
			{Name: "eu-west-1", ID: 2},
		},
	}
}

// LoadControllerConfig reads controller config from environment variables with defaults.
func LoadControllerConfig() ControllerConfig {
	return ControllerConfig{
		ReconcileInterval:   envDuration("RECONCILE_INTERVAL", 10*time.Second),
		ProvisioningTimeout: envDuration("PROVISIONING_TIMEOUT", 5*time.Minute),
		PendingExpiryDays:   envInt("PENDING_EXPIRY_DAYS", 7),
		MaxRetries:          envInt("MAX_RETRIES", 10),
		BGP:                 LoadBGPConfig(),
	}
}

// LoadBGPConfig reads BGP config from environment variables with defaults.
func LoadBGPConfig() BGPConfig {
	return BGPConfig{
		LocalASN:        uint32(envInt("BGP_LOCAL_ASN", 65000)),
		RouterID:        envOr("BGP_ROUTER_ID", "10.0.0.1"),
		ListenPort:      envInt("BGP_LISTEN_PORT", 179),
		RouteReflectors: envList("BGP_ROUTE_REFLECTORS", []string{"10.0.0.100"}),
	}
}

// LoadAgentConfig reads agent config from environment variables with defaults.
func LoadAgentConfig() AgentConfig {
	return AgentConfig{
		ReconcileInterval: envDuration("AGENT_RECONCILE_INTERVAL", 30*time.Second),
		ControllerAddr:    envOr("CONTROLLER_ADDR", "localhost:9090"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envList(key string, fallback []string) []string {
	if v := os.Getenv(key); v != "" {
		return strings.Split(v, ",")
	}
	return fallback
}
