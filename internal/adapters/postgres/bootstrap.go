package postgresadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	schedulerdatabase "github.com/LordFoxFairy/kokoro-scheduler/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ApplySchemaToEmptyDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("db:apply-schema requires a PostgreSQL pool")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var databaseName, schemaName string
	if err := tx.QueryRow(ctx, `SELECT current_database(), current_schema()`).Scan(&databaseName, &schemaName); err != nil {
		return err
	}
	lockIdentity := "kokoro-scheduler:db:apply-schema:" + databaseName + ":" + schemaName
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockIdentity); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT tablename
		  FROM pg_catalog.pg_tables
		 WHERE schemaname = current_schema()
		 ORDER BY tablename`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, table)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(tables) != 0 {
		return fmt.Errorf("db:apply-schema requires an empty database schema; found tables: %s", strings.Join(tables, ", "))
	}
	if _, err := tx.Exec(ctx, schedulerdatabase.Schema); err != nil {
		return fmt.Errorf("execute canonical database/schema.sql: %w", err)
	}
	return tx.Commit(ctx)
}
