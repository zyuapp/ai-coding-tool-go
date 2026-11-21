package domain

import (
	"errors"
	"slices"
	"strings"
	"unicode"
)

const DefaultEngine AgentEngine = AgentEngineClaude

var agentEngineEfforts = map[AgentEffort]EffortSpec{
	AgentEffortUltra:  {ID: AgentEffortUltra, Label: "Ultra", Description: new("Splits the work across agents"), Delegates: new(true)},
	AgentEffortMax:    {ID: AgentEffortMax, Label: "Max"},
	AgentEffortXhigh:  {ID: AgentEffortXhigh, Label: "Extra high"},
	AgentEffortHigh:   {ID: AgentEffortHigh, Label: "High"},
	AgentEffortMedium: {ID: AgentEffortMedium, Label: "Medium"},
	AgentEffortLow:    {ID: AgentEffortLow, Label: "Low"},
}

// AgentEfforts is every effort, deepest first.
var AgentEfforts = []AgentEffort{AgentEffortUltra, AgentEffortMax, AgentEffortXhigh, AgentEffortHigh, AgentEffortMedium, AgentEffortLow}

// AgentEngineEfforts is the named efforts as specs, always deepest first however they were named.
func AgentEngineEfforts(ids ...AgentEffort) []*EffortSpec {
	specs := []*EffortSpec{}
	for _, id := range AgentEfforts {
		if slices.Contains(ids, id) {
			spec := agentEngineEfforts[id]
			specs = append(specs, &spec)
		}
	}
	return specs
}

var effortsThroughMax = AgentEngineEfforts(AgentEffortMax, AgentEffortXhigh, AgentEffortHigh, AgentEffortMedium, AgentEffortLow)

var effortsThroughUltra = AgentEngineEfforts(AgentEffortUltra, AgentEffortMax, AgentEffortXhigh, AgentEffortHigh, AgentEffortMedium, AgentEffortLow)

var claudeModels = []*ModelSpec{
	{ID: "fable", Label: "Fable", Description: "Most capable for demanding work", ContextWindow: 1_000_000, Efforts: effortsThroughMax, ManualCompaction: new(true)},
	{ID: "opus", Label: "Opus", Description: "Best for complex reasoning", ContextWindow: 1_000_000, Efforts: effortsThroughMax, ManualCompaction: new(true)},
	{ID: "sonnet", Label: "Sonnet", Description: "Balanced speed and capability", ContextWindow: 1_000_000, Efforts: effortsThroughMax, ManualCompaction: new(true)},
	{ID: "haiku", Label: "Haiku", Description: "Fastest for lightweight work", ContextWindow: 200_000, Efforts: []*EffortSpec{}, ManualCompaction: new(true)},
}

// codexContextWindow is the default context window in Codex's model catalogue, used until the server reports its own.
const codexContextWindow = 272_000

var codexModels = []*ModelSpec{
	{ID: "gpt-6.2-astra", Label: "Astra", Description: "Most capable model for complex, demanding work", ContextWindow: codexContextWindow, Efforts: effortsThroughUltra, ManualCompaction: new(true)},
	{ID: "gpt-6.2-sol", Label: "Sol", Description: "Strong coding model for everyday work", ContextWindow: codexContextWindow, Efforts: effortsThroughUltra, ManualCompaction: new(true)},
	{ID: "gpt-6.1-terra", Label: "Terra", Description: "Balanced agentic coding model for everyday work", ContextWindow: codexContextWindow, Efforts: effortsThroughUltra, ManualCompaction: new(true)},
	{ID: "gpt-6-luna", Label: "Luna", Description: "Efficient model for lightweight work", ContextWindow: codexContextWindow, Efforts: effortsThroughMax, ManualCompaction: new(true)},
}

// modelIdLimit counts UTF-16 code units, as JavaScript's length does.
const modelIdLimit = 256

// modelIdShape stands for /^[^\s\p{Cc}]+$/u: JavaScript's \s is Go's unicode.IsSpace plus U+FEFF.
func modelIdShape(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == 0xFEFF {
			return false
		}
	}
	return true
}

// objectPrototypeNames are the names every JavaScript object inherits.
var objectPrototypeNames = map[string]bool{
	"constructor": true, "__defineGetter__": true, "__defineSetter__": true, "hasOwnProperty": true,
	"__lookupGetter__": true, "__lookupSetter__": true, "isPrototypeOf": true, "propertyIsEnumerable": true,
	"toString": true, "valueOf": true, "__proto__": true, "toLocaleString": true,
}

// IsModelID is true for a well-formed model id, known here or not. value is a string, or anything decoded from JSON.
func IsModelID(value any) bool {
	id, ok := value.(string)
	if !ok {
		return false
	}
	units := 0
	for _, r := range id {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	return units <= modelIdLimit && modelIdShape(id) && !objectPrototypeNames[id]
}

var retiredModels = map[string]BuiltInModel{
	"gpt-5.6-sol":  BuiltInModelGpt62Sol,
	"gpt-6-sol":    BuiltInModelGpt62Sol,
	"gpt-6.1-sol":  BuiltInModelGpt62Sol,
	"gpt-6-astra":  BuiltInModelGpt62Astra,
	"gpt-5.6-luna": BuiltInModelGpt6Luna,
}

// CurrentModel reads a retired model id as its successor; any other value comes back as it was.
func CurrentModel(value any) any {
	if id, ok := value.(string); ok {
		if successor, retired := retiredModels[id]; retired {
			return string(successor)
		}
	}
	return value
}

var policyOrder = []ExecutionPolicy{ExecutionPolicyBypass, ExecutionPolicyAutonomous, ExecutionPolicyAllowEdits, ExecutionPolicyConfirm}

// engineModels is how an engine's models are known: a built-in list (Discovered false) or only once the engine names them.
type engineModels struct {
	Discovered bool
	List       []*ModelSpec
	Default    AgentModel
}

// engineRecord is the TypeScript EngineSpec.
type engineRecord struct {
	Label         string
	Models        engineModels
	DefaultEffort AgentEffort
	Capabilities  EngineCapabilities
}

const DefaultModel AgentModel = "opus"

var engines = map[AgentEngine]*engineRecord{
	AgentEngineClaude: {
		Label:         "Claude",
		Models:        engineModels{List: claudeModels, Default: DefaultModel},
		DefaultEffort: AgentEffortHigh,
		Capabilities:  EngineCapabilities{FastMode: false, Workflows: true, Subagents: true, SubagentMetadata: true, Review: false, ChromeBrowser: true, Policies: ExecutionPolicies, UnattendedPolicy: ExecutionPolicyAutonomous, IdleCompactionCacheMs: new(60 * 60_000.0)},
	},
	AgentEngineCodex: {
		Label:         "Codex",
		Models:        engineModels{List: codexModels, Default: "gpt-6.2-sol"},
		DefaultEffort: AgentEffortHigh,
		Capabilities:  EngineCapabilities{FastMode: true, Workflows: false, Subagents: true, SubagentMetadata: true, Review: true, ChromeBrowser: false, Policies: ExecutionPolicies, UnattendedPolicy: ExecutionPolicyAutonomous},
	},
	AgentEngineOpencode: {
		Label:         "OpenCode",
		Models:        engineModels{Discovered: true},
		DefaultEffort: AgentEffortHigh,
		Capabilities:  EngineCapabilities{FastMode: false, Workflows: false, Subagents: false, SubagentMetadata: false, Review: false, ChromeBrowser: false, Policies: []ExecutionPolicy{ExecutionPolicyConfirm, ExecutionPolicyPlan, ExecutionPolicyAllowEdits, ExecutionPolicyBypass}, UnattendedPolicy: ExecutionPolicyBypass},
	},
}

// AgentEngines is every engine, in the order pickers list them.
var AgentEngines = []AgentEngine{AgentEngineClaude, AgentEngineCodex, AgentEngineOpencode}

// Get is the readiness of one engine, nil when the status names none (or is nil).
func (s *EngineStatus) Get(engine AgentEngine) *EngineReadiness {
	if s == nil {
		return nil
	}
	switch engine {
	case AgentEngineClaude:
		return s.Claude
	case AgentEngineCodex:
		return s.Codex
	case AgentEngineOpencode:
		return s.Opencode
	}
	return nil
}

// Get is one engine's models.
func (m *ModelCatalogModels) Get(engine AgentEngine) []*ModelSpec {
	switch engine {
	case AgentEngineClaude:
		return m.Claude
	case AgentEngineCodex:
		return m.Codex
	case AgentEngineOpencode:
		return m.Opencode
	}
	return nil
}

// AgentEngineEngineNotice is what is wrong with this engine on this machine, or nil when it is fine.
func AgentEngineEngineNotice(engine AgentEngine, readiness *EngineReadiness) *EngineNotice {
	label := EngineLabel(string(engine))
	named := label + " on this machine"
	if readiness.Version != nil {
		named = label + " " + *readiness.Version
	}
	var fix *string
	var updatable *bool
	if readiness.Fix != nil && *readiness.Fix != "" {
		fix, updatable = readiness.Fix, new(true)
	}
	switch {
	case readiness.Access == EngineAccessMissing:
		return &EngineNotice{Blocking: true, Message: label + " is not installed.", Fix: fix}
	case readiness.Access == EngineAccessOutdated:
		return &EngineNotice{Blocking: true, Message: named + " is too old. Update to " + jsString(readiness.Required) + ".", Fix: fix, Updatable: updatable}
	case readiness.Access == EngineAccessUnavailable:
		return &EngineNotice{Blocking: true, Message: label + " is installed but would not start."}
	case readiness.Access == EngineAccessReady && readiness.Catalog != nil && len(readiness.Catalog) == 0:
		return &EngineNotice{Blocking: true, Message: label + " has no models. Connect a provider first.", Fix: fix}
	case readiness.Required != nil && *readiness.Required != "":
		return &EngineNotice{Blocking: false, Message: named + " is behind " + *readiness.Required + ", so some models are hidden.", Fix: fix, Updatable: updatable}
	}
	return nil
}

func EngineStatusWord(readiness *EngineReadiness) string {
	switch {
	case readiness.Access == EngineAccessMissing:
		return "Not installed"
	case readiness.Access == EngineAccessOutdated:
		return "Too old to run"
	case readiness.Access == EngineAccessUnavailable:
		return "Will not start"
	case readiness.Access == EngineAccessSignedOut:
		return "Signed out"
	case readiness.Required != nil && *readiness.Required != "":
		return "Needs update"
	case readiness.Latest != nil && *readiness.Latest != "":
		return "Update available"
	}
	return "Ready"
}

func EngineStatusLine(engine AgentEngine, readiness *EngineReadiness) string {
	if notice := AgentEngineEngineNotice(engine, readiness); notice != nil {
		return notice.Message
	}
	if readiness.Access == EngineAccessSignedOut {
		return "Sign in to run threads on " + EngineLabel(string(engine)) + "."
	}
	if readiness.Latest != nil && *readiness.Latest != "" {
		how := "; update it the way you installed it"
		if readiness.Fix != nil && *readiness.Fix != "" {
			how = ""
		}
		return "Version " + jsString(readiness.Version) + ". " + *readiness.Latest + " is available" + how + "."
	}
	if readiness.Version != nil && *readiness.Version != "" {
		return "Version " + *readiness.Version
	}
	return "Installed and ready."
}

// AgentEngineEngineUpdate is nil when the engine is current or not installed.
func AgentEngineEngineUpdate(readiness *EngineReadiness) *EngineUpdate {
	target := readiness.Latest
	if target == nil {
		target = readiness.Required
	}
	if readiness.Access == EngineAccessMissing || target == nil || *target == "" {
		return nil
	}
	update := &EngineUpdate{Target: *target}
	if readiness.Version != nil && *readiness.Version != "" {
		update.Version = readiness.Version
	}
	if readiness.Fix != nil && *readiness.Fix != "" {
		update.Command = readiness.Fix
	}
	return update
}

// EngineBlocker is why this engine cannot take a run; false when it can.
func EngineBlocker(engine AgentEngine, readiness *EngineReadiness) (string, bool) {
	notice := AgentEngineEngineNotice(engine, readiness)
	if notice == nil || !notice.Blocking {
		return "", false
	}
	if notice.Updatable != nil && *notice.Updatable {
		return notice.Message + " Update it in Settings.", true
	}
	if notice.Fix != nil && *notice.Fix != "" {
		return notice.Message + " Run `" + *notice.Fix + "` to fix it.", true
	}
	return notice.Message, true
}

func EngineIsBlocked(engine AgentEngine, readiness *EngineReadiness) bool {
	notice := AgentEngineEngineNotice(engine, readiness)
	return notice != nil && notice.Blocking
}

// EngineNeedsAttention takes a nil status as none.
func EngineNeedsAttention(status *EngineStatus) bool {
	for _, engine := range AgentEngines {
		if readiness := status.Get(engine); readiness != nil && AgentEngineEngineNotice(engine, readiness) != nil {
			return true
		}
	}
	return false
}

var effortIds = map[AgentEffort]bool{
	AgentEffortUltra: true, AgentEffortMax: true, AgentEffortXhigh: true, AgentEffortHigh: true, AgentEffortMedium: true, AgentEffortLow: true,
}

func catalogKey(engine AgentEngine, model AgentModel) string {
	return string(engine) + "\x00" + model
}

func entryOf(catalog *ModelCatalog, engine AgentEngine, model AgentModel) *CatalogEntry {
	return catalog.Entries[catalogKey(engine, model)]
}

// isReadableSpec holds a spec a newer build or an engine sent to what this build can read.
func isReadableSpec(spec *ModelSpec) bool {
	if spec == nil || !IsModelID(spec.ID) || spec.Efforts == nil {
		return false
	}
	for _, effort := range spec.Efforts {
		if effort == nil || !IsAgentEffort(effort.ID) {
			return false
		}
	}
	return true
}

// AgentEngineModelCatalog builds the catalogue from each engine's list. Entries and ByID are keyed
// maps; iterate Models in AgentEngines order where TypeScript iterates entries in insertion order.
func AgentEngineModelCatalog(lists map[AgentEngine][]*ModelSpec) *ModelCatalog {
	entries := map[string]*CatalogEntry{}
	byID := map[string][]*CatalogEntry{}
	models := ByEngine(func(engine AgentEngine) []*ModelSpec {
		kept := []*ModelSpec{}
		for _, spec := range lists[engine] {
			if !isReadableSpec(spec) {
				continue
			}
			key := catalogKey(engine, spec.ID)
			if _, taken := entries[key]; taken {
				continue
			}
			coordinatorEfforts := []*EffortSpec{}
			for _, effort := range spec.Efforts {
				if effort.Delegates == nil || !*effort.Delegates {
					coordinatorEfforts = append(coordinatorEfforts, effort)
				}
			}
			entry := &CatalogEntry{Engine: engine, Spec: *spec, Efforts: spec.Efforts, CoordinatorEfforts: coordinatorEfforts}
			entries[key] = entry
			byID[spec.ID] = append(byID[spec.ID], entry)
			kept = append(kept, spec)
		}
		return kept
	})
	return &ModelCatalog{
		Models:  ModelCatalogModels{Claude: models[AgentEngineClaude], Codex: models[AgentEngineCodex], Opencode: models[AgentEngineOpencode]},
		Entries: entries,
		ByID:    byID,
	}
}

var builtInLists = ByEngine(func(engine AgentEngine) []*ModelSpec {
	if models := engines[engine].Models; !models.Discovered {
		return models.List
	}
	return []*ModelSpec{}
})

// BuiltInModels is the models this build ships knowing.
var BuiltInModels = AgentEngineModelCatalog(builtInLists)

// statusCatalogs memoizes EngineCatalog by status pointer.
var statusCatalogs weakMemo[EngineStatus, *ModelCatalog]

// EngineCatalog is the catalogue a machine's engines make up, one per status pointer. A nil status is none.
func EngineCatalog(status *EngineStatus) *ModelCatalog {
	named := func(engine AgentEngine) []*ModelSpec {
		if readiness := status.Get(engine); readiness != nil {
			return readiness.Catalog
		}
		return nil
	}
	if !slices.ContainsFunc(AgentEngines, func(engine AgentEngine) bool { return named(engine) != nil }) {
		return BuiltInModels
	}
	if catalog, ok := statusCatalogs.get(status); ok {
		return catalog
	}
	catalog := AgentEngineModelCatalog(ByEngine(func(engine AgentEngine) []*ModelSpec {
		if list := named(engine); list != nil {
			return list
		}
		return builtInLists[engine]
	}))
	statusCatalogs.set(status, catalog)
	return catalog
}

// IsAgentEngine accepts an AgentEngine or a string (an EngineID, or a value decoded from JSON).
func IsAgentEngine(value any) bool {
	switch v := value.(type) {
	case AgentEngine:
		return engines[v] != nil
	case string:
		return engines[AgentEngine(v)] != nil
	}
	return false
}

// IsAgentEffort accepts an AgentEffort or a string.
func IsAgentEffort(value any) bool {
	switch v := value.(type) {
	case AgentEffort:
		return effortIds[v]
	case string:
		return effortIds[AgentEffort(v)]
	}
	return false
}

func EffortLabel(value string) string {
	if IsAgentEffort(value) {
		return agentEngineEfforts[AgentEffort(value)].Label
	}
	return value
}

func CatalogHasModel(catalog *ModelCatalog, model AgentModel) bool {
	_, ok := catalog.ByID[model]
	return ok
}

func EngineHasModel(catalog *ModelCatalog, engine AgentEngine, model AgentModel) bool {
	_, ok := catalog.Entries[catalogKey(engine, model)]
	return ok
}

// EngineModelsKnown takes a nil status as none.
func EngineModelsKnown(status *EngineStatus, engine AgentEngine) bool {
	spec := engineSpec(string(engine))
	if spec == nil || !spec.Models.Discovered {
		return true
	}
	readiness := status.Get(engine)
	return readiness != nil && readiness.Catalog != nil
}

// ModelEngine is the engine for a model named without one. An empty preferred is none; false when the catalogue does not hold the model.
func ModelEngine(catalog *ModelCatalog, model AgentModel, preferred AgentEngine) (AgentEngine, bool) {
	entries := catalog.ByID[model]
	for _, entry := range entries {
		if entry.Engine == preferred {
			return entry.Engine, true
		}
	}
	if len(entries) > 0 {
		return entries[0].Engine, true
	}
	return "", false
}

func SavedModelEngine(model AgentModel) (AgentEngine, bool) {
	if engine, ok := ModelEngine(BuiltInModels, model, ""); ok {
		return engine, true
	}
	for _, engine := range AgentEngines {
		if engines[engine].Models.Discovered {
			return engine, true
		}
	}
	return "", false
}

func IsFavorite(favorites []*EngineModel, engine AgentEngine, model AgentModel) bool {
	return slices.ContainsFunc(favorites, func(favorite *EngineModel) bool { return favorite.Engine == engine && favorite.Model == model })
}

// UnknownModel takes an empty engine as none named.
func UnknownModel(model AgentModel, engine AgentEngine) string {
	if engine == "" {
		return "No engine here offers the " + model + " model. Pick another model."
	}
	return EngineLabel(string(engine)) + " here does not offer the " + model + " model. Pick another model."
}

func NoModel(engine AgentEngine) string {
	return EngineLabel(string(engine)) + " has not named a model for this thread yet. Pick a model."
}

func plainName(name string) string {
	var plain strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			plain.WriteRune(r)
		}
	}
	return plain.String()
}

// ResolveModel is the model a name means, on engine when one is named (empty is none). The error's
// text is the refusal the user reads.
func ResolveModel(catalog *ModelCatalog, name AgentModel, favorites []*EngineModel, engine AgentEngine) (EngineModel, error) {
	on := func(entries []*CatalogEntry) []*CatalogEntry {
		if engine == "" {
			return entries
		}
		kept := []*CatalogEntry{}
		for _, entry := range entries {
			if entry.Engine == engine {
				kept = append(kept, entry)
			}
		}
		return kept
	}
	exact := on(catalog.ByID[name])
	plain := plainName(name)
	matches := exact
	if len(exact) == 0 && plain != "" {
		matches = []*CatalogEntry{}
		for _, entry := range on(catalogEntries(catalog)) {
			id := entry.Spec.ID
			if plainName(id[strings.LastIndex(id, "/")+1:]) == plain || plainName(entry.Spec.Label) == plain {
				matches = append(matches, entry)
			}
		}
	}
	pinned := []*CatalogEntry{}
	for _, entry := range matches {
		if IsFavorite(favorites, entry.Engine, entry.Spec.ID) {
			pinned = append(pinned, entry)
		}
	}
	meant := matches
	if len(pinned) > 0 {
		meant = pinned
	}
	if len(meant) == 1 {
		return EngineModel{Engine: meant[0].Engine, Model: meant[0].Spec.ID}, nil
	}
	if len(meant) > 1 {
		ids := map[string]bool{}
		for _, entry := range meant {
			ids[entry.Spec.ID] = true
		}
		shared := len(ids) < len(meant)
		named := make([]string, len(meant))
		for i, entry := range meant {
			named[i] = entry.Spec.ID
			if shared {
				named[i] = entry.Spec.ID + " on " + EngineLabel(string(entry.Engine))
			}
		}
		advice := "Name one of them, or pin the one you mean in the model picker."
		if shared {
			advice = "Name the engine as well, or pin the one you mean in the model picker."
		}
		return EngineModel{}, errors.New(name + " could mean " + strings.Join(named, ", ") + ". " + advice)
	}
	return EngineModel{}, errors.New(UnknownModel(name, engine))
}

func ModelSupportsManualCompaction(catalog *ModelCatalog, engine AgentEngine, model AgentModel) bool {
	entry := entryOf(catalog, engine, model)
	return entry != nil && entry.Spec.ManualCompaction != nil && *entry.Spec.ManualCompaction
}

func AgentEngineClaudeEffort(effort AgentEffort) ClaudeEffort {
	if slices.ContainsFunc(effortsThroughMax, func(spec *EffortSpec) bool { return spec.ID == effort }) {
		return ClaudeEffort(effort)
	}
	return ClaudeEffortHigh
}

func EngineHasEffort(catalog *ModelCatalog, engine AgentEngine, effort AgentEffort) bool {
	return slices.ContainsFunc(ModelsFor(catalog, string(engine)), func(model *ModelSpec) bool {
		return slices.ContainsFunc(model.Efforts, func(spec *EffortSpec) bool { return spec.ID == effort })
	})
}

// ModelHasEffort takes an empty role as none.
func ModelHasEffort(catalog *ModelCatalog, engine AgentEngine, model AgentModel, effort AgentEffort, role CoordinationRole) bool {
	return slices.ContainsFunc(EffortsFor(catalog, engine, model, role), func(spec *EffortSpec) bool { return spec.ID == effort })
}

func ModelTakesEffort(catalog *ModelCatalog, engine AgentEngine, model AgentModel) bool {
	return len(EffortsFor(catalog, engine, model, "")) > 0
}

// EffortForModel takes an empty role as none.
func EffortForModel(catalog *ModelCatalog, engine AgentEngine, model AgentModel, effort AgentEffort, role CoordinationRole) AgentEffort {
	if !IsAgentEffort(effort) {
		return effort
	}
	offered := EffortsFor(catalog, engine, model, role)
	if len(offered) == 0 || slices.ContainsFunc(offered, func(spec *EffortSpec) bool { return spec.ID == effort }) {
		return effort
	}
	asked := slices.Index(AgentEfforts, effort)
	for _, spec := range offered {
		if slices.Index(AgentEfforts, spec.ID) >= asked {
			return spec.ID
		}
	}
	return offered[len(offered)-1].ID
}

// engineSpec is nil for an engine only a newer build knows.
func engineSpec(engine EngineID) *engineRecord {
	return engines[AgentEngine(engine)]
}

var noCapabilities = EngineCapabilities{Policies: []ExecutionPolicy{}, UnattendedPolicy: ExecutionPolicyConfirm}

func ModelsFor(catalog *ModelCatalog, engine EngineID) []*ModelSpec {
	if !IsAgentEngine(engine) {
		return []*ModelSpec{}
	}
	return catalog.Models.Get(AgentEngine(engine))
}

// EffortsFor takes an empty role as none.
func EffortsFor(catalog *ModelCatalog, engine AgentEngine, model AgentModel, role CoordinationRole) []*EffortSpec {
	entry := entryOf(catalog, engine, model)
	if entry == nil {
		return []*EffortSpec{}
	}
	if role == CoordinationRoleCoordinator {
		return entry.CoordinatorEfforts
	}
	return entry.Efforts
}

func DefaultModelFor(engine EngineID) (AgentModel, bool) {
	spec := engineSpec(engine)
	if spec == nil || spec.Models.Discovered {
		return "", false
	}
	return spec.Models.Default, true
}

func DefaultModelIn(catalog *ModelCatalog, engine AgentEngine) (AgentModel, bool) {
	fixed, hasFixed := DefaultModelFor(string(engine))
	if hasFixed && EngineHasModel(catalog, engine, fixed) {
		return fixed, true
	}
	if models := ModelsFor(catalog, string(engine)); len(models) > 0 {
		return models[0].ID, true
	}
	return fixed, hasFixed
}

// ThreadModel is the model a thread runs on; false while its engine has yet to name any.
func ThreadModel(catalog *ModelCatalog, thread *Thread) (AgentModel, bool) {
	if thread.Model != nil {
		return *thread.Model, true
	}
	return DefaultModelIn(catalog, thread.Engine)
}

func DefaultEffortFor(engine EngineID) AgentEffort {
	spec := engineSpec(engine)
	if spec == nil {
		spec = engines[DefaultEngine]
	}
	return spec.DefaultEffort
}

// CapabilitiesFor shares its Policies slice; never change it.
func CapabilitiesFor(engine EngineID) EngineCapabilities {
	if spec := engineSpec(engine); spec != nil {
		return spec.Capabilities
	}
	return noCapabilities
}

func PolicyFor(engine AgentEngine, policy ExecutionPolicy) ExecutionPolicy {
	offered := CapabilitiesFor(string(engine)).Policies
	if slices.Contains(offered, policy) {
		return policy
	}
	if asked := slices.Index(policyOrder, policy); asked >= 0 {
		for _, candidate := range policyOrder[asked:] {
			if slices.Contains(offered, candidate) {
				return candidate
			}
		}
	}
	return ExecutionPolicyConfirm
}

var agentEngineOfferedPolicies = ByEngine(func(engine AgentEngine) []ExecutionPolicy {
	offered := []ExecutionPolicy{}
	for _, policy := range PolicyChoices {
		if slices.Contains(engines[engine].Capabilities.Policies, policy) {
			offered = append(offered, policy)
		}
	}
	return offered
})

func AgentEngineOfferedPolicies(engine EngineID) []ExecutionPolicy {
	if !IsAgentEngine(engine) {
		return []ExecutionPolicy{}
	}
	return agentEngineOfferedPolicies[AgentEngine(engine)]
}

func EngineLabel(engine EngineID) string {
	if spec := engineSpec(engine); spec != nil {
		return spec.Label
	}
	return engine
}

func UnopenableEngine(engine EngineID) string {
	return "Runs on " + engine + ", which this version can't open. Update to work in it here."
}

// ModelLabel takes an empty engine as none named.
func ModelLabel(catalog *ModelCatalog, engine EngineID, model string) string {
	var entry *CatalogEntry
	if engine == "" {
		if entries := catalog.ByID[model]; len(entries) > 0 {
			entry = entries[0]
		}
	} else if IsAgentEngine(engine) {
		entry = entryOf(catalog, AgentEngine(engine), model)
	}
	if entry != nil {
		return entry.Spec.Label
	}
	return model
}

// ByEngine builds one value per engine.
func ByEngine[T any](build func(engine AgentEngine) T) map[AgentEngine]T {
	values := make(map[AgentEngine]T, len(AgentEngines))
	for _, engine := range AgentEngines {
		values[engine] = build(engine)
	}
	return values
}

func ContextWindowLimit(catalog *ModelCatalog, engine AgentEngine, model AgentModel) float64 {
	if offered := entryOf(catalog, engine, model); offered != nil {
		return offered.Spec.ContextWindow
	}
	defaultModel, ok := DefaultModelIn(catalog, engine)
	if !ok {
		return 0
	}
	if entry := entryOf(catalog, engine, defaultModel); entry != nil {
		return entry.Spec.ContextWindow
	}
	return 0
}

// catalogEntries is every entry in the order TypeScript's entries map was filled: engine by engine, each in list order.
func catalogEntries(catalog *ModelCatalog) []*CatalogEntry {
	entries := []*CatalogEntry{}
	for _, engine := range AgentEngines {
		for _, spec := range catalog.Models.Get(engine) {
			if entry := entryOf(catalog, engine, spec.ID); entry != nil {
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

// jsString is a template literal's reading of an optional string: "undefined" when absent.
func jsString(value *string) string {
	if value == nil {
		return "undefined"
	}
	return *value
}
