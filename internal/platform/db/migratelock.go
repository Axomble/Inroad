package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Serializing migrators BEFORE golang-migrate gets involved.
//
// golang-migrate already takes an advisory lock, and that lock is the problem.
// Its pgx/v5 driver waits for it with a blocking `SELECT pg_advisory_lock(..)` —
// a statement that stays RUNNING, and so holds a snapshot, for as long as the
// wait lasts. `CREATE INDEX CONCURRENTLY` must wait out every snapshot older
// than its own before it can finish. So when two processes migrate the same
// database at once and the first reaches a CONCURRENTLY file, the first waits
// for the second's snapshot while the second waits for the first's lock: a
// deadlock Postgres resolves by aborting the index build, which leaves the
// schema DIRTY at that version. Parallel integration packages on a fresh test
// database and replicas that migrate on boot both hit it.
//
// The fix is to never wait inside a statement. Every entry point below first
// takes a SEPARATE session-level advisory lock by POLLING pg_try_advisory_lock
// — each try is a statement that returns at once, and between tries the
// session is idle and holds no snapshot. Only the holder of that lock ever
// constructs a migrator, so golang-migrate's own lock is always uncontended and
// its blocking wait never happens.
//
// The lock is taken for EVERY migrator, including Version: opening the driver
// runs ensureVersionTable, which takes golang-migrate's lock too, so a status
// probe during a deploy is exactly as able to deadlock a CONCURRENTLY build as
// a second migrator is.

// migrationLockKey is this package's advisory-lock key. It must differ from
// golang-migrate's own key, which that library derives from the database
// name; this is an arbitrary fixed value ("inroadmg" as bytes). Advisory locks
// are scoped to the database, so two databases on one server never contend.
const migrationLockKey int64 = 0x696e726f61646d67

// migrationLockPoll is the pause between lock attempts: short enough that a
// waiter starts promptly after the holder finishes, long enough that N waiters
// polling do not load the server.
const migrationLockPoll = 250 * time.Millisecond

// migrationLockTimeout bounds how long a process waits for another's migration.
// Generous because a migration can legitimately run for minutes (a backfill,
// an index build on a large table); a waiter that gives up returns an error
// rather than proceeding, so the bound only decides how long a stuck deploy
// takes to say so.
const migrationLockTimeout = 15 * time.Minute

// ErrMigrationLockTimeout is returned when another process held the migration
// lock for longer than the wait allowed.
var ErrMigrationLockTimeout = errors.New("db: timed out waiting for another process's migration to finish")

// withMigrationLock runs fn while holding the migration lock on url's database.
//
// The exported migrate functions take no context (neither does golang-migrate's
// API), and they are top-level operations — a binary's startup, a CLI command, a
// test's setup — so this is where their deadline is chosen, rather than
// inherited: migrationLockTimeout for the wait, and fn itself runs unbounded
// under the held lock, exactly as it did before this lock existed.
func withMigrationLock(url string, fn func() error) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), migrationLockTimeout)
	defer cancel()
	return withMigrationLockCtx(ctx, url, migrationLockPoll, fn)
}

// withMigrationLockCtx is withMigrationLock with the wait bounded by ctx and
// the poll interval injectable (tests).
func withMigrationLockCtx(ctx context.Context, url string, poll time.Duration, fn func() error) (err error) {
	conn, err := pgx.Connect(ctx, WithoutPoolParams(url))
	if err != nil {
		return fmt.Errorf("migration lock: connect: %w", err)
	}
	// Closing the session releases a session-level advisory lock even if the
	// explicit unlock below never ran. A fresh context: the wait's may already
	// be spent, and releasing must not be cancelled with it.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = errors.Join(err, conn.Close(closeCtx))
	}()

	if err := acquireMigrationLock(ctx, conn, poll); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, uerr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationLockKey); uerr != nil {
			err = errors.Join(err, fmt.Errorf("migration lock: unlock: %w", uerr))
		}
	}()
	return fn()
}

// acquireMigrationLock polls pg_try_advisory_lock until it wins or ctx ends.
// Each attempt returns immediately, so between attempts this session is idle —
// no running statement, no snapshot for a concurrent index build to wait on.
func acquireMigrationLock(ctx context.Context, conn *pgx.Conn, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrationLockKey).Scan(&got); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %w", ErrMigrationLockTimeout, ctx.Err())
			}
			return fmt.Errorf("migration lock: try: %w", err)
		}
		if got {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrMigrationLockTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}
