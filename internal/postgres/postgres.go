// Package postgres owns PostgreSQL connection pools and schema migrations.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/sarahmaeve/go-prod-change-registry/migrations"
)

// PoolOptions controls the bounded connection pool used by the server.
type PoolOptions struct {
	MaxConnections int
	ConnectTimeout time.Duration
}

// Open creates a pool and verifies that PostgreSQL is reachable before returning.
func Open(ctx context.Context, databaseURL string, opts PoolOptions) (*pgxpool.Pool, error) {
	if opts.MaxConnections <= 0 || opts.MaxConnections > math.MaxInt32 {
		return nil, fmt.Errorf("max connections must be between 1 and %d", math.MaxInt32)
	}
	if opts.ConnectTimeout <= 0 {
		return nil, fmt.Errorf("connect timeout must be greater than 0")
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	poolConfig.MaxConns = int32(opts.MaxConnections)
	poolConfig.ConnConfig.ConnectTimeout = opts.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}

// Migrate applies all embedded PostgreSQL schema migrations.
func Migrate(databaseURL string, connectTimeout time.Duration) (err error) {
	if connectTimeout <= 0 {
		return fmt.Errorf("connect timeout must be greater than 0")
	}
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse PostgreSQL migration configuration: %w", err)
	}
	connConfig.ConnectTimeout = connectTimeout

	db := stdlib.OpenDB(*connConfig)
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("create PostgreSQL migration driver: %w", err)
	}

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("create migration source: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := migrator.Close()
		err = errors.Join(err, sourceErr, databaseErr)
	}()

	// Up checks the version and dirty state under the migration lock. A separate
	// Version call can observe another caller's in-progress migration as dirty.
	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		if dirty, ok := errors.AsType[migrate.ErrDirty](err); ok {
			return fmt.Errorf("database migration is dirty at version %d; refusing to continue until it is repaired", dirty.Version)
		}
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
