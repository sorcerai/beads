package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/steveyegge/beads/internal/storage/domain/db"
)

type doltServerTx struct {
	conn *sql.Conn
	done bool
}

var _ Tx = (*doltServerTx)(nil)

func (t *doltServerTx) Runner() db.Runner {
	return t.conn
}

func (t *doltServerTx) Commit(ctx context.Context, message string) error {
	if t.done {
		return errors.New("uow: commit: already done")
	}
	t.done = true
	defer t.releaseConn()
	_, err := t.conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?);", message)
	if err != nil {
		// The session still holds the open transaction; releasing the conn
		// like this would hand the next borrower a session whose START
		// TRANSACTION implicitly commits these orphaned writes
		// (bd-6dnrw.44 item 7). Roll back first; if even that fails,
		// poison the conn so the pool discards the session.
		if _, rbErr := t.conn.ExecContext(ctx, "ROLLBACK;"); rbErr != nil {
			t.poisonConn()
		}
		return err
	}
	return nil
}

func (t *doltServerTx) Rollback(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	defer t.releaseConn()
	if _, err := t.conn.ExecContext(ctx, "ROLLBACK;"); err != nil {
		// Same hazard as a failed commit: an open tx must not ride a pooled
		// session back to the next borrower.
		t.poisonConn()
		return err
	}
	return nil
}

func (t *doltServerTx) RollbackUnlessCommitted(ctx context.Context) {
	if !t.done {
		_ = t.Rollback(ctx)
	}
}

// poisonConn marks the underlying driver connection bad so the pool discards
// it on Close instead of recycling a session with an open transaction (same
// pattern as bd-578h9.17 item 4).
func (t *doltServerTx) poisonConn() {
	if t.conn != nil {
		_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// releaseConn returns the pinned session to the pool. The conn reference is
// deliberately kept: a closed *sql.Conn answers every Runner method with
// sql.ErrConnDone (QueryRowContext surfaces it via Row.Err), so a
// use-after-commit Runner fails with an error instead of the nil-pointer
// panic that nil-ing the field caused. Close is idempotent for our purposes
// (a second call just returns ErrConnDone, which we ignore).
func (t *doltServerTx) releaseConn() {
	if t.conn != nil {
		_ = t.conn.Close()
	}
}
