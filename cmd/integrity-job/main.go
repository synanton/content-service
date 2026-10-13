package main

import (
	"context"
	"log"
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
		log.Fatalf("setup: %v", err)
	}
	defer env.Pool.Close()

	store := postgres.NewStore(env.Pool, env.Config, env.Blobs)
	sweep := &jobs.Integrity{
		Store:        store,
		Content:      store,
		Batch:        env.Config.Jobs.BatchSize,
		SampleRate:   env.Config.Integrity.SampleRate,
		ShardIndex:   env.Config.Jobs.ShardIndex,
		ShardCount:   env.Config.Jobs.ShardCount,
	}

	runner := jobs.Runner{Name: "integrity", Interval: env.Config.Jobs.IntegrityInterval,
		Run: func(ctx context.Context) error {
			result, err := sweep.Sweep(ctx)
			if err != nil {
				return err
			}
			log.Printf("integrity: checked=%d failed=%d", result.Checked, result.Failed)
			return nil
		}}
	runner.Loop(ctx)
	log.Printf("integrity-job stopped")
}
