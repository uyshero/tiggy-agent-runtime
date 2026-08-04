# Skill Semantics

## Scope

A Skill is a trusted, versioned instruction resource selected by the application before an Agent run. It may declare Toolkit Groups and optional text Resources. A Skill does not execute code, register Tools, download dependencies, select itself, or mutate Agent state.

Core uses a strict Tiggy V1 manifest rather than guessing among incompatible `SKILL.md` frontmatter dialects. An external-format adapter may translate another ecosystem format into this contract later.

```text
skills/
  review/
    skill.json
    SKILL.md
    references/
      rules.md
```

```json
{
  "schema": "tiggy.skill/v1",
  "name": "review",
  "version": "1.2.0",
  "description": "Review Go changes.",
  "instructions": "SKILL.md",
  "tool_groups": ["repository.read"],
  "resources": [
    {
      "name": "rules",
      "path": "references/rules.md",
      "description": "Project review rules."
    }
  ]
}
```

Names use lowercase ASCII letters, digits, and internal hyphens. Version is an opaque bounded identifier; Core records it but does not infer ordering or compatibility. Paths are canonical forward-slash relative paths and cannot escape the Skill directory. Unknown fields, duplicate JSON keys, duplicate names, duplicate paths, and directory/name mismatches are rejected.

## Discovery and Activation

`Discover` receives a caller-owned `safespace.Reader`. `SafeSpace` is the standard local implementation and prevents symlink or parent-path escape. Discovery examines direct child directories, reads only `skill.json`, validates the complete catalog, and returns sorted immutable Descriptors. Non-directory files are ignored; every direct child directory must be a valid Skill. Discovery is fail-closed and never publishes a partial catalog.

Activation is a separate explicit step:

```go
space, err := safespace.OpenSafeSpace("/absolute/app/skills", safespace.Limits{})
if err != nil {
    return err
}
defer space.Close()

catalog, err := skill.Discover(ctx, space, ".", skill.Limits{})
if err != nil {
    return err
}
active, err := catalog.Activate(ctx, skill.ActivationRequest{
    Name: "review", Resources: []string{"rules"},
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name: "reviewer", Model: provider, ModelIdentity: identity,
    Toolkit: kit, Skills: active,
})
```

All Skill and Resource selections are validated before content I/O begins. Only selected Resources are read. Instructions, Resources, Tool Groups, and a SHA-256 content Digest are copied into one immutable Activation snapshot. Later file changes do not mutate an existing Agent; rediscovery and reactivation are explicit application decisions.

For a new conversation, Agent appends the rendered Activation to `Config.SystemPrompt` in one internal System Message. `Continue` reuses the previous State and does not inject the Skill again. Active Skill Tool Groups are unioned with `Config.ToolGroups`. When any Skill is active, an empty union exposes no Tools instead of falling back to the entire Toolkit. Unknown Groups fail during `agent.New`.

`run.started` records only Skill name, version, digest, and effective Tool Groups. Instruction and Resource content is not copied into default events.

## Limits

Zero fields use these defaults:

```text
Catalog Skills              128
Manifest                     64 KiB per Skill
Instructions                128 KiB per Skill
Declared Resources           32 per Skill
Selected Resource           256 KiB each
Rendered Activation         512 KiB total
```

Content must be non-empty UTF-8 text without null bytes. Catalog and Activation values are copied before publication. Context cancellation is checked around every Source call. Source panics become `source_failed`; they cannot crash the host process. The caller owns and closes the Reader and must make it concurrency-safe when a Catalog is activated concurrently.

## Trust Boundary

Activated content has System-instruction authority. Applications must activate only curated Skills from an authorized source. SafeSpace contains local paths but does not establish publisher trust, signature verification, tenant authorization, or remote package integrity.

Core intentionally provides no global Skill search path, environment-driven loading, automatic model-selected activation, remote URL fetch, package installer, script hook, or dynamic Tool registration. These choices keep authority visible at Agent construction and leave enterprise distribution, approval, and tenancy outside Core.
