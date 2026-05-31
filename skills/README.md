# Claude Code skills

Vendored copies of the Claude Code skills for working with this library, so the
whole team shares one version-controlled source of truth.

## `ax-discovery2-client`

Wires `github.com/axgrid/ax-discovery2-client` into a Go project — registration
(with version), version-constrained & sticky balancing, and the config store.

To use it locally, make it visible to Claude Code one of these ways:

```bash
# symlink (stays in sync with the repo as it updates)
ln -s "$PWD/skills/ax-discovery2-client" ~/.claude/skills/ax-discovery2-client

# or copy
cp -r skills/ax-discovery2-client ~/.claude/skills/
```

Then trigger it in Claude Code (e.g. "подключи discovery", "add ax-discovery
client", "read config from discovery").

`skills/ax-discovery2-client/SKILL.md` is the single source of truth in this
repo; `.claude/skills/ax-discovery2-client/SKILL.md` is a symlink to it (so
Claude Code auto-discovers it when working inside this repo). When you edit the
skill, also refresh your per-developer copy under `~/.claude/skills/`.
