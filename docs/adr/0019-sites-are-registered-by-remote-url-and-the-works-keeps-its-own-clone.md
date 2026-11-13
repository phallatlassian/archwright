# Sites are registered by remote URL, and the Works works only in its own clone

A Site is registered by the URL of its remote repository, never by a path to a local checkout. Registering clones the repository into a Works-owned directory under the factory state directory, outside every developer checkout, and every worktree a Wright, merge Wright or read-only Wright works in is created from that clone. The factory never reads, writes, fetches into, or runs git commands in the developer's own copy of the repository. This supersedes the path-based registration in ADR 0010 and tightens ADR 0008, whose "outside the Project checkout" rule kept worktree directories apart from the developer's checkout but still shared its `.git` directory, its branches and refs, its hooks and its config.

The clone is a bare clone with a normal fetch refspec, so remote branches appear as `refs/remotes/origin/*`; there is no working tree in it, so nothing can be checked out or edited there and the base branch is never "checked out" by anyone. The Works fetches before creating a Job's integration worktree and resolves the brief's base ref against the freshly fetched remote branch, never a local branch, so a Job always starts from what the remote has and not from whatever the developer happens to have unpushed. Gate defaults are detected from the base branch's tree in the clone and confirmed by the developer as before. The registered remote is the clone's `origin`: pushes and PRs go there, using the developer's own credentials and the runner's normal permission rules (ADR 0008), and the Works never stores credentials.

Registration input is the URL, a name (defaulting to the repository name), the base branch (defaulting to the remote's default branch), the concurrency cap and the gates. Registration fails if the URL is not reachable with the developer's existing credentials or the base branch does not exist on the remote. Local paths and `file://` URLs are refused: local-only repositories, with no remote to push to and open a PR against, are not supported. Two Sites cannot share a normalised URL. Removing a Site removes its clone; any Job state in the Works is kept per ADR 0017.

The `archwright` CLI's hint of the current Site (ADR 0010) now matches the launch directory's `origin` URL, normalised, against registered Sites rather than matching paths. Work the developer has not pushed is invisible to the factory by design; to hand it over, push it.

## Consequences

- The Works owns a clone's whole lifetime: create on register, fetch before Jobs, prune stale worktree metadata, delete on removal. Disk use is one object store per Site plus its worktrees.
- Worktree cleanup, branch deletion and `worktree prune` (ADR 0008) now touch only the Works' clone.
- A Wright with a shell can still reach the clone's `.git` through its worktree's pointer file, but that is the factory's copy and the developer's is out of reach.
- Developers lose the shortcut of registering a repo they have not pushed anywhere.
