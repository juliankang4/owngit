package webui

import "errors"

const (
	ActionImportConfigure        = "import_configure"
	ActionImportCredentials      = "import_credentials"
	ActionImportClearCredentials = "import_clear_credentials"
	ActionImportRefresh          = "import_refresh"
	ActionImportCancel           = "import_cancel"
	ActionImportSchedule         = "import_schedule"
	ActionImportResolve          = "import_resolve"
	RepoTabImport                = "import"
)

// ImportRunRow is one credential-free run shown on the import screen.
type ImportRunRow struct {
	ID      string
	Kind    string
	Status  string
	Started string
	Message string
	// ErrorClass is the stable failure class. The page explains it in the
	// reader's language and keeps Message as secondary technical detail.
	ErrorClass string
	RowID      int64
}

// ImportRefRow is one observed ref. The state word is visible text.
type ImportRefRow struct {
	Name   string
	State  string
	Source string
	Local  string
}

// ImportPage renders GET /repositories/{id}/import.
type ImportPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	SelfURL   string
	SubmitURL string
	OlderURL  string

	// Admin is true for an administrator session. Only then does the backend
	// fill the administrator data below (URL, the consents, the credential
	// fields and run messages) and does the page draw the change forms.
	// Everyone else sees the status and each change as a locked link.
	Admin bool
	// AdminLoginURL asks for the administrator password and returns to this
	// tab. SetupURL opens the source form: for an administrator this tab
	// with the form shown, for anyone else the password prompt first.
	AdminLoginURL string
	SetupURL      string
	// Setup shows the source form. It opens on request and stays open after
	// a refused change.
	Setup bool
	// CredentialChoice is the credential form submitted with a refused
	// change, so the same fields are shown again. Empty otherwise.
	CredentialChoice string

	// StatusUnreadable is true when the import service could not read this
	// repository's import status, so the fields below describe nothing.
	StatusUnreadable  bool
	Configured        bool
	URL               string
	Mode              string
	GitOnlyConsent    bool
	PrivateNetwork    bool
	CredentialForm    string
	CredentialBound   bool
	CAPresent         bool
	ContentIncomplete bool
	Unresolved        int
	StagingIssues     int
	// SchedulerFailed and ReconcileFailed report this process's import
	// runtime problems, so they are visible beside the repository's status.
	SchedulerFailed  bool
	ReconcileFailed  bool
	RefsTruncated    bool
	ScheduleEnabled  bool
	ScheduleInterval string
	Refs             []ImportRefRow
	Last             *ImportRunRow
	Active           *ImportRunRow
	// History lists earlier runs. HistoryAvailable is false when they could
	// not be read, which is not the same as no runs.
	History          []ImportRunRow
	HistoryAvailable bool

	// Options is the connection and limits group of the source form.
	// OptionsSummary lists, for an administrator, the options that differ
	// from their defaults, and OptionsProblem says which saved option cannot
	// be used.
	Options        ImportOptionsForm
	OptionsSummary []ImportOptionFact
	OptionsProblem bool
}

// ImportOptionFact is one changed option in the status strip: a message and
// an optional value such as an origin or a count.
type ImportOptionFact struct {
	Code  MessageCode
	Value string
}

func (ImportPage) page() string     { return "import" }
func (p ImportPage) chrome() Chrome { return p.Chrome }

// NewImportPage renders GET /repositories/new-import.
type NewImportPage struct {
	Chrome         Chrome
	SubmitURL      string
	Name           string
	Description    string
	URL            string
	Mode           string
	GitOnlyConsent bool
	PrivateNetwork bool
	// CredentialForm is the submitted credential form, so a refused import
	// shows the same fields again. The secrets themselves are never echoed.
	CredentialForm string
	Options        ImportOptionsForm
}

func (NewImportPage) page() string     { return "new-import" }
func (p NewImportPage) chrome() Chrome { return p.Chrome }

// Redirect choices of an import source, as the form submits them.
const (
	ImportRedirectRefuse     = "refuse"
	ImportRedirectSameOrigin = "same_origin"
	ImportRedirectApproved   = "approved"
)

// ImportOptionsForm is the connection and limits group of a source form.
type ImportOptionsForm struct {
	PlainHTTP      bool
	Redirects      string
	ApprovedOrigin string
	Reserved       bool
	Limits         []ImportLimitControl
	// Open shows the group expanded: an option differs from its default, so
	// a changed URL is never saved without the owner seeing what applies to
	// it, or a refusal points into the group. Refused names the refused
	// field, so its collapsed part opens too.
	Open    bool
	Refused string
}

// MainLimits and DeeperLimits split the limits into the ones most imports
// need and the transport and scan limits drawn collapsed.
func (f ImportOptionsForm) MainLimits() []ImportLimitControl   { return f.limits(false) }
func (f ImportOptionsForm) DeeperLimits() []ImportLimitControl { return f.limits(true) }

func (f ImportOptionsForm) limits(deeper bool) []ImportLimitControl {
	var controls []ImportLimitControl
	for _, control := range f.Limits {
		if control.Deeper == deeper {
			controls = append(controls, control)
		}
	}
	return controls
}

// DeeperOpen shows the deeper limits when one of them is changed or
// refused.
func (f ImportOptionsForm) DeeperOpen() bool {
	for _, control := range f.DeeperLimits() {
		if control.Changed || control.Field == f.Refused {
			return true
		}
	}
	return false
}

// ImportLimitControl is one import limit as the form shows it. Values are
// in the stored unit of the backend: bytes, seconds or a count.
type ImportLimitControl struct {
	importLimitSpec
	Input LimitInput
	// Changed is true when the owner set this limit.
	Changed bool
	// HintEN and HintKO state the allowed range and the default.
	HintEN, HintKO string
}

type importLimitSpec struct {
	// Field is the backend's limit name, also the input name.
	Field  string
	ID     string
	Kind   LimitKind
	Label  MessageCode
	Unit   string
	Deeper bool
}

// UnitField is the name of the input that carries this limit's unit.
func (c ImportLimitControl) UnitField() string { return c.Field + "_unit" }

// InputMode is the keyboard hint for the amount input.
func (c ImportLimitControl) InputMode() string {
	if c.Kind == LimitCount {
		return "numeric"
	}
	return "decimal"
}

var importLimitSpecs = []importLimitSpec{
	{Field: "pack_bytes", ID: "imp-pack", Kind: LimitSize, Label: MsgImportLimitPack, Unit: UnitGB},
	{Field: "run_seconds", ID: "imp-run", Kind: LimitDuration, Label: MsgImportLimitRun, Unit: UnitMinutes},
	{Field: "fetch_seconds", ID: "imp-fetch", Kind: LimitDuration, Label: MsgImportLimitFetch, Unit: UnitMinutes},
	{Field: "index_seconds", ID: "imp-index", Kind: LimitDuration, Label: MsgImportLimitIndex, Unit: UnitMinutes},
	{Field: "verify_seconds", ID: "imp-verify", Kind: LimitDuration, Label: MsgImportLimitVerify, Unit: UnitMinutes},
	{Field: "refs", ID: "imp-refs", Kind: LimitCount, Label: MsgImportLimitRefs},
	{Field: "advertisement_bytes", ID: "imp-adv", Kind: LimitSize, Label: MsgImportLimitAdvertisement, Unit: UnitMB, Deeper: true},
	{Field: "tls_handshake_seconds", ID: "imp-tls", Kind: LimitDuration, Label: MsgImportLimitTLS, Unit: UnitSeconds, Deeper: true},
	{Field: "response_header_seconds", ID: "imp-headers", Kind: LimitDuration, Label: MsgImportLimitHeaders, Unit: UnitSeconds, Deeper: true},
	{Field: "lfs_objects", ID: "imp-lfs", Kind: LimitCount, Label: MsgImportLimitLFS, Deeper: true},
}

// formScale converts a stored import limit to the form's base unit: a
// duration form field counts milliseconds, the backend seconds.
func (s importLimitSpec) formScale() int64 {
	if s.Kind == LimitDuration {
		return 1000
	}
	return 1
}

// ImportLimitFieldNames lists every import limit the form carries.
func ImportLimitFieldNames() []string {
	names := make([]string, 0, len(importLimitSpecs))
	for _, spec := range importLimitSpecs {
		names = append(names, spec.Field)
	}
	return names
}

// ImportLimitBounds is the range and default of one import limit, in its
// stored unit, as the backend enforces them.
type ImportLimitBounds struct {
	Min, Max, Default int64
}

// NewImportLimitControls draws every import limit. saved holds the limits
// the owner set (absent or zero means the default); posted, when not nil,
// holds a refused submission to show again as typed.
func NewImportLimitControls(saved map[string]int64, posted map[string]LimitInput, bounds map[string]ImportLimitBounds) []ImportLimitControl {
	controls := make([]ImportLimitControl, 0, len(importLimitSpecs))
	for _, spec := range importLimitSpecs {
		control := ImportLimitControl{importLimitSpec: spec, Changed: saved[spec.Field] != 0}
		control.Input = FormatLimit(spec.Kind, saved[spec.Field]*spec.formScale(), spec.Unit)
		if input, present := posted[spec.Field]; present {
			control.Input = input
		}
		if limit, known := bounds[spec.Field]; known {
			scale := spec.formScale()
			control.HintEN = "Default: " + humanLimit(LangEN, spec.Kind, limit.Default*scale) + ". Allowed: " +
				humanLimit(LangEN, spec.Kind, limit.Min*scale) + " to " + humanLimit(LangEN, spec.Kind, limit.Max*scale) + "."
			control.HintKO = "기본값: " + humanLimit(LangKO, spec.Kind, limit.Default*scale) + ". 허용 범위: " +
				humanLimit(LangKO, spec.Kind, limit.Min*scale) + " ~ " + humanLimit(LangKO, spec.Kind, limit.Max*scale) + "."
		}
		controls = append(controls, control)
	}
	return controls
}

// ParseImportLimit reads one posted limit in its stored unit. An empty
// amount is zero: the limit returns to its default.
func ParseImportLimit(field string, input LimitInput) (int64, error) {
	for _, spec := range importLimitSpecs {
		if spec.Field != field {
			continue
		}
		value, err := ParseLimit(spec.Kind, input)
		if err != nil {
			return 0, err
		}
		if value%spec.formScale() != 0 {
			return 0, ErrLimitFraction
		}
		return value / spec.formScale(), nil
	}
	return 0, ErrLimitSyntax
}

// ImportLimitNotice is the message for a refused posted limit.
func ImportLimitNotice(field string, err error) MessageCode {
	for _, spec := range importLimitSpecs {
		if spec.Field == field {
			if errors.Is(err, ErrLimitFraction) && spec.Kind == LimitDuration {
				return MsgImportLimitWholeSeconds
			}
			return LimitNoticeCode(spec.Kind, err)
		}
	}
	return MsgCCNumberInvalid
}
