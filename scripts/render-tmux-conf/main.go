// Command render-tmux-conf writes the tmux config the factory would start with
// and prints it, so you can check it. See scripts/render-tmux-conf.sh.
package main

import (
	"fmt"
	"os"

	"github.com/phallatlassian/archwright/internal/works"
)

func main() {
	path, err := works.WriteTmuxConf(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "render-tmux-conf:", err)
		os.Exit(1)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "render-tmux-conf:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "# %s\n", path)
	os.Stdout.Write(b)
}
