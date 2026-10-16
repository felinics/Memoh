package skills

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/templates"
)

// The built-in skill-creator ships its own path guidance and Python
// scaffolder. They must target the user Skill app, otherwise the Skills they
// create are never discovered.
const skillCreatorTemplateDir = ".memoh/skills/skill-creator"

var (
	managedSkillPathPattern  = regexp.MustCompile(regexp.QuoteMeta(ManagedDirPath) + "[^\\s`'\",)]*")
	initSkillDefaultPathExpr = regexp.MustCompile(`"--path",\s*default="([^"]+)"`)
)

func userSkillAppDir(t *testing.T) string {
	t.Helper()
	dir, err := AppDirForIDs(UserSkillNamespace, UserApp)
	if err != nil {
		t.Fatalf("AppDirForIDs() error = %v", err)
	}
	return dir
}

func readSkillCreatorTemplate(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(templates.WorkspaceFS(), path.Join(skillCreatorTemplateDir, name))
	if err != nil {
		t.Fatalf("read skill-creator template %s: %v", name, err)
	}
	return string(data)
}

func TestSkillCreatorTemplatePathsTargetUserSkillApp(t *testing.T) {
	userApp := userSkillAppDir(t)
	found := 0
	for _, name := range []string{
		"SKILL.md",
		"references/skill-files.md",
		"scripts/init_skill.py",
		"scripts/quick_validate.py",
	} {
		for _, match := range managedSkillPathPattern.FindAllString(readSkillCreatorTemplate(t, name), -1) {
			found++
			p := strings.TrimRight(match, ".")
			if p != userApp && !strings.HasPrefix(p, userApp+"/") {
				t.Errorf("%s references %q, want a path under %s", name, p, userApp)
			}
		}
	}
	if found == 0 {
		t.Fatalf("skill-creator templates no longer reference %s", ManagedDirPath)
	}
}

func TestSkillCreatorInitScriptCreatesDiscoverableUserSkill(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := readSkillCreatorTemplate(t, "scripts/init_skill.py")
	match := initSkillDefaultPathExpr.FindStringSubmatch(script)
	if match == nil {
		t.Fatal("init_skill.py --path default not found")
	}
	defaultRoot := match[1]
	if want := userSkillAppDir(t); defaultRoot != want {
		t.Fatalf("init_skill.py --path default = %q, want %q", defaultRoot, want)
	}

	scriptsDir := t.TempDir()
	workspaceRoot := t.TempDir()
	hostRoot := filepath.Join(workspaceRoot, filepath.FromSlash(strings.TrimPrefix(defaultRoot, config.DefaultDataMount)))
	runSkillCreatorScript(t, python, scriptsDir, "init_skill.py", "my-skill", "--path", hostRoot, "--resources", "scripts,references")
	runSkillCreatorScript(t, python, scriptsDir, "quick_validate.py", filepath.Join(hostRoot, "my-skill"))

	entries, err := LoadEffective(context.Background(), newWorkspaceTestClient(t, workspaceRoot), []string{})
	if err != nil {
		t.Fatalf("LoadEffective() error = %v", err)
	}
	wantSource := path.Join(defaultRoot, "my-skill", "SKILL.md")
	entry, ok := findBySourcePath(entries, wantSource)
	if !ok {
		t.Fatalf("skill created by init_skill.py was not discovered at %s: %+v", wantSource, entries)
	}
	if name, ok := UserSkillName(entry.SourcePath); !ok || name != "my-skill" || entry.Name != "my-skill" {
		t.Fatalf("discovered entry = %+v, want user Skill my-skill", entry)
	}
	catalog, err := BuildSafeCatalog(entries)
	if err != nil {
		t.Fatalf("BuildSafeCatalog() error = %v", err)
	}
	listed := false
	for _, item := range catalog {
		listed = listed || item.Name == "my-skill"
	}
	if !listed {
		t.Fatalf("my-skill missing from runtime catalog (unusable reason %q)", NormalizeRuntimeUsability(entry).RuntimeUnusableReason)
	}
}

func runSkillCreatorScript(t *testing.T, python, dir, name string, args ...string) {
	t.Helper()
	scriptPath := filepath.Join(dir, name)
	if err := os.WriteFile(scriptPath, []byte(readSkillCreatorTemplate(t, "scripts/"+name)), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	cmd := exec.CommandContext(context.Background(), python, append([]string{scriptPath}, args...)...) //nolint:gosec // G204: test runs the embedded template script.
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s error = %v\n%s", name, err, out)
	}
}
