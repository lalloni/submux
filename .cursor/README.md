# Cursor Configuration Guide

This document explains Cursor-specific configuration for the submux project and provides guidance for other AI coding assistants.

## 📁 Configuration Files Overview

```
submux/
├── CLAUDE.md                    # Claude Code entry point
├── AGENTS.md                    # Primary documentation for ALL agents
├── DESIGN.md                    # Architecture reference
├── .cursorrules                 # Cursor simple rules (project-wide)
└── .cursor/
    ├── README.md                # This file
    └── rules/
        ├── gopls-mcp-server.md  # Go tooling workflows (applyTo: **/*.go)
        ├── project-conventions.md # Project-wide standards (always_on)
        └── testing.md           # Testing guidelines (applyTo: **/*_test.go)
```

## 🤖 AI Assistant Compatibility Matrix

| AI Assistant | Loads Automatically | Configuration File(s) |
|--------------|--------------------|-----------------------|
| **Claude Code** | ✅ CLAUDE.md | `CLAUDE.md` → redirects to `AGENTS.md` |
| **Cursor** | ✅ .cursorrules + .cursor/rules/* | `.cursorrules` (simple) + `.cursor/rules/` (advanced context-aware) |
| **GitHub Copilot** | ❌ None | No project-specific instructions support |
| **Aider** | ⚠️ Manual | Requires `--read` flag to load `AGENTS.md` |
| **Continue.dev** | ⚠️ Manual | Add to context via UI |
| **Cline** | ⚠️ Manual | Add custom instructions via UI |
| **Windsurf** | ⚠️ Manual | Load via custom instructions |

## 📖 Primary Documentation Strategy

**All AI assistants should be directed to read these files in order:**

1. **AGENTS.md** (402 lines) - START HERE
   - Development workflows and commands
   - Quick reference with links to detailed docs
   - Code conventions and critical rules

2. **DESIGN.md** (355 lines) - Technical Deep Dive
   - Architecture and design patterns
   - Lock ordering rules (CRITICAL for thread safety)
   - Testing strategy and best practices

3. **README.md** (~100 lines) - User Documentation
   - Public API and examples
   - Feature overview
   - Getting started guide

4. **CHANGELOG.md** (~200 lines) - Version History
   - Release notes
   - Breaking changes

## 🎯 Recommended Setup by AI Assistant

### Claude Code

**Status:** ✅ Fully Configured

Claude Code automatically loads `CLAUDE.md`, which redirects to `AGENTS.md`. No additional setup needed.

**Files used:**
- `CLAUDE.md` (entry point)
- `AGENTS.md` (primary instructions)
- `DESIGN.md` (architecture reference)

### Cursor

**Status:** ✅ Fully Configured

Cursor automatically loads configuration from:
- `.cursorrules` - Simple project-wide rules (always active)
- `.cursor/rules/*.md` - Advanced context-aware rules (activate based on file type)

**Files used:**
- `.cursorrules` - Basic project conventions and workflows
- `.cursor/rules/gopls-mcp-server.md` - Activated when editing `*.go` files
- `.cursor/rules/project-conventions.md` - Always active
- `.cursor/rules/testing.md` - Activated when editing `*_test.go` files

**How it works:**
Cursor's advanced rules system uses YAML frontmatter to control when rules apply:
- `trigger: always_on` - Rule is always loaded
- `applyTo: "**/*.go"` - Rule only applies to Go files
- This provides context-specific guidance without overwhelming the AI

**Additional setup (optional):**
Add to Cursor settings to explicitly include documentation:
```json
{
  "cursor.general.projectContext": [
    "AGENTS.md",
    "DESIGN.md"
  ]
}
```

### Aider

**Status:** ⚠️ Requires Manual Setup

**Recommended command:**
```bash
aider --read AGENTS.md --read DESIGN.md
```

**Or create `.aider.conf.yml`:**
```yaml
read:
  - AGENTS.md
  - DESIGN.md

auto-commits: false
edit-format: diff
```

### GitHub Copilot

**Status:** ❌ Limited Support

GitHub Copilot doesn't support project-specific instructions. It learns from:
- Existing code patterns
- Comments in open files
- Recently edited files

**Workaround:** Add detailed comments in code files referencing AGENTS.md and DESIGN.md.

### Continue.dev

**Status:** ⚠️ Requires Manual Setup

**Setup:**
1. Open Continue.dev panel
2. Click "Add to context" (⊕)
3. Add `AGENTS.md` and `DESIGN.md`

**Or configure in `.continue/config.json`:**
```json
{
  "contextProviders": [
    {
      "name": "file",
      "params": {
        "files": ["AGENTS.md", "DESIGN.md"]
      }
    }
  ]
}
```

### Other AI Assistants (Cline, Windsurf, etc.)

**Status:** ⚠️ Varies by Tool

**General approach:**
1. Look for "Custom Instructions" or "Project Context" settings
2. Manually load `AGENTS.md` and `DESIGN.md`
3. Or start each session with: "Please read AGENTS.md and DESIGN.md"

## 🔍 About `.cursor/rules/` Directory

**Current Status:** ✅ **Cursor-Specific Advanced Rules**

The `.cursor/rules/` directory with YAML frontmatter (`trigger`, `applyTo`) is:
- ✅ Native Cursor feature for context-aware rules
- ✅ Provides file-type-specific guidance automatically
- ⚠️ Not supported by other AI assistants (Cursor-specific)
- 📚 Content can be adapted for other tools (see `.cursorrules`)

**Files in `.cursor/rules/`:**
1. `gopls-mcp-server.md` - Go development workflows with gopls MCP tools (applies to `*.go`)
2. `project-conventions.md` - Project-wide coding standards (always on)
3. `testing.md` - Testing guidelines and utilities (applies to `*_test.go`)

**Benefits for Cursor users:**
- Rules automatically activate based on file type
- Reduces cognitive load (only see relevant rules)
- Maintains consistency across different file types
- Scales better than single monolithic `.cursorrules` file

**For other AI assistants:**
Use `.cursorrules` which consolidates these rules into a simpler format.

## 🛠️ For AI Assistant Developers

If you're building an AI coding assistant and want to support this project's conventions:

**Option 1: Load AGENTS.md directly**
```
1. Check for CLAUDE.md or AGENTS.md in project root
2. Parse markdown and extract instructions
3. Present to LLM as system context
```

**Option 2: Support .cursor/rules/ format (Cursor's approach)**
```
1. Scan .cursor/rules/ directory
2. Parse YAML frontmatter (trigger, applyTo)
3. Apply rules based on current file context
4. Example: gopls-mcp-server.md activates for *.go files
```

**Recommended approach:** Support both! AGENTS.md for general instructions, `.cursor/rules/` for context-specific rules.

## 📊 Testing Your Configuration

To verify your AI assistant is using project instructions correctly:

1. **Ask about architecture:**
   - ✅ Should reference DESIGN.md sections
   - ✅ Should mention lock ordering rules
   - ✅ Should explain single event loop pattern

2. **Ask about workflows:**
   - ✅ Should reference AGENTS.md commands
   - ✅ Should know testing procedures
   - ✅ Should mention documentation update order

3. **Ask about testing:**
   - ✅ Should use retry utilities for integration tests
   - ✅ Should know about table-driven test patterns
   - ✅ Should mention 66% coverage target

If the assistant can't answer these, it's not loading the project documentation properly.

## 🔄 Maintenance

**When updating project instructions:**

1. Update `AGENTS.md` (primary source of truth)
2. Update `DESIGN.md` (if architecture changes)
3. Update `.cursorrules` to match `AGENTS.md` (keep in sync)
4. Update `.cursor/rules/*` if context-specific rules change
5. Update `CHANGELOG.md` (document the changes)

**Avoid:**
- ❌ Duplicating content between files
- ❌ Letting files drift out of sync
- ❌ Creating tool-specific docs without clear ownership

**Remember:** `AGENTS.md` + `DESIGN.md` are the source of truth. Tool-specific configs (`.cursorrules`, etc.) should derive from these, not add new content.

## 🆘 Troubleshooting

### "AI assistant isn't following project conventions"

**Diagnosis:**
1. Check which configuration file the tool loads
2. Verify file exists and has correct content
3. Try explicitly asking: "Have you read AGENTS.md?"

**Solutions:**
- Claude Code: Check `CLAUDE.md` exists and redirects to `AGENTS.md`
- Cursor: Verify `.cursorrules` and `.cursor/rules/*.md` exist and are up to date
- Others: Manually load `AGENTS.md` and `DESIGN.md`

### "AI assistant suggestions conflict with DESIGN.md"

**Response:**
Provide feedback like: "This conflicts with DESIGN.md section X. Please review the lock ordering rules."

Most assistants will correct themselves when given explicit references.

### "Want to add new rules"

**Process:**
1. Add to `AGENTS.md` first (source of truth)
2. Update `DESIGN.md` if architecture-related
3. Regenerate tool-specific configs (`.cursorrules`, etc.)
4. Update this README if affecting compatibility
5. Document in `CHANGELOG.md`

## 📚 Additional Resources

- **AGENTS.md** - Complete development guide
- **DESIGN.md** - Architecture documentation
- **integration/README.md** - Integration testing guide
- **CHANGELOG.md** - Version history

## 🤝 Contributing

When contributing to this project:

1. Read `AGENTS.md` thoroughly
2. Follow conventions in `DESIGN.md`
3. Ensure your AI assistant is configured correctly (see above)
4. Update documentation when making changes
5. Test with both unit and integration tests

---

**Last Updated:** 2026-01-31
**Maintained by:** submux project team
