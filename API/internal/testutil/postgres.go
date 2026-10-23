// Package testutil provides shared fixtures for integration tests.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// templateDB is the database the schema is migrated into once per package.
// Every test gets its own copy via CREATE DATABASE ... TEMPLATE.
const templateDB = "saidkick_template"

// Credentials for the throwaway test container only.
const (
	pgUser = "postgres"
	pgPass = "postgres"
)

// pg is the per-package Postgres server shared by every NewPostgresDB call
// in one test binary.
var pg struct {
	once sync.Once
	err  error

	container testcontainers.Container
	host      string
	port      string
	admin     *sql.DB // connected to the "postgres" maintenance database

	// createMu serialises CREATE DATABASE calls so parallel tests never race
	// on the template.
	createMu sync.Mutex

	// mainMu guards mainActive, set while RunMain is executing m.Run.
	mainMu     sync.Mutex
	mainActive bool
}

// RunMain runs the package's tests and stops the shared Postgres container
// afterwards. Packages that call NewPostgresDB must use it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) }
//
// The container starts lazily on the first NewPostgresDB call, so -short
// runs and -run filters that skip every database test never start Docker.
func RunMain(m *testing.M) int {
	pg.mainMu.Lock()
	pg.mainActive = true
	pg.mainMu.Unlock()

	code := m.Run()

	pg.mainMu.Lock()
	pg.mainActive = false
	pg.mainMu.Unlock()
	stopPostgres()
	return code
}

// NewPostgresDB returns a db.Handle on a fresh database cloned from the
// package's migrated template. Each call gets its own database, so tests
// using it may run with t.Parallel(). The handle is closed and the database
// dropped via t.Cleanup.
//
// The first call in a package starts a Postgres 16 container and runs all
// schema migrations once; later calls only clone the template. The package
// must call RunMain from TestMain so the container is stopped at the end.
//
// Tests using this function require Docker. They are automatically skipped in
// -short mode.
func NewPostgresDB(t *testing.T) *db.Handle {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test: requires Docker (-short)")
	}

	pg.mainMu.Lock()
	active := pg.mainActive
	pg.mainMu.Unlock()
	if !active {
		t.Fatal("testutil: NewPostgresDB needs func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) } in this package")
	}

	pg.once.Do(func() { pg.err = startPostgres() })
	if pg.err != nil {
		t.Fatalf("testutil: postgres: %v", pg.err)
	}

	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := createFromTemplate(t.Context(), name); err != nil {
		t.Fatalf("testutil: create database %s: %v", name, err)
	}
	// Registered first so it runs last, after the handle below is closed.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := pg.admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
			t.Logf("testutil: drop database %s: %v", name, err)
		}
	})

	dsn := dsnFor(pg.host, pg.port, name)
	handle, err := db.Open(db.Config{DSN: dsn, AuditDSN: dsn, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("testutil: open db: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Logf("testutil: close db: %v", err)
		}
	})
	return handle
}

// startPostgres starts the container, creates the template database and
// migrates it. It runs once per test binary.
func startPostgres() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image: "postgres:16-alpine",
		Env: map[string]string{
			"POSTGRES_DB":       "saidkick_test",
			"POSTGRES_USER":     pgUser,
			"POSTGRES_PASSWORD": pgPass,
		},
		// Durability is irrelevant for throwaway test data. The connection
		// limit leaves headroom for parallel tests, each holding two pools.
		Cmd:          []string{"postgres", "-c", "fsync=off", "-c", "max_connections=300"},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	// Kept even on error so stopPostgres can clean up a half-started container.
	pg.container = container
	if err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		return fmt.Errorf("container host: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		return fmt.Errorf("mapped port: %w", err)
	}
	pg.host, pg.port = host, port.Port()

	admin, err := sql.Open("pgx", dsnFor(pg.host, pg.port, "postgres"))
	if err != nil {
		return fmt.Errorf("open admin connection: %w", err)
	}
	pg.admin = admin
	if err := admin.PingContext(ctx); err != nil {
		return fmt.Errorf("ping admin connection: %w", err)
	}

	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+templateDB+`"`); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	if err := migrateTemplate(ctx); err != nil {
		return err
	}
	// Nothing may connect to the template from here on: CREATE DATABASE
	// refuses to copy a database that has other sessions.
	if _, err := admin.ExecContext(ctx, `ALTER DATABASE "`+templateDB+`" WITH ALLOW_CONNECTIONS false`); err != nil {
		return fmt.Errorf("lock template database: %w", err)
	}
	return nil
}

// migrateTemplate runs the schema migrations against the template database
// and closes every connection to it before returning.
func migrateTemplate(ctx context.Context) (err error) {
	dsn := dsnFor(pg.host, pg.port, templateDB)
	handle, err := db.Open(db.Config{DSN: dsn, AuditDSN: dsn, LogLevel: "silent"})
	if err != nil {
		return fmt.Errorf("open template database: %w", err)
	}
	defer func() {
		if cerr := handle.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close template database: %w", cerr)
		}
	}()
	if err := db.Migrate(ctx, handle); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

// createFromTemplate clones the migrated template into a new database.
func createFromTemplate(ctx context.Context, name string) error {
	pg.createMu.Lock()
	defer pg.createMu.Unlock()
	_, err := pg.admin.ExecContext(ctx, `CREATE DATABASE "`+name+`" TEMPLATE "`+templateDB+`"`)
	return err
}

// stopPostgres closes the admin connection and terminates the container.
func stopPostgres() {
	if pg.admin != nil {
		if err := pg.admin.Close(); err != nil {
			log.Printf("testutil: close admin connection: %v", err)
		}
	}
	if err := testcontainers.TerminateContainer(pg.container); err != nil {
		log.Printf("testutil: terminate postgres: %v", err)
	}
}

func dsnFor(host, port, dbname string) string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, pgUser, pgPass, dbname,
	)
}
