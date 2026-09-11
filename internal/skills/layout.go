package skills

import (
	"errors"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	// UserSkillNamespace is reserved for Skills authored from the Memoh UI.
	UserSkillNamespace = "user"
	// UserApp groups a workspace user's personal Skills.
	UserApp = "personal"
)

// SkillDirForIDs returns the canonical workspace directory for one Skill:
// /data/skills/<namespace_id>/<app_id>/<skill_id>.
func SkillDirForIDs(namespaceID, appID, skillID string) (string, error) {
	namespaceID = strings.TrimSpace(namespaceID)
	appID = strings.TrimSpace(appID)
	skillID = strings.TrimSpace(skillID)
	if !validSkillPathIDs(namespaceID, appID, skillID) {
		return "", bridge.ErrBadRequest
	}
	dirPath := path.Clean(path.Join(ManagedDirPath, namespaceID, appID, skillID))
	if !strings.HasPrefix(dirPath, ManagedDirPath+"/") {
		return "", bridge.ErrBadRequest
	}
	return dirPath, nil
}

// AppDirForIDs is the discovery root for a Skill app.
func AppDirForIDs(namespaceID, appID string) (string, error) {
	namespaceID = strings.TrimSpace(namespaceID)
	appID = strings.TrimSpace(appID)
	if !validAppIDs(namespaceID, appID) {
		return "", bridge.ErrBadRequest
	}
	dirPath := path.Clean(path.Join(ManagedDirPath, namespaceID, appID))
	if !strings.HasPrefix(dirPath, ManagedDirPath+"/") {
		return "", bridge.ErrBadRequest
	}
	return dirPath, nil
}

// RegistrySkillIDs returns the Registry identity encoded by one canonical
// /data/skills/<registry>/<app>/<skill>/SKILL.md path.
func RegistrySkillIDs(skillMDPath string) (registryID, appID, skillID string, ok bool) {
	registryID, appID, skillID, ok = skillPathIDs(skillMDPath)
	if !ok || registryID == UserSkillNamespace {
		return "", "", "", false
	}
	return registryID, appID, skillID, true
}

// RegistrySkillDirIDs returns the Registry identity encoded by one canonical
// /data/skills/<registry>/<app>/<skill> directory.
func RegistrySkillDirIDs(skillDir string) (registryID, appID, skillID string, ok bool) {
	skillDir = path.Clean(strings.TrimSpace(skillDir))
	return RegistrySkillIDs(path.Join(skillDir, "SKILL.md"))
}

func skillNamespaceDirForID(namespaceID string) (string, error) {
	namespaceID = strings.TrimSpace(namespaceID)
	if namespaceID != UserSkillNamespace && !IsValidRegistryID(namespaceID) {
		return "", bridge.ErrBadRequest
	}
	dirPath := path.Clean(path.Join(ManagedDirPath, namespaceID))
	if dirPath == ManagedDirPath || !strings.HasPrefix(dirPath, ManagedDirPath+"/") {
		return "", bridge.ErrBadRequest
	}
	return dirPath, nil
}

func userSkillDirForName(name string) (string, error) {
	return SkillDirForIDs(UserSkillNamespace, UserApp, name)
}

// UpsertPlan is the filesystem plan for creating or updating one Skill.
type UpsertPlan struct {
	WritePath     string
	RenameFromDir string
}

// PlanUpsert resolves the target for a user edit. User Skills retain their
// canonical namespace and rename by moving their containing directory. A
// Registry Skills are immutable and can only be replaced through their
// Registry installation flow.
func PlanUpsert(raw, sourcePath string) (UpsertPlan, error) {
	parsed := ParseFile(raw, "")
	if !IsValidName(parsed.Name) {
		return UpsertPlan{}, bridge.ErrBadRequest
	}
	userDir, err := userSkillDirForName(parsed.Name)
	if err != nil {
		return UpsertPlan{}, err
	}
	userWrite := path.Join(userDir, "SKILL.md")

	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return UpsertPlan{WritePath: userWrite}, nil
	}
	sourcePath = path.Clean(sourcePath)
	if path.Base(sourcePath) != "SKILL.md" || !path.IsAbs(sourcePath) {
		return UpsertPlan{}, bridge.ErrBadRequest
	}
	if oldName, ok := UserSkillName(sourcePath); ok {
		plan := UpsertPlan{WritePath: userWrite}
		if oldName != parsed.Name {
			oldDir, dirErr := userSkillDirForName(oldName)
			if dirErr != nil {
				return UpsertPlan{}, dirErr
			}
			plan.RenameFromDir = oldDir
		}
		return plan, nil
	}
	if _, ok := BuiltinSkillName(sourcePath); ok {
		return UpsertPlan{}, ErrBuiltinSkillReadOnly
	}
	if IsNamespacedSkillPath(sourcePath) {
		return UpsertPlan{}, ErrRegistrySkillReadOnly
	}
	return UpsertPlan{WritePath: userWrite}, nil
}

// DeletableSkillDirForSourcePath resolves the directory to remove for a
// user-managed Skill. Registry App Skills are immutable as individual items.
func DeletableSkillDirForSourcePath(sourcePath string) (string, error) {
	sourcePath = path.Clean(strings.TrimSpace(sourcePath))
	if !path.IsAbs(sourcePath) || path.Base(sourcePath) != "SKILL.md" {
		return "", bridge.ErrBadRequest
	}
	if _, ok := UserSkillName(sourcePath); ok {
		return path.Dir(sourcePath), nil
	}
	if _, ok := BuiltinSkillName(sourcePath); ok {
		return "", ErrBuiltinSkillReadOnly
	}
	if _, _, _, ok := RegistrySkillIDs(sourcePath); ok {
		return "", ErrRegistrySkillReadOnly
	}
	return "", bridge.ErrBadRequest
}

// PrunableSkillNamespaceDirs returns empty app and namespace directories
// after one namespaced Skill is deleted. It never removes ManagedDirPath.
func PrunableSkillNamespaceDirs(skillDir string) []string {
	skillDir = path.Clean(strings.TrimSpace(skillDir))
	appDir := path.Dir(skillDir)
	namespaceDir := path.Dir(appDir)
	if path.Dir(namespaceDir) != ManagedDirPath {
		return nil
	}
	if !validSkillPathIDs(path.Base(namespaceDir), path.Base(appDir), path.Base(skillDir)) {
		return nil
	}
	return []string{appDir, namespaceDir}
}

// UserSkillName reports the name for
// /data/skills/user/personal/<skill>/SKILL.md.
func UserSkillName(skillMDPath string) (string, bool) {
	namespaceID, appID, skillID, ok := skillPathIDs(skillMDPath)
	if !ok || namespaceID != UserSkillNamespace || appID != UserApp {
		return "", false
	}
	return skillID, true
}

// BuiltinSkillName reports the directory name for
// /data/.memoh/skills/<name>/SKILL.md.
func BuiltinSkillName(skillMDPath string) (string, bool) {
	skillMDPath = path.Clean(strings.TrimSpace(skillMDPath))
	if path.Base(skillMDPath) != "SKILL.md" {
		return "", false
	}
	dir := path.Dir(skillMDPath)
	if path.Dir(dir) != IndexDirPath {
		return "", false
	}
	name := path.Base(dir)
	if !IsValidName(name) {
		return "", false
	}
	return name, true
}

// IsNamespacedSkillPath reports whether path follows the canonical managed
// namespace/app/Skill layout below ManagedDirPath.
func IsNamespacedSkillPath(skillMDPath string) bool {
	_, _, _, ok := skillPathIDs(skillMDPath)
	return ok
}

func skillPathIDs(skillMDPath string) (namespaceID, appID, skillID string, ok bool) {
	skillMDPath = path.Clean(strings.TrimSpace(skillMDPath))
	if path.Base(skillMDPath) != "SKILL.md" {
		return "", "", "", false
	}
	skillDir := path.Dir(skillMDPath)
	appDir := path.Dir(skillDir)
	namespaceDir := path.Dir(appDir)
	if path.Dir(namespaceDir) != ManagedDirPath {
		return "", "", "", false
	}
	namespaceID, appID, skillID = path.Base(namespaceDir), path.Base(appDir), path.Base(skillDir)
	if !validSkillPathIDs(namespaceID, appID, skillID) {
		return "", "", "", false
	}
	return namespaceID, appID, skillID, true
}

func validAppIDs(namespaceID, appID string) bool {
	if namespaceID == UserSkillNamespace {
		return appID == UserApp
	}
	return IsValidRegistryID(namespaceID) && IsValidRegistryComponent(appID)
}

func validSkillPathIDs(namespaceID, appID, skillID string) bool {
	if namespaceID == UserSkillNamespace {
		return appID == UserApp && IsValidName(skillID)
	}
	return IsValidRegistryID(namespaceID) && IsValidRegistryComponent(appID) && IsValidRegistryComponent(skillID)
}

var (
	ErrBuiltinSkillReadOnly  = errors.New("built-in skills are read-only")
	ErrRegistrySkillReadOnly = errors.New("registry skills are read-only")
)
