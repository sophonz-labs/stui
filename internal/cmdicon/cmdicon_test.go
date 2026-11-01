package cmdicon

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		command, agent string
		argv           []string
		icon, cmd      string
	}{
		{"vim", "", nil, "vim", "vim"},
		{"nvim", "", nil, "neovim", "nvim"},
		{"Python3.12", "", nil, "python", "python3.12"},
		{"python3", "", []string{"python3", "-m", "pytest", "-q"}, "pytest", "pytest"},
		{"python3", "", []string{"python3", "manage.py", "runserver"}, "python", "python3"},
		{"python3", "", []string{"python3", "-c", "print(1)"}, "python", "python3"},
		{"pip3.11", "", nil, "pypi", "pip3.11"},
		{"-zsh", "", nil, "zsh", "zsh"},
		{"bash", "", nil, "gnubash", "bash"},
		{"fish", "", nil, "fishshell", "fish"},
		{"sh", "", nil, "generic-shell", "sh"},
		{"claude", "", nil, "claude", "claude"},
		{"2.1.3", "", nil, "claude", "claude"},
		{"node", "claude", nil, "claude", "claude"},
		{"node", "pi", nil, "generic-agent", "pi"},
		{"node", "kimi", nil, "generic-agent", "kimi"},
		{"vim", "codex", nil, "generic-agent", "codex"},
		{"node", "gemini", nil, "googlegemini", "gemini"},
		{"node", "", []string{"node", "/usr/lib/node_modules/@google/gemini-cli/dist/index.js"}, "googlegemini", "gemini-cli"},
		{"node", "", []string{"node", "server.js"}, "nodedotjs", "node"},
		{"node", "", nil, "nodedotjs", "node"},
		{"npx", "", []string{"npx", "-y", "@anthropic-ai/claude-code@latest"}, "claude", "claude-code"},
		{"npx", "", []string{"npx", "create-vite@latest", "app"}, "npm", "npx"},
		{"pnpm", "", []string{"pnpm", "dlx", "vercel", "deploy"}, "vercel", "vercel"},
		{"uvx", "", []string{"uvx", "ruff", "check"}, "ruff", "ruff"},
		{"bun", "", []string{"bun", "run", "vite"}, "vite", "vite"},
		{"codex", "", nil, "generic-agent", "codex"},
		{"k9s", "", nil, "kubernetes", "k9s"},
		{"k3s", "", nil, "k3s", "k3s"},
		{"kubectl", "", nil, "kubernetes", "kubectl"},
		{"terraform", "", nil, "terraform", "terraform"},
		{"tofu", "", nil, "opentofu", "tofu"},
		{"docker", "", []string{"docker", "compose", "up"}, "docker", "docker"},
		{"htop", "", nil, "htop", "htop"},
		{"btop", "", nil, "generic-monitor", "btop"},
		{"ssh", "", nil, "generic-network", "ssh"},
		{"sudo", "", []string{"sudo", "-E", "vim", "/etc/hosts"}, "vim", "vim"},
		{"sudo", "", nil, "generic", "sudo"},
		{"psql", "", nil, "postgresql", "psql"},
		{"gcc-13", "", nil, "gnu", "gcc-13"},
		{"emacs-29.1", "", nil, "gnuemacs", "emacs-29.1"},
		{"idea64", "", nil, "intellijidea", "idea64"},
		{"qemu-system-x86_64", "", nil, "qemu", "qemu-system-x86_64"},
		{"sleep", "", nil, "generic", "sleep"},
		{"nvim.exe", "", nil, "neovim", "nvim"},
		{"/bin/zsh", "", nil, "zsh", "zsh"},
		{"", "", nil, "", ""},
	}
	for _, c := range cases {
		var argv func() []string
		if c.argv != nil {
			a := c.argv
			argv = func() []string { return a }
		}
		got := Resolve(c.agent, c.command, argv)
		if got.Icon != c.icon || got.Command != c.cmd {
			t.Errorf("Resolve(%q, %q, %v) = %+v, want %s/%s", c.agent, c.command, c.argv, got, c.icon, c.cmd)
		}
	}
}

// argv is only read for a wrapper.
func TestArgvOnlyForWrappers(t *testing.T) {
	read := false
	Resolve("", "vim", func() []string { read = true; return nil })
	if read {
		t.Fatal("argv read for vim")
	}
	Resolve("", "python3.12", func() []string { read = true; return nil })
	if !read {
		t.Fatal("argv not read for python3.12")
	}
}

// The command name never carries anything but a registry name or the
// process name, cut down.
func TestCommandIsShort(t *testing.T) {
	got := Resolve("", strings.Repeat("a", 100)+" --token=secret", nil)
	if len(got.Command) > 40 || strings.Contains(got.Command, "secret") && len(got.Command) > 40 {
		t.Fatalf("command %q", got.Command)
	}
	got = Resolve("", "node", func() []string { return []string{"node", "--api-key=hunter2", "/tmp/hunter2.js"} })
	if strings.Contains(got.Command, "hunter2") {
		t.Fatalf("argv leaked: %q", got.Command)
	}
}

func TestEverySlugHasArtwork(t *testing.T) {
	for _, c := range reg.Commands {
		if _, ok := SVG(c.Icon); !ok {
			t.Errorf("%s has no svg", c.Icon)
		}
		if !slugRE.MatchString(c.Icon) {
			t.Errorf("bad slug %q", c.Icon)
		}
	}
	if _, ok := SVG(Fallback); !ok {
		t.Error("no fallback svg")
	}
	// Not programs, but tools a client draws beside them.
	for _, slug := range []string{"generic-upload", "generic-docs", "generic-file"} {
		if _, ok := SVG(slug); !ok {
			t.Errorf("no svg for %s", slug)
		}
	}
	if paths["generic-upload"] != "M11 16V6.83L7.91 9.91L6.5 8.5L12 3L17.5 8.5L16.09 9.91L13 6.83V16ZM3 15H5V19H19V15H21V21H3Z" {
		t.Error("generic-upload changed")
	}
	if _, ok := SVG("../etc/passwd"); ok {
		t.Error("bad slug served")
	}
}

func TestForeground(t *testing.T) {
	out := "  100   100   300 -zsh\n  300   300   300 npm exec claude-code\n  301   300   300 node /x/cli.js\n"
	got := foreground(out)
	if strings.Join(got, " ") != "npm exec claude-code" {
		t.Fatalf("got %v", got)
	}
	if foreground("  100 100 300 -zsh\n") != nil {
		t.Fatal("no foreground group should give nil")
	}
}
