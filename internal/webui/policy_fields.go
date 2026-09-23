package webui

// Backend policy refusals as interface messages.
//
// The backend decides what a policy may contain and reports which field it
// refused and by which rule. This file is the only place that turns those two
// names into a control name and a sentence, so the interface never keeps its
// own copy of a bound that could drift from the enforced one.
//
// A field this table does not know returns an empty control name, and the
// caller reports the refusal at form level. Nothing is silently dropped.

// Backend field names, mirrored so the mapping does not depend on importing
// the state package into the view layer.
const (
	PolicyFieldExecutor            = "executor"
	PolicyFieldAllowedEvents       = "allowed_events"
	PolicyFieldMaxTimeoutMS        = "max_timeout_ms"
	PolicyFieldMaxOutputLimitBytes = "max_output_limit_bytes"
	PolicyFieldQueueLimit          = "queue_limit"
	PolicyFieldMaxActiveJobs       = "max_active_jobs"
	PolicyFieldMaxLeaseMS          = "max_lease_ms"

	PolicyFieldContainerImage        = "container_image"
	PolicyFieldContainerRuntime      = "container_runtime"
	PolicyFieldContainerNetwork      = "container_network"
	PolicyFieldContainerCPUMillis    = "container_cpu_millis"
	PolicyFieldContainerMemoryBytes  = "container_memory_bytes"
	PolicyFieldContainerPIDs         = "container_pids"
	PolicyFieldContainerScratchBytes = "container_scratch_bytes"

	PolicyFieldSourceMaxEntries    = "source_max_entries"
	PolicyFieldSourceMaxFileBytes  = "source_max_file_bytes"
	PolicyFieldSourceMaxTotalBytes = "source_max_total_bytes"
	PolicyFieldSourceMaxPathDepth  = "source_max_path_depth"
	PolicyFieldSourceMaxPathBytes  = "source_max_path_bytes"
	PolicyFieldSourceMaxNameBytes  = "source_max_name_bytes"
	PolicyFieldSourceMetadataLimit = "source_metadata_limit_bytes"
)

// Backend rule names.
const (
	PolicyRuleRange         = "range"
	PolicyRuleRequired      = "required"
	PolicyRuleUnknown       = "unknown"
	PolicyRuleFormat        = "format"
	PolicyRuleNotApplicable = "not_applicable"
	PolicyRuleDuplicate     = "duplicate"
)

// policyFormFields maps a backend field to the control that carries it.
//
// Two of them are groups rather than single inputs. The events checkboxes and
// the network radios are addressed by the group's own name, so the refusal
// attaches to the fieldset a reader is actually looking at instead of to one
// arbitrary member of it.
var policyFormFields = map[string]string{
	PolicyFieldExecutor:      "executor",
	PolicyFieldAllowedEvents: "events",

	PolicyFieldMaxTimeoutMS:        "max_timeout_ms",
	PolicyFieldMaxOutputLimitBytes: "max_output_limit_bytes",
	PolicyFieldQueueLimit:          "queue_limit",
	PolicyFieldMaxActiveJobs:       "max_active_jobs",
	PolicyFieldMaxLeaseMS:          "max_lease_ms",

	PolicyFieldContainerImage:        "container_image",
	PolicyFieldContainerRuntime:      "container_image",
	PolicyFieldContainerNetwork:      "container_network",
	PolicyFieldContainerCPUMillis:    "container_cpu_millis",
	PolicyFieldContainerMemoryBytes:  "container_memory_bytes",
	PolicyFieldContainerPIDs:         "container_pids",
	PolicyFieldContainerScratchBytes: "container_scratch_bytes",

	PolicyFieldSourceMaxEntries:    "source_max_entries",
	PolicyFieldSourceMaxFileBytes:  "source_max_file_bytes",
	PolicyFieldSourceMaxTotalBytes: "source_max_total_bytes",
	PolicyFieldSourceMaxPathDepth:  "source_max_path_depth",
	PolicyFieldSourceMaxPathBytes:  "source_max_path_bytes",
	PolicyFieldSourceMaxNameBytes:  "source_max_name_bytes",
	PolicyFieldSourceMetadataLimit: "source_metadata_limit_bytes",
}

// PolicyFieldNotice names the control a backend refusal belongs to and the
// sentence to show beside it.
//
// An unknown field returns an empty name, which tells the caller to report the
// refusal at form level rather than attach it to the wrong control.
func PolicyFieldNotice(field, rule string) (string, MessageCode) {
	control, known := policyFormFields[field]
	if !known {
		return "", MsgCCPolicyRefused
	}
	return control, policyRuleMessage(field, rule)
}

// policyRuleMessage picks the sentence for a rule.
//
// The wording explains what to do, and never repeats a numeric bound: the
// accepted range is already stated beside each field from the same catalog, so
// one source describes it and a future bound change cannot leave two
// disagreeing sentences on one screen.
func policyRuleMessage(field, rule string) MessageCode {
	switch rule {
	case PolicyRuleRequired:
		if field == PolicyFieldAllowedEvents {
			return MsgCCEventsInvalid
		}
		if field == PolicyFieldContainerImage {
			return MsgCCImageRequired
		}
		return MsgCCFieldRequired
	case PolicyRuleUnknown:
		switch field {
		case PolicyFieldExecutor:
			return MsgCCExecutorInvalid
		case PolicyFieldContainerNetwork:
			return MsgCCNetworkInvalid
		case PolicyFieldContainerRuntime:
			return MsgCCRuntimeUnsupported
		}
		return MsgCCFieldUnknown
	case PolicyRuleFormat:
		if field == PolicyFieldContainerImage {
			return MsgCCImageInvalid
		}
		return MsgCCFieldFormat
	case PolicyRuleNotApplicable:
		return MsgCCFieldNotApplicable
	case PolicyRuleDuplicate:
		return MsgCCFieldDuplicate
	case PolicyRuleRange:
		if field == PolicyFieldSourceMaxTotalBytes {
			// This field's floor moves with the file limit above it, so the
			// message names that relationship. The maximum is stated with the
			// field like every other one.
			return MsgCCTotalBytesRange
		}
		return MsgCCFieldRange
	}
	return MsgCCPolicyRefused
}
