package orch

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sophonz-labs/stui/internal/home"
)

// The Claude Code plugin `stui`: two skill stubs that
// send the agent to `stui skills get`, so the guide always matches the stui that
// runs the commands. Kept in ~/.stui/plugins as a local marketplace.

func PluginRoot() string { return filepath.Join(home.Dir(), "plugins") }

const orchestrateSkill = `---
name: orchestrate
description: "Use stui for local terminal operations, agent status, task files and messages, hooks and skills, or worker orchestration. Applies to stui requests even without delegation."
allowed-tools: "Bash(stui *)"
---

Start with the common guide built into the installed stui:

` + "```bash\nstui skills get common\n```" + `

Read only the topic it selects for the user's task. Status, terminal, file and
hook work do not require a new run or workers. If STUI_RUN is set or a coordinator
gives you a task, also read stui skills get worker and report completion there.
Use stui's local interface; st and its MCP tool operate a different product.
`

const workerSkill = `---
name: worker
description: "Use when STUI_RUN is set or stui's coordinator assigns a task, including adoption: read the common guide and worker obligations, then report with stui worker done."
user-invocable: false
allowed-tools: "Bash(stui *)"
---

You are a worker in an stui run. Read the common guide and your obligations:

` + "```bash\nstui skills get common\nstui skills get worker\n```" + `

Your task is in STUI_TASK_FILE or the coordinator's brief. Keep its ownership
and acceptance criteria. Report verified completion with stui worker done
--summary "<result and checks>". Read the orchestrate topic only when delegating.
`

// pluginFiles is the versioned source for the Claude plugin and skill stubs.
func pluginFiles(version string) map[string]string {
	return map[string]string{
		".claude-plugin/marketplace.json": mustJSON(map[string]any{
			"name":     "stui",
			"owner":    map[string]string{"name": "sophonz-labs"},
			"metadata": map[string]string{"description": "stui terminal, agent operations and orchestration"},
			"plugins":  []map[string]string{{"name": "stui", "source": "./stui", "description": "Use stui terminals, agent hooks, briefs and worker orchestration"}},
		}),
		"stui/.claude-plugin/plugin.json": mustJSON(map[string]any{
			"name":        "stui",
			"version":     strings.TrimPrefix(version, "v"),
			"description": "Use stui terminals, agent hooks, briefs and worker orchestration",
			"author":      map[string]string{"name": "sophonz-labs"},
		}),
		"stui/skills/orchestrate/SKILL.md": orchestrateSkill,
		"stui/skills/worker/SKILL.md":      workerSkill,
	}
}

func WritePlugin(version string) (string, error) {
	root := PluginRoot()
	return root, writeFiles(root, pluginFiles(version))
}

func writeFiles(root string, files map[string]string) error {
	for rel, body := range files {
		p := filepath.Join(root, rel)
		old, err := os.ReadFile(p)
		if err == nil && string(old) == body {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			return err
		}
	}
	return nil
}

func filesCurrent(root string, files map[string]string) (bool, error) {
	for rel, body := range files {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if string(b) != body {
			return false, nil
		}
	}
	return true, nil
}

// CodexSkillsDir is where Codex looks for skills ($CODEX_HOME/skills).
func CodexSkillsDir() string {
	h := os.Getenv("CODEX_HOME")
	if h == "" {
		d, _ := os.UserHomeDir()
		h = filepath.Join(d, ".codex")
	}
	return filepath.Join(h, "skills")
}

// codexFiles namespaces the same two entrypoints for Codex.
func codexFiles() map[string]string {
	files := map[string]string{}
	for name, body := range map[string]string{"stui-orchestrate": orchestrateSkill, "stui-worker": workerSkill} {
		// user-invocable is Claude-specific; Codex discovers workers from
		// their description and does not accept that frontmatter property.
		body = strings.Replace(body, "user-invocable: false\n", "", 1)
		files[filepath.Join(name, "SKILL.md")] = strings.Replace(body, "name: "+strings.TrimPrefix(name, "stui-"), "name: "+name, 1)
	}
	return files
}

func WriteCodexSkills() (string, error) {
	d := CodexSkillsDir()
	return d, writeFiles(d, codexFiles())
}

// RefreshPlugin repairs only previously installed products. Comparing contents
// repairs old templates even when the binary version has not changed.
func RefreshPlugin(version string, claude func(...string) (string, error)) (bool, error) {
	changed := false
	installed := map[string]string{}
	for rel, body := range codexFiles() {
		if _, err := os.Stat(filepath.Join(CodexSkillsDir(), filepath.Dir(rel))); err == nil {
			installed[rel] = body
		} else if !errors.Is(err, os.ErrNotExist) {
			return changed, err
		}
	}
	current, err := filesCurrent(CodexSkillsDir(), installed)
	if err != nil {
		return changed, err
	}
	if !current {
		if err := writeFiles(CodexSkillsDir(), installed); err != nil {
			return changed, err
		}
		changed = true
	}
	root := PluginRoot()
	if _, err := os.Stat(filepath.Join(root, "stui")); errors.Is(err, os.ErrNotExist) {
		return changed, nil
	} else if err != nil {
		return changed, err
	}
	files := pluginFiles(version)
	current, err = filesCurrent(root, files)
	if err != nil {
		return changed, err
	}
	if !current {
		if _, err := WritePlugin(version); err != nil {
			return changed, err
		}
		changed = true
	}
	// Only acknowledge Claude's cached copy after its update succeeds. A failed
	// update must be retried on the next startup even though local files are new.
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(mustJSON(files))))
	receipt := filepath.Join(root, ".skills-current")
	have, err := os.ReadFile(receipt)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return changed, err
	}
	if claude != nil && (!current || !bytes.Equal(have, []byte(digest))) {
		if err := UpdateClaudePlugin(claude); err != nil {
			return changed, err
		}
		if err := os.WriteFile(receipt, []byte(digest), 0644); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// UpdateClaudePlugin refreshes a registered plugin; it never registers one.
func UpdateClaudePlugin(claude func(...string) (string, error)) error {
	out, err := claude("plugin", "list")
	if err != nil {
		return fmt.Errorf("list Claude plugins: %w: %s", err, out)
	}
	if !strings.Contains(out, "stui@stui") {
		return nil
	}
	for _, args := range [][]string{{"plugin", "marketplace", "update", "stui"}, {"plugin", "update", "stui@stui", "--scope", "user"}} {
		if out, err := claude(args...); err != nil {
			return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func RemoveCodexSkills() {
	d := CodexSkillsDir()
	os.RemoveAll(filepath.Join(d, "stui-orchestrate"))
	os.RemoveAll(filepath.Join(d, "stui-worker"))
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b) + "\n"
}
