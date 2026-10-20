package analyzer

type ContextMaintenanceKind string

const (
	ContextLocalPrune         ContextMaintenanceKind = "local_prune"
	ContextModelSummary       ContextMaintenanceKind = "model_summary"
	ContextMechanicalFallback ContextMaintenanceKind = "mechanical_fallback"
)

type ContextMaintenanceEvent struct {
	Kind         ContextMaintenanceKind `json:"kind"`
	BeforeTokens int                    `json:"before_tokens"`
	AfterTokens  int                    `json:"after_tokens"`
	BudgetTokens int                    `json:"budget_tokens"`
	Compacted    bool                   `json:"compacted"`
	Fallback     bool                   `json:"fallback"`
	// Counts logical summarizer invocations, not physical HTTP retries.
	SummaryOperations int `json:"summary_operations"`
}

func (opts StepOptions) notifyContextMaintenance(event ContextMaintenanceEvent) {
	event.Fallback = event.Kind == ContextMechanicalFallback
	event.Compacted = !event.Fallback
	if opts.OnContextMaintenance != nil {
		opts.OnContextMaintenance(event)
	}
	if opts.OnContextEvent != nil {
		opts.OnContextEvent(event.Compacted, event.Fallback, event.BeforeTokens, event.AfterTokens)
	}
}
