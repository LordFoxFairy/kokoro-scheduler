package postgresadapter

import (
	"context"
	"errors"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/config"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool   *pgxpool.Pool
	target config.DatabaseTarget
}

func NewStore(pool *pgxpool.Pool, target config.DatabaseTarget) *Store {
	return &Store{pool: pool, target: target}
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("scheduler PostgreSQL store is not configured")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return errors.New("acquire scheduler PostgreSQL readiness session failed")
	}
	defer conn.Release()
	return checkNamespaceReady(ctx, conn, s.target)
}

// Inspect all readiness facts on one acquired session, without DDL or namespace
// fallback. This lightweight probe is not a full catalog drift check.
func checkNamespaceReady(ctx context.Context, session interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, target config.DatabaseTarget) error {
	if target.SchemaName() == "" {
		return errors.New("scheduler PostgreSQL owner namespace is not configured")
	}
	var ready bool
	if err := session.QueryRow(ctx, `SELECT
		COALESCE(current_schema() = $1, false)
		AND current_setting('search_path') = $1
		AND current_setting('TimeZone') = 'UTC'
		AND EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)
		AND (SELECT count(*) = 4
		       FROM pg_catalog.pg_class c
		       JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		      WHERE n.nspname = $1 AND c.relkind = 'r'
		        AND c.relname IN ('scheduler_schedule', 'scheduler_occurrence',
		                          'scheduler_command_receipt', 'scheduler_dispatch_outbox'))`, target.SchemaName()).Scan(&ready); err != nil {
		return errors.New("inspect scheduler PostgreSQL owner readiness failed")
	}
	if !ready {
		return errors.New("scheduler PostgreSQL owner namespace, UTC session or required facts are not ready")
	}
	return nil
}

func (s *Store) WithinTx(ctx context.Context, run func(ports.TxStore) error) error {
	if s == nil || s.pool == nil || run == nil {
		return errors.New("scheduler PostgreSQL transaction is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := run(&txStore{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type txStore struct {
	tx pgx.Tx
}

var _ ports.Store = (*Store)(nil)
var _ ports.TxStore = (*txStore)(nil)
