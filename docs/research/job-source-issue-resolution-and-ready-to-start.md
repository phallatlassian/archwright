# Where should the factory guarantee a Job knows its source issue? (#134)

Researched 2026-11-26 against `main` at 38329c2. Primary sources only: this repo's code and ADRs, the local `gh` 2.104.0 help text, and GitHub's docs page on autolinked references. Nothing was run against a real Forge or a live Archwright; behaviour claims come from reading code. Anything not verified is marked UNVERIFIED.

## Summary

- The developer's hypothesis is half right. Making the Archwright resolve the URL from a Site plus issue number is correct and cheap, but putting it only in the Archwright's instructions is the weakest of the options: the instructions already say nothing that would have prevented attempt 7, and a model instruction is not enforced. The Works already holds everything needed to resolve a reference (Site remote URL, `remote.Normalize`), so the deterministic part belongs in `create_job`.
- The brief's description of the Warden failure is stale. `read_issue` no longer exists (ADR 0028, `docs/spec/tmux-runtime.md:162`); the Warden is told to `gh issue view` the URL in its step brief, and with no URL it has nothing to read. The failure mode is the same, the mechanism differs.
- Recommendation: a combination, with `create_job` doing the resolution and refusing a contradiction, the Archwright instructions telling it to pass the reference and to ask the developer when the Site or issue is unclear, and the Warden's rule changed to raise an Ask in place of `failed`. Details in section 5.

## 1. Current behaviour (verified)

- `create_job` is declared at `internal/works/tools.go:75`. It decodes `title`, `description`, `site`, `source_issue` (`tools.go:76-79`), and calls `CreateJobFor`. With no Site it refuses only when Sites exist (`tools.go:87-96`), returning the registered Site names. It never looks at `source_issue` beyond passing it on.
- `ValidSourceIssue` (`internal/store/jobs.go:98`) trims, requires http(s), a host, no query or fragment, no whitespace, and a path ending `/issues/<digits>` with at least four segments (`jobs.go:99-107`). It does not fetch, and it does not compare the issue's repository with the Job's Site. A URL for any repo on any host passes. It is called only from `createJob` (`jobs.go:127-132`; the only non-test caller, per grep).
- The Archwright instructions say `create_job` "takes an optional `source_issue`: the URL of the issue the Job is for" (`internal/launch/archwright.md:16`). They say nothing about a short reference such as `#1`, `archwright-fixture#1` or "fix W01", nothing about when the field is required, and nothing about `list_sites`' use for resolving one. `list_sites` itself is listed only by name, "(every registered Site)" (same line). It returns the full Site rows, including `url` (`tools.go:105-111`, `internal/store/sites.go:36-44`, `ListSites` at `sites.go:176`).
- The Archwright has no Ask tool. `ask` is Wright and Warden only (`internal/works/asks.go:21`); the Archwright's tools there are answer, accept-default, skip and cancel. ADR 0015 says the Archwright "puts [an ask] to the developer" (`docs/adr/0015-asks-travel-up-park-the-run-and-never-expire.md`, first paragraph), so the Archwright's route to the developer is plain conversation, with Lavish for asks that reach it. So "the Archwright raises an Ask (ADR 0015)" is not available as a tool call; for the Archwright it means asking the developer in the session.
- What the Warden sees: `renderStepBrief` adds "Source issue: URL (read it with `gh issue view` ...)" only when `job.SourceIssue` is non-empty (`internal/works/step.go:188-190`). `internal/works/warden.md` says to read the source issue "when the step names one" with `gh issue view <URL>`. The review brief does the same (`internal/works/review.go:107-109`). With an empty `source_issue` the brief carries no issue pointer at all; the Warden is only told the Job's title and description (`step.go:185-187`).
- The Job request text is the only other carrier. A `#N` in the title or description is left as text, never parsed (`createJob`, `jobs.go:122-148`).
- The shakedown record agrees with this: attempt 7 (`job-1796edaff4`) had no `source_issue` and no URL in the brief; the Warden dispatched a read-only Wright, found nothing and handed back failed (`docs/fixture-shakedown-plan.md:71`). Attempt 8, given the full URL, got past that point (`fixture-shakedown-plan.md:72`).
- The brief's own wording "`read_issue` returned 'This Job has no source issue'" is out of date: ADR 0022 defined that result (`docs/adr/0022-...md:7`), but ADR 0028 supersedes ADR 0022 and deletes `read_issue`; a call to it now answers `unknown_tool` (`docs/spec/tmux-runtime.md:162`). I found no Go code that still emits the "no source issue" string (grep of `*.go` and `*.md` outside tests and research returned only the ADR and the shakedown log).

## 2. Sites and remote URLs: what resolution has to handle

- ADR 0019: a Site is registered by the URL of its remote repository; local paths and `file://` are refused; two Sites cannot share a normalised URL (`docs/adr/0019-sites-are-registered-by-remote-url-and-the-works-keeps-its-own-clone.md`, paragraphs 1 and 3).
- The Works stores the URL as given by the developer plus a normalised `url_key` (`internal/store/sites.go:49-90`, in `AddSite`: `key, err := remote.Normalize(site.URL)` at line 57, then `url_key` in the INSERT). So `list_sites` returns whatever form the developer typed: `git@github.com:owner/repo.git`, `https://github.com/owner/repo`, or `ssh://git@host:2222/o/r.git`.
- `remote.Normalize` (`internal/remote/remote.go:24-71`) returns `host/path` lowercased, with scheme, user, default port, trailing slash and `.git` dropped. It deliberately makes `git@host:o/r.git`, `https://host/o/r` and `https://host/o/r.git` one key (`remote.go:19-23`). A non-default port is kept in the key (`remote.go:67-69`); for an SSH remote that port is the SSH port, not the web port, so `https://` plus the key is wrong for it. UNVERIFIED: no test or code in the repo covers deriving a web URL from such a Site.
- The existing precedent for "Site remote to web URL" is `forge.ParsePRURL` (`internal/forge/forge.go:121-146`) and `record_pr`: it accepts only `http(s)://HOST/OWNER/REPO/pull/N` and requires `HOST/OWNER/REPO` to equal the Site's normalised remote, case-insensitively (`docs/spec/tmux-runtime.md:162`, `record_pr` bullet). That is the same shape of check an issue URL needs, and `ValidSourceIssue` lacks it.
- GitHub reference forms (GitHub docs, "Autolinked references and URLs": https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/autolinked-references-and-urls): `https://github.com/OWNER/REPO/issues/N`, `#N`, `GH-N`, `OWNER/REPO#N`. These autolink "within conversations on GitHub" and "are not created in wikis or files in a repository". So they are GitHub UI conventions, not something `gh` or git resolves outside a repo. `#N` alone has no repository, so it is only resolvable given a Site (the launch hint or a named Site). `OWNER/REPO#N` names the repository but not the host, so the host has to come from a Site whose key starts with a matching `host/OWNER/REPO` (or be assumed github.com, which would be a guess).
- `gh issue view` accepts `{<number> | <url>}` and `-R [HOST/]OWNER/REPO` (local `gh issue view --help`, gh 2.104.0). A bare number needs a repository from context or `-R`; the Warden's checkout is an empty repo and the Wright's worktree is of the Works' bare clone, so a bare number is not safe to hand an agent (UNVERIFIED which of these `gh` would infer from; the code at `step.go` only passes URLs).
- Other hosts: `ValidSourceIssue` is host-agnostic (any http(s) host, final `issues/N`), which would accept GitLab's `.../-/issues/N` and Gitea-style URLs. Everything downstream in the factory reads issues through `gh` (ADR 0028; every fork skill says `gh issue view <URL>`, e.g. `internal/skills/vendor/forks/implement/SKILL.md:10`), and `gh` is a GitHub client; I did not find any non-GitHub Forge support in this repo (`internal/forge` has only `gh.go` and `gh_pr.go`). So a non-GitHub Site can carry a `source_issue` the agents cannot read. UNVERIFIED: that `gh` fails on a GitLab URL (not run); GitHub Enterprise hosts are supported by `gh` via `[HOST/]` and `GH_HOST` (`gh help environment`), so host-agnostic URL building is right for GHE.

## 3. Answers to the brief's questions

**Q1. Should the Works resolve Site plus issue number (or `#N` in the title, description or message) into a URL inside `create_job`, and how does it interact with `ValidSourceIssue`?**

Yes for an explicit, structured reference; no for free text.

- Accept a new optional input on `create_job` (for example `issue: 1`, or `source_issue` also accepting `#1`, `OWNER/REPO#1`). When the Site is known (given or from the launch hint), the Works builds `https://<url_key host and path>/issues/N`. For a normal GitHub or GHE remote the normalised key is exactly `host/owner/repo`, so this is `"https://" + key + "/issues/" + N`. Refuse to build when the key carries a non-default port (the SSH-port problem above) and tell the caller to pass the full URL.
- The built URL is then passed through `ValidSourceIssue` unchanged, so the shape rule stays the single gate for what is stored.
- Add to `ValidSourceIssue`'s caller (not the shape function) a Site match: when the Job has a Site, the URL's `host/owner/repo` should equal the Site's normalised URL, as `record_pr` does. Today a Job for Site A can store an issue URL of repo B. Whether that should be an error or a warning is a design decision; an agent reading an issue from another repo may be legitimate (a tracker repo separate from the code repo, as this repo's own shakedown fixture may be). UNVERIFIED which the developer wants; the recommendation is refuse by default.
- Do not parse `#N` out of title, description or the developer's message in the Works. The Works would be guessing from prose, and `#N` could be an ordinary number or a PR. That parsing, if wanted, is the Archwright's job, which sees the whole conversation and can ask.

**Q2. Should the Archwright refuse or raise an Ask when a request names an issue but the Job would have no `source_issue`, or when the Site is unknown?**

Yes, it should stop and ask the developer; but the Archwright cannot "raise an Ask" as a tool (`asks.go:21`), so this is an instruction plus a Works backstop.

- Instruction: when the developer's message mentions an issue (a `#N`, a "W01"-style ticket id, an `owner/repo#N`, an issue URL) the Archwright resolves the Site via `list_sites`, builds or confirms the URL, and passes it. If the name matches no Site or several, or the id matches no known issue, it asks the developer in the session before `create_job`.
- Backstop in the Works: when `site` is omitted and Sites exist, `create_job` already refuses and lists Site names (`tools.go:87-96`); that is the existing "Site is unknown" guard and it works. The Works cannot detect "the request names an issue", because that is prose, so the "names an issue but none stored" check cannot live there reliably. The cheap structured version: a `create_job` flag such as `no_issue: true`/`issue_reason`, or a refusal when the title or description contains an issue-shaped token (`#\d+`, an issues URL) and `source_issue` is empty. The second is a heuristic and will have false positives; it should be a refusal that the Archwright can override by passing an explicit field, never a silent rewrite. UNVERIFIED how often the false positives would matter; no data in this repo.

**Q3. Does `create_job` need a "ready to start" contract beyond `source_issue`?**

A small one, enforced in the Works where it is deterministic and in the instructions where it is judgement.

- Deterministic, Works side: a Site when any Site exists (already enforced, `tools.go:87-96`); a non-empty title (already, `jobs.go:124-126`); a well-formed, Site-matching `source_issue` when given (partly, per Q1).
- Judgement, instruction side: a description that says what done means; a source issue where the request is about one. Description is currently not required (`jobs.go:122-148` takes it unchecked) and cannot sensibly be, since a Job whose title is the whole request is legitimate.
- Where the Job has no Site because none are registered (`tools.go:97`), it is created siteless; that is existing behaviour and out of scope here.
- Note that `api.go:177-200` has a second creation path (`POST /jobs`, dashboard) that resolves the Site from a launch path and calls the same `CreateJobFor`. Any contract added to `CreateJobFor` or `createJob` covers both; one added in the `create_job` tool covers only the Archwright. UNVERIFIED which callers the dashboard has in practice.

**Q4. Warden: when a Job claims to be about an issue but has none, is an Ask better than handing back `failed`?**

Yes, in principle. The Warden's `ask` is allowed (`asks.go:21`), it parks nothing now (ADR 0029: the Session stays open and the answer is Nudged in, `docs/adr/0029-...md`, "Asks" paragraph), and ADR 0015 routes it Warden to Archwright to developer. Handing back `failed` ends the step and, per attempt 7, was cancelled from the dashboard (`fixture-shakedown-plan.md:71`).

- Caveat: the Warden cannot know a Job "claims to be about an issue" except from the title and description text; its rule would be a judgement ("the request mentions an issue but the brief has no Source issue line, ask for the URL first"). This is cheap and safe to add to `warden.md` next to the existing Forge paragraph, but it is a prompt rule, not enforced. It is the right last line of defence, not the primary fix: earlier resolution (Q1, Q2) avoids spending a Warden Session to find out.
- The ask kind is `clarification` (ADR 0015 kinds: decision, manual step, clarification; validated at `internal/store/asks.go:281-284`), blocking.
- UNVERIFIED: that a real Warden, given such a rule, reliably asks first rather than starting a read-only Wright as attempt 7's did; no live test was done.

**Q5. Prior art.**

- In this repo: ADR 0019 (a Site is identified by a normalised remote, which is what makes Site plus number resolvable), ADR 0015 (asks route a missing fact to the developer without failing the Job, and never expire), ADR 0029 (an Ask keeps the Session open and the answer is Nudged in, so asking is cheap), ADR 0022 (a no-issue Job once got a clear "no source issue" result; superseded by ADR 0028), `forge.ParsePRURL` plus the `record_pr` Site check as the model for validating a Forge URL against a Site. ADR 0010 (launch hint names the current Site) is why `create_job` already receives `site` (`archwright.md:20-24`).
- Other agent factories: none consulted. I did not find first-party documentation or source for another system's handling of short issue references that I could cite, and I did not search for it, so no claim is made.

## 4. Hypothesis check

"The Archwright makes sure everything is known before the Job starts, and works out the URL from a Site name and issue number."

- Confirmed that the Archwright is the right place to decide whether the request is about an issue and whether the Site is known: it is the only agent that sees the developer's words, it already receives the launch Site and `list_sites` (`archwright.md:16,20-24`), and it can talk to the developer.
- Challenged: making it work out the URL by prompt alone. The URL derivation is deterministic (Site key to `https://host/owner/repo/issues/N`), the Works already has `remote.Normalize`, and the only current check on the result is shape. Left to the model it can build a URL for the wrong host, the wrong repo, or an SSH-port key, and `ValidSourceIssue` would accept it. A tool that takes a number and a Site removes that class of error.
- Challenged: "everything is known before the Job starts" cannot be a Works guarantee for free-text requests, only for structured fields.

## 5. Recommendation: where the fix lives

A combination, in this order of weight.

1. `create_job` in the Works (primary, deterministic).
   - Accept an issue number or short reference with a Site and build the URL from the Site's normalised remote; refuse a key with a non-default port; run the result through `ValidSourceIssue`.
   - Check the issue URL's `host/owner/repo` against the Job's Site (the `record_pr` pattern), with an explicit override.
   - Optionally refuse an issue-shaped token in title or description with no `source_issue`, with an explicit "no issue" override.
   - Trade-offs: a few lines of Go with tests, same contract for the tool and `POST /jobs` if placed in `createJob`; cannot understand prose, and a non-default-port or non-GitHub remote needs the caller to pass the full URL.
2. Archwright instructions (`archwright.md`) (necessary, cheap, not sufficient).
   - Say what `source_issue` is for, that it is the full URL, how to turn a Site plus number or `owner/repo#N` into the new input, to call `list_sites` for the Site's remote, and to ask the developer before `create_job` when the Site or issue is ambiguous or when the request names an issue the Archwright cannot place.
   - Trade-offs: costs a prompt line and no code; unenforced, and the same prose-only fix would not have stopped attempt 7 (the instruction `archwright.md:16` already existed and it called the field optional).
3. Warden (`warden.md`) (backstop).
   - If the request names an issue and the step brief has no Source issue line, `ask` (clarification, blocking) for the URL before dispatching, in place of a read-only Wright and a `failed` handback.
   - Trade-offs: a prompt rule only; catches what slips through, but only after a Warden Session has started and one Ask round trip has happened.

Not recommended: resolving by fetching the issue in `create_job` (the Works' own `gh` may lack the developer's credentials; ADR 0024 found this for PR reads, quoted at `docs/spec/tmux-runtime.md:162`; and ADR 0028 moved Forge access to agents). Shape plus Site match is enough.

## 6. Suggested tickets

1. Works: `create_job` accepts an issue number or `OWNER/REPO#N` and builds the issue URL from the Site's normalised remote. Tests: SSH `git@host:o/r.git`, HTTPS with and without `.git`, GHE host, non-default SSH port refused, `#N` with no Site refused with the registered Site names. Touches `internal/works/tools.go`, `internal/store/jobs.go`. `ready-for-agent`.
2. Works: reject a `source_issue` whose `host/owner/repo` is not the Job's Site, with an explicit override, mirroring `record_pr`. Decide first (`ready-for-human`) whether a cross-repo tracker is a supported case; if so the override is required, not optional.
3. Archwright instructions: document the input, `list_sites` resolution, and the ask-before-create rule in `internal/launch/archwright.md`; check the existing launch-instruction tests. Depends on ticket 1 landing the input name. `ready-for-agent`.
4. Warden rule: ask for the issue URL, as a blocking clarification, when the request names an issue and the step brief has no Source issue line (`internal/works/warden.md`). Independent of 1 to 3. `ready-for-agent`.
5. (Optional, lower value) Works: refuse issue-shaped tokens in title or description when `source_issue` is empty unless the caller sets an explicit no-issue flag. Needs a decision on false positives first. `needs-info`.
6. Housekeeping: the issue brief and `fixture-shakedown-plan.md:71` refer to `read_issue`; amend the wording or leave a note that it is now `gh issue view` (ADR 0028). Trivial.

## UNVERIFIED list

- Behaviour of `gh issue view <number>` outside a repository checkout, in the Warden's and Wright's environments.
- Whether `gh` fails or succeeds on non-GitHub issue URLs.
- Web URL derivation for Sites registered with a non-default SSH port.
- Whether a real Warden will ask rather than proceed when given the proposed rule.
- Whether dashboard `POST /jobs` callers pass a source issue today.
- How other agent factories handle short issue references (none consulted).
