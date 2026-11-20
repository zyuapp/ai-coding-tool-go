package domain

import (
	"errors"
	"math"
	"strings"
)

const MaxRecentProjects = 5

// RememberProject is the recently picked project ids with projectID moved to the front, newest first.
func RememberProject(recent []string, projectID string) []string {
	remembered := []string{projectID}
	for _, id := range recent {
		if id != projectID {
			remembered = append(remembered, id)
		}
	}
	return remembered[:min(len(remembered), MaxRecentProjects)]
}

// FolderName is a folder's last path segment.
func FolderName(root string) string {
	end := len(root)
	for end > 0 && root[end-1] == '/' {
		end--
	}
	if end == 0 {
		return root
	}
	return root[strings.LastIndexByte(root[:end], '/')+1 : end]
}

// ProjectName is what a project is called everywhere in the UI.
func ProjectName(project *Project) string {
	if project.Name != nil {
		return *project.Name
	}
	return FolderName(project.Root)
}

func LegacyProjectID(root string) string {
	return "legacy-project-" + jsEncodeURIComponent(NormalizeProjectRoot(root))
}

func MatchingProjects(projects []*Project, reference string) []*Project {
	wanted := jsTrim(reference)
	for _, project := range projects {
		if project.ID == wanted || SameRoot(project.Root, wanted) {
			return []*Project{project}
		}
	}
	lower := strings.ToLower(wanted)
	named := []*Project{}
	for _, project := range projects {
		if strings.ToLower(ProjectName(project)) == lower || strings.ToLower(FolderName(project.Root)) == lower {
			named = append(named, project)
		}
	}
	return named
}

// FindProject's error text is the refusal the user reads.
func FindProject(projects []*Project, reference string) (*Project, error) {
	named := MatchingProjects(projects, reference)
	if len(named) == 1 {
		return named[0], nil
	}
	if len(named) > 1 {
		roots := make([]string, len(named))
		for i, project := range named {
			roots[i] = project.Root
		}
		return nil, errors.New("More than one open project is named \"" + reference + "\": " + strings.Join(roots, ", ") + ". Name the folder path instead.")
	}
	open := make([]string, len(projects))
	for i, project := range projects {
		open[i] = ProjectName(project) + " (" + project.Root + ")"
	}
	listed := strings.Join(open, ", ")
	if listed == "" {
		listed = "none"
	}
	return nil, errors.New("No project matches \"" + reference + "\". Open projects: " + listed + ".")
}

// IsProject checks a value decoded from JSON.
func IsProject(value any) bool {
	record, ok := projectIsRecord(value)
	if !ok {
		return false
	}
	name, hasName := record["name"]
	workspaceID, hasWorkspace := record["workspaceId"]
	sortIndex, hasSort := record["sortIndex"]
	unlisted, hasUnlisted := record["unlisted"]
	return projectNonEmptyString(record["id"]) && projectNonEmptyString(record["root"]) &&
		(!hasName || projectNonEmptyString(name)) && (!hasWorkspace || projectNonEmptyString(workspaceID)) &&
		(!hasSort || projectFiniteNumber(sortIndex)) && (!hasUnlisted || unlisted == true)
}

func SameRoot(left string, right string) bool {
	return NormalizeProjectRoot(left) == NormalizeProjectRoot(right)
}

// NormalizeProjectRoot drops trailing separators, keeping a root made only of them.
func NormalizeProjectRoot(root string) string {
	if normalized := strings.TrimRight(root, `\/`); normalized != "" {
		return normalized
	}
	return root
}

func projectIsRecord(value any) (map[string]any, bool) {
	return jsRecord(value)
}

func projectNonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && !jsBlank(text)
}

func projectFiniteNumber(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}
