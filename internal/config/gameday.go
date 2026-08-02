package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// GameDay is a multi-database restore exercise: member drills executed in
// dependency order (docs/gameday.md). Hash and Path are filled by
// LoadGameDay, never by YAML.
type GameDay struct {
	Name        string          `yaml:"name"`
	Timeout     Duration        `yaml:"timeout"`
	MaxParallel int             `yaml:"max_parallel"`
	Members     []GameDayMember `yaml:"members"`

	// Hash is "sha256:<hex>" over the exact file bytes as read, reported
	// in the game-day summary.
	Hash string `yaml:"-"`
	// Path is the config file path LoadGameDay read, for error messages.
	Path string `yaml:"-"`
}

// GameDayMember references one member drill. Config is a drill
// configuration path, resolved relative to the game-day file's directory
// by LoadGameDay; DependsOn names members whose drills must pass first.
type GameDayMember struct {
	Name      string   `yaml:"name"`
	Config    string   `yaml:"config"`
	DependsOn []string `yaml:"depends_on"`
}

// Parallelism returns the effective member concurrency: max_parallel,
// defaulting to 1 (strictly sequential).
func (g *GameDay) Parallelism() int {
	if g.MaxParallel < 1 {
		return 1
	}
	return g.MaxParallel
}

// LoadGameDay reads, parses, and validates a game-day configuration,
// including every member drill configuration — a game-day fails fast on
// any problem before a single sandbox exists.
func LoadGameDay(path string) (*GameDay, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read game-day config: %w", err)
	}
	g := &GameDay{}
	dec := yaml.NewDecoder(bytes.NewReader(raw), yaml.Strict())
	if err := dec.Decode(g); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("game-day config %s is empty", path)
		}
		return nil, fmt.Errorf("parse game-day config %s:\n%s", path, yaml.FormatError(err, false, true))
	}
	sum := sha256.Sum256(raw)
	g.Hash = "sha256:" + hex.EncodeToString(sum[:])
	g.Path = path
	if err := g.validate(); err != nil {
		return nil, fmt.Errorf("invalid game-day config %s:\n%w", path, err)
	}
	if err := g.loadMembers(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("invalid game-day config %s:\n%w", path, err)
	}
	return g, nil
}

// loadMembers resolves member config paths against the game-day file's
// directory and loads each one, so a broken member surfaces before the
// exercise starts. It also enforces the shared-evidence-log rule: with
// max_parallel above 1, two members writing one log would collide on the
// store's single-writer lock mid-exercise — reject the combination now.
func (g *GameDay) loadMembers(base string) error {
	var p problems
	logOwner := map[string]string{}
	for i := range g.Members {
		m := &g.Members[i]
		if !filepath.IsAbs(m.Config) {
			m.Config = filepath.Join(base, m.Config)
		}
		cfg, err := Load(m.Config)
		if err != nil {
			p.add("members[%d] (%s): %v", i, m.Name, err)
			continue
		}
		if g.Parallelism() > 1 {
			if first, taken := logOwner[cfg.Evidence.Path]; taken {
				p.add("members %s and %s share evidence log %s while max_parallel is %d — concurrent drills against one log fail on its single-writer lock; use per-member logs or max_parallel: 1",
					first, m.Name, cfg.Evidence.Path, g.MaxParallel)
			} else {
				logOwner[cfg.Evidence.Path] = m.Name
			}
		}
	}
	return errors.Join(p...)
}

func (g *GameDay) validate() error {
	var p problems
	if g.Name == "" {
		p.add("name is required — it identifies the exercise in the summary")
	}
	if g.Timeout == 0 {
		p.add(`timeout is required — every game-day needs a hard wall-clock limit (e.g. "2h")`)
	}
	if g.MaxParallel < 0 {
		p.add("max_parallel must not be negative")
	}
	if len(g.Members) == 0 {
		p.add("at least one member is required")
		return errors.Join(p...)
	}
	index := g.validateMembers(&p)
	g.validateDependencies(&p, index)
	if len(p) == 0 {
		if stuck := g.cycleMembers(); len(stuck) > 0 {
			p.add("dependency cycle involving members: %s", strings.Join(stuck, ", "))
		}
	}
	return errors.Join(p...)
}

// validateMembers checks per-member shape and returns the name index used
// for dependency resolution.
func (g *GameDay) validateMembers(p *problems) map[string]int {
	index := make(map[string]int, len(g.Members))
	for i := range g.Members {
		m := &g.Members[i]
		at := fmt.Sprintf("members[%d]", i)
		switch {
		case m.Name == "":
			p.add("%s: name is required", at)
		default:
			if prev, dup := index[m.Name]; dup {
				p.add("%s: name %q duplicates members[%d]", at, m.Name, prev)
			} else {
				index[m.Name] = i
			}
		}
		if m.Config == "" {
			p.add("%s: config is required — the member's drill configuration file", at)
		}
		seen := make(map[string]bool, len(m.DependsOn))
		for _, dep := range m.DependsOn {
			switch {
			case dep == m.Name:
				p.add("%s: depends_on must not reference the member itself", at)
			case seen[dep]:
				p.add("%s: duplicate dependency %q", at, dep)
			}
			seen[dep] = true
		}
	}
	return index
}

func (g *GameDay) validateDependencies(p *problems, index map[string]int) {
	for i := range g.Members {
		m := &g.Members[i]
		for _, dep := range m.DependsOn {
			if _, ok := index[dep]; !ok && dep != m.Name {
				p.add("members[%d]: depends_on references unknown member %q", i, dep)
			}
		}
	}
}

// cycleMembers runs Kahn's algorithm over the dependency graph; members
// whose in-degree never reaches zero sit in or behind a cycle.
func (g *GameDay) cycleMembers() []string {
	indegree := make(map[string]int, len(g.Members))
	dependents := map[string][]string{}
	for i := range g.Members {
		m := &g.Members[i]
		indegree[m.Name] += 0
		for _, dep := range m.DependsOn {
			indegree[m.Name]++
			dependents[dep] = append(dependents[dep], m.Name)
		}
	}
	var queue []string
	for i := range g.Members {
		if indegree[g.Members[i].Name] == 0 {
			queue = append(queue, g.Members[i].Name)
		}
	}
	removed := 0
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		removed++
		for _, d := range dependents[name] {
			indegree[d]--
			if indegree[d] == 0 {
				queue = append(queue, d)
			}
		}
	}
	if removed == len(g.Members) {
		return nil
	}
	var stuck []string
	for i := range g.Members {
		if indegree[g.Members[i].Name] > 0 {
			stuck = append(stuck, g.Members[i].Name)
		}
	}
	return stuck
}
