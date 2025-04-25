package config

import (
	"log"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type PomodoroConfig struct {
	FocusMinutes      int `mapstructure:"focus_minutes"`
	ShortBreakMinutes int `mapstructure:"short_break_minutes"`
	LongBreakMinutes  int `mapstructure:"long_break_minutes"`
	LongBreakInterval int `mapstructure:"long_break_interval"`
}

type Config struct {
	DatabasePath              string         `mapstructure:"database_path"`
	CollectMode               string         `mapstructure:"collect_mode"` // "always" or "focus"
	CollectionIntervalSeconds int            `mapstructure:"collection_interval_seconds"`
	KeypressCollection        bool           `mapstructure:"keypress_collection"` // Ignored for now
	Pomodoro                  PomodoroConfig `mapstructure:"pomodoro"`
}

func LoadConfig(configPath string) (*Config, error) {
	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("config")                // name of config file (without extension)
		viper.SetConfigType("yaml")                  // REQUIRED if the config file does not have the extension in the name
		viper.AddConfigPath(".")                     // optionally look for config in the working directory
		viper.AddConfigPath("$HOME/.config/quantme") // call multiple times to add many search paths
		viper.AddConfigPath("/etc/quantme/")         // path to look for the config file in
	}

	viper.SetEnvPrefix("QUANTME")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv() // read in environment variables that match

	// Set defaults
	viper.SetDefault("database_path", "quantme.db")
	viper.SetDefault("collect_mode", "always")
	viper.SetDefault("collection_interval_seconds", 2)
	viper.SetDefault("keypress_collection", false)
	viper.SetDefault("pomodoro.focus_minutes", 25)
	viper.SetDefault("pomodoro.short_break_minutes", 5)
	viper.SetDefault("pomodoro.long_break_minutes", 15)
	viper.SetDefault("pomodoro.long_break_interval", 4)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found; ignore error if defaults are okay
			log.Println("Config file not found, using defaults.")
		} else {
			// Config file was found but another error was produced
			return nil, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	if cfg.CollectionIntervalSeconds < 1 {
		log.Println("Warning: collection_interval_seconds too low, setting to 1")
		cfg.CollectionIntervalSeconds = 1
	}
	if cfg.CollectMode != "always" && cfg.CollectMode != "focus" {
		log.Printf("Warning: invalid collect_mode '%s', defaulting to 'always'", cfg.CollectMode)
		cfg.CollectMode = "always"
	}

	log.Printf("Configuration loaded: %+v", cfg)
	return &cfg, nil
}

func (p PomodoroConfig) FocusDuration() time.Duration {
	return time.Duration(p.FocusMinutes) * time.Minute
}
func (p PomodoroConfig) ShortBreakDuration() time.Duration {
	return time.Duration(p.ShortBreakMinutes) * time.Minute
}
func (p PomodoroConfig) LongBreakDuration() time.Duration {
	return time.Duration(p.LongBreakMinutes) * time.Minute
}
