// Package gaffer embeds the playbooks and agent skill shipped with the binary.
package gaffer

import "embed"

//go:embed playbooks/*.md
var Playbooks embed.FS

//go:embed skills/gaffer/SKILL.md
var Skill []byte
