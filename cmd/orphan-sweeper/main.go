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
	sweep := &jobs.Sweeper{
		Store:  store,
		Blobs:  env.Blobs,
		Grace:  env.Config.Orphan.GracePeriod,
		Batch:  env.Config.Jobs.BatchSize,
		DryRun: env.Config.Orphan.DryRun,
	}

	runner := jobs.Runner{Name: "orphan-sweeper", Interval: env.Config.Jobs.OrphanInterval,
		Run: func(ctx context.Context) error {
			lead, ok, err := jobs.AcquireLeadership(ctx, env.Pool, jobs.SweeperLockKey)
			if err != nil {
				return err
			}
			if !ok {
				log.Printf("orphan-sweeper: peer leads, skipping tick")
				return nil
			}
			defer lead.Release(ctx)
			return sweep.Sweep(ctx)
		}}
	runner.Loop(ctx)
	log.Printf("orphan-sweeper stopped")
}
