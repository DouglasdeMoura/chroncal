package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/viper"
)

// SMTPConfig holds SMTP connection settings for EMAIL action alarms.
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`
}

type Config struct {
	DB        string     `mapstructure:"db"`
	NerdFonts bool       `mapstructure:"nerd_fonts"`
	ProductID string     `mapstructure:"product_id"`
	SMTP      SMTPConfig `mapstructure:"smtp"`
}

// Load reads configuration with precedence: env > config file > defaults.
// The caller is responsible for applying flag overrides on top.
func Load() Config {
	v := newViper()

	if dir, err := configDir(); err == nil {
		v.AddConfigPath(filepath.Join(dir, "chroncal"))
	}

	v.ReadInConfig() // ignore error — file is optional

	var cfg Config
	v.Unmarshal(&cfg)
	return cfg
}

// newViper creates a pre-configured Viper instance with CHRONCAL_ env prefix
// and bindings for all known config keys.
func newViper() *viper.Viper {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.SetEnvPrefix("CHRONCAL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind env vars so Unmarshal picks them up even without a config file.
	v.BindEnv("db")
	v.BindEnv("nerd_fonts")
	v.BindEnv("product_id")
	v.BindEnv("smtp.host")
	v.BindEnv("smtp.port")
	v.BindEnv("smtp.username")
	v.BindEnv("smtp.password")
	v.BindEnv("smtp.from")

	return v
}

func configDir() (string, error) {
	if runtime.GOOS == "linux" {
		if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
			return dir, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
	return os.UserConfigDir()
}
