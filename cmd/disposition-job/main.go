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
	sweep := &jobs.Disposition{
		Store: store,
		Blobs: env.Blobs,
		Grace: env.Config.Retention.DispositionGracePeriod,
		Batch: env.Config.Jobs.BatchSize,
	}

	runner := jobs.Runner{Name: "disposition", Interval: env.Config.Jobs.DispositionInterval,
		Run: func(ctx context.Context) error {
			lead, ok, err := jobs.AcquireLeadership(ctx, env.Pool, jobs.DispositionLockKey)
			if err != nil {
				return err
			}
			if !ok {
				slog.Info("peer leads, skipping tick", "job", "disposition")
				return nil
			}
			defer lead.Release(ctx)
			return sweep.Sweep(ctx)
		}}
	runner.Loop(ctx)
	slog.Info("disposition-job stopped")
}
