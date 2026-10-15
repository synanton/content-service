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
	sweep := &jobs.Compactor{
		Store:      store,
		Blobs:      env.Blobs,
		TargetRows: env.Config.Storage.ParquetTargetRows,
		MaxFiles:   env.Config.Storage.ParquetMaxFiles,
		Batch:      env.Config.Jobs.BatchSize,
	}

	runner := jobs.Runner{Name: "parquet-compactor", Interval: env.Config.Jobs.CompactorInterval,
		Run: sweep.Sweep}
	runner.Loop(ctx)
	slog.Info("parquet-compactor stopped")
}
