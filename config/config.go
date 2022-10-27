package config

import (
	_ "embed"
	"os"
	"strings"
	"x-ui/pkg/config"
)

//go:embed config.yml
var basicConfig []byte

type LogLevel string

type Config struct {
	Debug    bool   `yaml:"debug"`
	Name     string `yaml:"name"`
	Version  string `yaml:"version"`
	DBPath   string `yaml:"db_path"`
	LogLever string `yaml:"log_level"`
	PWD      string `yaml:"pwd"`
}

const (
	Debug LogLevel = "debug"
	Info  LogLevel = "info"
	Warn  LogLevel = "warn"
	Error LogLevel = "error"
)

var cfg = &Config{}

func init() {
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	err = config.ParseByte(basicConfig, cfg)
	if err != nil {
		panic(err)
	}
	err = config.ReadLocalConfigs(cfg)
	if err != nil {
		panic(err)
	}
	cfg.PWD = pwd
}

func GetVersion() string {
	return strings.TrimSpace(cfg.Version)
}

func GetName() string {
	return strings.TrimSpace(cfg.Name)
}

func GetLogLevel() LogLevel {
	if IsDebug() {
		return Debug
	}
	logLevel := cfg.LogLever
	if logLevel == "" {
		return Info
	}
	return LogLevel(logLevel)
}

func IsDebug() bool {
	return cfg.Debug
}

func GetDBPath() string {
	return cfg.DBPath
}

func GetCFG() *Config {
	return cfg
}
