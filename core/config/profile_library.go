package config

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const libraryConfigurationID = "global-configuration"

type libraryObject map[string]json.RawMessage
type libraryRecord struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Profile     libraryObject     `json:"profile"`
	Source      json.RawMessage   `json:"source,omitempty"`
	Baseline    libraryObject     `json:"baseline,omitempty"`
	ResourceIDs map[string]string `json:"resourceIDs,omitempty"`
}
type libraryGroupSource struct {
	ProfileID      string   `json:"profileID"`
	GroupID        string   `json:"groupID"`
	FollowsMembers bool     `json:"followsMembers"`
	SourceMembers  []string `json:"sourceMembers"`
}
type profileLibrary struct {
	SchemaVersion    int                           `json:"schemaVersion"`
	Profiles         []libraryRecord               `json:"profiles"`
	ActiveProfileID  string                        `json:"activeProfileID"`
	EditingProfileID string                        `json:"editingProfileID"`
	Groups           []libraryObject               `json:"groups"`
	Scenarios        []libraryObject               `json:"scenarios"`
	ActiveScenarioID string                        `json:"activeScenarioID"`
	GroupSources     map[string]libraryGroupSource `json:"groupSources"`
	ScenarioSources  map[string]string             `json:"scenarioSources"`
	RuntimeMetadata  libraryObject                 `json:"runtimeMetadata,omitempty"`
	// Android pre-library identities used the base directory without a source
	// namespace. Keep that one encrypted-store namespace, never copy node keys.
	LegacyIdentitySourceID string `json:"legacyIdentitySourceID,omitempty"`
}

func libraryJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func libraryString(v libraryObject, key string) string {
	var s string
	json.Unmarshal(v[key], &s)
	return s
}
func libraryObjects(v libraryObject, key string) []libraryObject {
	var a []libraryObject
	json.Unmarshal(v[key], &a)
	return a
}
func libraryStrings(v libraryObject, key string) []string {
	var a []string
	json.Unmarshal(v[key], &a)
	return a
}
func libraryGroup(v libraryObject) bool {
	t := libraryString(v, "type")
	return t == string(LineTypeSelector) || t == string(LineTypeURLTest)
}
func copyLibraryObject(v libraryObject) libraryObject {
	r := libraryObject{}
	for k, b := range v {
		r[k] = append(json.RawMessage(nil), b...)
	}
	return r
}
func libraryProfile(raw string) (libraryObject, error) {
	var p Profile
	var doc libraryObject
	if len(raw) > 8<<20 || json.Unmarshal([]byte(raw), &p) != nil || json.Unmarshal([]byte(raw), &doc) != nil || doc == nil {
		return nil, fmt.Errorf("invalid configuration document")
	}
	if len(p.Subscriptions) != 0 {
		return nil, fmt.Errorf("nested subscriptions must be converted before import")
	}
	seenDirect := false
	for _, line := range p.Lines {
		if line.ID == "direct" {
			if seenDirect || line.Type != LineTypeDirect {
				return nil, fmt.Errorf("direct is a reserved line identity")
			}
			seenDirect = true
		}
	}
	return doc, nil
}
func libraryResources(p libraryObject) libraryObject {
	p = copyLibraryObject(p)
	var lines []libraryObject
	for _, l := range libraryObjects(p, "lines") {
		if !libraryGroup(l) {
			lines = append(lines, l)
		}
	}
	p["lines"] = libraryJSON(lines)
	p["scenarios"] = json.RawMessage("[]")
	p["active_scenario_id"] = json.RawMessage(`""`)
	delete(p, "subscriptions")
	return p
}
func libraryMetadata(p libraryObject) libraryObject {
	r := copyLibraryObject(p)
	for _, key := range []string{"profile_id", "lines", "rule_sets", "scenarios", "active_scenario_id", "subscriptions", "tailscale", "import_warnings", "import_adjustments", "library_revision"} {
		delete(r, key)
	}
	return r
}
func (l *profileLibrary) runtime() libraryObject {
	r := copyLibraryObject(l.RuntimeMetadata)
	r["profile_id"] = libraryJSON(libraryConfigurationID)
	r["library_revision"] = libraryJSON(libraryDigest(string(libraryJSON(l))))
	lines := []libraryObject{{"id": libraryJSON("direct"), "name": libraryJSON("直连"), "type": libraryJSON("direct"), "enabled": libraryJSON(true)}}
	var rules, warnings, adjustments []libraryObject
	for _, record := range l.Profiles {
		var tail libraryObject
		json.Unmarshal(record.Profile["tailscale"], &tail)
		for _, source := range libraryObjects(record.Profile, "lines") {
			if libraryString(source, "id") == "direct" {
				continue
			}
			line := copyLibraryObject(source)
			line["identity_profile_id"] = libraryJSON(record.ID)
			line["identity_hostname"] = libraryJSON(libraryString(tail, "hostname"))
			lines = append(lines, line)
		}
		rules = append(rules, libraryObjects(record.Profile, "rule_sets")...)
		warnings = append(warnings, libraryObjects(record.Profile, "import_warnings")...)
		adjustments = append(adjustments, libraryObjects(record.Profile, "import_adjustments")...)
	}
	lines = append(lines, l.Groups...)
	r["lines"] = libraryJSON(lines)
	r["rule_sets"] = libraryJSON(rules)
	r["scenarios"] = libraryJSON(l.Scenarios)
	r["active_scenario_id"] = libraryJSON(l.ActiveScenarioID)
	r["import_warnings"] = libraryJSON(warnings)
	r["import_adjustments"] = libraryJSON(adjustments)
	return r
}
func (l *profileLibrary) validate() error {
	if l.SchemaVersion != 3 || l.ActiveProfileID != libraryConfigurationID || len(l.Profiles) == 0 {
		return fmt.Errorf("unsupported configuration library")
	}
	seen := map[string]bool{}
	editor := false
	for _, r := range l.Profiles {
		if r.ID == "" || r.ID == libraryConfigurationID || seen[r.ID] || r.Profile == nil {
			return fmt.Errorf("invalid source identity")
		}
		seen[r.ID] = true
		editor = editor || r.ID == l.EditingProfileID
		p, err := libraryProfile(string(libraryJSON(r.Profile)))
		if err != nil {
			return err
		}
		if len(libraryObjects(p, "scenarios")) != 0 {
			return fmt.Errorf("source cannot own global scenarios")
		}
		for _, line := range libraryObjects(p, "lines") {
			if libraryGroup(line) {
				return fmt.Errorf("source cannot own global groups")
			}
		}
	}
	if !editor {
		return fmt.Errorf("selected source is missing")
	}
	if l.LegacyIdentitySourceID != "" && !seen[l.LegacyIdentitySourceID] {
		return fmt.Errorf("legacy identity owner is missing")
	}
	for _, g := range l.Groups {
		if !libraryGroup(g) {
			return fmt.Errorf("invalid global group")
		}
		members := map[string]bool{}
		for _, id := range libraryStrings(g, "group_members") {
			if members[id] {
				return fmt.Errorf("duplicate group member")
			}
			members[id] = true
		}
	}
	var profile Profile
	if json.Unmarshal(libraryJSON(l.runtime()), &profile) != nil {
		return fmt.Errorf("invalid runtime projection")
	}
	if err := validateDocumentProfileWithEmptyGroups(&profile, true); err != nil {
		return err
	}
	for _, record := range l.Profiles {
		if len(record.Source) == 0 || string(record.Source) == "null" {
			continue
		}
		// Preserve old sources even if their format is not supported by Android yet.
		var source struct {
			FetchLineID string `json:"fetchLineID"`
		}
		if json.Unmarshal(record.Source, &source) != nil {
			return fmt.Errorf("invalid source metadata")
		}
		if source.FetchLineID != "" && source.FetchLineID != "direct" && profile.FindLine(source.FetchLineID) == nil {
			return fmt.Errorf("source acquisition Line is missing")
		}
	}
	active := l.ActiveScenarioID == "" && len(l.Scenarios) == 0
	ssids := map[string]bool{}
	for _, s := range profile.Scenarios {
		active = active || s.ID == l.ActiveScenarioID
		for _, ssid := range s.MatchSSIDs {
			if ssids[ssid] {
				return fmt.Errorf("SSID belongs to more than one scenario")
			}
			ssids[ssid] = true
		}
	}
	if !active {
		return fmt.Errorf("active scenario is missing")
	}
	return nil
}
func decodeLibrary(raw string) (*profileLibrary, error) {
	var l profileLibrary
	if len(raw) > 16<<20 || json.Unmarshal([]byte(raw), &l) != nil {
		return nil, fmt.Errorf("invalid configuration library")
	}
	if err := l.validate(); err != nil {
		return nil, err
	}
	if l.GroupSources == nil {
		l.GroupSources = map[string]libraryGroupSource{}
	}
	if l.ScenarioSources == nil {
		l.ScenarioSources = map[string]string{}
	}
	return &l, nil
}
func encodeLibrary(l *profileLibrary) (string, error) {
	if err := l.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(l)
	return string(data), err
}

// NewProfileLibrary migrates a single Android runtime document. The host writes
// a new encrypted file and leaves the old file untouched. Unknown object fields
// are retained; the runtime remains a projection, never another stored copy.
func NewProfileLibrary(profileJSON string) (string, error) {
	hasLegacy := strings.TrimSpace(profileJSON) != ""
	if strings.TrimSpace(profileJSON) == "" {
		profileJSON = `{"lines":[{"id":"direct","type":"direct","enabled":true}],"scenarios":[{"id":"default","name":"默认场景","default_line_id":"direct"}],"active_scenario_id":"default"}`
	}
	p, err := libraryProfile(profileJSON)
	if err != nil {
		return "", err
	}
	id := libraryString(p, "profile_id")
	legacyIdentity := id == "" && hasLegacy
	if id == "" || id == libraryConfigurationID {
		id = "default"
	}
	p["profile_id"] = libraryJSON(id)
	l := &profileLibrary{SchemaVersion: 3, ActiveProfileID: libraryConfigurationID, EditingProfileID: id, ActiveScenarioID: libraryString(p, "active_scenario_id"), GroupSources: map[string]libraryGroupSource{}, ScenarioSources: map[string]string{}, RuntimeMetadata: libraryMetadata(p)}
	if legacyIdentity {
		l.LegacyIdentitySourceID = id
	}
	r := libraryRecord{ID: id, Name: "本机配置", Profile: p}
	l.addTemplates(r, false)
	r.Profile = libraryResources(p)
	l.Profiles = []libraryRecord{r}
	return encodeLibrary(l)
}
func ProfileLibraryRuntime(raw string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	return string(libraryJSON(l.runtime())), nil
}

// ApplyProfileLibraryRuntime distributes an editor/controller result back to its
// existing owners. New resources belong to the selected source; scene/group
// edits remain global. Protocol secrets stay in the host's encrypted payload.
func ApplyProfileLibraryRuntime(raw, draftJSON string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	draft, err := libraryProfile(draftJSON)
	if err != nil {
		return "", err
	}
	if libraryString(draft, "library_revision") != libraryDigest(string(libraryJSON(l))) {
		return "", fmt.Errorf("configuration changed; reload before editing")
	}
	lineOwners, ruleOwners := map[string]string{}, map[string]string{}
	for _, r := range l.Profiles {
		for _, x := range libraryObjects(r.Profile, "lines") {
			lineOwners[libraryString(x, "id")] = r.ID
		}
		for _, x := range libraryObjects(r.Profile, "rule_sets") {
			ruleOwners[libraryString(x, "id")] = r.ID
		}
	}
	l.Groups = nil
	for i := range l.Profiles {
		r := &l.Profiles[i]
		var lines, rules []libraryObject
		for _, x := range libraryObjects(draft, "lines") {
			if libraryGroup(x) {
				continue
			}
			id := libraryString(x, "id")
			owner := lineOwners[id]
			if owner == "" {
				owner = l.EditingProfileID
			}
			if owner != r.ID && id != "direct" {
				continue
			}
			x = copyLibraryObject(x)
			delete(x, "identity_profile_id")
			delete(x, "identity_hostname")
			lines = append(lines, x)
		}
		for _, x := range libraryObjects(draft, "rule_sets") {
			owner := ruleOwners[libraryString(x, "id")]
			if owner == "" {
				owner = l.EditingProfileID
			}
			if owner == r.ID {
				rules = append(rules, x)
			}
		}
		r.Profile["lines"] = libraryJSON(lines)
		r.Profile["rule_sets"] = libraryJSON(rules)
		if r.ID == l.EditingProfileID {
			var tail libraryObject
			if json.Unmarshal(draft["tailscale"], &tail) == nil && libraryString(tail, "hostname") != "" {
				r.Profile["tailscale"] = draft["tailscale"]
			}
		}
	}
	for _, x := range libraryObjects(draft, "lines") {
		if libraryGroup(x) {
			l.Groups = append(l.Groups, x)
		}
	}
	l.Scenarios = libraryObjects(draft, "scenarios")
	l.ActiveScenarioID = libraryString(draft, "active_scenario_id")
	l.RuntimeMetadata = libraryMetadata(draft)
	groups, scenes := map[string]bool{}, map[string]bool{}
	for _, g := range l.Groups {
		groups[libraryString(g, "id")] = true
	}
	for _, s := range l.Scenarios {
		scenes[libraryString(s, "id")] = true
	}
	for id := range l.GroupSources {
		if !groups[id] {
			delete(l.GroupSources, id)
		}
	}
	for id := range l.ScenarioSources {
		if !scenes[id] {
			delete(l.ScenarioSources, id)
		}
	}
	return encodeLibrary(l)
}

// ApplyProfileLibrarySelection permits a long-lived scenario controller to
// persist only its choice and automatic preference. Its resources must still
// match exactly; another source import cannot be overwritten by an old survey.
func ApplyProfileLibrarySelection(raw, draftJSON string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	draft, err := libraryProfile(draftJSON)
	if err != nil {
		return "", err
	}
	current := l.runtime()
	left, right := copyLibraryObject(current), copyLibraryObject(draft)
	for _, key := range []string{"library_revision", "active_scenario_id", "mobile_automatic"} {
		delete(left, key)
		delete(right, key)
	}
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(libraryJSON(left)))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(libraryJSON(right)))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil || !reflect.DeepEqual(leftValue, rightValue) {
		return "", fmt.Errorf("configuration resources changed; reload controller")
	}
	draft["library_revision"] = current["library_revision"]
	return ApplyProfileLibraryRuntime(raw, string(libraryJSON(draft)))
}

func (l *profileLibrary) addTemplates(r libraryRecord, rename bool) {
	for _, g := range libraryObjects(r.Profile, "lines") {
		if libraryGroup(g) {
			l.Groups = append(l.Groups, g)
			id := libraryString(g, "id")
			l.GroupSources[id] = libraryGroupSource{ProfileID: r.ID, GroupID: id, FollowsMembers: len(r.Source) > 0 && string(r.Source) != "null", SourceMembers: libraryStrings(g, "group_members")}
		}
	}
	scenes := libraryObjects(r.Profile, "scenarios")
	for _, s := range scenes {
		if rename {
			s = copyLibraryObject(s)
			name := r.Name
			if len(scenes) > 1 {
				name += " · " + libraryString(s, "name")
			}
			s["name"] = libraryJSON(name)
		}
		l.Scenarios = append(l.Scenarios, s)
		l.ScenarioSources[libraryString(s, "id")] = r.ID
	}
}
func libraryDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func NewProfileSourceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type libraryPreview struct {
	BaseDigest string `json:"base_digest"`
	Library    string `json:"library"`
	SourceID   string `json:"source_id"`
	Name       string `json:"name"`
	Lines      int    `json:"lines"`
	Rules      int    `json:"rules"`
	Scenarios  int    `json:"scenarios"`
	Updating   bool   `json:"updating"`
	Warnings   int    `json:"warnings"`
}

// PreviewProfileLibraryImport performs no downloads or writes. Imported IDs
// must have been scoped by ImportProfileDocument to sourceID first.
func PreviewProfileLibraryImport(raw, importedJSON, name, sourceID string, updating bool) (string, error) {
	return previewProfileLibraryImport(raw, importedJSON, name, sourceID, updating, nil)
}
func previewProfileLibraryImport(raw, importedJSON, name, sourceID string, updating bool, remote *ProfileRemoteSource) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	p, err := libraryProfile(importedJSON)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || sourceID == "" || sourceID == libraryConfigurationID {
		return "", fmt.Errorf("source name and identity required")
	}
	p["profile_id"] = libraryJSON(sourceID)
	index := -1
	for i, r := range l.Profiles {
		if r.ID == sourceID {
			index = i
		}
	}
	if updating {
		if index < 0 {
			return "", fmt.Errorf("source no longer exists")
		}
		if remote == nil && len(l.Profiles[index].Source) > 0 && string(l.Profiles[index].Source) != "null" {
			return "", fmt.Errorf("remote source must be refreshed from its recorded address")
		}
		if remote != nil {
			previous, err := profileRemoteSource(l.Profiles[index].Source)
			if err != nil || previous.URL != remote.URL || previous.FetchLineID != remote.FetchLineID {
				return "", fmt.Errorf("remote source identity changed")
			}
		}
		if err = l.refreshSource(index, p); err != nil {
			return "", err
		}
	} else {
		if index >= 0 {
			return "", fmt.Errorf("source already exists")
		}
		r := libraryRecord{ID: sourceID, Name: name, Profile: p, Baseline: p}
		if remote != nil {
			r.Source = libraryJSON(remote)
		}
		l.addTemplates(r, true)
		r.Profile = libraryResources(p)
		l.Profiles = append(l.Profiles, r)
	}
	l.EditingProfileID = sourceID
	if l.ActiveScenarioID == "" && len(l.Scenarios) > 0 {
		l.ActiveScenarioID = libraryString(l.Scenarios[0], "id")
	}
	candidate, err := encodeLibrary(l)
	if err != nil {
		return "", err
	}
	preview := libraryPreview{BaseDigest: libraryDigest(raw), Library: candidate, SourceID: sourceID, Name: name, Lines: len(libraryObjects(p, "lines")), Rules: len(libraryObjects(p, "rule_sets")), Scenarios: len(libraryObjects(p, "scenarios")), Updating: updating, Warnings: len(libraryObjects(p, "import_warnings"))}
	return string(libraryJSON(preview)), nil
}
func CommitProfileLibraryPreview(raw, previewJSON string) (string, error) {
	var preview libraryPreview
	if len(previewJSON) > 24<<20 || json.Unmarshal([]byte(previewJSON), &preview) != nil || preview.BaseDigest != libraryDigest(raw) {
		return "", fmt.Errorf("configuration changed; preview again")
	}
	l, err := decodeLibrary(preview.Library)
	if err != nil {
		return "", err
	}
	return encodeLibrary(l)
}

func (l *profileLibrary) refreshSource(index int, incoming libraryObject) error {
	r := &l.Profiles[index]
	incoming = remapLibraryProfile(incoming, r.ResourceIDs)
	previous := r.Baseline
	if previous == nil {
		previous = r.Profile
	}
	knownWarnings := map[string]bool{}
	for _, w := range libraryObjects(previous, "import_warnings") {
		knownWarnings[string(libraryJSON(w))] = true
	}
	for _, w := range libraryObjects(incoming, "import_warnings") {
		if !knownWarnings[string(libraryJSON(w))] {
			return fmt.Errorf("source introduces unsupported rules")
		}
	}
	resources := libraryResources(incoming)
	resources["tailscale"] = r.Profile["tailscale"]
	for _, key := range []string{"lines", "rule_sets"} {
		owned := map[string]bool{}
		for _, v := range libraryObjects(previous, key) {
			owned[libraryString(v, "id")] = true
		}
		values := libraryObjects(resources, key)
		// File sources have user-owned credential edits. Refresh must not replace
		// them, nor carry them to a changed server without explicit user review.
		if key == "lines" && (len(r.Source) == 0 || string(r.Source) == "null") {
			for i, next := range values {
				var current, baseline libraryObject
				for _, item := range libraryObjects(r.Profile, "lines") {
					if libraryString(item, "id") == libraryString(next, "id") {
						current = item
					}
				}
				for _, item := range libraryObjects(previous, "lines") {
					if libraryString(item, "id") == libraryString(next, "id") {
						baseline = item
					}
				}
				if current == nil || baseline == nil {
					continue
				}
				for _, field := range []string{"vpn_username", "vpn_password", "trojan_password", "ss_password", "vmess_uuid", "vless_uuid", "anytls_password"} {
					if libraryString(current, field) == libraryString(baseline, field) {
						continue
					}
					for _, endpoint := range []string{"type", "vpn_server", "trojan_server", "ss_server", "vmess_server", "vless_server", "anytls_server"} {
						if libraryString(current, endpoint) != libraryString(next, endpoint) {
							return fmt.Errorf("source changed a server with local credentials")
						}
					}
					if value, ok := current[field]; ok {
						next[field] = value
					} else {
						delete(next, field)
					}
				}
				values[i] = next
			}
		}
		for _, v := range libraryObjects(r.Profile, key) {
			if !owned[libraryString(v, "id")] {
				values = append(values, v)
			}
		}
		ranks := map[string]int{}
		for i, v := range libraryObjects(r.Profile, key) {
			ranks[libraryString(v, "id")] = i
		}
		sort.SliceStable(values, func(i, j int) bool {
			a, okA := ranks[libraryString(values[i], "id")]
			b, okB := ranks[libraryString(values[j], "id")]
			if !okA {
				return false
			}
			return !okB || a < b
		})
		resources[key] = libraryJSON(values)
	}
	for i, g := range l.Groups {
		source, ok := l.GroupSources[libraryString(g, "id")]
		if !ok || source.ProfileID != r.ID || !source.FollowsMembers {
			continue
		}
		var next libraryObject
		for _, v := range libraryObjects(incoming, "lines") {
			if libraryString(v, "id") == source.GroupID && libraryGroup(v) {
				next = v
			}
		}
		if next == nil {
			return fmt.Errorf("source removed a followed group")
		}
		members := libraryStrings(g, "group_members")
		if libraryString(g, "type") == "selector" && libraryString(g, "group_default") == "" && len(members) > 0 {
			g["group_default"] = libraryJSON(members[0])
		}
		owned := map[string]bool{}
		for _, id := range source.SourceMembers {
			owned[id] = true
		}
		nextMembers := libraryStrings(next, "group_members")
		source.SourceMembers = append([]string(nil), nextMembers...)
		seen := map[string]bool{}
		for _, id := range nextMembers {
			seen[id] = true
		}
		for _, id := range members {
			if !owned[id] && !seen[id] {
				nextMembers = append(nextMembers, id)
				seen[id] = true
			}
		}
		g["group_members"] = libraryJSON(nextMembers)
		l.Groups[i] = g
		l.GroupSources[libraryString(g, "id")] = source
	}
	r.Profile = resources
	r.Baseline = incoming
	return nil
}

func remapLibraryProfile(original libraryObject, ids map[string]string) libraryObject {
	if len(ids) == 0 {
		return original
	}
	p := copyLibraryObject(original)
	id := func(s string) string {
		if mapped, ok := ids[s]; ok {
			return mapped
		}
		return s
	}
	mapKey := func(o libraryObject, k string) {
		if _, ok := o[k]; ok {
			o[k] = libraryJSON(id(libraryString(o, k)))
		}
	}
	mapList := func(o libraryObject, k string) {
		if _, ok := o[k]; ok {
			a := libraryStrings(o, k)
			for i, s := range a {
				a[i] = id(s)
			}
			o[k] = libraryJSON(a)
		}
	}
	lines := libraryObjects(p, "lines")
	for _, line := range lines {
		mapKey(line, "id")
		mapKey(line, "group_default")
		mapList(line, "group_members")
	}
	p["lines"] = libraryJSON(lines)
	var remapRules func([]libraryObject)
	remapRules = func(rules []libraryObject) {
		for _, rule := range rules {
			mapKey(rule, "id")
			mapKey(rule, "fetch_line_id")
			if _, ok := rule["conditions"]; ok {
				conditions := libraryObjects(rule, "conditions")
				remapRules(conditions)
				rule["conditions"] = libraryJSON(conditions)
			}
		}
	}
	rules := libraryObjects(p, "rule_sets")
	remapRules(rules)
	p["rule_sets"] = libraryJSON(rules)
	scenes := libraryObjects(p, "scenarios")
	for _, scene := range scenes {
		mapKey(scene, "id")
		mapKey(scene, "default_line_id")
		mapList(scene, "match_order")
		bindings := libraryObjects(scene, "bindings")
		for _, binding := range bindings {
			mapKey(binding, "line_id")
			mapKey(binding, "rule_set_id")
			mapList(binding, "condition_ids")
		}
		scene["bindings"] = libraryJSON(bindings)
	}
	p["scenarios"] = libraryJSON(scenes)
	mapKey(p, "active_scenario_id")
	return p
}

func ProfileLibrarySummary(raw string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	var sources []map[string]any
	for _, r := range l.Profiles {
		sources = append(sources, map[string]any{"id": r.ID, "name": r.Name, "lines": len(libraryObjects(r.Profile, "lines")), "rules": len(libraryObjects(r.Profile, "rule_sets")), "remote": len(r.Source) > 0 && string(r.Source) != "null"})
	}
	return string(libraryJSON(map[string]any{"sources": sources, "editing_id": l.EditingProfileID, "scenarios": len(l.Scenarios)})), nil
}
func SelectProfileLibrarySource(raw, id string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	l.EditingProfileID = id
	return encodeLibrary(l)
}

// Removal never cascades into global scenes or groups. Any remaining resource
// dependency rejects the entire operation; at least one source must remain.
func RemoveProfileLibrarySource(raw, id string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	if len(l.Profiles) < 2 {
		return "", fmt.Errorf("at least one source is required")
	}
	found := false
	for i, r := range l.Profiles {
		if r.ID == id {
			l.Profiles = append(l.Profiles[:i], l.Profiles[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("source no longer exists")
	}
	for key, s := range l.GroupSources {
		if s.ProfileID == id {
			if s.FollowsMembers {
				return "", fmt.Errorf("a group is still following this source")
			}
			delete(l.GroupSources, key)
		}
	}
	for key, owner := range l.ScenarioSources {
		if owner == id {
			delete(l.ScenarioSources, key)
		}
	}
	if l.EditingProfileID == id {
		l.EditingProfileID = l.Profiles[0].ID
	}
	if l.LegacyIdentitySourceID == id {
		l.LegacyIdentitySourceID = ""
	}
	return encodeLibrary(l)
}

// ResolveProfileLibraryIdentityNamespace is only for encrypted platform stores.
// The alias is exact and recorded by migration; unrelated/new sources never
// fall back to another source's node key when their record is missing.
func ResolveProfileLibraryIdentityNamespace(raw, basePath, namespace string) (string, error) {
	l, err := decodeLibrary(raw)
	if err != nil {
		return "", err
	}
	if l.LegacyIdentitySourceID == "" {
		return namespace, nil
	}
	path, err := ProfileDataPath(&Profile{ID: l.LegacyIdentitySourceID}, basePath)
	if err != nil {
		return "", err
	}
	if filepath.Clean(namespace) == TailscaleStateDirectory(path) {
		return TailscaleStateDirectory(basePath), nil
	}
	return namespace, nil
}

// ValidateProfileLibraryRuntime validates an offline editor projection while
// allowing the same temporarily empty global groups as the library itself.
func ValidateProfileLibraryRuntime(profile *Profile) error {
	return validateDocumentProfileWithEmptyGroups(profile, true)
}
