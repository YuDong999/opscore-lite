Local patch against github.com/caretdev/go-irisnative v0.2.1:

- Added `//go:build !windows` to `src/connection/user_posix.go`.
  Upstream ships `user_windows.go` with a Windows filename suffix, but
  `user_posix.go` has no build constraint, so Windows builds compile both
  files and fail with `userCurrent redeclared`.
- Made `Connection.Disconnect` close the underlying TCP connection after
  sending the protocol disconnect message, so `database/sql` closes do not
  leak sockets.
- Made `Connection.BeginTx` return the `START TRANSACTION` error instead of
  marking the connection as in-transaction when the server rejected the begin.
