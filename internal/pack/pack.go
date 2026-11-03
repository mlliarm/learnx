// Package pack loads tech packs: per-technology config describing where the
// docs live and, later, how to sandbox and grade exercises.
package pack

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mlliarm/learnx/packs"
)

type Pack struct {
	Name        string  `yaml:"name"`
	Title       string  `yaml:"title"`
	Description string  `yaml:"description"`
	Docs        Docs    `yaml:"docs"`
	Tutor       Tutor   `yaml:"tutor"`
	Sandbox     Sandbox `yaml:"sandbox"`
}

type Sandbox struct {
	// Driver picks the sandbox implementation; empty means exercises can't be run.
	Driver string `yaml:"driver"`
	// Image is the Docker image used when the driver's binary isn't installed.
	Image string `yaml:"image"`
}

type Tutor struct {
	// ExerciseEnv tells the lesson writer where exercises will run, so it
	// only writes ones the sandbox can actually execute.
	ExerciseEnv string `yaml:"exercise_env"`
}

type Docs struct {
	MCP     *MCPDocs `yaml:"mcp"`
	LLMsTxt string   `yaml:"llms_txt"`
}

type MCPDocs struct {
	Endpoint   string `yaml:"endpoint"`
	SearchTool string `yaml:"search_tool"`
	AskTool    string `yaml:"ask_tool"`
	// BaseURL resolves relative links in tool results, e.g. "/docs/x".
	BaseURL string `yaml:"base_url"`
}

// Load returns the built-in pack with the given name.
func Load(name string) (*Pack, error) {
	data, err := fs.ReadFile(packs.FS, name+".yaml")
	if err != nil {
		return nil, fmt.Errorf("unknown pack %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	var p Pack
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse pack %q: %w", name, err)
	}
	return &p, nil
}

// Names lists the built-in pack names.
func Names() []string {
	entries, _ := fs.ReadDir(packs.FS, ".")
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".yaml"); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
