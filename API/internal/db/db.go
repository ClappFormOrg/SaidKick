// Package db owns the database connection lifecycle for the sAId-Kick
// Appstore API. Two logical roles are expected on the Postgres side:
//
//   - `app_rw`         : full read/write on every table EXCEPT audit_logs.
//                        It can SELECT audit_logs but not INSERT/UPDATE/DELETE.
//   - `audit_writer`   : INSERT-only on audit_logs. No UPDATE / DELETE.
//
// The application opens one connection pool per role; business code uses
// the rw pool for everything, and the dedicated audit writer is wired
// behind a small AuditLog repository so the role separation cannot be
// accidentally bypassed.
//
// V1 ships with a single combined role (`postgres` superuser via .env) so
// the two pools share a DSN; the V1 ops runbook upgrades to true role
// separation before public OSS launch.
package db

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Config holds the parameters for opening a GORM connection. Built from
// environment variables by Load().
type Config struct {
	// DSN is the libpq-style connection string for the read-write role.
	DSN string

	// AuditDSN is the connection string for the audit_writer role. When
	// empty, the same DSN is reused (V1 default) — see package doc.
	AuditDSN string

	// LogLevel for GORM. One of: silent | error | warn | info. Defaults to
	// warn when unset.
	LogLevel string

	// SlowQueryThreshold for GORM logger (e.g. "200ms").
	SlowQueryThreshold time.Duration
}

// Load reads the environment and returns a Config. Required: DATABASE_URL.
// Optional: AUDIT_DATABASE_URL, GORM_LOG_LEVEL, SLOW_QUERY_THRESHOLD.
func Load() (Config, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	cfg := Config{
		DSN:                dsn,
		AuditDSN:           os.Getenv("AUDIT_DATABASE_URL"),
		LogLevel:           strings.ToLower(os.Getenv("GORM_LOG_LEVEL")),
		SlowQueryThreshold: 200 * time.Millisecond,
	}
	if cfg.AuditDSN == "" {
		cfg.AuditDSN = dsn // V1 default
	}
	if v := os.Getenv("SLOW_QUERY_THRESHOLD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.SlowQueryThreshold = d
		}
	}
	return cfg, nil
}

// Handle bundles the two role-scoped connection pools so callers can pick
// the right one explicitly.
type Handle struct {
	// RW is the read-write pool used by everything that is not an
	// audit-log insert.
	RW *gorm.DB

	// AuditWriter is the INSERT-only pool used by the AuditLog repository.
	// Application code should never use this for SELECTs; use RW.
	AuditWriter *gorm.DB
}

// Open dials Postgres for both roles. Returns a Handle that callers should
// Close() at shutdown.
func Open(cfg Config) (*Handle, error) {
	gormCfg := &gorm.Config{
		Logger: newLogger(cfg.LogLevel, cfg.SlowQueryThreshold),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}

	rw, err := gorm.Open(postgres.Open(cfg.DSN), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("open rw: %w", err)
	}

	aw, err := gorm.Open(postgres.Open(cfg.AuditDSN), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("open audit: %w", err)
	}

	if err := tuneConnPool(rw); err != nil {
		return nil, fmt.Errorf("tune rw pool: %w", err)
	}
	if err := tuneConnPool(aw); err != nil {
		return nil, fmt.Errorf("tune audit pool: %w", err)
	}

	return &Handle{RW: rw, AuditWriter: aw}, nil
}

// Close drains both pools. Safe to call on a nil receiver.
func (h *Handle) Close() error {
	if h == nil {
		return nil
	}
	var errs []error
	for _, db := range []*gorm.DB{h.RW, h.AuditWriter} {
		if db == nil {
			continue
		}
		sqlDB, err := db.DB()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := sqlDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func tuneConnPool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	// Modest defaults — tuned for V1 traffic profile (low TPS gov software).
	// Operators can revise via env once we have real numbers from prod.
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	return nil
}

func newLogger(level string, slow time.Duration) logger.Interface {
	var lvl logger.LogLevel
	switch level {
	case "silent":
		lvl = logger.Silent
	case "error":
		lvl = logger.Error
	case "info":
		lvl = logger.Info
	default:
		lvl = logger.Warn
	}
	return logger.New(
		stdoutWriter{},
		logger.Config{
			SlowThreshold:             slow,
			LogLevel:                  lvl,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
}

type stdoutWriter struct{}

func (stdoutWriter) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stdout, format+"\n", args...)
}
