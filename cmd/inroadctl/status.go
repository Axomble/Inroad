package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/redisconn"
)

// statusCheckTimeout bounds every individual probe status makes. A hung
// dependency must not hang the one command whose entire job is telling an
// operator what's broken.
const statusCheckTimeout = 5 * time.Second

// statusWorkerLiveWindow mirrors workerLiveWindow in
// internal/coreapi/inprocess/workerrouting.go (unexported, and that file is
// owned by the in-flight per-IP routing work — duplicated here deliberately
// rather than importing across that boundary or exporting a constant from a
// package this one shouldn't otherwise depend on). Keep the two values in
// sync if the assigner's live window ever changes.
const statusWorkerLiveWindow = 15 * time.Minute

// runStatus implements `inroadctl status`: instance health across every
// backing dependency. Unlike every other command it does NOT share the
// fail-fast connect in run() — a database that is down is exactly the
// condition this command exists to report, so each check is independent and
// a failure in one does not stop the others from running.
func runStatus(ctx context.Context, cfg *config.Config, out io.Writer) error {
	healthy := true

	fmt.Fprintln(out, "inroadctl status")
	fmt.Fprintln(out)

	dbCtx, cancel := context.WithTimeout(ctx, statusCheckTimeout)
	pool, dbErr := db.Connect(dbCtx, cfg.DatabaseURL)
	cancel()
	switch {
	case dbErr != nil:
		healthy = false
		fmt.Fprintf(out, "database:    UNREACHABLE (%v)\n", dbErr)
	default:
		defer pool.Close()
		fmt.Fprintln(out, "database:    reachable")

		version, dirty, verErr := db.Version(cfg.DatabaseURL)
		switch {
		case verErr != nil:
			healthy = false
			fmt.Fprintf(out, "migrations:  ERROR reading version (%v)\n", verErr)
		case dirty:
			healthy = false
			fmt.Fprintf(out, "migrations:  version %d, DIRTY — a previous migration failed partway and needs manual repair\n", version)
		default:
			fmt.Fprintf(out, "migrations:  version %d, clean\n", version)
		}

		wCtx, wCancel := context.WithTimeout(ctx, statusCheckTimeout)
		liveSince := pgtype.Timestamptz{Time: time.Now().Add(-statusWorkerLiveWindow), Valid: true}
		counts, wErr := gen.New(pool).CountWorkers(wCtx, liveSince)
		wCancel()
		switch {
		case wErr != nil:
			healthy = false
			fmt.Fprintf(out, "workers:     ERROR (%v)\n", wErr)
		case counts.Live == 0:
			healthy = false
			fmt.Fprintf(out, "workers:     %d registered, 0 live (heartbeat within %s) — sends, warmup, and inbox polling are not running\n",
				counts.Total, statusWorkerLiveWindow)
		default:
			fmt.Fprintf(out, "workers:     %d registered, %d live (heartbeat within %s)\n", counts.Total, counts.Live, statusWorkerLiveWindow)
		}
	}

	rCtx, rCancel := context.WithTimeout(ctx, statusCheckTimeout)
	rdb := redis.NewClient(redisconn.MustOptions(cfg.RedisAddr))
	redisErr := rdb.Ping(rCtx).Err()
	rCancel()
	_ = rdb.Close()
	if redisErr != nil {
		healthy = false
		fmt.Fprintf(out, "redis:       UNREACHABLE (%v)\n", redisErr)
	} else {
		fmt.Fprintln(out, "redis:       reachable")
	}

	fmt.Fprintln(out)
	if !healthy {
		fmt.Fprintln(out, "status: UNHEALTHY")
		return errors.New("one or more checks failed")
	}
	fmt.Fprintln(out, "status: healthy")
	return nil
}
