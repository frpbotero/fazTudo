// Run state: the types the dashboard, the console and the journal read.

package pipeline

import (
	"slices"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// StepStatus is the lifecycle state of a stage.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepRunning  StepStatus = "running"
	StepDone     StepStatus = "done"
	StepSkipped  StepStatus = "skipped"
	StepOmitted  StepStatus = "omitted" // not selected by the architect
	StepFailed   StepStatus = "failed"
	StepWarning  StepStatus = "warning" // finished with unresolved issues; the run goes on
	StepCanceled StepStatus = "canceled"
	// StepWaiting is a story stopped on a product decision only the user can
	// make; answering it develops the story again from Passo 0.
	StepWaiting StepStatus = "waiting_input"
)

// Result is the final outcome of a run.
type Result string

const (
	ResultSucceeded Result = "succeeded"
	ResultFailed    Result = "failed"
	ResultCanceled  Result = "canceled"
)

// Log levels exposed to the UI.
const (
	LevelInfo    = "info"
	LevelSuccess = "success"
	LevelWarn    = "warn"
	LevelError   = "error"
)

// Step is the observable state of one stage.
type Step struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Role     string        `json:"role"`
	Tier     provider.Tier `json:"tier"`
	Status   StepStatus    `json:"status"`
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	Effort   string        `json:"effort,omitempty"`
	Output   string        `json:"output,omitempty"`
	Note     string        `json:"note,omitempty"`
	Kind     string        `json:"kind,omitempty"`  // "" (stage), "fix", "verify", "investigate" or "stream"
	Agent    string        `json:"agent,omitempty"` // implementer running a stream
	Round    int           `json:"round,omitempty"` // fix/verification round
	Gate     string        `json:"gate,omitempty"`  // gate that requested a fix
	Issues   []Issue       `json:"issues,omitempty"`
	// Validation is a gate's final outcome, recorded on the gate's own step;
	// empty on steps that judge nothing.
	Validation Validation `json:"validation,omitempty"`
	// Checks and Findings are the results of an executable gate.
	Checks   []quality.Result  `json:"checks,omitempty"`
	Findings []quality.Finding `json:"findings,omitempty"`
	// Candidates are the alternatives compared by best-of-N.
	Candidates []Candidate `json:"candidates,omitempty"`
	// Commit records the changes the step made, when git is enabled.
	Commit *vcs.Commit `json:"commit,omitempty"`
	// Calls record what each agent call of the step ran with.
	Calls []CallRecord `json:"calls,omitempty"`
	// QueueMS is how long the step's agent calls waited for a slot.
	QueueMS int64 `json:"queue_ms,omitempty"`
	// FailureClass is what a failed or unsettled step ran into (Failure*).
	FailureClass string `json:"failure_class,omitempty"`
	// Need says why the stage is part of the story (Need*), NeedReason why.
	Need       string `json:"need,omitempty"`
	NeedReason string `json:"need_reason,omitempty"`
	// Advisory are a review's style remarks: reported, not demanded.
	Advisory   []Issue    `json:"advisory,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
}

// LogEntry is a structured event shown in the UI console.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// StoryRun is the progress of one user story through the stages.
type StoryRun struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status StepStatus `json:"status"`
	// Validation is whether the delivery was approved, apart from Status,
	// which only says the story finished. Empty until the story closes.
	Validation Validation `json:"validation,omitempty"`
	// Risk is what the policy's risk rules found: level, reasons and the
	// stages they made mandatory.
	Risk *workspace.Risk `json:"risk,omitempty"`
	// Baseline is how the project commands behaved on the story's starting
	// snapshot.
	Baseline *Baseline `json:"baseline,omitempty"`
	// Readiness is what Passo 0 found: criteria with evidence and open
	// product decisions. Answers are the user's answers to them, kept apart
	// from the story text across attempts.
	Readiness *Readiness        `json:"readiness,omitempty"`
	Answers   []PendingDecision `json:"answers,omitempty"`
	// Change is the story's kind (ChangeBug, ChangeFeature, ChangeRefactor or
	// ChangeTrivial), which decides what proves it; ChangeReason justifies a
	// trivial one.
	Change       string `json:"change,omitempty"`
	ChangeReason string `json:"change_reason,omitempty"`
	// StartCommit is the snapshot the story started from.
	StartCommit string `json:"start_commit,omitempty"`
	// BuiltOn is the base commit each dependency entered with: the approved
	// snapshot the story was built on.
	BuiltOn map[string]string `json:"built_on,omitempty"`
	// Head is the last commit the run made on the story's branch: a resume
	// that finds the branch elsewhere knows its results judged other code.
	Head string `json:"head,omitempty"`
	// Usage is what the story's calls spent; HumanReview the review and
	// rework time people logged for it.
	Usage       *RunUsage     `json:"usage,omitempty"`
	HumanReview []HumanReview `json:"human_review,omitempty"`
	// Reproduction is a bug fix's proof that its tests fail before the fix.
	Reproduction *Reproduction `json:"reproduction,omitempty"`
	// Acceptance is the functional QA's judgment of each scenario.
	Acceptance []ScenarioResult `json:"acceptance,omitempty"`
	// Specialties are the planning specialties' reports, by stage.
	Specialties map[string]SpecialistReport `json:"specialties,omitempty"`
	// Performance are the benchmarks' baseline-against-candidate results.
	Performance []BenchResult `json:"performance,omitempty"`
	// Measurements are what the performance specialist read from the
	// benchmarks.
	Measurements []Measurement `json:"measurements,omitempty"`
	// Integration is whether the story entered the base branch; the note
	// says why it was kept out.
	Integration     Integration `json:"integration,omitempty"`
	IntegrationNote string      `json:"integration_note,omitempty"`
	StartedAt       *time.Time  `json:"started_at,omitempty"`
	DurationMS      int64       `json:"duration_ms,omitempty"`
	Plan            *Plan       `json:"plan,omitempty"`
	Branch          string      `json:"branch,omitempty"`
	// Wave is the 1-based group of stories developed in parallel; DependsOn
	// lists the stories it waits for, as the dependency analysis found them.
	Wave      int      `json:"wave,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	// Trace links the tests the specification requires to the test files.
	Trace []quality.Trace `json:"trace,omitempty"`
	// Causes are the problems found, followed across fix rounds.
	Causes []Cause `json:"causes,omitempty"`
	Steps  []Step  `json:"steps"`
}

// Run describes the current or last pipeline execution: a shared discovery
// followed by one or more stories developed in sequence.
type Run struct {
	// Kind is RunCleanup for a code health run, empty for a story run.
	Kind       string     `json:"kind,omitempty"`
	Running    bool       `json:"running"`
	Source     string     `json:"source,omitempty"`
	Repository string     `json:"repository,omitempty"`
	DryRun     bool       `json:"dry_run"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     Result     `json:"result,omitempty"`
	// Validation aggregates the stories' outcomes: Result says the run
	// finished, Validation whether what it delivered was approved.
	Validation Validation `json:"validation,omitempty"`
	BaseBranch string     `json:"base_branch,omitempty"`
	Protected  []string   `json:"protected,omitempty"` // input files guarded during the run
	Discovery  Step       `json:"discovery"`
	// Analysis maps the dependencies between the stories of a document; it
	// runs only when there are several.
	Analysis *Step `json:"analysis,omitempty"`
	// Waves group the story indexes developed in parallel, in order.
	Waves [][]int `json:"waves,omitempty"`
	// Integrations are the branches that merged the stories of each wave.
	Integrations []string `json:"integrations,omitempty"`
	// Promotions are the decisions on merging candidates into the base.
	Promotions []Promotion `json:"promotions,omitempty"`
	// Manifest is what the run was made of; Operations the side effects it
	// already performed, so a resume does not repeat them.
	Manifest   *RunManifest `json:"manifest,omitempty"`
	Operations []Operation  `json:"operations,omitempty"`
	// ResumeChecks are why the last resume redid what it redid.
	ResumeChecks []Invalidation `json:"resume_checks,omitempty"`
	// Delivery is where the run's delivery package was written.
	Delivery *Delivery `json:"delivery,omitempty"`
	// Usage is what the run spent, as observed.
	Usage *RunUsage `json:"usage,omitempty"`
	// ID correlates everything the run recorded.
	ID      string     `json:"id,omitempty"`
	Stories []StoryRun `json:"stories"`
	Current int        `json:"current"` // index into Stories; -1 while discovering
	Logs    []LogEntry `json:"logs"`
	// Bases are the branches each repository started from (keyed "." and by
	// nested path); every wave is merged into them before the next begins.
	Bases map[string]string `json:"bases,omitempty"`
	// Resume is where a stopped run continues; nil while running or done.
	Resume *ResumePoint `json:"resume,omitempty"`
	// Cleanup holds a code health run's options, checks and verdict.
	Cleanup *CleanupInfo `json:"cleanup,omitempty"`
}

// StartOptions configures a run.
type StartOptions struct {
	// Stories are developed in order; each one sees the previous as delivered.
	Stories []stories.Story
	// Preamble is document-level context shared by every story.
	Preamble string
	Source   string
	DryRun   bool
	// Rediscover forces a new discovery even if the session has one cached.
	Rediscover bool
	// DocumentName is the name of the loaded story file; a file with that
	// name in the repository is protected like the policy's patterns.
	DocumentName string
}

// stepRef addresses a step by ID: story -1 is the discovery step.
type stepRef struct {
	story int
	id    string
}

var discoveryRef = stepRef{story: -1, id: DiscoveryStage.ID}

// resultLabel says in Portuguese how the run ended. A run that finished with
// stories pending or a delivery not validated did not succeed as a delivery:
// the label says so instead of the bare "succeeded".
func resultLabel(run Run) string {
	switch run.Result {
	case ResultFailed:
		return "falhou"
	case ResultCanceled:
		return "cancelada"
	case ResultSucceeded:
		pending := slices.ContainsFunc(run.Stories, func(st StoryRun) bool { return st.Status == StepWarning || st.Status == StepFailed })
		if pending || run.Validation == ValidationFailed {
			return "concluída com pendências"
		}
		if slices.ContainsFunc(run.Stories, func(st StoryRun) bool { return st.Status == StepWaiting }) {
			return "concluída, aguardando decisão de produto"
		}
		return "concluída"
	}
	return ""
}
