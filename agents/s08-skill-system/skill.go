package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is one bundle: documentation (SKILL.md content), a short description
// the menu shows, and the list of tools the bundle activates. Mirrors the
// Python dataclass at guide/skill-system.md L111-L117 — Doc is the raw
// SKILL.md body so the model gets the full instructions when the bundle
// loads, while Description stays compact for the menu listing.
//
// Tools are attached to a Skill by the package that owns the SKILL.md fixture
// (see skills/git/tools.go etc.). LoadSkillFromDir does NOT magically discover
// Go code — it only parses the markdown. The wiring of code to bundle happens
// in the caller of NewSkillFromFile, by calling Skill.WithTools.
type Skill struct {
	Name        string
	Description string
	Doc         string // raw SKILL.md body, fed to the model on load
	Tools       []Tool // attached separately; LoadSkillFromDir leaves this empty
}

// WithTools attaches handlers to a parsed skill. Returns the same pointer for
// fluent style. We expose this as a small mutator (rather than a constructor
// arg) because the SKILL.md parser does not know the Go tool types yet — the
// fixture packages call WithTools right after LoadSkillFromDir.
func (s *Skill) WithTools(tools ...Tool) *Skill {
	s.Tools = append(s.Tools, tools...)
	return s
}

// LoadSkillFromDir reads <dir>/SKILL.md and returns a Skill with the parsed
// Name/Description/Doc. Tools are NOT loaded here — see WithTools.
//
// The simple format we accept (intentionally narrower than upstream's YAML
// frontmatter) is:
//
//	# <skill-name>
//
//	> <one-line description>
//
//	<body — any markdown, becomes Doc>
//
// Why this shape, and not the YAML frontmatter the upstream `skills/`
// directory uses? Three reasons:
//
//  1. Pedagogy. Frontmatter parsing is a distraction. A two-line "header" is
//     visually obvious and lets the file double as readable docs.
//  2. Single-source description. The blockquote IS the description shown in
//     menus. No risk of frontmatter `description:` drifting from the prose
//     summary.
//  3. The full SKILL.md content (including the H1 and blockquote) becomes
//     Doc, so when the model loads the skill it sees the whole document
//     verbatim — exactly what `guide/skill-system.md` L175-L178 specifies.
func LoadSkillFromDir(dir string) (*Skill, error) {
	skillMD := filepath.Join(dir, "SKILL.md")
	f, err := os.Open(skillMD)
	if err != nil {
		return nil, fmt.Errorf("open SKILL.md: %w", err)
	}
	defer f.Close()

	var (
		name        string
		description string
		body        strings.Builder
	)

	scanner := bufio.NewScanner(f)
	// SKILL.md docs can include long fenced code blocks. The default
	// bufio.Scanner buffer is 64KiB which is plenty for our fixtures, but we
	// bump it to 1MiB defensively in case a skill author paste an entire
	// example file into the doc body.
	scanner.Buffer(make([]byte, 0, 1024), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()
		// First H1 wins as the skill name. We keep the line in the body too
		// so the model sees it on load.
		if name == "" && strings.HasPrefix(line, "# ") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			body.WriteString(line)
			body.WriteByte('\n')
			continue
		}
		// First blockquote line wins as the description. Same rule: leave it
		// in the body for the model. We accept either "> X" or ">X".
		if description == "" && strings.HasPrefix(line, ">") {
			description = strings.TrimSpace(strings.TrimPrefix(line, ">"))
			body.WriteString(line)
			body.WriteByte('\n')
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SKILL.md: %w", err)
	}

	if name == "" {
		return nil, fmt.Errorf("%s: missing H1 (skill name)", skillMD)
	}
	if description == "" {
		// We DON'T hard-fail on a missing blockquote; the model can still see
		// the doc. But empty descriptions ruin the menu (it would show
		// "- foo: " which the model parses as a name with no description), so
		// we substitute the name itself rather than leaving it blank.
		description = name
	}

	return &Skill{
		Name:        name,
		Description: description,
		Doc:         body.String(),
	}, nil
}
