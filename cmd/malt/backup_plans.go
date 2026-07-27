package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dewebprotocol/malt-client/application"
	clientadd "github.com/dewebprotocol/malt-client/application/add"
	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	"github.com/dewebprotocol/malt-client/bucketsync"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/keyring"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

var (
	bindBucket         string
	bindBranch         string
	bindName           string
	bindPlanName       string
	bindMerge          bool
	bindCreateBranch   bool
	syncMergeConflicts bool
)

var backupBindCmd = &cobra.Command{
	Use:   "bind <local-path>",
	Short: "Bind a local directory to a complete Bucket branch backup plan",
	Args:  cobra.ExactArgs(1),
	RunE:  runBackupBind,
}

var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List backup plans and their local bindings",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		store, err := configuredPlanStore()
		if err != nil {
			return err
		}
		plans, err := store.List()
		if err != nil {
			return err
		}
		states := make(map[string]any, len(plans))
		for _, plan := range plans {
			history, err := clientbackup.NewHistory(planHistoryPath(cfg, plan.ID))
			if err != nil {
				return err
			}
			snapshot, err := history.Snapshot()
			if err != nil {
				return err
			}
			pending, err := history.Pending()
			if err != nil {
				return err
			}
			conflict, err := history.Conflict()
			if err != nil {
				return err
			}
			states[plan.ID] = map[string]any{
				"state": snapshot[plan.ID], "pending_backup": pending, "conflict_checkout": conflict,
			}
		}
		printJSON(map[string]any{"plans": plans, "states": states})
		return nil
	},
}

var syncCmd = &cobra.Command{
	Use:   "sync [plan...]",
	Short: "Back up local changes, then update every binding from its latest remote branch",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfiguredPlanOperation(cmd, "sync", args)
	},
}

func init() {
	backupBindCmd.Flags().StringVar(&bindBucket, "bucket", "", "Bucket name or ID (required)")
	backupBindCmd.Flags().StringVar(&bindBranch, "branch", "main", "Whole-plan Bucket branch")
	backupBindCmd.Flags().StringVar(&bindName, "name", "", "Local binding display name (defaults to directory name)")
	backupBindCmd.Flags().StringVar(&bindPlanName, "plan", "", "Backup plan display name")
	backupBindCmd.Flags().BoolVar(&bindMerge, "merge", false, "Explicitly add this directory to an existing plan on the same branch")
	backupBindCmd.Flags().BoolVar(&bindCreateBranch, "create-branch", false, "Create the selected explicit branch when it does not exist")
	_ = backupBindCmd.MarkFlagRequired("bucket")
	backupCmd.AddCommand(backupBindCmd, backupListCmd)
	syncCmd.Flags().BoolVar(&backupForeground, "foreground", false, "Run without the daemon (advanced bypass)")
	syncCmd.Flags().StringVarP(&backupMessage, "message", "m", "", "Bucket commit message")
	syncCmd.Flags().BoolVar(&syncMergeConflicts, "merge", false, "Attempt conservative three-way merge for unresolved conflict branches")
	rootCmd.AddCommand(syncCmd)
}

func runBackupBind(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	source, err := filepath.Abs(strings.TrimSpace(args[0]))
	if err != nil {
		return fmt.Errorf("resolve backup binding source: %w", err)
	}
	configPath, err := runtimeConfigPath()
	if err != nil {
		return err
	}
	if err := clientbackup.ValidateSource(source, configuredProtectedPaths(cfg, configPath)); err != nil {
		return err
	}
	accountOptions, err := requiredGatewayOptions(cfg, "", "")
	if err != nil {
		return err
	}
	account, err := gatewayclient.New(accountOptions)
	if err != nil {
		return err
	}
	bucketValue, err := resolveBucket(cmd.Context(), account, bindBucket)
	if err != nil {
		return err
	}
	branch, err := normalizeCLIPlanBranch(bindBranch)
	if err != nil {
		return err
	}
	selectedOptions, err := requiredGatewayOptions(cfg, bucketValue.ID, branch)
	if err != nil {
		return err
	}
	selected, err := gatewayclient.New(selectedOptions)
	if err != nil {
		return err
	}
	if err := ensureBucketBranch(cmd.Context(), selected, branch, bindCreateBranch); err != nil {
		return err
	}
	store, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
	if err != nil {
		return err
	}
	plan, binding, err := store.Bind(clientbackup.BindRequest{
		PlanName: bindPlanName, BucketID: bucketValue.ID, BucketName: bucketValue.Name,
		Branch: branch, BindingName: bindName, Source: source, Merge: bindMerge,
	})
	if err != nil {
		return err
	}
	printJSON(map[string]any{"plan": plan, "binding": binding})
	return nil
}

func resolveBucket(ctx context.Context, client *gatewayclient.Client, selector string) (gatewayclient.Bucket, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return gatewayclient.Bucket{}, fmt.Errorf("Bucket selector is empty")
	}
	values, err := client.ListBuckets(ctx)
	if err != nil {
		return gatewayclient.Bucket{}, err
	}
	var (
		result gatewayclient.Bucket
		found  bool
	)
	for _, value := range values {
		if value.ID == selector {
			return value, nil
		}
		if value.Name != selector {
			continue
		}
		if found {
			return gatewayclient.Bucket{}, fmt.Errorf("Bucket name %q is ambiguous; use its ID", selector)
		}
		result, found = value, true
	}
	if !found {
		return gatewayclient.Bucket{}, fmt.Errorf("Bucket %q was not found; create it with `malt bucket create %s`", selector, selector)
	}
	return result, nil
}

func ensureBucketBranch(ctx context.Context, client *gatewayclient.Client, branch string, create bool) error {
	if branch == "main" {
		_, err := client.BucketHead(ctx)
		return err
	}
	refs, err := client.BucketBranches(ctx)
	if err != nil {
		return err
	}
	want := "heads/" + branch
	for _, ref := range refs {
		if ref.Name == want {
			return nil
		}
	}
	if !create {
		return fmt.Errorf("Bucket branch %q does not exist; create it first or pass --create-branch", branch)
	}
	_, err = client.CreateBucketBranch(ctx, branch, "")
	return err
}

func runConfiguredPlanOperation(cmd *cobra.Command, operation string, selectors []string) error {
	request := clientbackup.PlanRequest{
		Plans: append([]string(nil), selectors...), Message: backupMessage,
		MergeConflicts: operation == "sync" && syncMergeConflicts,
	}
	for attempt := 0; attempt < 4; attempt++ {
		result, err := invokeConfiguredPlanOperation(cmd, operation, request)
		if err == nil {
			if result != nil {
				printJSON(result)
			}
			return nil
		}
		if operation != "sync" {
			if result != nil {
				printJSON(result)
			}
			return err
		}
		retry, promptErr := prepareSyncRetry(cmd, &request, result)
		if promptErr != nil {
			if result != nil {
				printJSON(result)
			}
			return promptErr
		}
		if !retry {
			if result != nil {
				printJSON(result)
			}
			return err
		}
	}
	return fmt.Errorf("sync state changed repeatedly; inspect `malt root list` and `malt conflict list` before retrying")
}

func invokeConfiguredPlanOperation(
	cmd *cobra.Command,
	operation string,
	request clientbackup.PlanRequest,
) (*clientbackup.BatchResult, error) {
	if backupForeground {
		runner := &configuredPlanRunner{}
		if operation == "sync" {
			return runner.SyncPlans(cmd.Context(), request)
		}
		return runner.BackupPlans(cmd.Context(), request)
	}
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	client, transport := daemonHTTPClient(cfg.Daemon.SocketPath)
	defer transport.CloseIdleConnections()
	client.Timeout = 0
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	route := "/v1/plan-backups"
	if operation == "sync" {
		route = "/v1/sync"
	}
	httpRequest, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, "http://malt.local"+route, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("contact backup daemon: %w; start it with `malt daemon start` or use --foreground", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var result clientbackup.BatchResult
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode daemon response: %w", err)
		}
		return &result, nil
	}
	var failure struct {
		Error  string                    `json:"error"`
		Result *clientbackup.BatchResult `json:"result"`
	}
	if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
		if failure.Result != nil {
			return failure.Result, errors.New(failure.Error)
		}
		return nil, errors.New(failure.Error)
	}
	return nil, fmt.Errorf("backup daemon returned %s", response.Status)
}

func prepareSyncRetry(
	cmd *cobra.Command,
	request *clientbackup.PlanRequest,
	result *clientbackup.BatchResult,
) (bool, error) {
	return prepareSyncRetryWithConfirm(cmd, request, result, func(prompt string) (bool, error) {
		return confirmAtTerminal(cmd, prompt)
	})
}

func prepareSyncRetryWithConfirm(
	cmd *cobra.Command,
	request *clientbackup.PlanRequest,
	result *clientbackup.BatchResult,
	confirm func(string) (bool, error),
) (bool, error) {
	if request == nil || result == nil {
		return false, nil
	}
	if confirm == nil {
		return false, fmt.Errorf("sync confirmation callback is nil")
	}
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return false, err
	}
	var roots *application.Roots
	trusted := false
	observed := make(map[string]string)
	for _, failure := range result.Failures {
		if failure.TrustAlias == "" || failure.ObservedRoot == "" {
			continue
		}
		if previous, exists := observed[failure.TrustAlias]; exists {
			if previous != failure.ObservedRoot {
				return false, fmt.Errorf(
					"sync observed multiple roots for trust alias %s: %s and %s",
					failure.TrustAlias, previous, failure.ObservedRoot,
				)
			}
			continue
		}
		observed[failure.TrustAlias] = failure.ObservedRoot
		accepted, err := confirm(fmt.Sprintf(
			"Plan %s observed remote root %s. Accept it for %s and continue sync?",
			failure.PlanName, failure.ObservedRoot, failure.TrustAlias,
		))
		if err != nil || !accepted {
			return false, err
		}
		if roots == nil {
			store, _, err := openTrustStore()
			if err != nil {
				return false, err
			}
			roots, err = application.NewRoots(store)
			if err != nil {
				return false, err
			}
		}
		if failure.AcceptedRoot == "" {
			if _, err := roots.Trust(
				failure.TrustAlias, failure.ObservedRoot, "unixfs",
				cfg.GatewayBaseURL(), "explicit-malt-sync",
			); err != nil {
				return false, err
			}
		} else {
			candidate, err := cid.Parse(failure.ObservedRoot)
			if err != nil {
				return false, err
			}
			if _, err := roots.AcceptCandidate(failure.TrustAlias, candidate, "explicit-malt-sync"); err != nil {
				return false, err
			}
		}
		trusted = true
	}
	if trusted {
		return true, nil
	}
	if request.MergeConflicts {
		return false, nil
	}
	var mergePlans []string
	for _, failure := range result.Failures {
		if !failure.MergeAvailable {
			continue
		}
		mergePlans = append(mergePlans, fmt.Sprintf("%s (%s)", failure.PlanName, failure.ConflictBranch))
	}
	if len(mergePlans) == 0 {
		return false, nil
	}
	accepted, err := confirm(fmt.Sprintf(
		"%d Plan conflict(s) could not be merged by the Gateway and were preserved at %s. Attempt local three-way merge for all of them?",
		len(mergePlans), strings.Join(mergePlans, ", "),
	))
	if err != nil || !accepted {
		return false, err
	}
	request.MergeConflicts = true
	return true, nil
}

func confirmAtTerminal(cmd *cobra.Command, prompt string) (bool, error) {
	input := cmd.InOrStdin()
	file, ok := input.(*os.File)
	if !ok {
		return false, nil
	}
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false, nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", prompt)
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

type configuredPlanRunner struct {
	mu sync.Mutex
}

func (r *configuredPlanRunner) BackupPlans(ctx context.Context, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	return r.run(ctx, "backup", request)
}

func (r *configuredPlanRunner) SyncPlans(ctx context.Context, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	return r.run(ctx, "sync", request)
}

func (r *configuredPlanRunner) run(ctx context.Context, operation string, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	store, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
	if err != nil {
		return nil, err
	}
	plans, err := selectBackupPlans(store, request.Plans)
	if err != nil {
		return nil, err
	}
	result := &clientbackup.BatchResult{Operation: operation, Runs: make([]clientbackup.PlanRun, 0, len(plans))}
	var causes []error
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			causes = append(causes, err)
			result.Failures = append(result.Failures, planFailure(plan, err))
			break
		}
		service, err := buildPlanService(cfg, plan)
		if err != nil {
			causes = append(causes, err)
			result.Failures = append(result.Failures, planFailure(plan, err))
			continue
		}
		var run *clientbackup.Result
		if operation == "sync" {
			run, err = service.SyncWithOptions(ctx, request.Message, clientbackup.SyncOptions{
				MergeConflicts: request.MergeConflicts,
			})
		} else {
			run, err = service.Backup(ctx, request.Message)
		}
		if run != nil {
			result.Runs = append(result.Runs, clientbackup.PlanRun{
				PlanID: plan.ID, PlanName: plan.Name, BucketID: plan.BucketID, Branch: plan.Branch, Result: run,
			})
		}
		if err != nil {
			causes = append(causes, err)
			result.Failures = append(result.Failures, planFailure(plan, err))
		}
	}
	result.CompletedAt = time.Now().UTC()
	return result, clientbackup.NewBatchError(operation, result.Failures, causes)
}

func planFailure(plan clientbackup.Plan, err error) clientbackup.PlanFailure {
	failure := clientbackup.PlanFailure{
		PlanID: plan.ID, PlanName: plan.Name, BucketID: plan.BucketID, Branch: plan.Branch,
		Conflict: errors.Is(err, clientbackup.ErrBackupConflict), Error: err.Error(),
	}
	var rootErr *clientbackup.UnacceptedRootError
	if errors.As(err, &rootErr) {
		failure.TrustAlias = rootErr.Alias
		failure.ObservedRoot = rootErr.Observed.String()
		failure.CandidateRecorded = rootErr.CandidateRecorded
		if rootErr.Accepted.Defined() {
			failure.AcceptedRoot = rootErr.Accepted.String()
		}
	}
	var conflictErr *clientbackup.ConflictError
	if errors.As(err, &conflictErr) {
		failure.ConflictBranch = conflictErr.Branch
		if conflictErr.Push.Result.Branch != nil {
			failure.ConflictBranch = conflictErr.Push.Result.Branch.Name
		}
		failure.MergeAvailable = true
	}
	var mergeErr *clientbackup.ManualMergeError
	if errors.As(err, &mergeErr) {
		checkout := mergeErr.Checkout
		failure.Checkout = &checkout
		failure.MergeAvailable = false
	}
	return failure
}

func configuredPlanStore() (*clientbackup.PlanStore, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	return clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
}

func selectBackupPlans(store *clientbackup.PlanStore, selectors []string) ([]clientbackup.Plan, error) {
	if len(selectors) == 0 {
		values, err := store.List()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("no backup plans are configured; run `malt backup bind <local-path> --bucket <bucket>`")
		}
		return values, nil
	}
	result := make([]clientbackup.Plan, 0, len(selectors))
	seen := map[string]struct{}{}
	for _, selector := range selectors {
		value, err := store.Get(selector)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[value.ID]; ok {
			continue
		}
		seen[value.ID] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func buildPlanService(cfg *clientconfig.Config, plan clientbackup.Plan) (*clientbackup.PlanService, error) {
	keys, err := keyring.Open(cfg.Backup.KeyringPath)
	if err != nil {
		return nil, err
	}
	remoteOptions, err := requiredGatewayOptions(cfg, plan.BucketID, plan.Branch)
	if err != nil {
		return nil, err
	}
	remote, err := gatewayclient.New(remoteOptions)
	if err != nil {
		return nil, err
	}
	lists, err := unixfs.NewGatewayMutationAdapter(remote)
	if err != nil {
		return nil, err
	}
	addGateway, err := clientadd.NewGateway(remote, lists)
	if err != nil {
		return nil, err
	}
	syncer, err := bucketsync.OpenBranch(cfg.Workspace.StatePath, remote, plan.BucketID, plan.Branch)
	if err != nil {
		return nil, err
	}
	statePath := planHistoryPath(cfg, plan.ID)
	history, err := clientbackup.NewHistory(statePath)
	if err != nil {
		return nil, err
	}
	configPath, err := runtimeConfigPath()
	if err != nil {
		return nil, err
	}
	trustStore, _, err := openTrustStore()
	if err != nil {
		return nil, err
	}
	roots, err := application.NewRoots(trustStore)
	if err != nil {
		return nil, err
	}
	operationLock := statePath + ".operation.lock"
	planStore, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
	if err != nil {
		return nil, err
	}
	allPlans, err := planStore.List()
	if err != nil {
		return nil, err
	}
	var restoreProtected []string
	for _, existingPlan := range allPlans {
		for _, binding := range existingPlan.Bindings {
			restoreProtected = append(restoreProtected, binding.Source)
		}
	}
	protected := append(
		configuredProtectedPaths(cfg, configPath),
		statePath, statePath+".lock", operationLock,
		operationLock+".sync-transaction.json", operationLock+".restore-transaction.json",
		operationLock+".conflicts",
	)
	return clientbackup.NewPlanService(clientbackup.PlanServiceOptions{
		Plan: plan, TempDir: cfg.Backup.TempDir,
		LockPath: operationLock, Keys: keys, Sync: syncer,
		Materializer: clientbackup.AddPlanMaterializer{Gateway: addGateway, CAS: remote},
		History:      history, Remote: remote, Blocks: remote, Roots: roots,
		Protected: protected, RestoreProtected: restoreProtected,
	})
}

func planHistoryPath(cfg *clientconfig.Config, planID string) string {
	return filepath.Join(cfg.Backup.HistoryDir, planID+".json")
}

func normalizeCLIPlanBranch(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "main" {
		return "main", nil
	}
	value = strings.TrimPrefix(value, "heads/")
	if value == "" || value == "main" || strings.ContainsAny(value, `/\`) || strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("invalid Bucket branch %q", value)
	}
	return value, nil
}

func runConfiguredPlanScheduler(ctx context.Context, runner *configuredPlanRunner) {
	nextAttempt := map[string]time.Time{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		runConfiguredPlanSchedulerTick(ctx, runner, nextAttempt)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runConfiguredPlanSchedulerTick(ctx context.Context, runner *configuredPlanRunner, nextAttempt map[string]time.Time) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "malt daemon: load scheduled backup configuration: %v\n", err)
		return
	}
	store, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "malt daemon: open scheduled backup plans: %v\n", err)
		return
	}
	plans, err := store.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "malt daemon: list scheduled backup plans: %v\n", err)
		return
	}
	now := time.Now().UTC()
	for _, plan := range plans {
		if ctx.Err() != nil {
			return
		}
		if !plan.Enabled || plan.Every <= 0 || now.Before(nextAttempt[plan.ID]) {
			continue
		}
		history, err := clientbackup.NewHistory(planHistoryPath(cfg, plan.ID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "malt daemon: open history for plan %s: %v\n", plan.Name, err)
			nextAttempt[plan.ID] = now.Add(time.Minute)
			continue
		}
		states, err := history.Snapshot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "malt daemon: read history for plan %s: %v\n", plan.Name, err)
			nextAttempt[plan.ID] = now.Add(time.Minute)
			continue
		}
		state := states[plan.ID]
		if state.LastResult != nil && !state.LastResult.CompletedAt.IsZero() &&
			now.Before(state.LastResult.CompletedAt.Add(plan.Every)) {
			nextAttempt[plan.ID] = state.LastResult.CompletedAt.Add(plan.Every)
			continue
		}
		if err := history.RecordPlanAttempt(plan.ID, now); err != nil {
			fmt.Fprintf(os.Stderr, "malt daemon: record attempt for plan %s: %v\n", plan.Name, err)
		}
		_, runErr := runner.BackupPlans(ctx, clientbackup.PlanRequest{Plans: []string{plan.ID}, Message: plan.Message})
		if runErr != nil {
			if err := history.RecordPlanFailure(plan.ID, time.Now().UTC(), runErr); err != nil {
				fmt.Fprintf(os.Stderr, "malt daemon: record failure for plan %s: %v\n", plan.Name, err)
			}
			fmt.Fprintf(os.Stderr, "malt daemon: automatic backup plan %s failed: %v\n", plan.Name, runErr)
			delay := plan.Every
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
			if delay < 30*time.Second {
				delay = 30 * time.Second
			}
			nextAttempt[plan.ID] = now.Add(delay)
			continue
		}
		nextAttempt[plan.ID] = now.Add(plan.Every)
	}
}

var _ clientbackup.PlanRunner = (*configuredPlanRunner)(nil)
