// Package owngitchecks embeds the shared owngit-checks Agent Skill, so every
// owngit binary can install the skill it shipped with. The file stays at its
// source path because the release tool and the guides refer to it there.
package owngitchecks

import _ "embed"

// Name is the skill's directory name, which coding tools use to find it.
const Name = "owngit-checks"

// Skill holds the exact bytes of SKILL.md.
//
//go:embed SKILL.md
var Skill []byte
