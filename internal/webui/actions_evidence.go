package webui

// EvidenceLanes is one revision's check evidence: the overall conclusion, the
// latest .owngit/checks.json attempt and the latest run of each workflow
// file. A lane from an earlier revision is stale and names its commit.
type EvidenceLanes struct {
	Conclusion       string
	RevisionShortOID string
	// JSON is nil when no checks.json attempt exists.
	JSON *EvidenceLane
	// Workflows are the listed runs; WorkflowsTotal counts every workflow
	// file with a run at the revision.
	Workflows          []WorkflowRunRow
	WorkflowsTotal     int
	WorkflowsTruncated bool
	// Stale is true when any lane comes from an earlier commit.
	Stale bool
	// AdmissionNote explains why a pull request revision admits no workflow
	// run, when that is the case.
	AdmissionNote *WorkflowMessage
}

type EvidenceLane struct {
	Conclusion string
	ShortOID   string
	Stale      bool
	URL        string
}

func (e *EvidenceLanes) Recorded() bool {
	return e != nil && e.Conclusion != ""
}

func (e *EvidenceLanes) HasWorkflows() bool {
	return e != nil && (len(e.Workflows) > 0 || e.WorkflowsTotal > 0)
}

func prChecksState(e CheckEvidence) stateLabel {
	if e.ReadFailure != nil || !e.Evidence.HasWorkflows() {
		return checkStateOf(e)
	}
	return workflowState(e.Evidence.Conclusion)
}

func prChecksRelevance(e CheckEvidence) stateLabel {
	if e.ReadFailure != nil || !e.Evidence.HasWorkflows() {
		return checkRelevance(e)
	}
	return evidenceRelevance(!e.Evidence.Stale, true, true)
}

func prChecksSummary(e CheckEvidence) MessageCode {
	if e.ReadFailure != nil || !e.Evidence.HasWorkflows() {
		return prCheckSummary(e)
	}
	return "wf.pr.lanes"
}
