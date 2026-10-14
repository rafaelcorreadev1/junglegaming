// Package testdb gives each test its own migrated PostgreSQL schema.
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/testenv"
)

func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testenv.DatabaseURL(t)
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})

	applyMigrations(t, pool)
	return pool
}

func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found in %s (%v)", dir, err)
	}
	slices.Sort(files)

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	for _, file := range files {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		// The simple protocol is the one that accepts several statements in one call.
		if _, err := conn.Conn().PgConn().Exec(ctx, string(sql)).ReadAll(); err != nil {
			t.Fatalf("apply %s: %v", strings.TrimPrefix(file, dir), err)
		}
	}
}
