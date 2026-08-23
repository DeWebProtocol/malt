package backup

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dewebprotocol/malt-client/internal/bucketbranch"
	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
)

const planStoreVersion = 1

// Plan is one complete backup and restore history on a writable Bucket branch.
// Bindings within a Plan are published together and restored together.
type Plan struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	BucketID   string        `json:"bucket_id"`
	BucketName string        `json:"bucket_name,omitempty"`
	Branch     string        `json:"branch"`
	Bindings   []Binding     `json:"bindings"`
	Every      time.Duration `json:"every,omitempty"`
	Enabled    bool          `json:"enabled"`
	Message    string        `json:"message,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

// Binding maps one local directory to one user-visible path name and one
// opaque authenticated token in a Plan. The token is derived at snapshot time
// and never persisted as authority in this local configuration.
type Binding struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	PathName  string    `json:"path_name"`
	CreatedAt time.Time `json:"created_at"`
}

// UnmarshalJSON accepts the pre-release archive_name field so existing local
// Plan configuration can cross the data-model cutover without implying that
// new snapshots are archives.
func (b *Binding) UnmarshalJSON(data []byte) error {
	type bindingWire struct {
		ID                string    `json:"id"`
		Name              string    `json:"name"`
		Source            string    `json:"source"`
		PathName          string    `json:"path_name"`
		LegacyArchiveName string    `json:"archive_name"`
		CreatedAt         time.Time `json:"created_at"`
	}
	var wire bindingWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.PathName != "" && wire.LegacyArchiveName != "" && wire.PathName != wire.LegacyArchiveName {
		return fmt.Errorf("backup binding path_name conflicts with legacy archive_name")
	}
	if wire.PathName == "" {
		wire.PathName = wire.LegacyArchiveName
	}
	*b = Binding{ID: wire.ID, Name: wire.Name, Source: wire.Source, PathName: wire.PathName, CreatedAt: wire.CreatedAt}
	return nil
}

type planFile struct {
	Version int             `json:"version"`
	Plans   map[string]Plan `json:"plans"`
}

type PlanStore struct {
	path string
}

func OpenPlanStore(path string) (*PlanStore, error) {
	if path == "" {
		return nil, fmt.Errorf("backup plan store path is empty")
	}
	store := &PlanStore{path: path}
	if _, err := store.List(); err != nil {
		return nil, err
	}
	return store, nil
}

func NewRestorePlan(bucketID, bucketName, branch string) (Plan, error) {
	bucketID = strings.TrimSpace(bucketID)
	bucketName = strings.TrimSpace(bucketName)
	branch, err := normalizePlanBranch(branch)
	if err != nil {
		return Plan{}, err
	}
	if bucketID == "" {
		return Plan{}, fmt.Errorf("restore Bucket ID is empty")
	}
	id, err := randomPlanID("restore")
	if err != nil {
		return Plan{}, err
	}
	name := bucketName
	if name == "" {
		name = bucketID
	}
	now := time.Now().UTC()
	return Plan{
		ID: id, Name: name, BucketID: bucketID, BucketName: bucketName,
		Branch: branch, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *PlanStore) List() ([]Plan, error) {
	var result []Plan
	err := s.withFile(false, func(value *planFile) error {
		result = make([]Plan, 0, len(value.Plans))
		for _, plan := range value.Plans {
			result = append(result, clonePlan(plan))
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].BucketName != result[j].BucketName {
				return result[i].BucketName < result[j].BucketName
			}
			if result[i].Branch != result[j].Branch {
				return result[i].Branch < result[j].Branch
			}
			return result[i].Name < result[j].Name
		})
		return nil
	})
	return result, err
}

func (s *PlanStore) Get(idOrName string) (Plan, error) {
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return Plan{}, fmt.Errorf("backup plan selector is empty")
	}
	var result Plan
	found := false
	err := s.withFile(false, func(value *planFile) error {
		if plan, ok := value.Plans[idOrName]; ok {
			result, found = clonePlan(plan), true
			return nil
		}
		for _, plan := range value.Plans {
			if plan.Name != idOrName {
				continue
			}
			if found {
				return fmt.Errorf("backup plan name %q is ambiguous; use its ID", idOrName)
			}
			result, found = clonePlan(plan), true
		}
		return nil
	})
	if err != nil {
		return Plan{}, err
	}
	if !found {
		return Plan{}, fmt.Errorf("backup plan %q was not found", idOrName)
	}
	return result, nil
}

func (s *PlanStore) FindTarget(bucketID, branch string) (Plan, bool, error) {
	bucketID = strings.TrimSpace(bucketID)
	branch, err := normalizePlanBranch(branch)
	if err != nil {
		return Plan{}, false, err
	}
	var result Plan
	found := false
	err = s.withFile(false, func(value *planFile) error {
		for _, plan := range value.Plans {
			if plan.BucketID != bucketID || plan.Branch != branch {
				continue
			}
			if found {
				return fmt.Errorf("multiple backup plans target Bucket %s branch %s", bucketID, branch)
			}
			result, found = clonePlan(plan), true
		}
		return nil
	})
	return result, found, err
}

type BindRequest struct {
	PlanName    string
	BucketID    string
	BucketName  string
	Branch      string
	BindingName string
	Source      string
	PathName    string
	Merge       bool
}

func (s *PlanStore) Bind(request BindRequest) (Plan, Binding, error) {
	request.PlanName = strings.TrimSpace(request.PlanName)
	request.BucketID = strings.TrimSpace(request.BucketID)
	request.BucketName = strings.TrimSpace(request.BucketName)
	request.BindingName = strings.TrimSpace(request.BindingName)
	branch, err := normalizePlanBranch(request.Branch)
	if err != nil {
		return Plan{}, Binding{}, err
	}
	if request.BucketID == "" {
		return Plan{}, Binding{}, fmt.Errorf("backup binding Bucket ID is empty")
	}
	if request.Source == "" {
		return Plan{}, Binding{}, fmt.Errorf("backup binding source is empty")
	}
	source, err := filepath.Abs(request.Source)
	if err != nil {
		return Plan{}, Binding{}, fmt.Errorf("resolve backup binding source: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return Plan{}, Binding{}, fmt.Errorf("stat backup binding source: %w", err)
	}
	if !info.IsDir() {
		return Plan{}, Binding{}, fmt.Errorf("backup binding source must be a directory")
	}
	if request.BindingName == "" {
		request.BindingName = filepath.Base(source)
	}
	if request.PathName == "" {
		request.PathName = request.BindingName
	}
	if err := validateDisplayName(request.BindingName, "binding"); err != nil {
		return Plan{}, Binding{}, err
	}
	if err := validatePathName(request.PathName); err != nil {
		return Plan{}, Binding{}, err
	}
	now := time.Now().UTC()
	var result Plan
	var binding Binding
	err = s.withFile(true, func(value *planFile) error {
		var plan Plan
		found := false
		for _, candidate := range value.Plans {
			if candidate.BucketID == request.BucketID && candidate.Branch == branch {
				plan, found = candidate, true
				break
			}
		}
		for _, existingPlan := range value.Plans {
			for _, existing := range existingPlan.Bindings {
				overlap, err := bindingSourcesOverlap(existing.Source, source)
				if err != nil {
					return err
				}
				if overlap {
					return fmt.Errorf(
						"local directory %s overlaps binding %s in plan %s at %s; binding sources must be globally disjoint",
						source, existing.Name, existingPlan.Name, existing.Source,
					)
				}
			}
		}
		if found && len(plan.Bindings) > 0 && !request.Merge {
			return fmt.Errorf("Bucket %s branch %s already has bindings; choose another branch or explicitly merge", request.BucketID, branch)
		}
		if !found {
			id, err := randomPlanID("plan")
			if err != nil {
				return err
			}
			name := request.PlanName
			if name == "" {
				name = request.BucketName
				if branch != "main" {
					name += "/" + branch
				}
			}
			if err := validateDisplayName(name, "plan"); err != nil {
				return err
			}
			plan = Plan{
				ID: id, Name: name, BucketID: request.BucketID, BucketName: request.BucketName,
				Branch: branch, Enabled: true, CreatedAt: now,
			}
		}
		for _, existing := range plan.Bindings {
			if existing.Name == request.BindingName {
				return fmt.Errorf("backup binding name %q is already used in this plan", request.BindingName)
			}
			if existing.PathName == request.PathName {
				return fmt.Errorf("backup path name %q is already used in this plan", request.PathName)
			}
		}
		bindingID, err := randomPlanID("binding")
		if err != nil {
			return err
		}
		binding = Binding{
			ID: bindingID, Name: request.BindingName, Source: source,
			PathName: request.PathName, CreatedAt: now,
		}
		plan.Bindings = append(plan.Bindings, binding)
		sort.Slice(plan.Bindings, func(i, j int) bool { return plan.Bindings[i].Name < plan.Bindings[j].Name })
		plan.UpdatedAt = now
		value.Plans[plan.ID] = plan
		result = clonePlan(plan)
		return nil
	})
	return result, binding, err
}

func (s *PlanStore) SetSchedule(selector string, every time.Duration, enabled bool, message string) (Plan, error) {
	if every <= 0 {
		return Plan{}, fmt.Errorf("backup schedule interval must be positive")
	}
	var result Plan
	err := s.withFile(true, func(value *planFile) error {
		id, plan, err := selectPlan(value.Plans, selector)
		if err != nil {
			return err
		}
		plan.Every = every
		plan.Enabled = enabled
		plan.Message = strings.TrimSpace(message)
		plan.UpdatedAt = time.Now().UTC()
		value.Plans[id] = plan
		result = clonePlan(plan)
		return nil
	})
	return result, err
}

// ImportRestored registers a Plan reconstructed from an authenticated,
// decrypted branch manifest. Its bindings must already exist below the chosen
// whole-plan restore destination.
func (s *PlanStore) ImportRestored(plan Plan) (Plan, error) {
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	if len(plan.Bindings) == 0 {
		return Plan{}, fmt.Errorf("restored backup plan has no bindings")
	}
	var result Plan
	err := s.withFile(true, func(value *planFile) error {
		if _, ok := value.Plans[plan.ID]; ok {
			return fmt.Errorf("backup plan %s already exists", plan.ID)
		}
		for _, existingPlan := range value.Plans {
			if existingPlan.BucketID == plan.BucketID && existingPlan.Branch == plan.Branch {
				return fmt.Errorf("Bucket %s branch %s already has local plan %s", plan.BucketID, plan.Branch, existingPlan.Name)
			}
			for _, existing := range existingPlan.Bindings {
				for _, binding := range plan.Bindings {
					overlap, err := bindingSourcesOverlap(existing.Source, binding.Source)
					if err != nil {
						return err
					}
					if overlap {
						return fmt.Errorf(
							"restored binding %s overlaps binding %s in plan %s",
							binding.Source, existing.Source, existingPlan.Name,
						)
					}
				}
			}
		}
		value.Plans[plan.ID] = clonePlan(plan)
		result = clonePlan(plan)
		return nil
	})
	return result, err
}

func (s *PlanStore) ClearSchedule(selector string) (Plan, error) {
	var result Plan
	err := s.withFile(true, func(value *planFile) error {
		id, plan, err := selectPlan(value.Plans, selector)
		if err != nil {
			return err
		}
		plan.Every = 0
		plan.Enabled = false
		plan.Message = ""
		plan.UpdatedAt = time.Now().UTC()
		value.Plans[id] = plan
		result = clonePlan(plan)
		return nil
	})
	return result, err
}

func (s *PlanStore) withFile(write bool, operation func(*planFile) error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	unlock, err := filelock.Acquire(s.path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock backup plans: %w", err)
	}
	defer func() { _ = unlock() }()
	value, err := s.load()
	if err != nil {
		return err
	}
	if err := operation(&value); err != nil {
		return err
	}
	if !write {
		return nil
	}
	return s.write(value)
}

func (s *PlanStore) load() (planFile, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return planFile{Version: planStoreVersion, Plans: map[string]Plan{}}, nil
	}
	if err != nil {
		return planFile{}, fmt.Errorf("read backup plans: %w", err)
	}
	if err := securefile.Secure(s.path); err != nil {
		return planFile{}, fmt.Errorf("secure backup plans: %w", err)
	}
	var value planFile
	if err := json.Unmarshal(data, &value); err != nil {
		return planFile{}, fmt.Errorf("decode backup plans: %w", err)
	}
	if value.Version != planStoreVersion {
		return planFile{}, fmt.Errorf("unsupported backup plan version %d", value.Version)
	}
	if value.Plans == nil {
		value.Plans = map[string]Plan{}
	}
	for id, plan := range value.Plans {
		if id != plan.ID {
			return planFile{}, fmt.Errorf("backup plan key does not match record")
		}
		if err := validatePlan(plan); err != nil {
			return planFile{}, fmt.Errorf("backup plan %s: %w", id, err)
		}
	}
	plans := make([]Plan, 0, len(value.Plans))
	targets := make(map[string]string, len(value.Plans))
	for _, plan := range value.Plans {
		target := plan.BucketID + "\x00" + plan.Branch
		if existing := targets[target]; existing != "" {
			return planFile{}, fmt.Errorf(
				"backup plans %s and %s target the same Bucket %s branch %s",
				existing, plan.ID, plan.BucketID, plan.Branch,
			)
		}
		targets[target] = plan.ID
		plans = append(plans, plan)
	}
	for i := range plans {
		for _, left := range plans[i].Bindings {
			for j := i + 1; j < len(plans); j++ {
				for _, right := range plans[j].Bindings {
					overlap, err := bindingSourcesOverlap(left.Source, right.Source)
					if err != nil {
						return planFile{}, err
					}
					if overlap {
						return planFile{}, fmt.Errorf(
							"backup plans %s and %s have overlapping binding sources %q and %q",
							plans[i].ID, plans[j].ID, left.Source, right.Source,
						)
					}
				}
			}
		}
	}
	return value, nil
}

func (s *PlanStore) write(value planFile) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".backup-plans-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	if err := securefile.Secure(s.path); err != nil {
		return err
	}
	return durablefile.SyncParent(s.path)
}

func validatePlan(plan Plan) error {
	if plan.ID == "" || plan.BucketID == "" || plan.Name == "" || plan.Branch == "" || plan.CreatedAt.IsZero() {
		return fmt.Errorf("record is incomplete")
	}
	if err := validateOpaqueID(plan.ID, "plan"); err != nil {
		return err
	}
	if _, err := normalizePlanBranch(plan.Branch); err != nil {
		return err
	}
	seenNames := map[string]struct{}{}
	seenSources := map[string]struct{}{}
	seenPathNames := map[string]struct{}{}
	for _, binding := range plan.Bindings {
		if binding.ID == "" || binding.Name == "" || binding.Source == "" || binding.PathName == "" || binding.CreatedAt.IsZero() {
			return fmt.Errorf("binding is incomplete")
		}
		if err := validateOpaqueID(binding.ID, "binding"); err != nil {
			return err
		}
		if _, ok := seenNames[binding.Name]; ok {
			return fmt.Errorf("duplicate binding name %q", binding.Name)
		}
		if _, ok := seenSources[binding.Source]; ok {
			return fmt.Errorf("duplicate binding source %q", binding.Source)
		}
		if _, ok := seenPathNames[binding.PathName]; ok {
			return fmt.Errorf("duplicate backup path name %q", binding.PathName)
		}
		seenNames[binding.Name] = struct{}{}
		seenSources[binding.Source] = struct{}{}
		seenPathNames[binding.PathName] = struct{}{}
	}
	for i := range plan.Bindings {
		for j := i + 1; j < len(plan.Bindings); j++ {
			leftInsideRight, err := resolvedPathWithin(plan.Bindings[j].Source, plan.Bindings[i].Source)
			if err != nil {
				return err
			}
			rightInsideLeft, err := resolvedPathWithin(plan.Bindings[i].Source, plan.Bindings[j].Source)
			if err != nil {
				return err
			}
			if leftInsideRight || rightInsideLeft {
				return fmt.Errorf("binding sources %q and %q overlap", plan.Bindings[i].Source, plan.Bindings[j].Source)
			}
		}
	}
	return nil
}

func bindingSourcesOverlap(left, right string) (bool, error) {
	if left == right {
		return true, nil
	}
	leftInsideRight, err := resolvedPathWithin(right, left)
	if err != nil {
		return false, err
	}
	rightInsideLeft, err := resolvedPathWithin(left, right)
	if err != nil {
		return false, err
	}
	return leftInsideRight || rightInsideLeft, nil
}

func validateOpaqueID(value, kind string) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || value == "" || len(value) > 128 ||
		strings.ContainsAny(value, `/\`) || strings.ContainsAny(value, " \t\r\n\x00") {
		return fmt.Errorf("invalid backup %s ID", kind)
	}
	return nil
}

func normalizePlanBranch(raw string) (string, error) {
	return bucketbranch.NormalizeSelector(raw)
}

func validateDisplayName(value, kind string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("invalid backup %s name", kind)
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("invalid backup %s name", kind)
	}
	return nil
}

func validatePathName(value string) error {
	if !utf8.ValidString(value) || value == "" || len(value) > 128 || value == "." || value == ".." ||
		strings.HasPrefix(value, "@") || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("backup path name must be one portable path segment")
	}
	return nil
}

func randomPlanID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func selectPlan(values map[string]Plan, selector string) (string, Plan, error) {
	selector = strings.TrimSpace(selector)
	if value, ok := values[selector]; ok {
		return selector, value, nil
	}
	foundID := ""
	var found Plan
	for id, value := range values {
		if value.Name != selector {
			continue
		}
		if foundID != "" {
			return "", Plan{}, fmt.Errorf("backup plan name %q is ambiguous", selector)
		}
		foundID, found = id, value
	}
	if foundID == "" {
		return "", Plan{}, fmt.Errorf("backup plan %q was not found", selector)
	}
	return foundID, found, nil
}

func clonePlan(value Plan) Plan {
	value.Bindings = append([]Binding(nil), value.Bindings...)
	return value
}
