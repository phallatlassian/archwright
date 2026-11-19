package sites

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// git runs git in dir and returns trimmed stdout. Prompts are disabled and
// the caller's GIT_* repository overrides are dropped so only dir decides
// which repository is touched.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_WORK_TREE=") || strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// gitOK reports whether a git command succeeds.
func gitOK(dir string, args ...string) bool {
	_, err := git(dir, args...)
	return err == nil
}
