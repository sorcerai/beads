package uow

// bd-6dnrw.44 item 7: a failed DOLT_COMMIT used to release the pinned conn
// with the transaction still open; the next borrower's START TRANSACTION
// would implicitly commit the orphaned writes. These tests pin the repair
// sequence: rollback on commit failure, and poison the conn (pool discard,
// observable via db.Stats) when even the rollback fails.

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockTxProvider(t *testing.T) (*doltSQLProvider, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })
	return &doltSQLProvider{defaultBranch: defaultBranch, db: mockDB}, mock
}

func TestDoltServerTxCommitFailureRollsBackBeforeRelease(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(errors.New("commit exploded"))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "msg")
	require.ErrorContains(t, err, "commit exploded")
	require.NoError(t, mock.ExpectationsWereMet(), "ROLLBACK must run on the session before the conn is released")
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "rolled-back session is clean and may return to the pool")
}

func TestDoltServerTxCommitAndRollbackFailurePoisonsConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(errors.New("commit exploded"))
	mock.ExpectExec("ROLLBACK").WillReturnError(errors.New("rollback exploded too"))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "msg")
	require.ErrorContains(t, err, "commit exploded")
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "session with an open tx must be discarded, not pooled")
}

func TestDoltServerTxRollbackFailurePoisonsConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ROLLBACK").WillReturnError(errors.New("rollback exploded"))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Rollback(context.Background())
	require.ErrorContains(t, err, "rollback exploded")
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "session with an open tx must be discarded, not pooled")
}

func TestBeginTxStartTransactionFailureReleasesConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnError(errors.New("no tx for you"))

	_, err := p.BeginTx(context.Background())
	require.ErrorContains(t, err, "no tx for you")
	assert.Equal(t, 0, p.db.Stats().InUse, "pinned conn must not leak when START TRANSACTION fails")
}

func TestDoltServerTxRunnerAfterCommitErrorsInsteadOfPanicking(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DOLT_COMMIT").WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(context.Background(), "msg"))

	r := tx.Runner()
	require.NotNil(t, r, "Runner must stay usable for error reporting after commit")

	_, err = r.ExecContext(context.Background(), "SELECT 1")
	assert.ErrorIs(t, err, sql.ErrConnDone, "exec on a committed tx must error, not panic")
	_, err = r.QueryContext(context.Background(), "SELECT 1")
	assert.ErrorIs(t, err, sql.ErrConnDone, "query on a committed tx must error, not panic")
	row := r.QueryRowContext(context.Background(), "SELECT 1")
	require.NotNil(t, row)
	assert.ErrorIs(t, row.Err(), sql.ErrConnDone, "row on a committed tx must carry the error, not panic")
}
