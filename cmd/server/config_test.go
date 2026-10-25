package main

import (
	"testing"
	"time"
)

func TestConfigurationDefaultsAndOverrides(t *testing.T) {
	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":8088" || cfg.maxRooms != 1000 || cfg.roomTTL != 0 || cfg.dbPath != "data/unour.db" || cfg.adminConfig != "" {
		t.Fatalf("bad defaults: %+v", cfg)
	}
	env := map[string]string{"UNO_ADMIN_CONFIG": "/private/admin.json", "UNO_ADDR": "127.0.0.1:9090", "UNO_MAX_ROOMS": "42", "UNO_ROOM_TTL": "30m", "UNO_DB_PATH": "custom/rooms.db", "UNO_ALLOWED_ORIGINS": "http://localhost:5173, https://uno.example.com", "UNO_TRUSTED_PROXIES": "172.16.0.0/12, fd00::/64"}
	cfg, err = loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxRooms != 42 || cfg.roomTTL != 30*time.Minute || cfg.dbPath != "custom/rooms.db" || len(cfg.origins) != 2 || cfg.origins[1] != "https://uno.example.com" || cfg.adminConfig != "/private/admin.json" {
		t.Fatalf("ignored overrides: %+v", cfg)
	}
	if len(cfg.trustedProxies) != 2 || cfg.trustedProxies[0].String() != "172.16.0.0/12" || cfg.trustedProxies[1].String() != "fd00::/64" {
		t.Fatalf("ignored trusted proxies: %v", cfg.trustedProxies)
	}
}
func TestInvalidConfigurationIsRejected(t *testing.T) {
	for key, value := range map[string]string{"UNO_ADDR": "missing-port", "UNO_MAX_ROOMS": "-1", "UNO_ROOM_TTL": "-1s", "UNO_DB_PATH": ":memory:", "UNO_TRUSTED_PROXIES": "0.0.0.0"} {
		t.Run(key, func(t *testing.T) {
			if _, err := loadConfig(func(k string) string {
				if k == key {
					return value
				}
				return ""
			}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
