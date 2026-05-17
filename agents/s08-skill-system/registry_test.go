package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSkillDir writes a minimal SKILL.md with the simple header format the
// LoadSkillFromDir parser accepts. Returns the directory it created so a test
// can ScanDir(parent) over it. Keeping the fixture inline (rather than under
// testdata/) makes the file shape obvious at the call site — you can see the
// markdown without opening another file.
func fixtureSkillDir(t *testing.T, parent, name, description, body string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "# " + name + "\n\n> " + description + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

// gitTestTool is a stand-in for the real git package's tools. We keep a
// local stub here so registry_test.go has no compile-time dependency on the
// fixtures under skills/ — that decouples the registry tests from the
// fixtures' tool shapes (which may evolve independently).
type gitTestTool struct {
	name string
}

func (g gitTestTool) Name() string             { return g.name }
func (g gitTestTool) Description() string      { return "stub " + g.name }
func (g gitTestTool) Schema() json.RawMessage  { return json.RawMessage(`{"type":"object"}`) }
func (g gitTestTool) Run(_ context.Context, _ json.RawMessage) (string, error) {
	return "ran " + g.name, nil
}

// TestRegistry_ScanBuildsCatalogFromDir proves the directory walk picks up
// every subdir that has a SKILL.md, builds Skill values with Name/Description
// derived from the markdown, and ignores directories that lack SKILL.md.
func TestRegistry_ScanBuildsCatalogFromDir(t *testing.T) {
	root := t.TempDir()
	fixtureSkillDir(t, root, "file_ops", "Read and write files within the workspace.", "Body for file_ops.")
	fixtureSkillDir(t, root, "git", "Inspect working tree status and diffs.", "Body for git.")
	fixtureSkillDir(t, root, "web", "Fetch HTTP resources.", "Body for web.")

	// Sibling dir with no SKILL.md — must be silently skipped.
	if err := os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755); err != nil {
		t.Fatalf("mkdir not-a-skill: %v", err)
	}
	// Stray top-level file — must not cause an error either.
	if err := os.WriteFile(filepath.Join(root, "README.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatalf("write README.txt: %v", err)
	}

	reg := NewSkillRegistry()
	if err := reg.ScanDir(root); err != nil {
		t.Fatalf("scan: %v", err)
	}

	cat := reg.Catalog()
	if len(cat) != 3 {
		t.Fatalf("expected 3 catalog entries, got %d (catalog=%v)", len(cat), keys(cat))
	}
	for _, want := range []string{"file_ops", "git", "web"} {
		s, ok := cat[want]
		if !ok {
			t.Errorf("missing skill %q in catalog", want)
			continue
		}
		if s.Description == "" || s.Description == s.Name {
			t.Errorf("skill %q has empty/default description %q — blockquote not parsed", want, s.Description)
		}
		if !strings.Contains(s.Doc, "Body for "+want) {
			t.Errorf("skill %q Doc missing body text; got %q", want, s.Doc)
		}
	}
}

// TestRegistry_MenuFormatMatchesGuide locks down the menu wording so a future
// refactor cannot drift away from guide/skill-system.md L78. The model is
// trained against this exact phrasing in some sense — keeping the header
// stable is the cheapest insurance.
func TestRegistry_MenuFormatMatchesGuide(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add(&Skill{Name: "git", Description: "git ops"})
	reg.Add(&Skill{Name: "file_ops", Description: "file ops"})

	menu := reg.Menu()

	if !strings.HasPrefix(menu, "Available skills (use load_skill to activate):") {
		t.Errorf("menu must open with the guide L78 header; got:\n%s", menu)
	}
	// Both skills listed, alphabetised.
	idxFile := strings.Index(menu, "- file_ops:")
	idxGit := strings.Index(menu, "- git:")
	if idxFile == -1 || idxGit == -1 {
		t.Fatalf("menu missing entries:\n%s", menu)
	}
	if idxFile > idxGit {
		t.Errorf("menu entries not alphabetised (file_ops should appear before git):\n%s", menu)
	}
}

// TestRegistry_LoadActivatesTools is the key teaching test: before loading a
// skill, ActiveSchemas is empty (no tool tax); after loading, only that
// skill's tools appear. This is what saves the ~9,450 tokens per turn from
// guide/skill-system.md L98.
func TestRegistry_LoadActivatesTools(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add((&Skill{Name: "git", Description: "git ops"}).WithTools(
		gitTestTool{name: "git_status"},
		gitTestTool{name: "git_diff"},
	))
	reg.Add((&Skill{Name: "file_ops", Description: "file ops"}).WithTools(
		gitTestTool{name: "read_file"},
	))

	// Before load: no schemas.
	if got := reg.ActiveSchemas(); len(got) != 0 {
		t.Errorf("ActiveSchemas before load = %d items, want 0", len(got))
	}

	if _, err := reg.LoadSkill("git"); err != nil {
		t.Fatalf("load git: %v", err)
	}

	// After load: exactly the git tools, nothing from file_ops.
	schemas := reg.ActiveSchemas()
	if len(schemas) != 2 {
		t.Fatalf("ActiveSchemas after loading git = %d, want 2; got %+v", len(schemas), schemas)
	}
	names := []string{schemas[0].Name, schemas[1].Name}
	wantNames := []string{"git_diff", "git_status"} // alphabetical
	for i, want := range wantNames {
		if names[i] != want {
			t.Errorf("schema[%d] = %q, want %q (alphabetical)", i, names[i], want)
		}
	}

	// And the menu now marks git as [loaded].
	menu := reg.Menu()
	if !strings.Contains(menu, "- git: git ops [loaded]") {
		t.Errorf("menu should mark git as [loaded]; got:\n%s", menu)
	}
}

// TestRegistry_LoadUnknownSkillReturnsErrorString exercises the *meta-tool*
// path: an LLM-issued load_skill({"name":"bogus"}) must come back as a
// human-readable error STRING, never a Go error — otherwise the loop would
// crash on a model typo. Mirrors the wording at L168.
func TestRegistry_LoadUnknownSkillReturnsErrorString(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add(&Skill{Name: "git", Description: "git ops"})

	tool := LoadSkillTool{Registry: reg}
	out, err := tool.Run(context.Background(), json.RawMessage(`{"name":"bogus"}`))
	if err != nil {
		t.Fatalf("LoadSkillTool.Run returned a Go error; should return string instead: %v", err)
	}
	if !strings.Contains(out, "unknown skill") {
		t.Errorf("error string should mention 'unknown skill'; got %q", out)
	}
	if !strings.Contains(out, "bogus") {
		t.Errorf("error string should include the offending name 'bogus'; got %q", out)
	}
	if !strings.Contains(out, "list_skills") {
		t.Errorf("error string should hint at list_skills for recovery; got %q", out)
	}
}

// TestRegistry_UnloadFreesContext closes the loop: load → assert non-empty →
// unload → assert empty again. The pitfall at L254 (no unload mechanism) is
// why this test exists.
func TestRegistry_UnloadFreesContext(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add((&Skill{Name: "git", Description: "git ops"}).WithTools(
		gitTestTool{name: "git_status"},
	))

	if _, err := reg.LoadSkill("git"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(reg.ActiveSchemas()); got != 1 {
		t.Fatalf("expected 1 active schema after load, got %d", got)
	}

	if err := reg.UnloadSkill("git"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if got := len(reg.ActiveSchemas()); got != 0 {
		t.Fatalf("expected 0 active schemas after unload, got %d", got)
	}

	// Catalog entry survives — unload does not delete the bundle, it just
	// hides it from the model again.
	if _, ok := reg.Catalog()["git"]; !ok {
		t.Errorf("catalog entry for 'git' should remain after unload")
	}

	// Unloading something not loaded must be a Go error (callers wrap it
	// into a model-string in UnloadSkillTool; the raw method returns the
	// error so test code can assert on it).
	if err := reg.UnloadSkill("git"); err == nil {
		t.Errorf("expected error unloading already-unloaded skill")
	}
}

// TestMetaTools_ListSkillsRunReturnsMenu confirms the list_skills meta-tool
// proxies through to Registry.Menu(). Important: the meta-tool path is what
// the LLM uses; if this drifts from Menu() the model gets stale state.
func TestMetaTools_ListSkillsRunReturnsMenu(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add(&Skill{Name: "git", Description: "git ops"})
	reg.Add(&Skill{Name: "web", Description: "web ops"})

	tool := ListSkillsTool{Registry: reg}
	out, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_skills.Run: %v", err)
	}
	if out != reg.Menu() {
		t.Errorf("list_skills.Run output should match Menu() verbatim.\nGot:\n%s\nWant:\n%s", out, reg.Menu())
	}
	if !strings.Contains(out, "Available skills") {
		t.Errorf("list_skills output missing the menu header; got %q", out)
	}
}

// TestRegistry_DispatchTool_RoutesToActiveSkill is the small but important
// "the right tool is found via the active set" check. A request for a tool
// belonging to an UNLOADED skill must come back as an error string the
// model can react to (per the L219 contract).
func TestRegistry_DispatchTool_RoutesToActiveSkill(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add((&Skill{Name: "git", Description: "git ops"}).WithTools(
		gitTestTool{name: "git_status"},
	))
	reg.Add((&Skill{Name: "web", Description: "web ops"}).WithTools(
		gitTestTool{name: "http_get"},
	))

	// Without loading anything, even known tool names are unreachable.
	if got := reg.DispatchTool(context.Background(), "git_status", nil); !strings.Contains(got, "not found") {
		t.Errorf("expected 'not found' before load; got %q", got)
	}

	if _, err := reg.LoadSkill("git"); err != nil {
		t.Fatalf("load git: %v", err)
	}

	// After loading git, git_status is reachable...
	if got := reg.DispatchTool(context.Background(), "git_status", json.RawMessage(`{}`)); got != "ran git_status" {
		t.Errorf("git_status dispatch: got %q, want %q", got, "ran git_status")
	}
	// ...but http_get (in unloaded web skill) is still hidden.
	if got := reg.DispatchTool(context.Background(), "http_get", nil); !strings.Contains(got, "not found") {
		t.Errorf("http_get should not be reachable while web is unloaded; got %q", got)
	}
}

// keys is a tiny helper used in error messages — Go map iteration order is
// random so we sort for a stable diff.
func keys(m map[string]*Skill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
