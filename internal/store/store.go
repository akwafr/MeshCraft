// Package store provides PostgreSQL access and embedded migrations.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	var pool, err = pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgx pool: %w", err)
	}
	if err = pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies missing embedded migrations, in order.
func (s *Store) Migrate(ctx context.Context) error {
	var _, err = s.pool.Exec(ctx,
		`create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())`)
	if err != nil {
		return err
	}
	var entries, dirErr = migrationsFS.ReadDir("migrations")
	if dirErr != nil {
		return dirErr
	}
	var names = make([]string, 0, len(entries))
	var e fs.DirEntry
	for _, e = range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var name string
	for _, name = range names {
		var done bool
		if err = s.pool.QueryRow(ctx,
			`select exists(select 1 from schema_migrations where name = $1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		var sql, readErr = migrationsFS.ReadFile("migrations/" + name)
		if readErr != nil {
			return readErr
		}
		var tx, txErr = s.pool.Begin(ctx)
		if txErr != nil {
			return txErr
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, `insert into schema_migrations (name) values ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
