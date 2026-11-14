package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrCollision means a skill to deliver clashes with something the Site tracks.
var ErrCollision = errors.New("skill collides with the Site")

// CollisionError names the skill (empty when the Site tracks the skills directory itself as a file or link) and the Site path it collides with. A run
// that hits one fails visibly and never overwrites the Site's file (ADR 0018).
type CollisionError struct {
	Skill string
	Path  string
}

func (e *CollisionError) Error() string {
	if e.Skill == "" {
		return fmt.Sprintf("the Site tracks %s as a file or link, so the Works cannot add skills beside it", e.Path)
	}
	return fmt.Sprintf("skill %q collides with %s, which the Site tracks: rename the Site's skill or drop it from the Guild", e.Skill, e.Path)
}

func (e *CollisionError) Unwrap() error { return ErrCollision }

// Request is one run's skills delivery.
type Request struct {
	Worktree string  // the run's worktree
	RunDir   string  // the Works' per-run state directory, outside the worktree
	Runner   string  // claude or codex; claude also gets .claude/skills
	Skills   []Skill // the Guild's skills
}

// Delivery is what the run must be started with.
type Delivery struct {
	Env    map[string]string // GIT_CONFIG_GLOBAL, to set on the runner process
	Hidden []string          // the worktree paths hidden from git for the run
}

const (
	agentsSkills = ".agents/skills"
	claudeSkills = ".claude/skills"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validName(n string) bool { return nameRE.MatchString(n) }

// Deliver copies the Guild's skills into the worktree and hides exactly what
// it created from git for the run, with no change to the Site's repository:
// the run's own git configuration (GIT_CONFIG_GLOBAL) includes the developer's
// global configuration and points core.excludesFile at a generated excludes
// file that carries the developer's global excludes plus the created paths.
// It fails with a CollisionError, before writing anything, if the Site tracks
// a path a skill would occupy.
func Deliver(req Request) (Delivery, error) {
	if err := req.check(); err != nil {
		return Delivery{}, err
	}
	if len(req.Skills) == 0 {
		return Delivery{}, nil
	}
	claude := req.Runner == "claude"

	agentsTracked, err := tracked(req.Worktree, agentsSkills)
	if err != nil {
		return Delivery{}, err
	}
	claudeTracked, err := tracked(req.Worktree, claudeSkills)
	if err != nil {
		return Delivery{}, err
	}
	for _, s := range req.Skills {
		paths := []string{path.Join(agentsSkills, s.Name)}
		if claude {
			paths = append(paths, path.Join(claudeSkills, s.Name))
		}
		for _, p := range paths {
			if ts, err := tracked(req.Worktree, p); err != nil {
				return Delivery{}, err
			} else if len(ts) > 0 {
				return Delivery{}, &CollisionError{Skill: s.Name, Path: p}
			}
		}
	}
	if claude && len(claudeTracked) == 1 && claudeTracked[0] == claudeSkills {
		return Delivery{}, &CollisionError{Path: claudeSkills} // the Site tracks it as a file or link
	}

	// Where the Site tracks content in a skills directory the Works adds
	// beside it and hides only its own entries; otherwise it owns the whole
	// directory (replacing any copy from an earlier run) and hides that.
	var hidden []string
	agentsRoot := filepath.Join(req.Worktree, agentsSkills)
	if len(agentsTracked) == 0 {
		hidden = append(hidden, "/"+agentsSkills)
		if err := os.RemoveAll(agentsRoot); err != nil {
			return Delivery{}, err
		}
	}
	for _, s := range req.Skills {
		if err := copySkill(s, filepath.Join(agentsRoot, s.Name)); err != nil {
			return Delivery{}, fmt.Errorf("copying skill %q: %w", s.Name, err)
		}
		if len(agentsTracked) > 0 {
			hidden = append(hidden, "/"+path.Join(agentsSkills, s.Name))
		}
	}
	if claude {
		claudeRoot := filepath.Join(req.Worktree, claudeSkills)
		if len(claudeTracked) == 0 {
			if err := os.RemoveAll(claudeRoot); err != nil {
				return Delivery{}, err
			}
			if err := os.MkdirAll(filepath.Dir(claudeRoot), 0o755); err != nil {
				return Delivery{}, err
			}
			if err := os.Symlink("../.agents/skills", claudeRoot); err != nil {
				return Delivery{}, err
			}
			hidden = append(hidden, "/"+claudeSkills)
		} else {
			for _, s := range req.Skills {
				link := filepath.Join(claudeRoot, s.Name)
				if err := os.RemoveAll(link); err != nil {
					return Delivery{}, err
				}
				if err := os.Symlink("../../.agents/skills/"+s.Name, link); err != nil {
					return Delivery{}, err
				}
				hidden = append(hidden, "/"+path.Join(claudeSkills, s.Name))
			}
		}
	}

	cfg, err := writeGitConfig(req.RunDir, hidden)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{Env: map[string]string{"GIT_CONFIG_GLOBAL": cfg}, Hidden: hidden}, nil
}

func (r Request) check() error {
	if r.Worktree == "" || r.RunDir == "" {
		return errors.New("skills delivery needs a worktree and a run directory")
	}
	if r.Runner != "claude" && r.Runner != "codex" {
		return fmt.Errorf("unknown runner %q", r.Runner)
	}
	if rel, err := filepath.Rel(r.Worktree, r.RunDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("run directory %s is inside the worktree", r.RunDir)
	}
	seen := map[string]bool{}
	for _, s := range r.Skills {
		if !validName(s.Name) {
			return fmt.Errorf("invalid skill name %q", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("skill %q listed twice", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

// copySkill writes one skill's files; a file that starts with a shebang is executable.
func copySkill(s Skill, dest string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return fs.WalkDir(s.FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(s.FS, name)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if bytes.HasPrefix(b, []byte("#!")) {
			mode = 0o755
		}
		return os.WriteFile(target, b, mode)
	})
}

// tracked lists the paths in the worktree's index at or under p.
func tracked(worktree, p string) ([]string, error) {
	out, err := git(worktree, nil, "ls-files", "-z", "--", ":(literal)"+p)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			paths = append(paths, f)
		}
	}
	return paths, nil
}

// git runs git in dir with the caller's repository overrides dropped.
func git(dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_WORK_TREE=") || strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

// developerConfigs are the global git configuration files git would read.
func developerConfigs() []string {
	if v := os.Getenv("GIT_CONFIG_GLOBAL"); v != "" {
		return existing(v)
	}
	var files []string
	if home := os.Getenv("HOME"); home != "" {
		files = append(files, filepath.Join(home, ".gitconfig"))
	}
	files = append(files, filepath.Join(xdgConfig(), "git", "config"))
	return existing(files...)
}

func xdgConfig() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

func existing(files ...string) []string {
	var out []string
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func quote(p string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

// writeGitConfig generates the run's git configuration and excludes file in
// runDir and returns the configuration's path.
func writeGitConfig(runDir string, hidden []string) (string, error) {
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", err
	}
	cfgPath := filepath.Join(runDir, "gitconfig")
	excludesPath := filepath.Join(runDir, "excludes")

	var includes strings.Builder
	includes.WriteString("# Generated by the Works for one run; includes the developer's own configuration.\n[include]\n")
	for _, f := range developerConfigs() {
		includes.WriteString("\tpath = " + quote(f) + "\n")
	}
	if err := os.WriteFile(cfgPath, []byte(includes.String()), 0o600); err != nil {
		return "", err
	}

	// Ask git what the developer's excludes file is, so includes, ~ and
	// conditional configuration resolve exactly as they would outside a run.
	var devExcludes string
	if out, err := git(runDir, []string{"GIT_CONFIG_GLOBAL=" + cfgPath}, "config", "--type=path", "--get", "core.excludesFile"); err == nil {
		devExcludes = strings.TrimSpace(out)
	} else {
		devExcludes = filepath.Join(xdgConfig(), "git", "ignore") // git's default when core.excludesFile is unset
	}
	var excludes bytes.Buffer
	if b, err := os.ReadFile(devExcludes); err == nil {
		excludes.Write(b)
		if !bytes.HasSuffix(b, []byte("\n")) {
			excludes.WriteByte('\n')
		}
	}
	excludes.WriteString("# Factory skills created by the Works for this run.\n")
	for _, h := range hidden {
		excludes.WriteString(h + "\n")
	}
	if err := os.WriteFile(excludesPath, excludes.Bytes(), 0o600); err != nil {
		return "", err
	}
	cfg := includes.String() + "[core]\n\texcludesFile = " + quote(excludesPath) + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return "", err
	}
	return cfgPath, nil
}
