package config

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/joho/godotenv"
)

type Config struct {
	AppPort string
	AppEnv  string

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBDSN      string

	JWTSecret      string
	JWTExpiryHours int
}

var (
	instance *Config
	one      sync.Once
)

func GetInstance() (*Config, error) {
	var err error

	one.Do(func() {
		_ = godotenv.Load()

		cfg := &Config{
			AppPort: getEnv("APP_PORT", "8080"),
			AppEnv:  getEnv("APP_ENV", "development"),

			DBHost:     getEnv("DB_HOST", "localhost"),
			DBPort:     getEnv("DB_PORT", "5432"),
			DBUser:     getEnv("DB_USER", "taskuser"),
			DBPassword: getEnv("DB_PASSWORD", ""),
			DBName:     getEnv("DB_NAME", "taskdb"),

			JWTSecret: getEnv("JWT_SECRET", ""),
		}

		cfg.JWTExpiryHours, err = strconv.Atoi(getEnv("JWT_EXPIRY_HOURS", "24"))
		if err != nil {
			err = fmt.Errorf("config: JWT_EXPIRY_HOURS must be an integer: %v", err)
			return
		}

		cfg.DBDSN = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName,
		)

		if vErr := cfg.validate(); vErr != nil {
			err = vErr
			return
		}

		instance = cfg
	})

	if err != nil {
		return nil, err
	}
	return instance, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func (c *Config) validate() error {
	required := map[string]string{
		"DB_PASSWORD": c.DBPassword,
		"JWT_SECRET":  c.JWTSecret,
	}
	for key, val := range required {
		if val == "" {
			return fmt.Errorf("config: %s must be set", key)
		}
	}

	return nil
}
