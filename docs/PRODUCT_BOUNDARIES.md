# Product boundaries

These constraints define OwnGit's product behavior. They are not a list of implemented features; see the [README](../README.md) for current capabilities. OwnGit provides private Git storage and a browser dashboard on user-controlled computers and servers while preserving familiar Git workflows. Changes, check results, and recovery should remain understandable without Git expertise. The development workflow is documented in [AGENTS.md](../AGENTS.md).

## Private Git storage

- Keep OwnGit's public source separate from users' private repositories. Core repository use must not require published code, an external Git-host account, or an AI subscription.
- Preserve submitted work and Git history. Before accepting a force-push or branch/tag deletion, retain the history it replaces or deletes. Never silently discard current work or delete history. Recovery covers Git-tracked files, not application databases, uploads, untracked files, or full-system state.
- Browser restore must preview additions, changes, and deletions, then add a commit without rewriting an existing branch. Refuse the write if the branch changed after preview. Repository paths and symbolic links are data, not host filesystem paths.
- A portable backup preserves every repository ref and object reachable from those refs, repository metadata, HEAD identity, access mode, and password hashes. Host sessions, setup capabilities, trusted-host approval, and transport consent stay machine-local.
- Build repository activity from commit author dates across working branches and retained historical branches. Count an identical commit once per repository. Ref rewriting or deletion must not erase retained activity, and activity is not evidence that checks passed. When a bounded observation is truncated, prefer current history and report the result as incomplete.

## Access and administration

- Support local and private-network use, including Tailscale networks and company LANs. Recommend Tailscale when connecting from another device. Public Internet hosting is outside this scope.
- Let the owner choose password-free general repository access or one shared general-access password. General password protection can be enabled, changed, or disabled. Do not require individual accounts or permanent registration of an administrator browser.
- Use a separate administrator password for security settings, and require the current password for every change from any browser. Host management rights must allow password recovery without deleting repository data.
- Begin setup through an installation-owner-only, one-time link. Configure repository storage, optional general-access protection, and the administrator password before opening an empty dashboard. Do not require repository creation during setup.
- Support ordinary LAN HTTP only after clear notice that transport is unencrypted and the user makes an informed choice. Keep connection status visible. Password protection does not encrypt transport, and a Tailscale-related name alone does not prove end-to-end protection. Certificate issuance and management are outside this design.

## Verification and coding-tool integrations

- Keep storage, visibility, and acceptance policy separate from coding-agent execution. OwnGit must not silently launch coding sessions or move heavyweight checks onto the storage host.
- Bind check evidence to the tested revision, configuration, and policy. Never reuse stale success for changed work or present an agent summary or AI review as an executed check.
- Missing checks are not passing checks, but their absence alone does not block progress. A failed check preserves submitted work and holds main-version promotion within the verified enforcement boundary.
- Keep ordinary push independent from pull requests. An optional pull request review must be requested or explicitly skipped. Bind every decision and merge to exact source and target revisions, and invalidate the decision when either revision moves.
- Record an external coding tool's reviewer label as supplied provenance. Do not present it as independent review or executed checks, and do not launch an AI reviewer implicitly.
- Publish a pull request merge as a fast-forward or a nonrewriting merge commit. Preserve exact revision refs and a protected merge receipt so retries and recovery do not create another merge.
- Claim protection for an external Git host only when that enforcement has been verified. In coexistence mode, keep the external host authoritative.
- External review or integration must not silently transmit private code. Require explicit, informed enablement and disclose the data being sent.

## Security and durable records

- Treat hosted code and check commands as untrusted. Keep unrelated files, credentials, and host control data out of their reach.
- Password-free general access does not remove administrator-confirmation requirements, Host/origin/CSRF protections, or input and data safeguards. Private-network membership alone does not prove installation ownership. Authenticate helper results separately from general repository access.
- Keep repository history, task summaries, and check outcomes separate from disposable raw logs. Log cleanup must not remove those durable records.
