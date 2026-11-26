package pipeline

// Validation tells whether the evidence gathered lets a change be accepted.
// It is kept apart from the execution status (a step or run that finished)
// so a run that completed is never mistaken for a delivery that was approved.
type Validation string

const (
	// ValidationPassed means every gate that ran approved with valid evidence.
	ValidationPassed Validation = "passed"
	// ValidationFailed means a gate left blocking issues unresolved.
	ValidationFailed Validation = "failed"
	// ValidationInconclusive means a gate produced no valid verdict, even
	// after the bounded format recovery.
	ValidationInconclusive Validation = "inconclusive"
	// ValidationNotEvaluated means nothing was actually checked: simulation,
	// a story blocked in Passo 0, or gates with no evidence to judge.
	ValidationNotEvaluated Validation = "not_evaluated"
)

// validationRank orders the outcomes from best to worst, so aggregating
// keeps the worst one.
var validationRank = map[Validation]int{
	ValidationPassed:       0,
	ValidationNotEvaluated: 1,
	ValidationInconclusive: 2,
	ValidationFailed:       3,
}

// worseValidation returns the worse of two outcomes; an empty one counts as
// absent.
func worseValidation(a, b Validation) Validation {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case validationRank[b] > validationRank[a]:
		return b
	}
	return a
}

// storyValidation aggregates the gate outcomes recorded on a story's steps.
// A story with no recorded outcome was not evaluated: the absence of a
// verdict never counts as an approval.
func storyValidation(steps []Step) Validation {
	var v Validation
	for _, s := range steps {
		v = worseValidation(v, s.Validation)
	}
	if v == "" {
		return ValidationNotEvaluated
	}
	return v
}

// runValidation aggregates the stories that were developed; stories without
// an outcome (older journals or never started) count as not evaluated.
func runValidation(stories []StoryRun) Validation {
	var v Validation
	for _, st := range stories {
		v = worseValidation(v, cmpValidation(st.Validation))
	}
	if v == "" {
		return ValidationNotEvaluated
	}
	return v
}

func cmpValidation(v Validation) Validation {
	if v == "" {
		return ValidationNotEvaluated
	}
	return v
}

// Label is how the console and logs name an outcome.
func (v Validation) Label() string {
	switch v {
	case ValidationPassed:
		return "aprovada"
	case ValidationFailed:
		return "reprovada"
	case ValidationInconclusive:
		return "inconclusiva"
	default:
		return "não avaliada"
	}
}
