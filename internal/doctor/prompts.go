package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Agents of any role run `gh` and `git push` as the developer, and the
// developer's approval in the pane is the only gate on a push (ADR 0028). So
// doctor looks at whether each runner would prompt for them under the policy
// the developer and their managed settings give it. It reads the policy files
// and runs nothing: no push, no `gh` call, nothing that touches a remote.
//
// What it evaluates is the commands an agent runs to deliver a Job, `git push
// origin HEAD` and `gh pr create`; a rule written for one branch or one verb
// says nothing about them. It reads the user's and the managed policy, not a
// Site's own project settings.

type promptVerdict string

const (
	vAsks  promptVerdict = "asks"
	vAllow promptVerdict = "runs without a prompt"
	vDeny  promptVerdict = "is denied"
)

// deciding is a promptVerdict and the rule behind it ("" when it is the default).
type deciding struct {
	v    promptVerdict
	rule string
	file string
}

func (d deciding) String() string {
	if d.rule == "" {
		return string(d.v) + " (no rule matches: the default mode prompts)"
	}
	return fmt.Sprintf("%s (%s in %s)", d.v, d.rule, d.file)
}

type promptTarget struct {
	check   string // the check's name
	what    string // what it is called in a message
	command string // the representative command
	// the fix for a runner whose promptVerdict is not asks
	claudeAsk string
	codexAsk  string
}

var promptTargets = []promptTarget{
	{check: "prompt:git-push", what: "`git push`", command: "git push origin HEAD", claudeAsk: "Bash(git push:*)", codexAsk: `["git", "push"]`},
	{check: "prompt:gh", what: "`gh`", command: "gh pr create", claudeAsk: "Bash(gh:*)", codexAsk: `["gh"]`},
}

// checkPrompts reports, for each installed runner, whether `git push` and
// `gh` prompt. A prompt is the wanted outcome: it is the gate. No prompt, or
// a denial, is a warning with the way to change it in the developer's own
// policy, which the factory never edits.
func checkPrompts(r *collector, d Deps) {
	type runnerPolicy struct {
		name string
		eval func(command string) deciding
	}
	var runners []runnerPolicy
	var unreadable []string
	for _, spec := range runnerSpecs {
		if _, err := d.LookPath(spec.name); err != nil {
			continue
		}
		var files []string
		if d.PolicyFiles != nil {
			files = d.PolicyFiles(spec.name)
		}
		switch spec.name {
		case "claude":
			pol, bad := readClaudePolicy(files)
			unreadable = append(unreadable, bad...)
			runners = append(runners, runnerPolicy{name: "claude", eval: pol.eval})
		case "codex":
			pol, bad := readCodexPolicy(files)
			unreadable = append(unreadable, bad...)
			runners = append(runners, runnerPolicy{name: "codex", eval: pol.eval})
		}
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		r.warn("prompt:policy", "cannot read policy file(s), so what they say about prompts is not counted: "+strings.Join(unreadable, "; "),
			"repair or remove the file(s) named; the factory never edits them")
	}
	if len(runners) == 0 {
		return // no runner installed: reported by the runner checks
	}
	for _, t := range promptTargets {
		var detail, fixes []string
		worst := vAsks
		for _, rn := range runners {
			got := rn.eval(t.command)
			detail = append(detail, rn.name+": "+got.String())
			if got.v == vAsks {
				continue
			}
			if got.v == vDeny || worst == vAsks {
				worst = got.v
			}
			fixes = append(fixes, promptFix(rn.name, t, got))
		}
		if worst == vAsks {
			r.pass(t.check, "evaluated for `%s`: %s; you approve it in the pane", t.command, strings.Join(detail, " | "))
			continue
		}
		r.warn(t.check, fmt.Sprintf("evaluated for `%s`: %s", t.command, strings.Join(detail, " | ")), strings.Join(fixes, "\n"))
	}
}

func promptFix(runner string, t promptTarget, got deciding) string {
	if got.v == vDeny {
		return fmt.Sprintf("%s: %s is denied, so no agent can use it: a Warden's open-pr step fails and the PR has to be opened by you. "+
			"Ask whoever manages your policy to let it prompt instead of denying it (the factory never overrides policy, ADR 0027); or remove the rule %s from %s if it is yours",
			runner, t.what, got.rule, got.file)
	}
	if runner == "codex" {
		where := got.file
		if got.rule == "" {
			where = "~/.codex/rules/default.rules"
		}
		return fmt.Sprintf("codex: %s runs with no prompt, and the developer's approval is the only gate on a push (ADR 0028). To approve each one yourself, add this to %s (the factory never writes it):\n  prefix_rule(pattern=%s, decision=\"prompt\")", t.what, where, t.codexAsk)
	}
	return fmt.Sprintf("claude: %s runs with no prompt, and the developer's approval is the only gate on a push (ADR 0028). To approve each one yourself, remove %s from %s, or add this to your own Claude Code settings (the factory never writes it):\n  {\"permissions\": {\"ask\": [%q]}}",
		t.what, ruleOrMode(got), got.file, t.claudeAsk)
}

func ruleOrMode(g deciding) string {
	if g.rule == "" {
		return "its permission mode"
	}
	return "the rule " + g.rule
}

// ---- Claude Code ----

type claudePolicy struct {
	allow, ask, deny []sourced
	mode, modeFile   string
}

type sourced struct{ rule, file string }

// readClaudePolicy reads each settings file that exists. A file that is
// there and cannot be read or parsed is returned in bad, and not counted.
func readClaudePolicy(files []string) (claudePolicy, []string) {
	var pol claudePolicy
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			bad = append(bad, f+": "+err.Error())
			continue
		}
		var s struct {
			Permissions struct {
				Allow       []string `json:"allow"`
				Ask         []string `json:"ask"`
				Deny        []string `json:"deny"`
				DefaultMode string   `json:"defaultMode"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			bad = append(bad, f+": "+err.Error())
			continue
		}
		for _, x := range s.Permissions.Allow {
			pol.allow = append(pol.allow, sourced{x, f})
		}
		for _, x := range s.Permissions.Ask {
			pol.ask = append(pol.ask, sourced{x, f})
		}
		for _, x := range s.Permissions.Deny {
			pol.deny = append(pol.deny, sourced{x, f})
		}
		if m := s.Permissions.DefaultMode; m != "" {
			pol.mode, pol.modeFile = m, f
		}
	}
	return pol, bad
}

// eval: a deny rule wins over an ask rule, which wins over an allow rule, as
// in Claude Code; with none matching, the default mode decides.
func (p claudePolicy) eval(command string) deciding {
	for _, set := range []struct {
		v     promptVerdict
		rules []sourced
	}{{vDeny, p.deny}, {vAsks, p.ask}, {vAllow, p.allow}} {
		for _, r := range set.rules {
			if bashRuleMatches(r.rule, command) {
				return deciding{set.v, r.rule, r.file}
			}
		}
	}
	switch p.mode {
	case "bypassPermissions":
		return deciding{vAllow, "defaultMode bypassPermissions", p.modeFile}
	case "dontAsk":
		return deciding{vDeny, "defaultMode dontAsk", p.modeFile}
	}
	return deciding{v: vAsks}
}

// bashRuleMatches says whether a permission rule, `Bash`, `Bash(*)`,
// `Bash(git push:*)`, `Bash(git push *)` or `Bash(git push origin main)`,
// covers command. A rule for another tool covers nothing here.
func bashRuleMatches(rule, command string) bool {
	rule = strings.TrimSpace(rule)
	if rule == "Bash" {
		return true
	}
	spec, ok := strings.CutPrefix(rule, "Bash(")
	if !ok || !strings.HasSuffix(spec, ")") {
		return false
	}
	spec = strings.TrimSuffix(spec, ")")
	switch {
	case spec == "*":
		return true
	case strings.HasSuffix(spec, ":*"):
		prefix := strings.TrimSuffix(spec, ":*")
		return command == prefix || strings.HasPrefix(command, prefix+" ")
	case strings.Contains(spec, "*"):
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(spec), `\*`, ".*") + "$"
		ok, _ := regexp.MatchString(re, command)
		return ok
	}
	return command == spec
}

// ---- Codex ----

type codexPolicy struct {
	rules  []codexRule
	policy string // approval_policy
	file   string // where it is set
}

type codexRule struct {
	tokens   [][]string // each position: the words that match it
	decision string
	text     string
	file     string
}

var (
	codexCall     = regexp.MustCompile(`prefix_rule\s*\(`)
	codexPattern  = regexp.MustCompile(`pattern\s*=\s*\[`)
	codexDecision = regexp.MustCompile(`decision\s*=\s*"(\w+)"`)
	codexToken    = regexp.MustCompile(`\[[^\]]*\]|"[^"]*"`)
	codexQuoted   = regexp.MustCompile(`"([^"]*)"`)
	codexApproval = regexp.MustCompile(`(?m)^\s*approval_policy\s*=\s*"([\w-]+)"`)
)

// readCodexPolicy reads Codex's rules files (prefix_rule calls) and the
// approval_policy of its config files.
func readCodexPolicy(files []string) (codexPolicy, []string) {
	var pol codexPolicy
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			bad = append(bad, f+": "+err.Error())
			continue
		}
		text := stripComments(string(b))
		if strings.HasSuffix(f, ".toml") {
			if m := codexApproval.FindStringSubmatch(topLevel(text)); m != nil {
				pol.policy, pol.file = m[1], f
			}
			continue
		}
		for _, at := range codexCall.FindAllStringIndex(text, -1) {
			body, ok := balanced(text, at[1]-1, '(', ')')
			if !ok {
				continue
			}
			pat := codexPattern.FindStringIndex(body)
			dm := codexDecision.FindStringSubmatch(body)
			if pat == nil || dm == nil {
				continue
			}
			list, ok := balanced(body, pat[1]-1, '[', ']')
			if !ok {
				continue
			}
			var toks [][]string
			for _, t := range codexToken.FindAllString(list, -1) {
				var alts []string
				for _, q := range codexQuoted.FindAllStringSubmatch(t, -1) {
					alts = append(alts, q[1])
				}
				toks = append(toks, alts)
			}
			pol.rules = append(pol.rules, codexRule{tokens: toks, decision: dm[1], text: "prefix_rule(" + strings.Join(strings.Fields(body), " ") + ")", file: f})
		}
	}
	return pol, bad
}

// balanced returns what lies between the bracket of kind open at s[i] and its
// partner, skipping brackets inside quoted strings (a rule's match examples and
// justification come after its pattern and decision, in any order).
func balanced(s string, i int, open, close byte) (string, bool) {
	depth, quoted := 0, false
	for j := i; j < len(s); j++ {
		switch c := s[j]; {
		case c == '"' && (j == 0 || s[j-1] != '\\'):
			quoted = !quoted
		case quoted:
		case c == open:
			depth++
		case c == close:
			if depth--; depth == 0 {
				return s[i+1 : j], true
			}
		}
	}
	return "", false
}

// topLevel is a TOML file up to its first table: a profile's own approval_policy is not the default.
func topLevel(s string) string {
	if loc := regexp.MustCompile(`(?m)^\s*\[`).FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}

func stripComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if j := strings.Index(l, "#"); j >= 0 && !strings.Contains(l[:j], `"`) {
			lines[i] = l[:j]
		}
	}
	return strings.Join(lines, "\n")
}

func (r codexRule) matches(words []string) bool {
	if len(r.tokens) == 0 || len(r.tokens) > len(words) {
		return false
	}
	for i, alts := range r.tokens {
		found := false
		for _, a := range alts {
			found = found || a == words[i]
		}
		if !found {
			return false
		}
	}
	return true
}

// eval: of the rules that match, the most restrictive decision wins
// (forbidden, then prompt, then allow); with none, approval_policy decides.
func (p codexPolicy) eval(command string) deciding {
	words := strings.Fields(command)
	var best *codexRule
	rank := map[string]int{"forbidden": 3, "prompt": 2, "allow": 1}
	for i := range p.rules {
		r := &p.rules[i]
		if r.matches(words) && (best == nil || rank[r.decision] > rank[best.decision]) {
			best = r
		}
	}
	if best != nil {
		switch best.decision {
		case "forbidden":
			return deciding{vDeny, best.text, best.file}
		case "allow":
			return deciding{vAllow, best.text, best.file}
		}
		return deciding{vAsks, best.text, best.file}
	}
	if p.policy == "never" {
		return deciding{vAllow, `approval_policy "never"`, p.file}
	}
	return deciding{v: vAsks}
}

// ---- where the policy lives ----

// DefaultPolicyFiles are the files a runner reads its permissions from on this
// machine: the developer's own and the company's managed ones.
func DefaultPolicyFiles(home string, getenv func(string) string) func(runner string) []string {
	return func(runner string) []string {
		var files []string
		glob := func(pattern string) {
			m, _ := filepath.Glob(pattern)
			sort.Strings(m)
			files = append(files, m...)
		}
		switch runner {
		case "claude":
			cfg := getenv("CLAUDE_CONFIG_DIR")
			if cfg == "" {
				cfg = filepath.Join(home, ".claude")
			}
			managed := "/etc/claude-code"
			if runtime.GOOS == "darwin" {
				managed = "/Library/Application Support/ClaudeCode"
			}
			files = append(files, filepath.Join(cfg, "settings.json"), filepath.Join(managed, "managed-settings.json"))
			glob(filepath.Join(managed, "managed-settings.d", "*.json"))
		case "codex":
			cfg := getenv("CODEX_HOME")
			if cfg == "" {
				cfg = filepath.Join(home, ".codex")
			}
			// Later files win: the system's config under the user's, the managed one over both.
			files = append(files, "/etc/codex/config.toml", filepath.Join(cfg, "config.toml"), "/etc/codex/managed_config.toml")
			glob(filepath.Join(cfg, "rules", "*.rules"))
			glob("/etc/codex/rules/*.rules")
		}
		return files
	}
}
