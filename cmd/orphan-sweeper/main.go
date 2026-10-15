package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/synanton/content-service/internal/adapter/postgres"
	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/jobsboot"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	env, err := jobsboot.Setup(ctx)
	if err != nil {
		slog.Error("setup failed", "err", err)
		os.Exit(1)
	}
	defer env.Pool.Close()

	store := postgres.NewStore(env.Pool, env.Config, env.Blobs, env.Rows)
	sweep := &jobs.Sweeper{
		Store:   store,
		Blobs:   env.Blobs,
		Auditor: env.Auditor,
		Grace:   env.Config.Orphan.GracePeriod,
		Batch:   env.Config.Jobs.BatchSize,
		DryRun:  env.Config.Orphan.DryRun,
	}

	runner := jobs.Runner{Name: "orphan-sweeper", Interval: env.Config.Jobs.OrphanInterval,
		Run: func(ctx context.Context) error {
			lead, ok, err := jobs.AcquireLeadership(ctx, env.Pool, jobs.SweeperLockKey)
			if err != nil {
				return err
			}
			if !ok {
				slog.Info("peer leads, skipping tick", "job", "orphan-sweeper")
				return nil
			}
			defer lead.Release(ctx)
			return sweep.Sweep(ctx)
		}}
	runner.Loop(ctx)
	slog.Info("orphan-sweeper stopped")
}
