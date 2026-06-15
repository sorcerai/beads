package uow

import (
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"syscall"

	mysql "github.com/go-sql-driver/mysql"
)

// isSerializationError returns true if the error is a Dolt/MySQL serialization
// failure that guarantees the transaction was rolled back. Safe to retry.
//   - 1213 (ER_LOCK_DEADLOCK): concurrent transactions conflict at commit time
//   - 1205 (ER_LOCK_WAIT_TIMEOUT): lock wait exceeded, transaction rolled back
func isSerializationError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
}

// isRetryableWarmupError reports whether an error during provider warmup is
// plausibly transient. The managed dolt child (or the proxy relaying to it)
// can accept TCP before the SQL engine is ready, so dial, reset, and
// handshake failures must retry within the warmup window rather than abort
// it (bd-6dnrw.44 item 8). Serialization failures are retryable everywhere;
// anything else (auth refusal, SQL errors, the remote-migrate gate) is
// genuinely permanent.
func isRetryableWarmupError(err error) bool {
	if err == nil {
		return false
	}
	if isSerializationError(err) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, mysql.ErrInvalidConn) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET)
}
