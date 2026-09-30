package webui

import (
	"slices"
	"strings"
)

// LimitValues reads every numeric field of the policy by its backend name.
func (p CheckPolicyView) LimitValues() map[string]int64 {
	return map[string]int64{
		PolicyFieldMaxTimeoutMS:        p.MaxTimeoutMS,
		PolicyFieldMaxOutputLimitBytes: p.MaxOutputLimitBytes,
		PolicyFieldQueueLimit:          int64(p.QueueLimit),
		PolicyFieldMaxActiveJobs:       int64(p.MaxActiveJobs),
		PolicyFieldMaxLeaseMS:          p.MaxLeaseMS,

		PolicyFieldSourceMaxEntries:    int64(p.Source.MaxEntries),
		PolicyFieldSourceMaxFileBytes:  p.Source.MaxFileBytes,
		PolicyFieldSourceMaxTotalBytes: p.Source.MaxTotalBytes,
		PolicyFieldSourceMaxPathDepth:  int64(p.Source.MaxPathDepth),
		PolicyFieldSourceMaxPathBytes:  int64(p.Source.MaxPathBytes),
		PolicyFieldSourceMaxNameBytes:  int64(p.Source.MaxNameBytes),
		PolicyFieldSourceMetadataLimit: p.Source.MetadataLimit,

		PolicyFieldContainerCPUMillis:    p.Container.CPUMillis,
		PolicyFieldContainerMemoryBytes:  p.Container.MemoryBytes,
		PolicyFieldContainerPIDs:         p.Container.PIDs,
		PolicyFieldContainerScratchBytes: p.Container.ScratchBytes,
	}
}

// PolicyChanges lists each setting that differs between the stored policy
// and the one a save would store, in the order the editor draws them.
//
// A setting that applies only to container execution reads "not used" on a
// side that runs elsewhere, so a change of mode is not shown as a change of
// every container value to zero.
func PolicyChanges(before, after CheckPolicyView) []PolicyChange {
	type fact struct {
		label     MessageCode
		container bool
		value     func(CheckPolicyView) BiValue
	}
	facts := []fact{
		{MsgCCStateWhere, false, func(p CheckPolicyView) BiValue { return biCode(executorName(p.Executor)) }},
		{MsgCCStateWhen, false, eventsValue},
	}
	for _, limit := range policyLimitFields {
		field := limit.Field
		facts = append(facts, fact{limit.Label, limit.Group == LimitGroupContainer, func(p CheckPolicyView) BiValue {
			value := p.LimitValues()[field]
			return BiValue{LimitText(LangEN, field, value), LimitText(LangKO, field, value)}
		}})
	}
	facts = append(facts,
		fact{MsgCCImage, true, func(p CheckPolicyView) BiValue { return BiValue{p.Container.Image, p.Container.Image} }},
		fact{MsgCCAllowTags, true, func(p CheckPolicyView) BiValue { return switchValue(p.Container.AllowTags) }},
		fact{MsgCCPullMissing, true, func(p CheckPolicyView) BiValue { return switchValue(p.Container.PullMissing) }},
		fact{MsgCCNetwork, true, networkValue},
		fact{MsgCCImageVolumes, true, func(p CheckPolicyView) BiValue { return switchValue(p.Container.ImageVolumes) }},
		fact{MsgCCWritableRoot, true, func(p CheckPolicyView) BiValue { return switchValue(p.Container.WritableRoot) }},
		fact{MsgCCMissingLimits, true, missingValue},
	)
	var changes []PolicyChange
	for _, fact := range facts {
		read := func(p CheckPolicyView) BiValue {
			switch {
			case !p.Saved:
				return biCode(MsgCCValueNotSaved)
			case fact.container && p.Executor != ExecutorContainer:
				return biCode(MsgCCValueNotUsed)
			}
			return fact.value(p)
		}
		if was, now := read(before), read(after); was != now {
			changes = append(changes, PolicyChange{Label: fact.label, Before: was, After: now})
		}
	}
	return changes
}

func biCode(code MessageCode) BiValue { return BiValue{Text(LangEN, code), Text(LangKO, code)} }

func switchValue(on bool) BiValue {
	if on {
		return biCode(MsgCCValueOn)
	}
	return biCode(MsgCCValueOff)
}

func eventsValue(p CheckPolicyView) BiValue {
	var labels []MessageCode
	if p.AllowsPush() {
		labels = append(labels, MsgCCStateEventPush)
	}
	if p.AllowsPullRequest() {
		labels = append(labels, MsgCCStateEventPR)
	}
	return joinCodes(labels)
}

func networkValue(p CheckPolicyView) BiValue {
	switch p.Container.Network {
	case "", ContainerNetworkNone, ContainerNetworkBridge:
		return biCode(networkName(p.Container.Network))
	}
	return BiValue{p.Container.Network, p.Container.Network}
}

func missingValue(p CheckPolicyView) BiValue {
	var labels []MessageCode
	for _, limit := range ContainerLimitNames {
		if slices.Contains(p.Container.MissingEnforcement, limit) {
			labels = append(labels, containerLimitLabel(limit))
		}
	}
	return joinCodes(labels)
}

func joinCodes(codes []MessageCode) BiValue {
	if len(codes) == 0 {
		return biCode(MsgCCValueNone)
	}
	en, ko := make([]string, len(codes)), make([]string, len(codes))
	for index, code := range codes {
		en[index], ko[index] = Text(LangEN, code), Text(LangKO, code)
	}
	return BiValue{strings.Join(en, ", "), strings.Join(ko, ", ")}
}
