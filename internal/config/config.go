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

type Config struct {
	CustomCommands []Command `yaml:"customCommands"`
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
