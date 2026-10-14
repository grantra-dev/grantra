package db

import (
	"context"
	"testing"
)

func TestJobLocksRunOneReplicaAtATime(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Two replicas, each with its own lock connection.
	a, b := NewJobLocks(pool), NewJobLocks(pool)
	t.Cleanup(func() { a.Close(ctx); b.Close(ctx) })

	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ran, err := a.TryRun(ctx, "test job", func(context.Context) error {
			close(started)
			<-release
			return nil
		})
		if err == nil && !ran {
			t.Error("replica a did not run the job")
		}
		done <- err
	}()
	<-started

	ran, err := b.TryRun(ctx, "test job", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("replica b ran the job while replica a held its lock")
	}
	ran, err = b.TryRun(ctx, "other job", func(context.Context) error { return nil })
	if err != nil || !ran {
		t.Fatalf("a different job should run on replica b: ran=%v err=%v", ran, err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ran, err = b.TryRun(ctx, "test job", func(context.Context) error { return nil })
	if err != nil || !ran {
		t.Fatalf("replica b should run the job once a released it: ran=%v err=%v", ran, err)
	}
}

func TestJobLocksReconnect(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := NewJobLocks(pool)
	t.Cleanup(func() { l.Close(ctx) })

	run := func() {
		t.Helper()
		ran, err := l.TryRun(ctx, "reconnect job", func(context.Context) error { return nil })
		if err != nil || !ran {
			t.Fatalf("ran=%v err=%v", ran, err)
		}
	}
	run()
	l.Close(ctx) // as if the connection had dropped
	run()
}
