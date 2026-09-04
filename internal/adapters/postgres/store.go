package postgresadapter

import (
	"context"
	"errors"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("scheduler PostgreSQL store is not configured")
	}
	return s.pool.Ping(ctx)
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
