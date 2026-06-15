package uow

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// bd-6dnrw.44 item 8: warmup must retry dial/handshake transients (the child
// server accepts TCP before the SQL engine answers) and stay permanent on
// real refusals (auth, SQL errors, the remote-migrate gate).
func TestIsRetryableWarmupError(t *testing.T) {
	retryable := map[string]error{
		"dial refused":      &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		"wrapped net error": fmt.Errorf("uow: pin connection: %w", &net.OpError{Op: "read", Err: syscall.ECONNRESET}),
		"bad conn":          fmt.Errorf("exec: %w", driver.ErrBadConn),
		"invalid conn":      mysql.ErrInvalidConn,
		"eof handshake":     io.EOF,
		"deadlock":          &mysql.MySQLError{Number: 1213},
		"lock wait":         &mysql.MySQLError{Number: 1205},
	}
	for name, err := range retryable {
		assert.True(t, isRetryableWarmupError(err), "%s must be retryable: %v", name, err)
	}

	permanent := map[string]error{
		"nil":           nil,
		"access denied": &mysql.MySQLError{Number: 1045},
		"unknown db":    &mysql.MySQLError{Number: 1049},
		"plain error":   errors.New("something structural"),
		"gate refusal":  &schema.RemoteMigrateGateError{},
	}
	for name, err := range permanent {
		assert.False(t, isRetryableWarmupError(err), "%s must be permanent: %v", name, err)
	}
}
