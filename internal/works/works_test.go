package works

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/sites"
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

func TestSitesAndWorktreesOverAPI(t *testing.T) {
	d := dirs(t)
	var n atomic.Int32
	c, err := Connect(context.Background(), d, inProcess(t, &n))
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}

	// Unregistered paths are refused, for lookups and for Jobs.
	if err := c.Do("GET", "/sites/resolve?path="+url.QueryEscape(repo), nil, nil); err == nil {
		t.Fatal("unregistered path resolved")
	}
	if err := c.Do("POST", "/jobs", map[string]string{"title": "T", "path": repo}, nil); err == nil {
		t.Fatal("job created on unregistered path")
	}
	if err := c.Do("POST", "/sites", sites.RegisterInput{Name: "r", Path: repo, BaseBranch: "main"}, nil); err == nil {
		t.Fatal("registered without confirmed gates")
	}

	in := sites.RegisterInput{Name: "r", Path: repo, BaseBranch: "main", Gates: &store.Gates{Test: "go test ./..."}}
	var site store.Site
	if err := c.Do("POST", "/sites", in, &site); err != nil || site.ConcurrencyCap != 3 {
		t.Fatalf("%+v %v", site, err)
	}
	if err := c.Do("POST", "/sites", in, nil); err == nil {
		t.Fatal("registered twice")
	}
	var list []store.Site
	if err := c.Do("GET", "/sites", nil, &list); err != nil || len(list) != 1 || list[0].Name != "r" {
		t.Fatalf("%+v %v", list, err)
	}

	var job store.Job
	if err := c.Do("POST", "/jobs", map[string]string{"title": "T", "path": repo}, &job); err != nil || job.Site != "r" {
		t.Fatalf("%+v %v", job, err)
	}
	realWorktrees, _ := filepath.EvalSymlinks(d.Worktrees())
	var wt store.Worktree
	if err := c.Do("POST", "/jobs/"+job.ID+"/worktrees", map[string]string{"kind": "wright"}, &wt); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(wt.Path, repo+"/") || !strings.HasPrefix(wt.Path, realWorktrees) {
		t.Fatalf("worktree at %s", wt.Path)
	}
	var wts []store.Worktree
	if err := c.Do("GET", "/jobs/"+job.ID+"/worktrees", nil, &wts); err != nil || len(wts) != 1 {
		t.Fatalf("%+v %v", wts, err)
	}
	if err := c.Do("POST", "/jobs/"+job.ID+"/close", map[string]string{"outcome": "discarded"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatal("worktree kept after discard")
	}
}
