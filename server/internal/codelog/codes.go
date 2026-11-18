package codelog

import (
	"context"
	"errors"
	"regexp"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

var errnoNames = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENOSPC: "ENOSPC",
	syscall.ELOOP: "ELOOP", syscall.ENOTDIR: "ENOTDIR", syscall.EISDIR: "EISDIR", syscall.EMFILE: "EMFILE",
	syscall.ENFILE: "ENFILE", syscall.EIO: "EIO", syscall.EROFS: "EROFS", syscall.EBUSY: "EBUSY",
	syscall.EDQUOT: "EDQUOT", syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.ECONNREFUSED: "ECONNREFUSED",
	syscall.ECONNRESET: "ECONNRESET", syscall.ETIMEDOUT: "ETIMEDOUT", syscall.EPIPE: "EPIPE",
}

var sqlstate = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// PGCode is EPG_<SQLSTATE> for a server error, ECONNREFUSED and the like for a connect failure, else "".
func PGCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && sqlstate.MatchString(pgErr.Code) {
		return "EPG_" + pgErr.Code
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name := errnoNames[errno]; name != "" {
			return name
		}
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return "EPG_CONNECT"
	}
	return ""
}

// Of names an error by a fixed code, never its text: "" for nil, then timeouts, errno names, SQLSTATEs and Code().
func Of(err error) string {
	var errno syscall.Errno
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "ETIMEDOUT"
	case errors.Is(err, context.Canceled):
		return "ECANCELED"
	case errors.As(err, &errno):
		if name := errnoNames[errno]; name != "" {
			return name
		}
		return "ERRNO"
	}
	if code := PGCode(err); code != "" {
		return code
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return Clean(coded.Code())
	}
	return "EOTHER"
}
