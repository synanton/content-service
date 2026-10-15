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
	sweep := &jobs.Integrity{
		Store:      store,
		Content:    store,
		Auditor:    env.Auditor,
		Batch:      env.Config.Jobs.BatchSize,
		SampleRate: env.Config.Integrity.SampleRate,
		ShardIndex: env.Config.Jobs.ShardIndex,
		ShardCount: env.Config.Jobs.ShardCount,
	}

	runner := jobs.Runner{Name: "integrity", Interval: env.Config.Jobs.IntegrityInterval,
		Run: func(ctx context.Context) error {
			result, err := sweep.Sweep(ctx)
			if err != nil {
				return err
			}
			slog.Info("integrity sweep result", "checked", result.Checked, "failed", result.Failed)
			return nil
		}}
	runner.Loop(ctx)
	slog.Info("integrity-job stopped")
}
