package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// fileConfig mirrors the on-disk YAML shape.
type fileConfig struct {
	LB2120 struct {
		DeviceIP string `yaml:"device_ip"`
		ATPort   int    `yaml:"at_port"`
		Password string `yaml:"password"`
	} `yaml:"lb2120"`

	RemoteWrite struct {
		URL string `yaml:"url"`
	} `yaml:"remote_write"`

	Agent struct {
		PollInterval string `yaml:"poll_interval"`
		Instance     string `yaml:"instance"`
		Job          string `yaml:"job"`
		StateFile    string `yaml:"state_file"`
	} `yaml:"agent"`

	Recovery struct {
		ConfirmPolls int    `yaml:"confirm_polls"`
		Cooldown     string `yaml:"cooldown"`
		MaxFailures  int    `yaml:"max_failures"`
		Backoff      string `yaml:"backoff"`
	} `yaml:"recovery"`
}

// Config is the resolved, typed configuration used by the rest of the app.
type Config struct {
	DeviceIP        string
	ATPort          int
	Password        string
	PollInterval    time.Duration
	RemoteWriteURL  string
	StateFile       string
	Instance        string
	Job             string
	ATTimeout       time.Duration
	WebTimeout      time.Duration
	RemoteWriteTO   time.Duration
	ConfirmPolls    int
	Cooldown        time.Duration
	MaxFailures     int
	BackoffDuration time.Duration
}

func parseDurationOr(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

func parseConfig() (Config, error) {
	var configPath string
	flag.StringVar(&configPath, "config", "/opt/lb2120-agent/config.yaml", "path to config.yaml")
	flag.Parse()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %s: %w", configPath, err)
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return Config{}, fmt.Errorf("parse config file %s: %w", configPath, err)
	}

	if fc.LB2120.Password == "" {
		return Config{}, fmt.Errorf("lb2120.password is required in %s", configPath)
	}
	if fc.RemoteWrite.URL == "" {
		return Config{}, fmt.Errorf("remote_write.url is required in %s", configPath)
	}

	c := Config{
		DeviceIP:       fc.LB2120.DeviceIP,
		ATPort:         fc.LB2120.ATPort,
		Password:       fc.LB2120.Password,
		RemoteWriteURL: fc.RemoteWrite.URL,
		StateFile:      fc.Agent.StateFile,
		Instance:       fc.Agent.Instance,
		Job:            fc.Agent.Job,
		ConfirmPolls:   fc.Recovery.ConfirmPolls,
		MaxFailures:    fc.Recovery.MaxFailures,

		ATTimeout:     10 * time.Second,
		WebTimeout:    15 * time.Second,
		RemoteWriteTO: 10 * time.Second,
	}

	if c.DeviceIP == "" {
		c.DeviceIP = "192.168.1.1"
	}
	if c.ATPort == 0 {
		c.ATPort = 5510
	}
	if c.StateFile == "" {
		c.StateFile = "/var/lib/lb2120-agent/state.json"
	}
	if c.Instance == "" {
		c.Instance = "lb2120"
	}
	if c.Job == "" {
		c.Job = "lb2120_agent"
	}
	if c.ConfirmPolls == 0 {
		c.ConfirmPolls = 2
	}
	if c.MaxFailures == 0 {
		c.MaxFailures = 3
	}

	if c.PollInterval, err = parseDurationOr(fc.Agent.PollInterval, 5*time.Minute); err != nil {
		return Config{}, fmt.Errorf("agent.poll_interval: %w", err)
	}
	if c.Cooldown, err = parseDurationOr(fc.Recovery.Cooldown, 15*time.Minute); err != nil {
		return Config{}, fmt.Errorf("recovery.cooldown: %w", err)
	}
	if c.BackoffDuration, err = parseDurationOr(fc.Recovery.Backoff, 2*time.Hour); err != nil {
		return Config{}, fmt.Errorf("recovery.backoff: %w", err)
	}

	return c, nil
}
