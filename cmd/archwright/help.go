package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// hint ends the short errors that point a reader at the command listing.
const hint = "run 'archwright help' for the commands"

// helpFooter closes the top-level listing.
const helpFooter = "All output is JSON. The Works starts on demand."

// command is one entry of the command listing. Name is the noun-prefixed
// command ("site register"); Args and Flags are its synopsis, shown only by a
// group's usage. Short is the one-line description of the top-level listing;
// Long, when set, replaces it in the group's usage.
type command struct {
	Section string
	Name    string
	Args    string
	Short   string
	Long    string
}

func (c command) synopsis() string {
	if c.Args == "" {
		return c.Name
	}
	return c.Name + " " + c.Args
}

func (c command) group() string { return strings.Fields(c.Name)[0] }

func (c command) long() string {
	if c.Long != "" {
		return c.Long
	}
	return c.Short
}

// sectionOrder is the order of the workflow sections of the top-level help.
var sectionOrder = []string{"Start", "Sites", "Jobs", "Asks", "Works"}

// commands is the public listing, in lifecycle order within each section.
var commands = []command{
	{"Start", "doctor", "", "check everything a run depends on, read-only",
		"check everything a run depends on, read-only; prints the exact fix for\nanything wrong (exit 1 if a check fails; runs refuse to start until none does)"},
	{"Start", "dashboard", "", "print the dashboard's URL",
		"print the dashboard's URL (the token rides in the fragment, which browsers\nnever send); open it in a browser. The Works starts on demand"},
	{"Start", "(no command)", "", "launch the Archwright in the neutral factory home",
		"launch the Archwright: your claude or codex (runner in config.toml), in the\nneutral factory home; the current directory is the Site hint"},

	{"Sites", "site detect", "URL", "propose Site values and gate commands from a remote",
		"propose Site values and gate commands from a remote's base branch"},
	{"Sites", "site register", "--url U --typecheck C --lint C --test C [--name N] [--base-branch B] [--cap N]", "register a remote repository as a Site (the trust act)",
		"register a remote repository as a Site (the trust act); the Works keeps its\nown clone; gates must be given. Local paths are refused"},
	{"Sites", "site gates", "NAME [--typecheck C] [--lint C] [--test C]", "propose new gate commands for a Site",
		"propose new gate commands (unset ones stay); takes effect only once approved"},
	{"Sites", "site gate-changes", "[--status pending|approved|rejected]", "list proposed gate changes", ""},
	{"Sites", "site gate-approve", "ID", "approve a proposed gate change, at a terminal",
		"approve a proposed change (confirms at a terminal and asks for your approval credential)"},
	{"Sites", "site gate-reject", "ID", "reject a proposed gate change", ""},
	{"Sites", "site list", "", "list Sites", ""},
	{"Sites", "site remove", "NAME", "unregister a Site and delete its clone",
		"unregister a Site and delete its clone (Job history is kept)"},
	{"Sites", "site resolve", "DIR", "show the Site whose remote is DIR's git origin",
		"show the Site whose remote is DIR's git origin; refused if unregistered"},
	{"Sites", "approval set", "", "set your approval credential, at a terminal",
		"set (or change) your approval credential, a secret only you know, at a\nterminal; gate changes cannot be approved until you have set one"},

	{"Jobs", "job create", "--title T [--description D] [--issue URL] [--site NAME | --path DIR]", "create a Job",
		"create a Job (on a registered Site; --path matches DIR's git origin)"},
	{"Jobs", "job list", "", "list Jobs", ""},
	{"Jobs", "job show", "ID", "show a Job and its Plan", ""},
	{"Jobs", "job close", "ID accepted|discarded", "end a Job and remove its remaining worktrees", ""},
	{"Jobs", "job purge", "ID", "remove a Job's raw run content now",
		"remove a Job's raw run content now, keeping its summary (confirms at a terminal)"},
	{"Jobs", "job prune", "[--dry-run] [--days N]", "remove raw run content of long-ended Jobs",
		"remove raw run content of Jobs that ended over retention_days (default 30)\nago; never a Job with active runs; --dry-run shows what would go and changes nothing"},
	{"Jobs", "job delete", "ID", "remove a Job entirely",
		"remove a Job entirely (confirms at a terminal)"},
	{"Jobs", "plan set", "ID [FILE]", "set a Job's Plan from JSON",
		"set a Job's Plan from JSON ({\"steps\":[...]}); stdin if no FILE"},
	{"Jobs", "plan show", "ID", "show a Job's Plan", ""},
	{"Jobs", "plan approve", "ID", "the developer's go on a Job's ad hoc Plan",
		"the developer's go on a Job's ad hoc Plan (asks you to confirm at a terminal);\na Jig Plan needs none, and no work is dispatched before a go"},
	{"Jobs", "plan reject", "ID", "the developer's no-go on a Job's ad hoc Plan", ""},
	{"Jobs", "plan start", "ID JIG [--param NAME=VALUE]", "start a Job's Plan from a Jig",
		"start a Job's Plan from a Jig; it starts without approval"},
	{"Jobs", "worktree create", "ID integration|wright|readonly [--base-ref REF]", "create a Job's worktree from the Works' clone",
		"create a Job's worktree from the Works' clone, after a fetch"},
	{"Jobs", "worktree list", "ID", "list a Job's worktrees", ""},
	{"Jobs", "worktree cleanup", "ID", "remove worktrees of Wrights merged into the integration branch", ""},

	{"Asks", "ask list", "[--status S] [--holder H] [--job ID]", "list asks",
		"list asks (an agent's structured questions; ADR 0015)"},
	{"Asks", "ask show", "ID", "show an ask and its plain-text form", ""},
	{"Asks", "ask page", "ID", "put an ask to the developer as a Lavish page",
		"put an ask to the developer as a Lavish page (lavish-axi), or as plain text\nif Lavish is unavailable"},
	{"Asks", "ask answer", "ID", "answer an ask as the developer",
		"answer an ask as the developer ({\"answers\":[...]} on stdin)"},
	{"Asks", "ask accept-default", "ID", "accept an ask's recommended defaults", ""},
	{"Asks", "ask skip", "ID", "skip an ask", ""},
	{"Asks", "ask cancel", "ID", "cancel an ask, and the Job", ""},
	{"Asks", "needs-you list", "", "list the Needs-you inbox",
		"list the Needs-you inbox (such as scope-overlap warnings)"},

	{"Works", "works status", "", "report whether the Works is running", ""},
	{"Works", "works stop", "", "stop the Works", ""},
	{"Works", "works upgrade", "", "restart the Works on this binary",
		"restart the Works on this binary (migrating the database, keeping the three\nmost recent pre-migration copies); refuses while runs are active unless you confirm"},
	{"Works", "queue show", "", "show running and queued Wrights, the caps and any runner pause", ""},
	{"Works", "catalogue show", "", "show the loaded Guilds and Jigs and any that failed to load", ""},
	{"Works", "catalogue reload", "", "re-read definitions from the config directory",
		"re-read definitions from the config directory; an invalid one keeps its\nprevious good version and is reported"},
}

// internalCommands are agent-only: hidden from the top-level listing, shown by `help internal`.
var internalCommands = []command{
	{"Internal", "hook", "session-start|prompt|post-tool|stop", "the Archwright's runner hooks (rehydrate, inbox)", ""},
	{"Internal", "tool", "NAME [--id ID] [--wait D]", "call a Works tool inside a run",
		"call a Works tool inside a run: JSON on stdin, JSON on stdout\n(exit 75 if the Works does not answer within D, default 60s)"},
	{"Internal", "ask record", "ID", "record a page's submitted answer, inside a run",
		"inside a run: record the page's submitted answer (JSON on stdin) through the\ntool API, and end the page; a rejected one is reported"},
	{"Internal", "ask end", "ID", "end an ask's Lavish page", ""},
	{"Internal", "works serve", "", "run the Works in the foreground (started on demand)", ""},
}

// aliases are the pre-regrouping command names, kept for one release: each
// maps to the words it is now spelled with.
var aliases = map[string][]string{
	"prune":     {"job", "prune"},
	"upgrade":   {"works", "upgrade"},
	"stop":      {"works", "stop"},
	"jig start": {"plan", "start"},
}

// resolveAlias rewrites a deprecated command name to its new spelling and
// says so on stderr; stdout is unchanged.
func resolveAlias(args []string, stderr io.Writer) []string {
	if len(args) == 0 {
		return args
	}
	for _, n := range []int{2, 1} {
		if len(args) < n {
			continue
		}
		old := strings.Join(args[:n], " ")
		if to, ok := aliases[old]; ok {
			_, _ = fmt.Fprintf(stderr, "archwright: '%s' is deprecated and will be removed; use '%s'\n", old, strings.Join(to, " "))
			return append(append([]string{}, to...), args[n:]...)
		}
	}
	return args
}

// helpRequest reports whether args ask for help and, if so, writes it to
// stdout: `help [TOPIC] [--json]`, `-h`/`--help`, `GROUP --help` and a bare
// `GROUP` that has verbs. Unknown help topics are an error.
func helpRequest(args []string, stdout io.Writer) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "help":
		return true, help(args[1:], stdout)
	case "-h", "--help":
		return true, help(nil, stdout)
	}
	if len(args) >= 2 && (args[1] == "-h" || args[1] == "--help") {
		if len(groupCommands(args[0])) > 0 {
			return true, help([]string{args[0]}, stdout)
		}
		return false, nil
	}
	if len(args) == 1 {
		// Bare `doctor`, `dashboard` and `hook` do something; a bare noun lists its verbs.
		for _, c := range commands {
			if c.group() == args[0] && c.Name != args[0] {
				return true, help([]string{args[0]}, stdout)
			}
		}
	}
	return false, nil
}

// help prints the top-level listing, or the usage of one group or section.
func help(args []string, stdout io.Writer) error {
	asJSON := false
	var topic string
	for _, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case strings.HasPrefix(a, "-") || topic != "":
			return fmt.Errorf("help takes an optional group and --json; %s", hint)
		default:
			topic = a
		}
	}
	var sections []helpSection
	switch topic {
	case "":
		sections = listing(commands, sectionOrder)
	case "internal":
		sections = listing(internalCommands, []string{"Internal"})
	default:
		cmds := groupCommands(topic)
		if len(cmds) == 0 {
			cmds = sectionCommands(topic)
		}
		if len(cmds) == 0 {
			return &exitError{1, fmt.Errorf("no command or group %q; %s", topic, hint)}
		}
		if asJSON {
			return encodeSections(stdout, listing(cmds, uniqueSections(cmds)))
		}
		return writeGroupUsage(stdout, cmds)
	}
	if asJSON {
		return encodeSections(stdout, sections)
	}
	return writeListing(stdout, sections, topic == "")
}

type helpCommand struct {
	Name    string `json:"name"`
	Usage   string `json:"usage"`
	Summary string `json:"summary"`
}

type helpSection struct {
	Name     string        `json:"name"`
	Commands []helpCommand `json:"commands"`
}

func listing(cmds []command, order []string) []helpSection {
	var out []helpSection
	for _, s := range order {
		sec := helpSection{Name: s}
		for _, c := range cmds {
			if c.Section == s {
				sec.Commands = append(sec.Commands, helpCommand{Name: c.Name, Usage: c.synopsis(), Summary: c.Short})
			}
		}
		if len(sec.Commands) > 0 {
			out = append(out, sec)
		}
	}
	return out
}

func uniqueSections(cmds []command) []string {
	var out []string
	for _, c := range cmds {
		if len(out) == 0 || out[len(out)-1] != c.Section {
			out = append(out, c.Section)
		}
	}
	return out
}

func encodeSections(w io.Writer, sections []helpSection) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"sections": sections})
}

func writeListing(w io.Writer, sections []helpSection, withHint bool) error {
	width := 0
	for _, s := range sections {
		for _, c := range s.Commands {
			width = max(width, len(c.Name))
		}
	}
	var b strings.Builder
	b.WriteString("usage: archwright <command>\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "\n%s\n", s.Name)
		for _, c := range s.Commands {
			fmt.Fprintf(&b, "  %-*s  %s\n", width, c.Name, c.Summary)
		}
	}
	if withHint {
		b.WriteString("\n`archwright help <group>` prints a group's flags (site, job, plan, ask, works, ...);\n`archwright help internal` lists the agent-only commands.\n")
	}
	fmt.Fprintf(&b, "\n%s\n", helpFooter)
	_, err := io.WriteString(w, b.String())
	return err
}

// groupCommands are the commands of one noun ("site"). The agent-only
// commands show only for a group with no public ones (hook, tool); the rest are
// listed by `help internal`.
func groupCommands(group string) []command {
	var out []command
	for _, c := range commands {
		if c.group() == group {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		for _, c := range internalCommands {
			if c.group() == group {
				out = append(out, c)
			}
		}
	}
	return out
}

// sectionCommands are all commands of a section named case-insensitively ("jobs").
func sectionCommands(name string) []command {
	var out []command
	for _, c := range commands {
		if strings.EqualFold(c.Section, name) {
			out = append(out, c)
		}
	}
	return out
}

// writeGroupUsage prints full usage: each synopsis with its flags, then its description.
func writeGroupUsage(w io.Writer, cmds []command) error {
	var b strings.Builder
	b.WriteString("usage: archwright " + cmds[0].group() + " <command>\n\n")
	const col = 44
	for _, c := range cmds {
		syn := "  " + c.synopsis()
		lines := strings.Split(c.long(), "\n")
		if len(syn) >= col-1 {
			b.WriteString(syn + "\n")
			syn = ""
		}
		fmt.Fprintf(&b, "%-*s%s\n", col, syn, lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(&b, "%*s%s\n", col, "", l)
		}
	}
	b.WriteString("\n" + helpFooter + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// unknownCommand is the error for a command that is not one.
func unknownCommand(args []string) error {
	return fmt.Errorf("unknown command %q; %s", strings.Join(args[:min(len(args), 2)], " "), hint)
}
