// Package config loads berth's optional user config file.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Command is a user-defined action run against the selected container.
// Command is a Go template: {{.ID}} {{.Name}} {{.Project}} {{.Service}} {{.Image}}.
type Command struct {
	Name    string `yaml:"name"`
	Command string `yaml:"command"`
	// Pause waits for Enter after the command finishes so its output can be
	// read (default true). Set false for interactive or streaming commands.
	Pause *bool `yaml:"pause"`
}

func (c Command) ShouldPause() bool { return c.Pause == nil || *c.Pause }

// Host is a saved Docker environment (the Environments view, key 0).
type Host struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

type Config struct {
	CustomCommands []Command `yaml:"customCommands"`
	Hosts          []Host    `yaml:"hosts"`
}

// Path is where berth looks for its config
// (~/.config/berth/config.yaml, %AppData%\berth\config.yaml on Windows).
func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "berth.yaml"
	}
	return filepath.Join(dir, "berth", "config.yaml")
}

// Load reads the config; a missing file is not an error.
func Load() (Config, error) {
	var c Config
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, yaml.Unmarshal(b, &c)
}

// Example is written by "Create example config" in the custom-commands menu.
const Example = `# berth config — reloaded each time you open the custom commands menu (c)
#
# customCommands run on the Docker host (inside WSL / over ssh when remote).
# Templates: {{.ID}} {{.Name}} {{.Project}} {{.Service}} {{.Image}}
# pause: false  → for interactive or streaming commands (Ctrl+C returns)

customCommands:
  - name: Disk usage inside container
    command: docker exec {{.ID}} df -h

  - name: Environment (sorted)
    command: docker exec {{.ID}} sh -c 'env | sort'

  - name: Follow logs since 10 minutes ago
    command: docker logs -f --since 10m {{.ID}}
    pause: false

  - name: Python / Django shell
    command: docker exec -it {{.ID}} sh -c 'python manage.py shell 2>/dev/null || python'
    pause: false
`

// WriteExample creates the example config if no config exists yet.
func WriteExample() (string, error) {
	p := Path()
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return p, err
	}
	return p, os.WriteFile(p, []byte(Example), 0o644)
}

// AddHost saves (or renames) a host, editing the YAML tree so the user's
// comments and layout survive.
func AddHost(h Host) error {
	return edit(func(hosts *yaml.Node) {
		for _, n := range hosts.Content {
			if field(n, "url") == h.URL {
				setField(n, "name", h.Name)
				return
			}
		}
		n := &yaml.Node{Kind: yaml.MappingNode}
		setField(n, "name", h.Name)
		setField(n, "url", h.URL)
		hosts.Content = append(hosts.Content, n)
	})
}

// RemoveHost deletes a saved host by URL.
func RemoveHost(url string) error {
	return edit(func(hosts *yaml.Node) {
		kept := hosts.Content[:0]
		for _, n := range hosts.Content {
			if field(n, "url") != url {
				kept = append(kept, n)
			}
		}
		hosts.Content = kept
	})
}

func edit(f func(hosts *yaml.Node)) error {
	p := Path()
	var doc yaml.Node
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	var hosts *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "hosts" {
			hosts = root.Content[i+1]
		}
	}
	if hosts == nil {
		hosts = &yaml.Node{Kind: yaml.SequenceNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "hosts",
			HeadComment: "Docker environments for the Environments view (0). url: ssh://user@host · tcp://host:2375 · unix:///path"}, hosts)
	}
	if hosts.Kind != yaml.SequenceNode {
		*hosts = yaml.Node{Kind: yaml.SequenceNode}
	}
	f(hosts)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

func field(n *yaml.Node, k string) string {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == k {
			return n.Content[i+1].Value
		}
	}
	return ""
}

func setField(n *yaml.Node, k, v string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == k {
			n.Content[i+1].Value = v
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
}
