# Guidelines for developing OwnGit

## Audience and scope

These instructions govern agents changing OwnGit's source. They do not govern end users or projects hosted in OwnGit. Read [Product principles in CONTRIBUTING.md](CONTRIBUTING.md#product-principles) before changing product behavior.

- These rules apply across the repository. Read any applicable directory-level `AGENTS.md` before changing a file.
- Keep durable rules here. Put detailed procedures in public development documentation and link them from the relevant rule. Public instructions must not depend on private planning files, a contributor's machine configuration, or a particular coding-agent setup.

## Task scope and permissions

- Answer questions and complete requested research before editing. A request for explanation or review does not authorize implementation.
- Within an authorized task, make the necessary code, test, and documentation changes. Clarify material ambiguity, and inspect the repository instead of asking for facts already available there.
- Obtain explicit authorization before destructive data operations, history rewrites, deployment, publication, or other external writes. Commit and push only when authorized. Do not ask again for authorization already given.
- Fix the underlying cause and any related changes needed to complete the task. Report unrelated defects or improvements separately with their locations and impact; do not include opportunistic cleanup.
- Preserve existing changes and data. Before Git writes, inspect the working tree and index. Stage only intended paths, and never discard unrelated work to clean the workspace.

## Protect user repositories and data

- Keep OwnGit's public source separate from users' private repositories. Permission to change or publish OwnGit does not authorize access to, changes to, or publication of hosted projects.
- Preserve user files and Git history, including commits, branches, and tags. Never use real user repositories as disposable development or test data.
- Keep private repository contents, credentials, and operational data out of development output, fixtures, logs, and public artifacts.

## Code and dependencies

- Read the relevant implementation and its callers before changing behavior. Choose the simplest design that meets the requirements, including failure handling and security.
- Separate modules by responsibility, not line count. Do not add unrelated responsibilities to a large file or split straightforward code into unnecessary helpers or layers.
- Reuse or improve suitable shared functionality instead of duplicating it, without forcing unrelated cases into one abstraction. Keep interfaces, ownership, and data flow clear. Prefer simple control flow, and preserve required behavior and public contracts unless the task changes them.
- Prefer existing dependencies and standard facilities. Before adding a library, evaluate its maintenance health, license, security, runtime cost, and ongoing maintenance cost, then explain why it is needed and verify the result. Obtain approval for dependencies that materially change installation or operations, such as a new database, always-on service, or paid external service. Do not add infrastructure merely to reduce code.

## Verification and completion

- Find build and test commands in checked-in configuration, scripts, or documentation. Do not invent commands, import assumptions from another project, or report unavailable checks as completed.
- Match verification to the change. Use focused tests for local behavior and cover affected integration boundaries for shared, storage, recovery, or security changes. Documentation-only changes usually need content and link checks rather than a build.
- For a bug, reproduce the failure when practical, fix its cause, verify the original behavior, and add a meaningful regression check when appropriate. State when reproduction was unavailable.
- Test observable behavior and required failure paths. Do not weaken checks, remove required behavior, or add meaningless tests to manufacture a pass. Distinguish pre-existing failures from failures introduced by the change.
- For meaningful UI changes, use the actual interface at relevant sizes and check keyboard access, focus, and visible results. A click or screenshot alone does not prove an invisible state change; inspect the resulting state when relevant.
- Reuse valid verification. Repeat or broaden it only after a new change, failure, or unresolved risk.
- Report the changes, executed checks and outcomes, and remaining limitations with relevant links. Distinguish source review, tests, synthetic fixtures, and real integration evidence. Do not infer success from exit status alone.

## Public documentation and interface

- Write instructions, technical documentation, and code comments in concise English. Explain user-facing behavior plainly and define necessary terms. Keep public documentation in `README.md`, `CONTRIBUTING.md` and `docs/`, and keep private notes and operational material out of public files and Git history. Public instructions must not depend on ignored local files.
- Never include credentials, private keys, private repository contents, personal machine details, or sensitive logs in public examples, documentation, or captures. Use synthetic fixtures and preserve attribution and license notices.
- Keep documentation aligned with observable behavior. Record changing status separately from durable rules, and do not present planned features or untested platforms as supported.
- Preserve English-default and Korean product support, established Git terminology, and keyboard and assistive-technology access. Keep Light, Dark, and System appearance usable across the interface, preserve state during appearance changes, and do not use color as the only status indicator. Do not translate code identifiers, commands, paths, or commit hashes as interface prose.
