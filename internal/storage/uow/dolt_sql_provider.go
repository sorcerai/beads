package uow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	db "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/schema"
)

const (
	defaultBranch           = "main"
	defaultProxyIdleTimeout = 30 * time.Second
)

type doltSQLProvider struct {
	defaultBranch string
	db            *sql.DB
}

var (
	_ UnitOfWorkProvider = (*doltSQLProvider)(nil)
	_ TxProvider         = (*doltSQLProvider)(nil)
)

func (p *doltSQLProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	return NewUOW(ctx, p)
}

func (p *doltSQLProvider) Close(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	db := p.db
	p.db = nil
	return db.Close()
}

func (p *doltSQLProvider) BeginTx(ctx context.Context) (Tx, error) {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("uow: pin connection: %w", err)
	}

	_, err = conn.ExecContext(ctx, "START TRANSACTION;")
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("uow: failed to start transaction: %w", err)
	}

	return &doltServerTx{
		conn: conn,
	}, nil
}

func (p *doltSQLProvider) initSchema(ctx context.Context, database string, hasRemoteProbe func() bool) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = 15 * time.Second
	return backoff.Retry(func() error {
		// Dial/handshake failures here are the NORMAL warmup case — the
		// freshly spawned child server can accept TCP before the SQL engine
		// answers — so only mark genuinely permanent errors Permanent
		// (bd-6dnrw.44 item 8).
		conn, err := p.db.Conn(ctx)
		if err != nil {
			if isRetryableWarmupError(err) {
				return fmt.Errorf("uow: pin connection: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: pin connection: %w", err))
		}
		defer conn.Close()

		ddl := db.NewDDLSQLRepository(conn)
		if err := ddl.CreateDatabaseIfNotExists(ctx, database); err != nil {
			if isRetryableWarmupError(err) {
				return fmt.Errorf("uow: creating database: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: creating database: %w", err))
		}
		if err := ddl.UseDatabase(ctx, database); err != nil {
			if isRetryableWarmupError(err) {
				return fmt.Errorf("uow: switching to database: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: switching to database: %w", err))
		}

		// #4259: refuse to silently auto-apply pending migrations to a
		// remote-backed database — the same gate the dolt and embeddeddolt
		// store opens run (bd-6dnrw.28: this third store-open path used to
		// bypass it). hasRemoteProbe is the on-disk fallback for a freshly
		// started child server whose dolt_remotes table is still empty
		// (GH#2315); nil disables it (e.g. external servers with no local
		// data dir). A gate refusal is permanent — never retried into a
		// migration.
		if err := schema.CheckRemoteMigrateGateWithRemoteCheck(ctx, conn, hasRemoteProbe); err != nil {
			if isRetryableWarmupError(err) {
				return fmt.Errorf("uow: remote-migrate gate: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: remote-migrate gate: %w", err))
		}

		if _, err := schema.MigrateUpWithLock(ctx, conn, database); err != nil {
			if isRetryableWarmupError(err) || schema.IsMigrationLockError(err) {
				return fmt.Errorf("uow: migrate: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: migrate: %w", err))
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

func buildDSN(ep proxy.Endpoint, database, user, password string) string {
	return util.DoltServerDSN{
		Host:     ep.Host,
		Port:     ep.Port,
		User:     user,
		Password: password,
		Database: database,
	}.String()
}

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("uow: open db: %w", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("uow: ping db: %w", err), conn.Close())
	}
	return conn, nil
}

// openAndInitSchema connects to the proxied server, initializes the schema,
// and returns a ready provider. hasRemoteProbe is the remote-migrate gate's
// on-disk remote probe (nil to rely on dolt_remotes alone — see initSchema).
func openAndInitSchema(ctx context.Context, ep proxy.Endpoint, database, rootUser, rootPassword string, hasRemoteProbe func() bool) (UnitOfWorkProvider, error) {
	initDB, err := openDB(ctx, buildDSN(ep, "", rootUser, rootPassword))
	if err != nil {
		return nil, err
	}

	initProvider := &doltSQLProvider{
		defaultBranch: defaultBranch,
		db:            initDB,
	}

	if err := initProvider.initSchema(ctx, database, hasRemoteProbe); err != nil {
		_ = initDB.Close()
		return nil, fmt.Errorf("uow: init schema: %w", err)
	}

	if err := initDB.Close(); err != nil {
		return nil, fmt.Errorf("uow: close init db: %w", err)
	}

	dbConn, err := openDB(ctx, buildDSN(ep, database, rootUser, rootPassword))
	if err != nil {
		return nil, err
	}

	return &doltSQLProvider{
		defaultBranch: defaultBranch,
		db:            dbConn,
	}, nil
}
