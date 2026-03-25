package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tahini.dev/tahini/internal/db"
	"tahini.dev/tahini/internal/server"
	"tahini.dev/tahini/internal/tofu"
)

func main() {
	dataDir := os.Getenv("TAHINI_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}

	adminUser := os.Getenv("TAHINI_ADMIN_USER")
	if adminUser == "" {
		adminUser = "admin"
	}

	adminPass := os.Getenv("TAHINI_ADMIN_PASS")
	if adminPass == "" {
		log.Fatal("TAHINI_ADMIN_PASS is required")
	}

	addr := os.Getenv("TAHINI_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	tofuBin := os.Getenv("TOFU_BIN")
	if tofuBin == "" {
		tofuBin = "tofu"
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = filepath.Join(dataDir, "tahini.db")
	}
	if strings.HasPrefix(dsn, "postgres") {
		log.Printf("database: postgres")
	} else {
		log.Printf("database: sqlite (%s)", dsn)
	}
	database, err := db.New(dsn)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	executor := &tofu.Executor{
		DataDir: dataDir,
		Bin:     tofuBin,
	}

	var idleTimeout time.Duration
	if raw := os.Getenv("TAHINI_IDLE_TIMEOUT_MINUTES"); raw != "" {
		if mins, err := strconv.Atoi(raw); err == nil && mins > 0 {
			idleTimeout = time.Duration(mins) * time.Minute
		}
	}

	srv := server.New(database, executor, server.Config{
		AdminUser:     adminUser,
		AdminPass:     adminPass,
		SessionSecret: os.Getenv("TAHINI_SESSION_SECRET"),
		Addr:          addr,
		InternalURL:   os.Getenv("TAHINI_INTERNAL_URL"),
		IdleTimeout:   idleTimeout,
	})

	if err := database.SeedDefaultBlueprints(); err != nil {
		log.Printf("warning: failed to seed default blueprints: %v", err)
	}

	log.Printf("tahini listening on %s", addr)
	if err := srv.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
