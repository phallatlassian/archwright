package works

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

func dirs(t *testing.T) paths.Dirs {
	root := t.TempDir()
	return paths.Dirs{Config: filepath.Join(root, "config"), State: filepath.Join(root, "state")}
}

// inProcess starts Serve in a goroutine and counts how often it is asked to.
func inProcess(t *testing.T, spawned *atomic.Int32) Spawner {
	return func(d paths.Dirs) error {
		spawned.Add(1)
		ready := make(chan struct{})
		failed := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if err := Serve(ctx, d, ready); err != nil {
				failed <- err
			}
		}()
		select {
		case <-ready:
			return nil
		case err := <-failed:
			return err
		}
	}
}

func TestStartsOnDemandAndIsReused(t *testing.T) {
	d := dirs(t)
	var spawned atomic.Int32
	spawn := inProcess(t, &spawned)

	c1, err := Connect(context.Background(), d, spawn)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Connect(context.Background(), d, spawn)
	if err != nil {
		t.Fatal(err)
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawned %d times", spawned.Load())
	}
	if c1.Info != c2.Info {
		t.Fatalf("different instances: %+v %+v", c1.Info, c2.Info)
	}
	if err := Serve(context.Background(), d, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Serve: %v", err)
	}
	// the running instance is untouched by the refused start
	if again, _ := ReadInfo(d); again != c1.Info {
		t.Fatalf("pid file changed: %+v", again)
	}
}

func TestPidFileRecordsLoopbackPortAndToken(t *testing.T) {
	d := dirs(t)
	var n atomic.Int32
	if _, err := Connect(context.Background(), d, inProcess(t, &n)); err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(d)
	if err != nil || info.PID == 0 || info.Port == 0 || len(info.Token) < 32 {
		t.Fatalf("%+v %v", info, err)
	}
	resp, err := http.Get("http://" + info.Addr() + "/health") // no token
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestJobAndPlanOverAPI(t *testing.T) {
	d := dirs(t)
	var n atomic.Int32
	c, err := Connect(context.Background(), d, inProcess(t, &n))
	if err != nil {
		t.Fatal(err)
	}
	var job store.Job
	if err := c.Do("POST", "/jobs", map[string]string{"title": "T"}, &job); err != nil {
		t.Fatal(err)
	}
	var withPlan JobWithPlan
	if err := c.Do("GET", "/jobs/"+job.ID, nil, &withPlan); err != nil || withPlan.Plan != nil {
		t.Fatalf("%+v %v", withPlan, err)
	}
	steps := map[string]any{"steps": []store.Step{{ID: "a", Guild: "g", Title: "t"}}}
	if err := c.Do("PUT", "/jobs/"+job.ID+"/plan", steps, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do("GET", "/jobs/"+job.ID, nil, &withPlan); err != nil || withPlan.Plan == nil || len(withPlan.Plan.Steps) != 1 {
		t.Fatalf("%+v %v", withPlan, err)
	}
	if err := c.Do("GET", "/jobs/missing", nil, nil); err == nil {
		t.Fatal("expected not found")
	}
	if err := c.Do("POST", "/jobs", map[string]string{"title": ""}, nil); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestShutdownReleasesLock(t *testing.T) {
	d := dirs(t)
	var n atomic.Int32
	c, err := Connect(context.Background(), d, inProcess(t, &n))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do("POST", "/shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := Running(d); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := Connect(context.Background(), d, inProcess(t, &n)); err != nil || n.Load() != 2 {
		t.Fatalf("restart: %v spawned=%d", err, n.Load())
	}
}
