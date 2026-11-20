package domain

import (
	"encoding/json/jsontext"
	"errors"
	"maps"
	"slices"
	"strconv"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx/lit"
)

const ThreadStoreVersion = 2

// unrecordedEngine is the engine every thread written before the engine was recorded ran on.
const unrecordedEngine = AgentEngineClaude

var legacyModes = map[string]ExecutionPolicy{
	"default":     ExecutionPolicyConfirm,
	"plan":        ExecutionPolicyPlan,
	"acceptEdits": ExecutionPolicyAllowEdits,
	"auto":        ExecutionPolicyAutonomous,
}

// versionedValue is TypeScript's VersionedValue: a stored value under its store version.
type versionedValue[T any] struct {
	Version jsonx.Tag[lit.N2] `json:"version"`
	Value   T                 `json:"value"`
}

// DecodedThreadStore is the v2 values as decoded from storage, unchecked: each is what jsonx.Unmarshal
// gives into any (nil for null or a missing worktrees value).
type DecodedThreadStore struct {
	Tasks      any
	Projects   any
	Worktrees  any
	LastFolder any
}

// ThreadStoreValidationResult is the readable store and how many threads it hid when Ok, else the
// errors that make the whole store unreadable.
type ThreadStoreValidationResult struct {
	Ok          bool
	Data        ThreadStoreData
	HiddenTasks int
	Errors      []string
}

// decodedV2 is what decodeV2 finds: Kind "none", "v2" with Values, or "corrupt" with Errors.
type decodedV2 struct {
	Kind   string
	Values DecodedThreadStore
	Errors []string
}

func ParseThreadStore(raw StorageValues) ThreadStoreParseResult {
	if isEmpty(raw) {
		return ThreadStoreParseResultTrue{Data: ThreadStoreData{Tasks: []*StoredThread{}, Projects: []*Project{}, Worktrees: []*Worktree{}}}
	}
	decoded := decodeV2(raw)
	switch decoded.Kind {
	case "v2":
		validated := ValidateThreadStoreData(decoded.Values)
		if !validated.Ok {
			return corrupt(2, validated.Errors, raw)
		}
		return ThreadStoreParseResultTrue{Data: validated.Data, HiddenTasks: float64(validated.HiddenTasks), SourceVersion: 2}
	case "corrupt":
		return ThreadStoreParseResultFalse{SourceVersion: 2, ErrorKind: ThreadStoreParseResultFalseErrorKindCorrupt, Errors: decoded.Errors, Raw: raw}
	}
	return MigrateV1ToV2(raw)
}

func MigrateV1ToV2(raw StorageValues) ThreadStoreParseResult {
	errs := []string{}
	tasksValue := parseJson(raw.Tasks, "tasks", &errs)
	projectsValue := parseJson(raw.Projects, "projects", &errs)
	lastFolder := parseLegacyLastFolder(raw.LastFolder, &errs)
	if len(errs) > 0 {
		return corrupt(1, errs, raw)
	}

	legacyProjectsValue, projectsListed := projectsValue.([]any)
	legacyTasks, tasksListed := tasksValue.([]any)
	if tasksValue != nil && !tasksListed {
		errs = append(errs, "tasks must be an array")
	}
	if projectsValue != nil && !projectsListed {
		errs = append(errs, "projects must be an array")
	}
	if len(errs) > 0 {
		return corrupt(1, errs, raw)
	}

	legacyProjects := []string{}
	for _, value := range legacyProjectsValue {
		if root, ok := value.(string); ok && root != "" {
			legacyProjects = append(legacyProjects, root)
		}
	}
	if !coordEvery(legacyProjectsValue, coordIsString) {
		errs = append(errs, "projects must contain only strings")
	}
	if len(errs) > 0 {
		return corrupt(1, errs, raw)
	}
	roots := []string{}
	seen := map[string]bool{}
	addRoot := func(root string) {
		if root = NormalizeProjectRoot(root); !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	for _, root := range legacyProjects {
		addRoot(root)
	}
	for _, value := range legacyTasks {
		task, ok := threadStorageIsRecord(value)
		if !ok {
			errs = append(errs, "tasks contains a non-object value")
			continue
		}
		if folder, ok := task["folder"].(string); ok && folder != "" {
			addRoot(folder)
		}
	}

	projects := make([]*Project, len(roots))
	projectByRoot := make(map[string]string, len(roots))
	for i, root := range roots {
		projects[i] = &Project{ID: LegacyProjectID(root), Root: root}
		projectByRoot[root] = projects[i].ID
	}
	tasks := []*StoredThread{}
	for index, value := range legacyTasks {
		if task := migrateLegacyThread(value, index, projectByRoot, &errs); task != nil {
			tasks = append(tasks, task)
		}
	}
	if len(errs) > 0 {
		return corrupt(1, errs, raw)
	}
	return ThreadStoreParseResultTrue{
		Data:          ThreadStoreData{Tasks: tasks, Projects: projects, Worktrees: []*Worktree{}, LastFolder: lastFolder},
		SourceVersion: 1,
		PreservedV1:   &raw,
	}
}

// SerializeThreadStore writes each value as JSON.stringify would.
func SerializeThreadStore(data *ThreadStoreData) SerializedThreadStore {
	text := func(value any) *string {
		encoded, err := jsonx.Marshal(value)
		if err != nil {
			panic(err)
		}
		return new(string(encoded))
	}
	return SerializedThreadStore{
		Tasks:      text(versioned(orEmpty(data.Tasks))),
		Projects:   text(versioned(orEmpty(data.Projects))),
		Worktrees:  text(versioned(orEmpty(data.Worktrees))),
		LastFolder: text(versioned(data.LastFolder)),
	}
}

func migrateLegacyThread(value any, index int, projectByRoot map[string]string, errs *[]string) *StoredThread {
	task, ok := threadStorageIsRecord(value)
	at := "tasks[" + strconv.Itoa(index) + "]"
	if !ok {
		*errs = append(*errs, at+" must be an object")
		return nil
	}
	id, idOK := task["id"].(string)
	idOK = idOK && id != ""
	title, titleOK := task["title"].(string)
	folder, folderOK := task["folder"].(string)
	modeOK := isLegacyMode(task["mode"])
	_, messagesOK := task["messages"].([]any)
	changedFiles, filesOK := task["changedFiles"].([]any)
	filesOK = filesOK && coordEvery(changedFiles, coordIsString)
	updatedAt, updatedOK := task["updatedAt"].(float64)
	updatedOK = updatedOK && threadStorageFiniteNumber(updatedAt)
	if !idOK {
		*errs = append(*errs, at+".id must be a non-empty string")
	}
	if !titleOK {
		*errs = append(*errs, at+".title must be a string")
	}
	if !folderOK {
		*errs = append(*errs, at+".folder must be a string")
	}
	if !modeOK {
		*errs = append(*errs, at+".mode is invalid")
	}
	if !messagesOK {
		*errs = append(*errs, at+".messages must be an array")
	}
	if !filesOK {
		*errs = append(*errs, at+".changedFiles must be an array of strings")
	}
	if !updatedOK {
		*errs = append(*errs, at+".updatedAt must be a number")
	}
	messages, listed := migrateMessages(task["messages"], index, errs)
	if _, isList := task["changedFiles"].([]any); !idOK || !titleOK || !folderOK || !modeOK || !isList || !updatedOK || !listed {
		return nil
	}

	sessionID, hasSession := task["sessionId"]
	continuationStatus := continuationStatusFor(sessionID, hasSession)
	files := make([]string, 0, len(changedFiles))
	for _, file := range changedFiles {
		if name, ok := file.(string); ok {
			files = append(files, name)
		}
	}
	stored := &StoredThread{
		ID:                 id,
		Title:              title,
		ExecutionPolicy:    StoredThreadExecutionPolicy(legacyModes[task["mode"].(string)]),
		Engine:             unrecordedEngine,
		Messages:           messages,
		ContinuationStatus: continuationStatus,
		LastChangeSnapshot: ChangeSnapshot{Files: files, CapturedAt: updatedAt},
		UpdatedAt:          updatedAt,
	}
	if folder != "" {
		projectID, known := projectByRoot[NormalizeProjectRoot(folder)]
		if !known {
			projectID = LegacyProjectID(folder)
		}
		stored.ProjectID = &projectID
	}
	if continuationStatus == ContinuationStatusAvailable {
		stored.Continuation = new(toContinuation(unrecordedEngine, sessionID))
	}
	return stored
}

// migrateMessages is false when value is not an array.
func migrateMessages(value any, taskIndex int, errs *[]string) ([]*ConversationMessage, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	messages := []*ConversationMessage{}
	for index, item := range list {
		message, ok := threadStorageIsRecord(item)
		id, idOK := message["id"].(string)
		kind, kindOK := message["kind"].(string)
		text, textOK := message["text"].(string)
		at, atOK := message["at"].(float64)
		detail, hasDetail := message["detail"]
		detailText, detailOK := detail.(string)
		if !ok || !idOK || !kindOK || !isMessageKind(kind) || !textOK || !atOK || !threadStorageFiniteNumber(at) || hasDetail && !detailOK {
			*errs = append(*errs, "tasks["+strconv.Itoa(taskIndex)+"].messages["+strconv.Itoa(index)+"] is invalid")
			continue
		}
		migrated := &ConversationMessage{ID: id, Kind: ConversationMessageKind(kind), Text: text, At: at}
		if hasDetail {
			migrated.Detail = &detailText
			if kind == "user" {
				migrated.Origin = LegacyOrigin(detailText)
			}
		}
		messages = append(messages, migrated)
	}
	return messages, true
}

func decodeV2(raw StorageValues) decodedV2 {
	if isEmpty(raw) {
		return decodedV2{Kind: "none"}
	}
	errs := []string{}
	tasks := parseJson(raw.Tasks, "tasks", &errs)
	projects := parseJson(raw.Projects, "projects", &errs)
	worktrees := parseJson(raw.Worktrees, "worktrees", &errs)
	lastFolder := parseJson(raw.LastFolder, "lastFolder", &errs)
	marked := func(value any) bool {
		record, ok := threadStorageIsRecord(value)
		return ok && record["version"] == float64(ThreadStoreVersion)
	}
	versioned := func(value any) bool {
		record, _ := threadStorageIsRecord(value)
		_, hasValue := record["value"]
		return marked(value) && hasValue
	}
	if !marked(tasks) && !marked(projects) && !marked(lastFolder) {
		return decodedV2{Kind: "none"}
	}
	if len(errs) > 0 {
		return decodedV2{Kind: "corrupt", Errors: errs}
	}
	if !versioned(tasks) || !versioned(projects) || !versioned(lastFolder) {
		return decodedV2{Kind: "corrupt", Errors: []string{"version 2 storage must contain all three versioned values"}}
	}
	if worktrees != nil && !versioned(worktrees) {
		return decodedV2{Kind: "corrupt", Errors: []string{"version 2 worktrees is not a versioned value"}}
	}
	values := DecodedThreadStore{
		Tasks:      tasks.(map[string]any)["value"],
		Projects:   projects.(map[string]any)["value"],
		LastFolder: lastFolder.(map[string]any)["value"],
	}
	if worktrees != nil {
		values.Worktrees = worktrees.(map[string]any)["value"]
	}
	return decodedV2{Kind: "v2", Values: values}
}

// ValidateThreadStoreData validates v2 values already decoded from their storage transport.
func ValidateThreadStoreData(values DecodedThreadStore) ThreadStoreValidationResult {
	errs := []string{}
	hiddenTasks := 0
	taskValues, tasksListed := values.Tasks.([]any)
	projectValues, projectsListed := values.Projects.([]any)
	worktreeValues, worktreesListed := values.Worktrees.([]any)
	lastFolder, folderNamed := values.LastFolder.(string)
	if !tasksListed {
		errs = append(errs, "v2 tasks must be an array")
	}
	if !projectsListed {
		errs = append(errs, "v2 projects must be an array")
	}
	if values.Worktrees != nil && !worktreesListed {
		errs = append(errs, "v2 worktrees must be an array")
	}
	if values.LastFolder != nil && !folderNamed {
		errs = append(errs, "v2 lastFolder must be a string or null")
	}
	projects := []*Project{}
	for _, value := range projectValues {
		if IsProject(value) {
			if project, ok := decodeRecord[Project](value); ok {
				projects = append(projects, project)
			}
		}
	}
	stored := []*Worktree{}
	for _, value := range worktreeValues {
		if IsWorktree(value) {
			if worktree, ok := decodeRecord[Worktree](value); ok {
				stored = append(stored, worktree)
			}
		}
	}
	if worktreesListed && len(stored) != len(worktreeValues) {
		errs = append(errs, "v2 worktrees contains an invalid value")
	}
	lifted := []*Worktree{}
	tasks := []*StoredThread{}
	for _, value := range taskValues {
		if task := sanitizeV2Thread(value, &lifted); task != nil {
			tasks = append(tasks, task)
		} else {
			hiddenTasks++
		}
	}
	if projectsListed && len(projects) != len(projectValues) {
		errs = append(errs, "v2 projects contains an invalid value")
	}
	projectIDs := jsonx.Set[string]{}
	for _, project := range projects {
		projectIDs[project.ID] = struct{}{}
	}
	placed := []*StoredThread{}
	for _, task := range tasks {
		if task.ProjectID == nil || *task.ProjectID == "" || projectIDs.Has(*task.ProjectID) {
			placed = append(placed, task)
		}
	}
	hiddenTasks += len(tasks) - len(placed)
	if len(errs) > 0 {
		return ThreadStoreValidationResult{Errors: errs}
	}
	worktrees := []*Worktree{}
	taken := map[string]bool{}
	for _, worktree := range append(stored, lifted...) {
		if !taken[worktree.ID] {
			taken[worktree.ID] = true
			if projectIDs.Has(worktree.ProjectID) {
				worktrees = append(worktrees, worktree)
			}
		}
	}
	worktreeIDs := jsonx.Set[string]{}
	for _, worktree := range worktrees {
		worktreeIDs[worktree.ID] = struct{}{}
	}
	for i, task := range placed {
		placed[i] = claiming(task, worktreeIDs)
	}
	data := ThreadStoreData{Tasks: placed, Projects: projects, Worktrees: worktrees}
	if folderNamed {
		data.LastFolder = &lastFolder
	}
	return ThreadStoreValidationResult{Ok: true, Data: data, HiddenTasks: hiddenTasks}
}

// RankedThread is a stored thread as the workspace holds it, ranked by when it last spoke when it was
// saved before threads were ranked. Its Subagents are left behind.
func RankedThread(thread *StoredThread) *Thread {
	shell := *thread
	shell.Messages, shell.Subagents = nil, nil
	ranked, ok := decodeRecord[Thread](&shell)
	if !ok {
		panic("domain: a stored thread does not read as a thread")
	}
	ranked.Messages = orEmpty(thread.Messages)
	if thread.RankedAt == nil {
		ranked.RankedAt = ThreadLastOutputAt(ranked)
	}
	return ranked
}

func claiming(task *StoredThread, worktreeIDs jsonx.Set[string]) *StoredThread {
	if task.WorktreeID == nil || *task.WorktreeID == "" || worktreeIDs.Has(*task.WorktreeID) {
		return task
	}
	local := *task
	local.WorktreeID, local.WorktreeEnteredAt = nil, nil
	return &local
}

func liftEmbeddedWorktree(value map[string]any, lifted *[]*Worktree) map[string]any {
	worktree, ok := threadStorageIsRecord(value["worktree"])
	if !ok {
		return value
	}
	task := maps.Clone(value)
	delete(task, "worktree")
	enteredAt := worktree["enteredAt"]
	candidate := maps.Clone(worktree)
	delete(candidate, "enteredAt")
	delete(candidate, "projectId")
	if projectID, has := task["projectId"]; has {
		candidate["projectId"] = projectID
	}
	if !IsWorktree(candidate) {
		return task
	}
	record, ok := decodeRecord[Worktree](candidate)
	if !ok {
		return task
	}
	*lifted = append(*lifted, record)
	task["worktreeId"] = candidate["id"]
	if threadStorageFiniteNumber(enteredAt) {
		task["worktreeEnteredAt"] = enteredAt
	}
	return task
}

// parseJson is nil for a nil value, and for one that is not JSON, which it records in errors.
func parseJson(value *string, label string, errs *[]string) any {
	if value == nil {
		return nil
	}
	var parsed any
	if err := jsonx.Unmarshal([]byte(*value), &parsed); err != nil {
		*errs = append(*errs, label+" is not valid JSON")
		return nil
	}
	return parsed
}

func parseLegacyLastFolder(value *string, errs *[]string) *string {
	return value
}

func corrupt(sourceVersion float64, errs []string, raw StorageValues) ThreadStoreParseResult {
	var preserved *StorageValues
	if sourceVersion == 1 {
		preserved = &raw
	}
	return ThreadStoreParseResultFalse{SourceVersion: sourceVersion, ErrorKind: ThreadStoreParseResultFalseErrorKindCorrupt, Errors: errs, PreservedV1: preserved, Raw: raw}
}

func versioned[T any](value T) versionedValue[T] {
	return versionedValue[T]{Value: value}
}

// continuationStatusFor reads a legacy session id; present is false when the field is absent.
func continuationStatusFor(value any, present bool) ContinuationStatus {
	if !present {
		return ContinuationStatusNone
	}
	if text, ok := value.(string); ok && !jsBlank(text) {
		return ContinuationStatusAvailable
	}
	return ContinuationStatusInvalid
}

func toContinuation(engine AgentEngine, value any) Continuation {
	text, _ := value.(string)
	return Continuation{Provider: string(engine), Value: text}
}

func isLegacyMode(value any) bool {
	mode, ok := value.(string)
	_, known := legacyModes[mode]
	return ok && known
}

func isMessageKind(value string) bool {
	return value == "user" || value == "assistant" || value == "tool" || value == "system"
}

// The checks below read values decoded from JSON into any (map[string]any, []any, float64, string, bool, nil).

func isThreadBase(value any) bool {
	task, ok := threadStorageIsRecord(value)
	if !ok || !droppableFieldsReadable(task) {
		return false
	}
	engine, engineOK := task["engine"].(string)
	messages, messagesOK := task["messages"].([]any)
	snapshot, snapshotOK := threadStorageIsRecord(task["lastChangeSnapshot"])
	files, filesOK := snapshot["files"].([]any)
	return threadStorageNonEmptyString(task["id"]) &&
		threadStorageNonEmptyString(task["title"]) &&
		coordAbsentOr(task, "titleByUser", coordIsBool) &&
		coordAbsentOr(task, "role", IsThreadRole) &&
		coordAbsentOr(task, "parentId", threadStorageNonEmptyString) &&
		coordAbsentOr(task, "decisions", everyListed(IsDecision)) &&
		coordAbsentOr(task, "coordinationNotes", everyListed(IsCoordinationNote)) &&
		coordAbsentOr(task, "projectId", threadStorageNonEmptyString) &&
		IsExecutionPolicy(task["executionPolicy"]) &&
		engineOK && IsAgentEngine(engine) &&
		coordAbsentOr(task, "model", func(value any) bool {
			if !IsModelID(value) {
				return false
			}
			offering, ok := ModelEngine(BuiltInModels, value.(string), AgentEngine(engine))
			return !ok || offering == AgentEngine(engine)
		}) &&
		coordAbsentOr(task, "fastMode", func(value any) bool { return CapabilitiesFor(engine).FastMode && coordIsBool(value) }) &&
		coordAbsentOr(task, "effort", IsAgentEffort) &&
		coordAbsentOr(task, "contextUsage", isContextUsage) &&
		messagesOK && coordEvery(messages, isConversationMessage) &&
		coordAbsentOr(task, "subagents", everyListed(isSubagent)) &&
		snapshotOK && filesOK && coordEvery(files, coordIsString) && threadStorageFiniteNumber(snapshot["capturedAt"]) &&
		coordAbsentOr(task, "sortIndex", threadStorageFiniteNumber) &&
		coordAbsentOr(task, "outcome", isThreadOutcome) &&
		coordAbsentOr(task, "outcomeUnread", coordIsTrue) &&
		coordAbsentOr(task, "findings", everyListed(isAutomationFinding)) &&
		coordAbsentOr(task, "lastFindingAt", threadStorageFiniteNumber) &&
		coordAbsentOr(task, "handledIssues", everyListed(threadStorageNonEmptyString)) &&
		coordAbsentOr(task, "snoozedUntil", func(value any) bool { return threadStorageFiniteNumber(value) && value.(float64) > 0 }) &&
		coordAbsentOr(task, "resourceStop", IsResourceStop) &&
		coordAbsentOr(task, "limitPause", func(value any) bool {
			if !IsLimitPause(value) {
				return false
			}
			held, _ := value.(map[string]any)["held"].([]any)
			return coordEvery(held, isHeldMessage)
		}) &&
		coordAbsentOr(task, "lastChecked", func(value any) bool {
			checked, ok := threadStorageIsRecord(value)
			return ok && threadStorageFiniteNumber(checked["at"]) && threadStorageNonEmptyString(checked["note"])
		}) &&
		coordAbsentOr(task, "worktreeId", threadStorageNonEmptyString) &&
		coordAbsentOr(task, "worktreeEnteredAt", threadStorageFiniteNumber) &&
		coordAbsentOr(task, "inheritedContinuation", coordIsTrue) &&
		coordAbsentOr(task, "runEndedAt", threadStorageFiniteNumber) &&
		coordAbsentOr(task, "runEndedAs", isThreadOutcome) &&
		coordAbsentOr(task, "createdAt", threadStorageFiniteNumber) &&
		coordAbsentOr(task, "rankedAt", threadStorageFiniteNumber) &&
		threadStorageFiniteNumber(task["updatedAt"]) &&
		coordAbsentOr(task, "archivedAt", threadStorageFiniteNumber)
}

// everyListed is Array.isArray(value) && value.every(check).
func everyListed(check func(value any) bool) func(value any) bool {
	return func(value any) bool {
		list, ok := value.([]any)
		return ok && coordEvery(list, check)
	}
}

func isAutomationFinding(value any) bool {
	finding, ok := threadStorageIsRecord(value)
	return ok &&
		threadStorageNonEmptyString(finding["id"]) &&
		threadStorageNonEmptyString(finding["headline"]) &&
		coordAbsentOr(finding, "detail", coordIsString) &&
		coordAbsentOr(finding, "key", threadStorageNonEmptyString) &&
		threadStorageFiniteNumber(finding["at"]) &&
		coordAbsentOr(finding, "read", coordIsTrue)
}

func isThreadOutcome(value any) bool {
	outcome, ok := value.(string)
	return ok && (outcome == "finished" || outcome == "failed" || outcome == "stopped")
}

func threadStorageIsRecord(value any) (map[string]any, bool) {
	return jsRecord(value)
}

func dropRetiredSettings(value any) any {
	task, ok := threadStorageIsRecord(value)
	if !ok {
		return value
	}
	_, window := task["contextWindow"]
	_, attention := task["attention"]
	_, attentionRead := task["attentionRead"]
	if !window && !attention && !attentionRead && task["model"] != "default" {
		return value
	}
	retired := maps.Clone(task)
	delete(retired, "contextWindow")
	delete(retired, "attention")
	delete(retired, "attentionRead")
	if retired["model"] == "default" {
		delete(retired, "model")
	}
	return retired
}

// retiredModel is the successor a retired model id is read as; false for any other value.
func retiredModel(value any) (string, bool) {
	id, ok := value.(string)
	if !ok {
		return "", false
	}
	successor, retired := retiredModels[id]
	return string(successor), retired
}

func renamedFields(value any) any {
	original, ok := threadStorageIsRecord(value)
	if !ok {
		return value
	}
	task := original
	set := func(key string, value any) {
		task = maps.Clone(task)
		task[key] = value
	}
	if model, retired := retiredModel(task["model"]); retired {
		set("model", model)
	}
	if usage, changed := currentUsage(task["usage"]); changed {
		set("usage", usage)
	}
	if threadModel, ok := threadStorageIsRecord(task["threadModel"]); ok {
		if model, retired := retiredModel(threadModel["model"]); retired {
			renamed := maps.Clone(threadModel)
			renamed["model"] = model
			set("threadModel", renamed)
		}
	}
	if before, ok := task["proposals"].([]any); ok {
		var proposals []any
		for index, item := range before {
			proposal, ok := threadStorageIsRecord(item)
			model, retired := retiredModel(proposal["model"])
			if !ok || !retired {
				if proposals != nil {
					proposals = append(proposals, item)
				}
				continue
			}
			if proposals == nil {
				proposals = slices.Clone(before[:index:index])
			}
			renamed := maps.Clone(proposal)
			renamed["model"] = model
			proposals = append(proposals, renamed)
		}
		if proposals != nil {
			set("proposals", proposals)
		}
	}
	if handled, has := original["silencedKeys"]; has {
		renamed := maps.Clone(original)
		delete(renamed, "silencedKeys")
		if _, kept := renamed["handledIssues"]; !kept {
			renamed["handledIssues"] = handled
		}
		task = renamed
	}
	if notes, has := task["crewNotes"]; has {
		renamed := maps.Clone(task)
		delete(renamed, "crewNotes")
		if _, kept := renamed["coordinationNotes"]; !kept {
			renamed["coordinationNotes"] = notes
		}
		task = renamed
	}
	if limitPause, changed := heldWithOrigins(task["limitPause"]); changed {
		set("limitPause", limitPause)
	}
	if list, ok := task["messages"].([]any); ok {
		var messages []any
		for index, message := range list {
			renamed, changed := renamedMessageFields(message)
			if changed && messages == nil {
				messages = slices.Clone(list[:index:index])
			}
			if messages != nil {
				messages = append(messages, renamed)
			}
		}
		if messages != nil {
			set("messages", messages)
		}
	}
	return task
}

// currentUsage is a thread's spend counted toward the model each spend is now known as; false when
// nothing in it changes.
func currentUsage(value any) (any, bool) {
	usage, ok := threadStorageIsRecord(value)
	if !ok {
		return value, false
	}
	current := true
	for model := range usage {
		if _, retired := retiredModel(model); retired || !IsModelID(model) {
			current = false
			break
		}
	}
	if current {
		return value, false
	}
	counted := map[string]any{}
	for _, recorded := range slices.Sorted(maps.Keys(usage)) {
		tokens := usage[recorded]
		model := recorded
		if successor, retired := retiredModel(recorded); retired {
			model = successor
		}
		if !IsModelID(model) {
			continue
		}
		before, isNumber := counted[model].(float64)
		if spent, ok := tokens.(float64); ok && isNumber {
			counted[model] = before + spent
		} else {
			counted[model] = tokens
		}
	}
	return counted, true
}

// renamedMessageFields is a stored message under the current field names; false when it is unchanged.
func renamedMessageFields(value any) (any, bool) {
	message, ok := threadStorageIsRecord(value)
	if !ok {
		return value, false
	}
	labelled := labelledOrigin(message)
	quiet, has := labelled["quiet"]
	if !has {
		return labelled, !sameRecord(labelled, message)
	}
	renamed := maps.Clone(labelled)
	delete(renamed, "quiet")
	if _, withdrawn := renamed["withdrawn"]; !withdrawn {
		renamed["withdrawn"] = quiet
	}
	return renamed, true
}

func labelledOrigin(value map[string]any) map[string]any {
	kind, hasKind := value["kind"]
	_, hasOrigin := value["origin"]
	detail, labelled := value["detail"].(string)
	if hasKind && kind != "user" || hasOrigin || !labelled {
		return value
	}
	origin := maps.Clone(value)
	origin["origin"] = originRecord(LegacyOrigin(detail))
	return origin
}

// originRecord is an origin as JSON decodes it into any.
func originRecord(origin MessageOrigin) map[string]any {
	switch origin := origin.(type) {
	case MessageOriginThread:
		record := map[string]any{"kind": "thread", "title": origin.Title}
		if origin.ThreadID != nil {
			record["threadId"] = *origin.ThreadID
		}
		if origin.Computer != nil {
			record["computer"] = *origin.Computer
		}
		return record
	case MessageOriginAutomation:
		return map[string]any{"kind": "automation", "runNumber": origin.RunNumber}
	case MessageOriginCoordination:
		return map[string]any{"kind": "coordination"}
	case MessageOriginLegacy:
		return map[string]any{"kind": "legacy", "label": origin.Label}
	}
	return nil
}

// heldWithOrigins gives the messages a paused thread holds their origins; false when none changes.
func heldWithOrigins(pause any) (any, bool) {
	record, ok := threadStorageIsRecord(pause)
	if !ok {
		return pause, false
	}
	before, ok := record["held"].([]any)
	if !ok {
		return pause, false
	}
	held := make([]any, len(before))
	changed := false
	for index, item := range before {
		held[index] = item
		if message, ok := threadStorageIsRecord(item); ok {
			if labelled := labelledOrigin(message); !sameRecord(labelled, message) {
				held[index], changed = labelled, true
			}
		}
	}
	if !changed {
		return pause, false
	}
	next := maps.Clone(record)
	next["held"] = held
	return next, true
}

// sameRecord is identity of two decoded objects.
func sameRecord(left, right map[string]any) bool {
	return jsonx.SameMap(left, right)
}

// droppableField is one field a thread can do without, and the check it must pass to be kept.
type droppableField struct {
	Field    string
	Readable func(value any) bool
}

// droppableFields is TypeScript's DROPPABLE_FIELDS, in its order.
var droppableFields = []droppableField{
	{"resourceStop", IsResourceStop},
	{"brief", IsThreadBrief},
	{"threadModel", IsThreadModelChoice},
	{"memory", IsCoordinatorMemory},
	{"report", IsCoordinationReport},
	{"resolvedAt", threadStorageFiniteNumber},
	{"pullRequest", IsPullRequestLink},
	{"muted", func(value any) bool { return value == true }},
	{"runOpen", func(value any) bool { return value == true }},
	{"usage", IsThreadUsage},
	{"remoteLead", IsHeldLead},
	{"remoteHeard", IsRemoteHeard},
	{"remoteWorking", IsRemoteWorking},
	{"remoteStops", IsRemoteStops},
	{"caughtStarts", IsCaughtStarts},
}

// droppableItems is TypeScript's DROPPABLE_ITEMS: lists each item of which is checked on its own.
var droppableItems = []droppableField{
	{"proposals", IsThreadProposal},
}

func droppableFieldsReadable(value map[string]any) bool {
	for _, field := range droppableFields {
		if !coordAbsentOr(value, field.Field, field.Readable) {
			return false
		}
	}
	for _, field := range droppableItems {
		if !coordAbsentOr(value, field.Field, everyListed(field.Readable)) {
			return false
		}
	}
	return true
}

var nothingKept = jsonx.Set[string]{}

// withoutUnreadableFields keeps the fields named in asSent as they are; nil asSent is nothingKept.
func withoutUnreadableFields(value any, asSent jsonx.Set[string]) any {
	task, ok := threadStorageIsRecord(value)
	if !ok {
		return value
	}
	for _, field := range droppableFields {
		if asSent.Has(field.Field) || coordAbsentOr(task, field.Field, field.Readable) {
			continue
		}
		task = maps.Clone(task)
		delete(task, field.Field)
	}
	for _, field := range droppableItems {
		if asSent.Has(field.Field) || coordAbsentOr(task, field.Field, everyListed(field.Readable)) {
			continue
		}
		items, _ := task[field.Field].([]any)
		task = maps.Clone(task)
		delete(task, field.Field)
		kept := []any{}
		for _, item := range items {
			if field.Readable(item) {
				kept = append(kept, item)
			}
		}
		if len(kept) > 0 {
			task[field.Field] = kept
		}
	}
	return task
}

// ReadableRemoteThread is a thread another computer's build sent, read honestly; the thread itself
// while it holds nothing to leave out.
func ReadableRemoteThread(thread *Thread) *Thread {
	shell := *thread
	shell.Messages = nil
	encoded, err := jsonx.Marshal(&shell)
	if err != nil {
		panic(err)
	}
	var sent map[string]any
	if err := jsonx.Unmarshal(encoded, &sent); err != nil {
		panic(err)
	}
	read := withoutUnreadableFields(renamedFields(sent), shownAsSent).(map[string]any)
	without := func(field string) {
		read = maps.Clone(read)
		delete(read, field)
	}
	if threadModel, has := read["threadModel"]; has {
		choice, ok := threadStorageIsRecord(threadModel)
		if _, named := choice["model"].(string); !ok || !named {
			without("threadModel")
		}
	}
	if proposals, has := read["proposals"]; has {
		list, _ := proposals.([]any)
		shown := []any{}
		for _, item := range list {
			proposal, ok := threadStorageIsRecord(item)
			if !ok || !coordAbsentOr(proposal, "engine", coordIsString) {
				continue
			}
			bare := maps.Clone(proposal)
			delete(bare, "engine")
			delete(bare, "model")
			delete(bare, "effort")
			if IsThreadProposal(bare) {
				shown = append(shown, item)
			}
		}
		if len(shown) == 0 {
			without("proposals")
		} else if len(shown) != len(list) {
			read = maps.Clone(read)
			read["proposals"] = shown
		}
	}
	if !coordAbsentOr(read, "role", IsThreadRole) {
		without("role")
	}
	if !coordAbsentOr(read, "outcome", isThreadOutcome) {
		without("outcome")
	}
	messages := thread.Messages
	for index, message := range thread.Messages {
		if labelled := WithLegacyOrigin(message); labelled != message {
			if jsonx.SameSlice(messages, thread.Messages) {
				messages = slices.Clone(thread.Messages)
			}
			messages[index] = labelled
		}
	}
	if sameRecord(read, sent) && jsonx.SameSlice(messages, thread.Messages) && thread.RankedAt != 0 {
		return thread
	}
	next, ok := decodeRecord[Thread](read)
	if !ok {
		panic("domain: a remote thread does not read back as a thread")
	}
	next.Messages = orEmpty(messages)
	if next.RankedAt == 0 {
		next.RankedAt = ThreadLastOutputAt(next)
	}
	return next
}

var shownAsSent = jsonx.SetOf("threadModel", "proposals")

// ParseStoredConversationMessages reads a conversation loaded separately, each value as decoded from
// JSON into any. Its error names the first unreadable message.
func ParseStoredConversationMessages(values []any) ([]*ConversationMessage, error) {
	messages := make([]*ConversationMessage, len(values))
	for index, value := range values {
		message, ok := parseStoredMessage(value, nil)
		if !ok {
			return nil, storedMessageUnreadable(index)
		}
		messages[index] = message
	}
	return messages, nil
}

// ParseStoredConversationRows is ParseStoredConversationMessages over the stored JSON rows. A message
// stored in today's shape is decoded from its row directly rather than encoded again first.
func ParseStoredConversationRows(rows []jsontext.Value) ([]*ConversationMessage, error) {
	messages := make([]*ConversationMessage, len(rows))
	for index, row := range rows {
		var value any
		if err := jsonx.Unmarshal(row, &value); err != nil {
			return nil, err
		}
		message, ok := parseStoredMessage(value, row)
		if !ok {
			return nil, storedMessageUnreadable(index)
		}
		messages[index] = message
	}
	return messages, nil
}

func storedMessageUnreadable(index int) error {
	return errors.New("Stored message " + strconv.Itoa(index+1) + " is unreadable.")
}

// parseStoredMessage reads value, decoding row, its encoding, when no field needed renaming.
func parseStoredMessage(value any, row jsontext.Value) (*ConversationMessage, bool) {
	renamed, changed := renamedMessageFields(value)
	if !isConversationMessage(renamed) {
		return nil, false
	}
	if changed || row == nil {
		return decodeRecord[ConversationMessage](renamed)
	}
	var message ConversationMessage
	if err := jsonx.Unmarshal(row, &message); err != nil {
		return nil, false
	}
	return &message, true
}

func recordedEngine(value any) any {
	task, ok := threadStorageIsRecord(value)
	if !ok {
		return value
	}
	if _, has := task["engine"]; has {
		return value
	}
	recorded := maps.Clone(task)
	recorded["engine"] = string(unrecordedEngine)
	return recorded
}

// sanitizeV2Thread is nil for a thread this build cannot read.
func sanitizeV2Thread(raw any, lifted *[]*Worktree) *StoredThread {
	retired := withoutUnreadableFields(renamedFields(dropRetiredSettings(recordedEngine(raw))), nothingKept)
	value := retired
	if task, ok := threadStorageIsRecord(retired); ok {
		value = liftEmbeddedWorktree(task, lifted)
	}
	if !isThreadBase(value) {
		return nil
	}
	task := value.(map[string]any)
	continuation, hasContinuation := task["continuation"]
	if hasContinuation && !isContinuation(continuation) {
		task = maps.Clone(task)
		delete(task, "continuation")
		task["continuationStatus"] = string(ContinuationStatusInvalid)
		return settledThread(task)
	}
	status := task["continuationStatus"]
	if hasContinuation && status != string(ContinuationStatusAvailable) {
		return nil
	}
	if !hasContinuation && status != string(ContinuationStatusNone) && status != string(ContinuationStatusInvalid) {
		return nil
	}
	return settledThread(task)
}

// settledThread is a checked thread record as a StoredThread, its subagents settled; nil when it does not read as one.
func settledThread(task map[string]any) *StoredThread {
	stored, ok := decodeRecord[StoredThread](task)
	if !ok {
		return nil
	}
	return settleStoredSubagents(stored)
}

func settleStoredSubagents(task *StoredThread) *StoredThread {
	if !slices.ContainsFunc(task.Subagents, func(subagent *Subagent) bool { return subagent.Status == SubagentStatusWorking }) {
		return task
	}
	settled := *task
	settled.Subagents = make([]*Subagent, len(task.Subagents))
	for i, subagent := range task.Subagents {
		settled.Subagents[i] = subagent
		if subagent.Status == SubagentStatusWorking {
			stopped := *subagent
			stopped.Status = SubagentStatusStopped
			if stopped.FinishedAt == nil {
				stopped.FinishedAt = new(task.UpdatedAt)
			}
			settled.Subagents[i] = &stopped
		}
	}
	return &settled
}

func isConversationMessage(value any) bool {
	message, ok := threadStorageIsRecord(value)
	if !ok {
		return false
	}
	kind, kindOK := message["kind"].(string)
	return threadStorageNonEmptyString(message["id"]) &&
		kindOK && isMessageKind(kind) &&
		coordIsString(message["text"]) &&
		coordAbsentOr(message, "artifact", coordIsTrue) &&
		coordAbsentOr(message, "answer", coordIsTrue) &&
		coordAbsentOr(message, "origin", func(value any) bool { return kind == "user" && isMessageOrigin(value) }) &&
		coordAbsentOr(message, "detail", coordIsString) &&
		coordAbsentOr(message, "tone", func(value any) bool { return value == "error" }) &&
		coordAbsentOr(message, "attachments", everyListed(threadStorageNonEmptyString)) &&
		coordAbsentOr(message, "annotations", everyListed(isAnnotation)) &&
		coordAbsentOr(message, "pastes", everyListed(isPastedText)) &&
		coordAbsentOr(message, "files", everyListed(isAttachedFile)) &&
		coordAbsentOr(message, "withdrawn", coordIsTrue) &&
		threadStorageFiniteNumber(message["at"])
}

func isHeldMessage(value any) bool {
	message, ok := threadStorageIsRecord(value)
	return ok &&
		threadStorageNonEmptyString(message["id"]) &&
		coordIsString(message["text"]) &&
		coordIsString(message["prompt"]) &&
		everyListed(threadStorageNonEmptyString)(message["attachments"]) &&
		coordAbsentOr(message, "annotations", everyListed(isAnnotation)) &&
		coordAbsentOr(message, "pastes", everyListed(isPastedText)) &&
		coordAbsentOr(message, "files", everyListed(isAttachedFile)) &&
		coordAbsentOr(message, "origin", isMessageOrigin) &&
		coordAbsentOr(message, "detail", coordIsString) &&
		coordAbsentOr(message, "answers", threadStorageNonEmptyString) &&
		coordAbsentOr(message, "usersOwn", coordIsTrue)
}

func isMessageOrigin(value any) bool {
	origin, ok := threadStorageIsRecord(value)
	if !ok {
		return false
	}
	switch origin["kind"] {
	case "thread":
		return coordIsString(origin["title"]) && coordAbsentOr(origin, "threadId", threadStorageNonEmptyString) && coordAbsentOr(origin, "computer", threadStorageNonEmptyString)
	case "automation":
		return threadStorageFiniteNumber(origin["runNumber"])
	case "coordination":
		return true
	case "legacy":
		return coordIsString(origin["label"])
	}
	return false
}

func isAnnotation(value any) bool {
	annotation, ok := threadStorageIsRecord(value)
	return ok && threadStorageNonEmptyString(annotation["id"]) && threadStorageNonEmptyString(annotation["quote"]) && coordIsString(annotation["note"])
}

func isPastedText(value any) bool {
	paste, ok := threadStorageIsRecord(value)
	return ok && threadStorageNonEmptyString(paste["id"]) && coordIsString(paste["text"])
}

func isAttachedFile(value any) bool {
	file, ok := threadStorageIsRecord(value)
	return ok && threadStorageNonEmptyString(file["id"]) && threadStorageNonEmptyString(file["path"]) && threadStorageNonEmptyString(file["name"]) &&
		coordAbsentOr(file, "folder", coordIsTrue)
}

func isContinuation(value any) bool {
	continuation, ok := threadStorageIsRecord(value)
	return ok && threadStorageNonEmptyString(continuation["provider"]) && threadStorageNonEmptyString(continuation["value"])
}

func isContextUsage(value any) bool {
	usage, ok := threadStorageIsRecord(value)
	if !ok || !threadStorageFiniteNumber(usage["tokens"]) || !threadStorageFiniteNumber(usage["limit"]) {
		return false
	}
	return usage["tokens"].(float64) >= 0 && usage["limit"].(float64) > 0 && threadStorageNonEmptyString(usage["model"])
}

func isSubagent(value any) bool {
	subagent, ok := threadStorageIsRecord(value)
	if !ok {
		return false
	}
	status, _ := subagent["status"].(string)
	return threadStorageNonEmptyString(subagent["id"]) &&
		coordIsString(subagent["description"]) &&
		coordAbsentOr(subagent, "agentType", coordIsString) &&
		coordAbsentOr(subagent, "sessionScoped", coordIsTrue) &&
		(status == "working" || status == "idle" || status == "completed" || status == "failed" || status == "stopped") &&
		coordAbsentOr(subagent, "lastToolName", coordIsString) &&
		coordAbsentOr(subagent, "summary", coordIsString) &&
		coordAbsentOr(subagent, "totalTokens", func(value any) bool { return threadStorageFiniteNumber(value) && value.(float64) >= 0 }) &&
		threadStorageFiniteNumber(subagent["startedAt"]) &&
		coordAbsentOr(subagent, "finishedAt", threadStorageFiniteNumber) &&
		everyListed(func(value any) bool {
			item, ok := threadStorageIsRecord(value)
			kind := item["kind"]
			return ok && threadStorageNonEmptyString(item["id"]) && (kind == "text" || kind == "tool") &&
				coordAbsentOr(item, "title", coordIsString) && coordIsString(item["text"]) && threadStorageFiniteNumber(item["at"])
		})(subagent["activity"])
}

func threadStorageNonEmptyString(value any) bool {
	return projectNonEmptyString(value)
}

func threadStorageFiniteNumber(value any) bool {
	return projectFiniteNumber(value)
}

// isEmpty is true when storage holds none of the values.
func isEmpty(raw StorageValues) bool {
	return raw.Tasks == nil && raw.Projects == nil && raw.Worktrees == nil && raw.LastFolder == nil
}

// decodeRecord reads a value decoded into any, or another typed value, as T.
func decodeRecord[T any](value any) (*T, bool) {
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		return nil, false
	}
	var decoded T
	if err := jsonx.Unmarshal(encoded, &decoded); err != nil {
		return nil, false
	}
	return &decoded, true
}
