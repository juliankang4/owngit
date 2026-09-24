package webui

import (
	"errors"
	"strings"
	"testing"

	"owngit/internal/checkworkflow"
)

func TestLimitsRoundTripThroughTheirReadableUnit(t *testing.T) {
	for _, tc := range []struct {
		kind  LimitKind
		value int64
		want  LimitInput
	}{
		{LimitDuration, 600_000, LimitInput{"10", UnitMinutes}},
		{LimitDuration, 90_000, LimitInput{"90", UnitSeconds}},
		{LimitDuration, 86_400_000, LimitInput{"24", UnitHours}},
		{LimitDuration, 1_500, LimitInput{"1.5", UnitSeconds}},
		{LimitSize, 64 << 20, LimitInput{"64", UnitMB}},
		{LimitSize, 1536, LimitInput{"1536", UnitBytes}},
		{LimitSize, 1000, LimitInput{"1000", UnitBytes}},
		{LimitCores, 2_500, LimitInput{"2.5", UnitCores}},
		{LimitCount, 20_000, LimitInput{"20000", ""}},
	} {
		got := FormatLimit(tc.kind, tc.value, "")
		if got != tc.want {
			t.Errorf("FormatLimit(%s, %d) = %+v, want %+v", tc.kind, tc.value, got, tc.want)
		}
		// Resubmitting what the page shows stores the same number.
		back, err := ParseLimit(tc.kind, got)
		if err != nil || back != tc.value {
			t.Errorf("ParseLimit(%+v) = %d, %v, want %d", got, back, err, tc.value)
		}
	}
}

func TestLimitParsingRefusesWhatItWouldHaveToGuess(t *testing.T) {
	for _, tc := range []struct {
		kind  LimitKind
		input LimitInput
		want  error
	}{
		// Half a millisecond or half a byte cannot be stored.
		{LimitDuration, LimitInput{"0.0005", UnitSeconds}, ErrLimitFraction},
		{LimitSize, LimitInput{"1.5", UnitBytes}, ErrLimitFraction},
		{LimitCount, LimitInput{"2.5", ""}, ErrLimitFraction},
		// Signs, exponents, separators and units a field does not offer.
		{LimitDuration, LimitInput{"-1", UnitSeconds}, ErrLimitSyntax},
		{LimitDuration, LimitInput{"1e3", UnitSeconds}, ErrLimitSyntax},
		{LimitCount, LimitInput{"1,000", ""}, ErrLimitSyntax},
		{LimitCount, LimitInput{".", ""}, ErrLimitSyntax},
		{LimitDuration, LimitInput{"5", UnitMB}, ErrLimitSyntax},
		{LimitSize, LimitInput{"99999999999999", UnitGB}, ErrLimitOverflow},
		// A whole part longer than any stored value is out of range, not
		// unreadable, and is refused before any arithmetic.
		{LimitCount, LimitInput{strings.Repeat("1", 41), ""}, ErrLimitOverflow},
		{LimitDuration, LimitInput{strings.Repeat("9", 20), UnitSeconds}, ErrLimitOverflow},
		{LimitCount, LimitInput{strings.Repeat("1", 41) + "x", ""}, ErrLimitSyntax},
		// Too many significant decimal places cannot be stored exactly.
		{LimitSize, LimitInput{"1." + strings.Repeat("1", 41), UnitMB}, ErrLimitFraction},
		{LimitCores, LimitInput{"1.2345", UnitCores}, ErrLimitFraction},
	} {
		if _, err := ParseLimit(tc.kind, tc.input); !errors.Is(err, tc.want) {
			t.Errorf("ParseLimit(%s, %+v) error = %v, want %v", tc.kind, tc.input, err, tc.want)
		}
	}
	// The bounds count significant digits only: zero padding still converts.
	if value, err := ParseLimit(LimitCount, LimitInput{strings.Repeat("0", 60) + "7." + strings.Repeat("0", 60), ""}); value != 7 || err != nil {
		t.Errorf("a zero-padded amount = %d, %v", value, err)
	}
	if value, err := ParseLimit(LimitCount, LimitInput{"000.000", ""}); value != 0 || err != nil {
		t.Errorf("a zero amount = %d, %v", value, err)
	}
	// An out-of-range amount points at the range shown with the field.
	if _, err := ParseLimit(LimitCount, LimitInput{strings.Repeat("1", 41), ""}); LimitNoticeCode(LimitCount, err) != MsgCCFieldRange {
		t.Errorf("a 41-digit amount gets %q, want the range message", LimitNoticeCode(LimitCount, err))
	}
	// An empty amount is "use the default", in any unit.
	if value, err := ParseLimit(LimitSize, LimitInput{"", UnitMB}); value != 0 || err != nil {
		t.Errorf("an empty amount = %d, %v", value, err)
	}
	// A posted number without a unit is in the stored unit, as older clients send it.
	if value, err := ParseLimit(LimitDuration, LimitInput{"600000", ""}); value != 600_000 || err != nil {
		t.Errorf("a unitless amount = %d, %v", value, err)
	}
}

func TestEveryPolicyLimitHasAControlWithWords(t *testing.T) {
	seen := map[string]bool{}
	for _, limit := range PolicyLimitFields() {
		if seen[limit.Field] {
			t.Errorf("%s is listed twice", limit.Field)
		}
		seen[limit.Field] = true
		if limit.Label == "" || limit.ID == "" {
			t.Errorf("%s has no label or id", limit.Field)
		}
		for _, lang := range Langs() {
			if strings.HasPrefix(Text(lang, limit.Label), "cc.") {
				t.Errorf("%s has no %s wording", limit.Field, lang)
			}
		}
	}
	for _, field := range []string{
		"max_timeout_ms", "max_output_limit_bytes", "queue_limit", "max_active_jobs", "max_lease_ms",
		"source_max_entries", "source_max_file_bytes", "source_max_total_bytes", "source_max_path_depth",
		"source_max_path_bytes", "source_max_name_bytes", "source_metadata_limit_bytes",
		"container_cpu_millis", "container_memory_bytes", "container_pids", "container_scratch_bytes",
	} {
		if !seen[field] {
			t.Errorf("%s has no control", field)
		}
	}
}

func TestTheOfferedCheckFileIsOneOwnGitAccepts(t *testing.T) {
	document, err := checkworkflow.Parse([]byte(CheckFileExample))
	if err != nil {
		t.Fatalf("the example check file is refused: %v", err)
	}
	if len(document.Checks) != 1 || len(document.EventNames()) != 2 {
		t.Fatalf("the example no longer shows one check on both events: %+v", document)
	}
	// Every key the page explains appears in the example, apart from the
	// optional branch filter and limits the explanation introduces.
	for _, key := range []string{`"version"`, `"events"`, `"checks"`, `"name"`, `"command"`} {
		if !strings.Contains(CheckFileExample, key) {
			t.Errorf("the example lacks %s", key)
		}
	}
}

func TestTheNextStepFollowsTheSetupOrder(t *testing.T) {
	ready := func() ConfiguredChecksPage {
		return ConfiguredChecksPage{
			Policy: CheckPolicyView{Saved: true, ConsentActive: true, Executor: ExecutorHost,
				AllowedEvents: []string{CheckEventPush}},
			Runtime:   CheckRuntimeView{Available: true},
			CheckFile: CheckFileView{State: CheckFileFound, Checks: 1, Events: []string{CheckEventPush}},
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*ConfiguredChecksPage)
		want MessageCode
	}{
		{"everything in place", func(*ConfiguredChecksPage) {}, MsgCCNextNone},
		{"a closed runtime comes first", func(p *ConfiguredChecksPage) {
			p.Runtime.Available, p.Policy.Saved = false, false
		}, MsgCCNextRepair},
		{"nothing saved", func(p *ConfiguredChecksPage) { p.Policy = CheckPolicyView{} }, MsgCCNextSave},
		{"restored settings", func(p *ConfiguredChecksPage) { p.Policy.Legacy = true }, MsgCCNextResave},
		{"no check file", func(p *ConfiguredChecksPage) { p.CheckFile = CheckFileView{State: CheckFileMissing} }, MsgCCNextFile},
		{"an empty repository", func(p *ConfiguredChecksPage) { p.CheckFile = CheckFileView{State: CheckFileNoCommits} }, MsgCCNextFile},
		{"a refused check file", func(p *ConfiguredChecksPage) { p.CheckFile = CheckFileView{State: CheckFileInvalid} }, MsgCCNextFixFile},
		{"the file and settings share no event", func(p *ConfiguredChecksPage) {
			p.CheckFile.Events = []string{CheckEventPullRequest}
		}, MsgCCNextEvents},
		{"checks are off", func(p *ConfiguredChecksPage) { p.Policy.ConsentActive = false }, MsgCCNextEnable},
		{"a runner mode with no token", func(p *ConfiguredChecksPage) {
			p.Policy.Executor, p.RunnerTokensKnown = ExecutorExternalRunner, true
		}, MsgCCNextRunner},
		// A token is not evidence that a runner is running, so runner mode
		// never gets the promise that checks will run.
		{"a runner mode with a token", func(p *ConfiguredChecksPage) {
			p.Policy.Executor, p.RunnerTokensKnown, p.ActiveRunnerTokens = ExecutorExternalRunner, true, 1
		}, MsgCCNextRunnerStart},
		// A fact that could not be read yields "could not tell", never the
		// promise that checks will run and never an instruction it cannot back.
		{"an unreadable file", func(p *ConfiguredChecksPage) { p.CheckFile = CheckFileView{State: CheckFileUnreadable} }, MsgCCNextFileUnknown},
		{"a file state never looked up", func(p *ConfiguredChecksPage) { p.CheckFile = CheckFileView{} }, MsgCCNextFileUnknown},
		{"an unknown token count", func(p *ConfiguredChecksPage) { p.Policy.Executor = ExecutorExternalRunner }, MsgCCNextRunnerUnknown},
		// Nothing on the page shows that Docker runs or the image exists, so
		// container mode says what has to be true instead of promising.
		{"a container mode with everything saved", func(p *ConfiguredChecksPage) {
			p.Policy.Executor = ExecutorContainer
		}, MsgCCNextContainerStart},
		// The one piece of evidence the page does hold: the newest job, under
		// these settings, could not use its container.
		{"a container job that could not run", func(p *ConfiguredChecksPage) {
			p.Policy.Executor, p.Policy.Version = ExecutorContainer, 3
			p.Jobs = []CheckJobRow{{Status: JobUnavailable, Executor: ExecutorContainer, PolicyVersion: 3}}
		}, MsgCCNextContainerFailed},
		{"an unavailable job from older settings", func(p *ConfiguredChecksPage) {
			p.Policy.Executor, p.Policy.Version = ExecutorContainer, 3
			p.Jobs = []CheckJobRow{{Status: JobUnavailable, Executor: ExecutorContainer, PolicyVersion: 2}}
		}, MsgCCNextContainerStart},
		{"an unavailable host job before switching to a container", func(p *ConfiguredChecksPage) {
			p.Policy.Executor, p.Policy.Version = ExecutorContainer, 3
			p.Jobs = []CheckJobRow{{Status: JobUnavailable, Executor: ExecutorHost, PolicyVersion: 3}}
		}, MsgCCNextContainerStart},
		{"an unavailable job that a newer job followed", func(p *ConfiguredChecksPage) {
			p.Policy.Executor, p.Policy.Version = ExecutorContainer, 3
			p.Jobs = []CheckJobRow{
				{Status: JobPassed, Executor: ExecutorContainer, PolicyVersion: 3},
				{Status: JobUnavailable, Executor: ExecutorContainer, PolicyVersion: 3},
			}
		}, MsgCCNextContainerStart},
		{"a place this page does not know", func(p *ConfiguredChecksPage) { p.Policy.Executor = "somewhere_new" }, MsgCCNextUnknown},
		// A known step that is still required comes before the uncertainty.
		{"an unreadable file while checks are off", func(p *ConfiguredChecksPage) {
			p.CheckFile, p.Policy.ConsentActive = CheckFileView{State: CheckFileUnreadable}, false
		}, MsgCCNextEnable},
	} {
		page := ready()
		tc.edit(&page)
		if got := nextCheckStep(page); got != tc.want {
			t.Errorf("%s: next step %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOnlyKnownPrerequisitesPromiseThatChecksRun(t *testing.T) {
	// The affirmative wording belongs to MsgCCNextNone alone. The uncertain
	// and runner states must not borrow it.
	promise := Text(LangEN, MsgCCNextNone)
	for _, code := range []MessageCode{MsgCCNextFileUnknown, MsgCCNextRunnerUnknown, MsgCCNextRunnerStart,
		MsgCCNextContainerStart, MsgCCNextContainerFailed, MsgCCNextUnknown} {
		for _, lang := range Langs() {
			text := Text(lang, code)
			if strings.HasPrefix(text, "cc.") {
				t.Errorf("%s has no %s wording", code, lang)
			}
			if text == Text(lang, MsgCCNextNone) || strings.Contains(text, "Checks run on the next") || strings.Contains(text, promise) {
				t.Errorf("%s/%s promises execution: %q", code, lang, text)
			}
		}
	}
	// Where the final step does mention running, it is a condition.
	for _, code := range []MessageCode{MsgCCNextRunnerStart, MsgCCNextContainerStart} {
		if text := Text(LangEN, code); !strings.HasPrefix(text, "If ") {
			t.Errorf("%s is not stated as a condition: %q", code, text)
		}
		if text := Text(LangKO, code); !strings.Contains(text, "다면") {
			t.Errorf("%s/ko is not stated as a condition: %q", code, text)
		}
	}
	// Only the place whose prerequisites the page can see ends with the promise.
	for _, executor := range []string{ExecutorHost, ExecutorContainer, ExecutorExternalRunner} {
		page := ConfiguredChecksPage{
			Policy: CheckPolicyView{Saved: true, ConsentActive: true, Executor: executor,
				AllowedEvents: []string{CheckEventPush}},
			Runtime:           CheckRuntimeView{Available: true},
			CheckFile:         CheckFileView{State: CheckFileFound, Checks: 1, Events: []string{CheckEventPush}},
			RunnerTokensKnown: true, ActiveRunnerTokens: 1,
		}
		if got := nextCheckStep(page) == MsgCCNextNone; got != (executor == ExecutorHost) {
			t.Errorf("%s: promises that checks run = %v", executor, got)
		}
	}
	// The repair sentence follows the problem it refers to, so it points up.
	for _, lang := range Langs() {
		text := Text(lang, MsgCCNextRepair)
		if strings.Contains(text, "below") || strings.Contains(text, "아래") {
			t.Errorf("%s: the repair step points below the problem it refers to: %q", lang, text)
		}
	}
	out := render(t, newRenderer(t), cc(LangEN, ccFixtureRuntimeDown, func(*ConfiguredChecksPage) {}))
	problem, next := strings.Index(out, `class="ev__warn" role="alert"`), strings.Index(out, `class="ccnext"`)
	if problem < 0 || next < 0 || problem > next {
		t.Errorf("the runtime problem is not above the next step (problem at %d, next step at %d)", problem, next)
	}
	if !strings.Contains(out, wantText(LangEN, MsgCCNextRepair)) {
		t.Error("a closed runtime does not lead the next step")
	}
}

func TestModeSettingsAreMarkedForTheirMode(t *testing.T) {
	out := render(t, newRenderer(t), cc(LangEN, ccFixtureEnabled, func(p *ConfiguredChecksPage) {}))
	for _, want := range []string{
		`class="ccpanel ccmode ccmode--container"`,
		`class="ccpanel ccmode ccmode--runner"`,
		`class="fset ccmode ccmode--container"`,
		// The copy button does nothing without the script, so the script shows it.
		`data-copy-target="cc-example" hidden`,
		`name="max_timeout_ms_unit"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	sheet := readSheet(t)
	for _, want := range []string{
		`.ccform:not(:has(input[name="executor"][value="container"]:checked)) .ccmode--container`,
		`.ccform:not(:has(input[name="executor"][value="external_runner"]:checked)) .ccmode--runner`,
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("the stylesheet does not hide mode settings with %q", want)
		}
	}
	if !strings.Contains(sheet, "@supports selector(:has(*))") {
		t.Error("mode hiding is not limited to browsers that support :has()")
	}
}

func TestTheFailedContainerHintDoesNotAssertACause(t *testing.T) {
	// An unavailable container job may have failed on Docker, on the image,
	// or before Docker was touched (a commit over a file limit). The page does
	// not know which, so the hint names Docker only as a possibility, sends
	// the reader to the job for the reason, and places the rerun on the job
	// page, which is where the button is.
	for lang, want := range map[Lang]struct{ condition, open, rerun string }{
		LangEN: {"If the cause was Docker", "Open that job", "from its page"},
		LangKO: {"원인이 Docker나 이미지였다면", "작업을 열어 이유를 확인", "작업 페이지에서 다시 실행"},
	} {
		text := Text(lang, MsgCCNextContainerFailed)
		for _, part := range []string{want.condition, want.open, want.rerun} {
			if !strings.Contains(text, part) {
				t.Errorf("%s: the hint lacks %q: %q", lang, part, text)
			}
		}
		// Docker appears only inside the condition.
		if strings.Count(text, "Docker") != 1 {
			t.Errorf("%s: the hint names Docker outside its condition: %q", lang, text)
		}
		for _, claim := range []string{"so its container", "Make sure Docker", "컨테이너를 쓰지 못했습니다", "Docker가 실행 중이고", "list below.", "목록에서 그 체크를 다시"} {
			if strings.Contains(text, claim) {
				t.Errorf("%s: the hint still asserts a cause or the wrong place: %q", lang, text)
			}
		}
	}
}

func TestTheJobLimitWordingMatchesWhatAJobGets(t *testing.T) {
	// The two job limits are ceilings. Without a request in the check file a
	// check gets the workflow defaults, so the words beside the fields and the
	// field explanation must state those defaults, from the parser's own
	// constants, and name the key that changes them.
	output := func(lang Lang) string { return humanLimit(lang, LimitSize, checkworkflow.DefaultOutputLimitBytes) }
	timeout := func(lang Lang) string { return humanLimit(lang, LimitDuration, checkworkflow.DefaultTimeoutMS) }
	for _, lang := range Langs() {
		for code, want := range map[MessageCode][]string{
			MsgCCOutputHelp:    {output(lang), "limits"},
			MsgCCTimeoutHelp:   {timeout(lang), "limits"},
			MsgCCFileKeyLimits: {output(lang), timeout(lang)},
		} {
			for _, part := range want {
				if text := Text(lang, code); !strings.Contains(text, part) {
					t.Errorf("%s/%s does not say %q: %q", code, lang, part, text)
				}
			}
		}
	}
	// The labels name a maximum, not an amount every check keeps.
	for code, want := range map[MessageCode][2]string{
		MsgCCOutput:  {"Most", "최대"},
		MsgCCTimeout: {"Longest", "최대"},
	} {
		if !strings.Contains(Text(LangEN, code), want[0]) || !strings.Contains(Text(LangKO, code), want[1]) {
			t.Errorf("%s does not read as a maximum: %q / %q", code, Text(LangEN, code), Text(LangKO, code))
		}
	}
	// The explanation of limits is on the page with the other keys.
	out := render(t, newRenderer(t), cc(LangEN, ccFixtureEnabled, func(*ConfiguredChecksPage) {}))
	if !strings.Contains(out, wantText(LangEN, MsgCCFileKeyLimits)) {
		t.Error("the page does not explain the limits key")
	}
}

func TestACoreAmountWithTooManyDecimalsNamesCores(t *testing.T) {
	_, err := ParseLimit(LimitCores, LimitInput{"1.2345", UnitCores})
	if code := LimitNoticeCode(LimitCores, err); code != MsgCCNumberCores {
		t.Fatalf("a CPU amount with four decimals is refused with %q", code)
	}
	for _, lang := range Langs() {
		text := Text(lang, MsgCCNumberCores)
		if !strings.Contains(text, "CPU") {
			t.Errorf("%s: the CPU refusal does not name CPU: %q", lang, text)
		}
		for _, wrong := range []string{"millisecond", "byte", "밀리초", "바이트"} {
			if strings.Contains(text, wrong) {
				t.Errorf("%s: the CPU refusal names %q: %q", lang, wrong, text)
			}
		}
	}
	// Times and sizes keep their own message.
	for _, kind := range []LimitKind{LimitDuration, LimitSize} {
		if code := LimitNoticeCode(kind, ErrLimitFraction); code != MsgCCNumberFraction {
			t.Errorf("%s fraction refusal = %q", kind, code)
		}
	}
}
