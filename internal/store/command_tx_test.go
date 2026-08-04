package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"

	"github.com/dotwaffle/beamers/ent"
)

func TestBeginCommandPreservesCancellationDuringDriverBegin(t *testing.T) {
	driver := &blockingBeginDriver{entered: make(chan struct{})}
	installation := &SQLite{client: ent.NewClient(ent.Driver(driver))}
	ctx, cancel := context.WithCancel(hostMaintenanceContext(t.Context()))
	result := make(chan error, 1)
	go func() {
		_, err := installation.BeginCommand(ctx)
		result <- err
	}()
	select {
	case <-driver.entered:
	case <-time.After(time.Second):
		t.Fatal("BeginCommand did not reach the driver")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("BeginCommand error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BeginCommand did not return after cancellation")
	}
}

type blockingBeginDriver struct {
	entered chan struct{}
}

func (driver *blockingBeginDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	close(driver.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*blockingBeginDriver) Exec(context.Context, string, any, any) error {
	return errors.New("unexpected driver Exec")
}

func (*blockingBeginDriver) Query(context.Context, string, any, any) error {
	return errors.New("unexpected driver Query")
}

func (*blockingBeginDriver) Close() error { return nil }

func (*blockingBeginDriver) Dialect() string { return dialect.SQLite }
