package webui

import (
	"fmt"
	"html/template"
	"strings"
)

type CheckOutcome struct {
	Workflow                                                              bool
	Total, Failed, Error, Unavailable, Incomplete, CancelledCount, Passed int
	Worktree                                                              string
	Cancelled, NothingRan                                                 bool
	Tolerated                                                             int
	Refusal                                                               string
}

func CheckOutcomeText(lang Lang, outcome CheckOutcome) string {
	if outcome.Refusal != "" {
		return fmt.Sprintf(Text(lang, MsgOutcomeRefusal), outcome.Refusal)
	}
	counted := func(singular, plural MessageCode, n int) string {
		code := plural
		if n == 1 {
			code = singular
		}
		return fmt.Sprintf(Text(lang, code), formatNumber(n))
	}
	var summary string
	if outcome.NothingRan {
		summary = Text(lang, MsgOutcomeNothingRan)
	} else {
		if outcome.Workflow {
			summary = counted(MsgOutcomeStep, MsgOutcomeSteps, outcome.Total)
		} else {
			summary = counted(MsgOutcomeCheck, MsgOutcomeChecks, outcome.Total)
		}
		parts := []string{}
		for _, count := range []struct {
			status string
			n      int
		}{
			{"failed", outcome.Failed}, {"error", outcome.Error}, {"unavailable", outcome.Unavailable},
			{"incomplete", outcome.Incomplete}, {"cancelled", outcome.CancelledCount}, {"passed", outcome.Passed},
		} {
			if count.n == 0 {
				continue
			}
			label := strings.ToLower(Text(lang, MessageCode("wf.state."+count.status)))
			if lang == LangKO {
				parts = append(parts, label+" "+formatNumber(count.n)+"개")
			} else {
				parts = append(parts, formatNumber(count.n)+" "+label)
			}
		}
		if len(parts) > 0 {
			summary += ": " + strings.Join(parts, ", ")
		}
		if outcome.Worktree == "dirty" {
			summary += "; " + Text(lang, MsgOutcomeDirty)
		} else if outcome.Worktree == "unknown" {
			summary += "; " + Text(lang, MsgOutcomeUnknown)
		}
		if outcome.Cancelled {
			summary += "; " + Text(lang, MsgCheckStateCancelled)
		}
	}
	if outcome.Tolerated > 0 {
		summary += "; " + counted(MsgOutcomeTolerated, MsgOutcomeToleratedPlural, outcome.Tolerated)
	}
	return summary
}

func biOutputLimit(lang Lang, limit int64) template.HTML {
	return biF(lang, MsgOutputLimitExceeded, formatNumber(int(limit)))
}

func biCheckOutcome(lang Lang, outcome CheckOutcome) template.HTML {
	return biText(lang, CheckOutcomeText(LangEN, outcome), CheckOutcomeText(LangKO, outcome))
}
