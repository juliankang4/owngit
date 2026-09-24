package webui

import (
	"errors"
	"html/template"
	"math/big"
	"strconv"
	"strings"
)

// Human units for the numeric policy fields.
//
// The backend stores every limit in its base unit: milliseconds, bytes,
// millicores, or a plain count. People think in minutes and megabytes, so the
// editor shows an amount and a unit, and this file converts between the two.
//
// The conversion is exact. An amount is read as a decimal and multiplied by
// the unit with arbitrary precision, so "1.5 MB" is exactly 1572864 bytes, and
// an amount that does not come out to a whole number of the base unit, or that
// does not fit the stored integer, is refused instead of being rounded. A
// stored value is shown in the largest unit that holds it exactly, so saving
// the page again without edits resubmits the same number.
//
// Accepted ranges stay the backend's. This file only changes how a number is
// written, never which numbers are allowed.

// LimitKind says what a numeric policy field measures.
type LimitKind string

const (
	// LimitDuration is stored in milliseconds.
	LimitDuration LimitKind = "duration"
	// LimitSize is stored in bytes. KB, MB, and GB are powers of 1024.
	LimitSize LimitKind = "size"
	// LimitCores is stored in thousandths of a CPU core.
	LimitCores LimitKind = "cores"
	// LimitCount is a plain whole number.
	LimitCount LimitKind = "count"
)

// Unit names a form submits. An empty unit means the stored base unit, which
// keeps a raw number posted without a unit (by a script or an older page)
// meaning exactly what it always meant.
const (
	UnitSeconds = "s"
	UnitMinutes = "min"
	UnitHours   = "h"
	UnitBytes   = "B"
	UnitKB      = "KB"
	UnitMB      = "MB"
	UnitGB      = "GB"
	UnitCores   = "cores"
)

// LimitUnit is one unit a field can be written in.
type LimitUnit struct {
	Name   string
	Factor int64
	// Label is the option text in the unit menu.
	Label MessageCode
	// one and many are the words used in running text, with %s standing for
	// the amount.
	one, many MessageCode
}

var (
	durationUnits = []LimitUnit{
		{Name: UnitSeconds, Factor: 1000, Label: MsgCCUnitSeconds, one: MsgCCAmountSecond, many: MsgCCAmountSeconds},
		{Name: UnitMinutes, Factor: 60 * 1000, Label: MsgCCUnitMinutes, one: MsgCCAmountMinute, many: MsgCCAmountMinutes},
		{Name: UnitHours, Factor: 60 * 60 * 1000, Label: MsgCCUnitHours, one: MsgCCAmountHour, many: MsgCCAmountHours},
	}
	sizeUnits = []LimitUnit{
		{Name: UnitBytes, Factor: 1, Label: MsgCCUnitBytes, one: MsgCCAmountByte, many: MsgCCAmountBytes},
		{Name: UnitKB, Factor: 1 << 10, Label: MsgCCUnitKB, one: MsgCCAmountKB, many: MsgCCAmountKB},
		{Name: UnitMB, Factor: 1 << 20, Label: MsgCCUnitMB, one: MsgCCAmountMB, many: MsgCCAmountMB},
		{Name: UnitGB, Factor: 1 << 30, Label: MsgCCUnitGB, one: MsgCCAmountGB, many: MsgCCAmountGB},
	}
	coreUnits = []LimitUnit{
		{Name: UnitCores, Factor: 1000, Label: MsgCCUnitCores, one: MsgCCAmountCore, many: MsgCCAmountCores},
	}
	countUnits = []LimitUnit{{Name: "", Factor: 1}}
)

// Units lists the units a kind can be written in, smallest first.
func (k LimitKind) Units() []LimitUnit {
	switch k {
	case LimitDuration:
		return durationUnits
	case LimitSize:
		return sizeUnits
	case LimitCores:
		return coreUnits
	default:
		return countUnits
	}
}

// HasUnitMenu reports whether the field offers a choice of unit. A count has
// no unit and a core amount has only one, so neither draws a menu.
func (k LimitKind) HasUnitMenu() bool { return len(k.Units()) > 1 }

// FixedUnit is the single unit of a kind without a menu, or nil.
func (k LimitKind) FixedUnit() *LimitUnit {
	units := k.Units()
	if len(units) != 1 || units[0].Name == "" {
		return nil
	}
	return &units[0]
}

func (k LimitKind) unit(name string) (LimitUnit, bool) {
	if name == "" {
		return LimitUnit{Factor: 1}, true
	}
	for _, unit := range k.Units() {
		if unit.Name == name {
			return unit, true
		}
	}
	return LimitUnit{}, false
}

// LimitInput is one numeric field as the form carries it: the amount as typed
// and the selected unit. It is text on purpose, so a refused submission shows
// exactly what was typed.
type LimitInput struct {
	Amount string
	Unit   string
}

// FormatLimit writes a stored value as an amount and unit.
//
// The largest unit that holds the value exactly is chosen, so the amount has
// no decimals whenever that is possible. A value no unit holds exactly (1500
// milliseconds, 2500 millicores) uses the smallest unit with an exact decimal.
// Zero is an empty amount: the field was left to its default.
func FormatLimit(kind LimitKind, value int64, preferred string) LimitInput {
	if value == 0 {
		if preferred == "" {
			preferred = kind.Units()[0].Name
		}
		return LimitInput{Unit: preferred}
	}
	unit := bestUnit(kind, value)
	return LimitInput{Amount: exactDecimal(value, unit.Factor), Unit: unit.Name}
}

func bestUnit(kind LimitKind, value int64) LimitUnit {
	units := kind.Units()
	for i := len(units) - 1; i >= 0; i-- {
		if value%units[i].Factor == 0 {
			return units[i]
		}
	}
	return units[0]
}

// exactDecimal writes value/factor as a decimal. Every factor used here is a
// power of 1024 or a multiple of 1000, and bestUnit only picks a factor that
// does not divide the value when that factor is a power of ten, so the
// division always terminates within three places.
func exactDecimal(value, factor int64) string {
	whole := new(big.Rat).SetFrac(big.NewInt(value), big.NewInt(factor))
	text := whole.FloatString(3)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return text
}

// Errors ParseLimit returns. They are distinct so each gets its own message.
var (
	// ErrLimitSyntax means the amount is not a plain decimal number, or the
	// unit is not one this field offers.
	ErrLimitSyntax = errors.New("limit is not a number")
	// ErrLimitFraction means the amount does not come out to a whole number
	// of the stored unit.
	ErrLimitFraction = errors.New("limit is not a whole number of the stored unit")
	// ErrLimitOverflow means the amount is larger than any stored value.
	ErrLimitOverflow = errors.New("limit is too large")
)

// The largest stored value has 19 digits, and no unit factor is below one, so
// a whole part with more significant digits is out of range. Fraction digits
// are bounded so that the exact arithmetic below stays cheap for any input.
const (
	maximumLimitWholeDigits    = 19
	maximumLimitFractionDigits = 40
)

// ParseLimit converts an amount and unit into the stored value.
//
// An empty amount is zero, which the backend reads as "use the default" where
// a default exists and refuses where one does not. The amount is plain decimal
// digits with at most one decimal point; signs, exponents, and separators are
// refused rather than guessed at.
func ParseLimit(kind LimitKind, input LimitInput) (int64, error) {
	amount := strings.TrimSpace(input.Amount)
	if amount == "" {
		return 0, nil
	}
	unit, known := kind.unit(strings.TrimSpace(input.Unit))
	if !known {
		return 0, ErrLimitSyntax
	}
	whole, fraction, _ := strings.Cut(amount, ".")
	if (whole == "" && fraction == "") || !allDigits(whole) || !allDigits(fraction) {
		return 0, ErrLimitSyntax
	}
	// Only significant digits count toward the bounds, so padding with zeros
	// never changes the result. A long number is out of range, not unreadable.
	whole = strings.TrimLeft(whole, "0")
	fraction = strings.TrimRight(fraction, "0")
	if len(whole) > maximumLimitWholeDigits {
		return 0, ErrLimitOverflow
	}
	if len(fraction) > maximumLimitFractionDigits {
		return 0, ErrLimitFraction
	}
	if whole == "" && fraction == "" {
		return 0, nil
	}
	digits, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return 0, ErrLimitSyntax
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction))), nil)
	scaled := new(big.Int).Mul(digits, big.NewInt(unit.Factor))
	quotient, remainder := new(big.Int).QuoRem(scaled, scale, new(big.Int))
	if remainder.Sign() != 0 {
		return 0, ErrLimitFraction
	}
	if !quotient.IsInt64() {
		return 0, ErrLimitOverflow
	}
	return quotient.Int64(), nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// humanLimit writes a stored value in running text, such as "10 minutes" or
// "64 MB", in one language.
func humanLimit(lang Lang, kind LimitKind, value int64) string {
	unit := bestUnit(kind, value)
	amount := exactDecimal(value, unit.Factor)
	if kind == LimitCount || unit.Name == "" {
		return formatNumber64(value)
	}
	if !strings.Contains(amount, ".") {
		if parsed, err := strconv.ParseInt(amount, 10, 64); err == nil {
			amount = formatNumber64(parsed)
		}
	}
	words := unit.many
	if amount == "1" {
		words = unit.one
	}
	return strings.ReplaceAll(Text(lang, words), "%s", amount)
}

func formatNumber64(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// biLimit renders a stored value in running text in both languages.
func biLimit(lang Lang, kind LimitKind, value int64) template.HTML {
	return biText(lang, humanLimit(LangEN, kind, value), humanLimit(LangKO, kind, value))
}

// Groups the numeric policy fields are drawn in.
const (
	// LimitGroupRun holds the limits every mode uses for one job.
	LimitGroupRun = "run"
	// LimitGroupSource holds the bounds on the files copied for a job.
	LimitGroupSource = "source"
	// LimitGroupContainer holds the resources of the container mode only.
	LimitGroupContainer = "container"
)

// PolicyLimitField describes one numeric control of the policy editor.
type PolicyLimitField struct {
	// Field is the backend's field name, which is also the input name and the
	// name a refusal reports.
	Field string
	// ID is the element id of the amount input.
	ID    string
	Group string
	Kind  LimitKind
	Label MessageCode
	// Help is an optional one-line explanation.
	Help MessageCode
	// Unit is the unit an empty field starts with.
	Unit string
}

// UnitField is the name of the input that carries this field's unit.
func (l PolicyLimitField) UnitField() string { return l.Field + "_unit" }

// InputMode is the keyboard hint for the amount input.
func (l PolicyLimitField) InputMode() string {
	if l.Kind == LimitCount {
		return "numeric"
	}
	return "decimal"
}

var policyLimitFields = []PolicyLimitField{
	{Field: PolicyFieldMaxTimeoutMS, ID: "cc-timeout", Group: LimitGroupRun, Kind: LimitDuration,
		Label: MsgCCTimeout, Help: MsgCCTimeoutHelp, Unit: UnitMinutes},
	{Field: PolicyFieldMaxOutputLimitBytes, ID: "cc-output", Group: LimitGroupRun, Kind: LimitSize,
		Label: MsgCCOutput, Help: MsgCCOutputHelp, Unit: UnitKB},
	{Field: PolicyFieldQueueLimit, ID: "cc-queue", Group: LimitGroupRun, Kind: LimitCount,
		Label: MsgCCQueue, Help: MsgCCQueueHelp},
	{Field: PolicyFieldMaxActiveJobs, ID: "cc-active", Group: LimitGroupRun, Kind: LimitCount,
		Label: MsgCCActive},
	{Field: PolicyFieldMaxLeaseMS, ID: "cc-lease", Group: LimitGroupRun, Kind: LimitDuration,
		Label: MsgCCLease, Help: MsgCCLeaseHelp, Unit: UnitSeconds},

	{Field: PolicyFieldSourceMaxEntries, ID: "cc-src-entries", Group: LimitGroupSource, Kind: LimitCount,
		Label: MsgCCSrcEntries},
	{Field: PolicyFieldSourceMaxFileBytes, ID: "cc-src-file", Group: LimitGroupSource, Kind: LimitSize,
		Label: MsgCCSrcFileBytes, Unit: UnitMB},
	{Field: PolicyFieldSourceMaxTotalBytes, ID: "cc-src-total", Group: LimitGroupSource, Kind: LimitSize,
		Label: MsgCCSrcTotalBytes, Unit: UnitMB},
	{Field: PolicyFieldSourceMaxPathDepth, ID: "cc-src-depth", Group: LimitGroupSource, Kind: LimitCount,
		Label: MsgCCSrcDepth},
	{Field: PolicyFieldSourceMaxPathBytes, ID: "cc-src-path", Group: LimitGroupSource, Kind: LimitCount,
		Label: MsgCCSrcPathBytes},
	{Field: PolicyFieldSourceMaxNameBytes, ID: "cc-src-name", Group: LimitGroupSource, Kind: LimitCount,
		Label: MsgCCSrcNameBytes},
	{Field: PolicyFieldSourceMetadataLimit, ID: "cc-src-meta", Group: LimitGroupSource, Kind: LimitSize,
		Label: MsgCCSrcMetaBytes, Help: MsgCCSrcMetaHelp, Unit: UnitMB},

	{Field: PolicyFieldContainerCPUMillis, ID: "cc-cpu", Group: LimitGroupContainer, Kind: LimitCores,
		Label: MsgCCCPU, Unit: UnitCores},
	{Field: PolicyFieldContainerMemoryBytes, ID: "cc-memory", Group: LimitGroupContainer, Kind: LimitSize,
		Label: MsgCCMemory, Unit: UnitMB},
	{Field: PolicyFieldContainerPIDs, ID: "cc-pids", Group: LimitGroupContainer, Kind: LimitCount,
		Label: MsgCCPIDs},
	{Field: PolicyFieldContainerScratchBytes, ID: "cc-scratch", Group: LimitGroupContainer, Kind: LimitSize,
		Label: MsgCCScratch, Unit: UnitMB},
}

// PolicyLimitFields lists every numeric control, in the order drawn.
func PolicyLimitFields() []PolicyLimitField {
	return append([]PolicyLimitField(nil), policyLimitFields...)
}

// PolicyLimitFor finds one numeric control by its backend field name.
func PolicyLimitFor(field string) (PolicyLimitField, bool) {
	for _, limit := range policyLimitFields {
		if limit.Field == field {
			return limit, true
		}
	}
	return PolicyLimitField{}, false
}

// limitFields lists the controls of one group, for the template.
func limitFields(group string) []PolicyLimitField {
	var fields []PolicyLimitField
	for _, limit := range policyLimitFields {
		if limit.Group == group {
			fields = append(fields, limit)
		}
	}
	return fields
}

// LimitNoticeCode is the message for a ParseLimit error on one field.
func LimitNoticeCode(kind LimitKind, err error) MessageCode {
	switch {
	case errors.Is(err, ErrLimitOverflow):
		return MsgCCFieldRange
	case errors.Is(err, ErrLimitFraction):
		switch kind {
		case LimitCount:
			return MsgCCNumberWhole
		case LimitCores:
			return MsgCCNumberCores
		}
		return MsgCCNumberFraction
	default:
		return MsgCCNumberInvalid
	}
}

// biOption carries both languages on an option element. An option holds only
// text, so the language switch has to find the words on the element itself.
func biOption(code MessageCode) template.HTMLAttr {
	en, ko := Text(LangEN, code), Text(LangKO, code)
	if en == ko {
		return ""
	}
	return template.HTMLAttr(`data-en="` + template.HTMLEscapeString(en) + `" data-ko="` + template.HTMLEscapeString(ko) + `"`)
}

// unitMenuLabel names a unit menu after the field it belongs to.
func unitMenuLabel(lang Lang, label MessageCode) template.HTMLAttr {
	return biAttrText(lang, "aria-label",
		Text(LangEN, label)+", "+Text(LangEN, MsgCCUnitWord),
		Text(LangKO, label)+" "+Text(LangKO, MsgCCUnitWord))
}

// fieldDefault states the value an empty field uses. A field without a
// default renders nothing, because leaving it empty is refused.
func fieldDefault(lang Lang, defaults map[string]int64, field string) template.HTML {
	value := defaults[field]
	limit, known := PolicyLimitFor(field)
	if value == 0 || !known {
		return ""
	}
	return biText(lang,
		strings.ReplaceAll(Text(LangEN, MsgCCDefaultIs), "%s", humanLimit(LangEN, limit.Kind, value)),
		strings.ReplaceAll(Text(LangKO, MsgCCDefaultIs), "%s", humanLimit(LangKO, limit.Kind, value)))
}
