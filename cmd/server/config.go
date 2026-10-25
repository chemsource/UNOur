package main

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type config struct {
	secureCookies  bool
	addr           string
	maxRooms       int
	roomTTL        time.Duration
	origins        []string
	trustedProxies []netip.Prefix
	dbPath         string
	adminConfig    string
	rulesDir       string
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{addr: ":8088", maxRooms: 1000, dbPath: "data/unour.db", rulesDir: "rules"}
	if path := getenv("UNO_RULES_DIR"); path != "" {
		if strings.TrimSpace(path) == "" {
			return config{}, fmt.Errorf("UNO_RULES_DIR must be a rule directory path")
		}
		cfg.rulesDir = path
	}
	if path := getenv("UNO_ADMIN_CONFIG"); path != "" {
		if strings.TrimSpace(path) == "" {
			return config{}, fmt.Errorf("UNO_ADMIN_CONFIG must be a private configuration file path")
		}
		cfg.adminConfig = path
	}
	if value := getenv("UNO_TRUSTED_PROXIES"); value != "" {
		for _, part := range strings.Split(value, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
			if err != nil {
				return config{}, fmt.Errorf("UNO_TRUSTED_PROXIES must contain valid CIDRs: %w", err)
			}
			cfg.trustedProxies = append(cfg.trustedProxies, prefix.Masked())
		}
	}
	if value := getenv("UNO_SECURE_COOKIES"); value != "" {
		var err error
		cfg.secureCookies, err = strconv.ParseBool(value)
		if err != nil {
			return config{}, fmt.Errorf("UNO_SECURE_COOKIES must be a boolean")
		}
	}
	if v := getenv("UNO_DB_PATH"); v != "" {
		if strings.TrimSpace(v) == "" || v == ":memory:" {
			return config{}, fmt.Errorf("UNO_DB_PATH must be a persistent file path")
		}
		cfg.dbPath = v
	}
	if v := getenv("UNO_ADDR"); v != "" {
		cfg.addr = v
	}
	_, port, err := net.SplitHostPort(cfg.addr)
	if err != nil {
		return config{}, fmt.Errorf("UNO_ADDR must be host:port: %w", err)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return config{}, fmt.Errorf("UNO_ADDR has an invalid port")
	}
	if v := getenv("UNO_MAX_ROOMS"); v != "" {
		cfg.maxRooms, err = strconv.Atoi(v)
		if err != nil || cfg.maxRooms <= 0 {
			return config{}, fmt.Errorf("UNO_MAX_ROOMS must be a positive integer")
		}
	}
	if v := getenv("UNO_ROOM_TTL"); v != "" {
		cfg.roomTTL, err = time.ParseDuration(v)
		if err != nil || cfg.roomTTL < 0 {
			return config{}, fmt.Errorf("UNO_ROOM_TTL must be zero (disabled) or a positive duration")
		}
	}
	if v := getenv("UNO_ALLOWED_ORIGINS"); v != "" {
		for _, origin := range strings.Split(v, ",") {
			origin = strings.TrimSpace(origin)
			if origin == "" {
				return config{}, fmt.Errorf("UNO_ALLOWED_ORIGINS contains an empty origin")
			}
			cfg.origins = append(cfg.origins, origin)
		}
	}
	return cfg, nil
}
