package state

// Legacy direct review records.
//
// The built-in provider review feature was removed after it had written
// durable rows, so these types, their validators, and their digest facts stay:
// an old database and an old offline backup must still decode, validate, and
// restore. The digest fact structures and their JSON tags are immutable,
// because genuine historical records are checked against them.
//
// What is deliberately absent is the runtime lifecycle: nothing here decides
// when a row is written, which connection it belongs to, or whether a provider
// may be called.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DirectReviewProtocolOpenAIResponses = "openai_responses"
	DirectReviewProtocolAnthropic       = "anthropic_messages"
	DirectReviewProtocolCompatibleChat  = "compatible_chat_completions"

	DirectReviewAuthenticationStored = "stored_credential"
	DirectReviewAuthenticationNone   = "none"

	DirectReviewTriggerManual        = "manual"
	DirectReviewTriggerAutomaticPR   = "automatic_pr"
	DirectReviewTriggerAutomaticTask = "automatic_task"

	DirectReviewPhaseObserved  = "observed"
	DirectReviewPhasePreparing = "preparing"
	DirectReviewPhaseRunning   = "running"
	DirectReviewPhaseTerminal  = "terminal"

	DirectReviewDiffTwoCommit = "two_commit"

	DirectReviewOutcomeContextUnavailable                = "context_unavailable"
	DirectReviewOutcomeCancelledBeforeSubmission         = "cancelled_before_submission"
	DirectReviewOutcomeInterruptedBeforeSubmission       = "interrupted_before_submission"
	DirectReviewOutcomeInterruptedMayHaveSubmitted       = "interrupted_may_have_submitted"
	DirectReviewOutcomeCapabilityUnavailable             = "capability_unavailable"
	DirectReviewOutcomeCapacityUnavailable               = "capacity_unavailable"
	MaximumDirectReviewCredentialBytes                   = 16 << 10
	MaximumDirectReviewEndpointBytes                     = 2048
	MaximumDirectReviewModelBytes                        = 200
	MaximumDirectReviewInstructionVersionBytes           = 100
	MaximumDirectReviewLimitsJSONBytes                   = 4096
	MaximumDirectReviewResultJSONBytes                   = 1 << 20
	MaximumDirectReviewProviderRequestBytes        int64 = 8 << 20
	MaximumDirectReviewProviderContextBytes        int64 = 8 << 20
	MaximumDirectReviewProviderResponseBytes       int64 = 2 << 20
	MaximumDirectReviewProviderToolOutputBytes     int64 = 1 << 20
	MaximumDirectReviewProviderToolRounds                = 32
	MaximumDirectReviewProviderToolCalls                 = 128
	MaximumDirectReviewProviderOutputTokens              = 1_000_000
	MaximumDirectReviewProviderDurationMS          int64 = 30 * 60 * 1000
	MaximumDirectReviewInitialContextBytes         int64 = 4 << 20
	MaximumDirectReviewDirectoryBytes              int64 = 4 << 20
	MaximumDirectReviewFileChunkBytes              int64 = 1 << 20
	MaximumDirectReviewFilePrefixBytes             int64 = 16 << 20
	MaximumDirectReviewRepositoryToolOutputBytes   int64 = 1 << 20
	MaximumDirectReviewOperationDurationMS         int64 = 5 * 60 * 1000
	MaximumDirectReviewFindings                          = 64
	MaximumDirectReviewFindingTitleBytes                 = 1024
	MaximumDirectReviewFindingDetailBytes                = 8 << 10
	MaximumDirectReviewPathBytes                         = 4096
	MaximumDirectReviewCoveragePaths                     = 256
	MaximumDirectReviewToolArgumentBytes                 = 8 << 10
)

// DirectReviewProviderLimits are stored in milliseconds and bytes so JSON and
// SQLite never depend on time.Duration's nanosecond encoding.
type DirectReviewProviderLimits struct {
	MaxRequestBytes    int64 `json:"max_request_bytes"`
	MaxContextBytes    int64 `json:"max_context_bytes"`
	MaxResponseBytes   int64 `json:"max_response_bytes"`
	MaxToolOutputBytes int64 `json:"max_tool_output_bytes"`
	MaxToolRounds      int   `json:"max_tool_rounds"`
	MaxToolCalls       int   `json:"max_tool_calls"`
	MaxOutputTokens    int   `json:"max_output_tokens"`
	MaxDurationMS      int64 `json:"max_duration_ms"`
}

type DirectReviewRepositoryLimits struct {
	MaxInitialContextBytes int64 `json:"max_initial_context_bytes"`
	MaxDirectoryBytes      int64 `json:"max_directory_bytes"`
	MaxFileChunkBytes      int64 `json:"max_file_chunk_bytes"`
	MaxFilePrefixBytes     int64 `json:"max_file_prefix_bytes"`
	MaxToolOutputBytes     int64 `json:"max_tool_output_bytes"`
	MaxOperationDurationMS int64 `json:"max_operation_duration_ms"`
}

type DirectReviewSettings struct {
	RepositoryID         string                       `json:"repository_id"`
	ConfigurationVersion int64                        `json:"configuration_version"`
	Protocol             string                       `json:"protocol"`
	Endpoint             string                       `json:"endpoint"`
	Model                string                       `json:"model"`
	AuthenticationMode   string                       `json:"authentication_mode"`
	ProviderLimits       DirectReviewProviderLimits   `json:"provider_limits"`
	RepositoryLimits     DirectReviewRepositoryLimits `json:"repository_limits"`
	InstructionVersion   string                       `json:"instruction_version"`
	CredentialID         string                       `json:"-"`
	CredentialAttached   bool                         `json:"credential_attached"`
	ConnectionVersion    int64                        `json:"connection_version"`
	// AuthorityEpoch is local, nonsecret authority. It is replaced on restore
	// and is never portable or returned by the API.
	AuthorityEpoch       string     `json:"-"`
	ProbeFingerprint     string     `json:"probe_fingerprint,omitempty"`
	ProbeRequestID       string     `json:"probe_request_id,omitempty"`
	ProbeUpdatedAt       *time.Time `json:"probe_updated_at,omitempty"`
	AutomaticPREnabled   bool       `json:"automatic_pr_enabled"`
	AutomaticTaskEnabled bool       `json:"automatic_task_enabled"`
	AutomaticConsent     bool       `json:"automatic_consent_active"`
	AutomaticConsentVer  int64      `json:"automatic_consent_version"`
	AutomaticConsentHash string     `json:"automatic_consent_digest,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type DirectReviewCredential struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	Label        string    `json:"label"`
	Value        string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type DirectReviewProbe struct {
	RequestID             string                     `json:"request_id"`
	RepositoryID          string                     `json:"repository_id"`
	RegistrationDigest    string                     `json:"registration_digest"`
	CapabilityFingerprint string                     `json:"capability_fingerprint"`
	ConfigurationVersion  int64                      `json:"configuration_version"`
	ConnectionVersion     int64                      `json:"connection_version"`
	ConnectionFingerprint string                     `json:"connection_fingerprint"`
	Protocol              string                     `json:"protocol"`
	Endpoint              string                     `json:"endpoint"`
	Model                 string                     `json:"model"`
	AuthenticationMode    string                     `json:"authentication_mode"`
	ProviderLimits        DirectReviewProviderLimits `json:"provider_limits"`
	DisclosureVersion     string                     `json:"disclosure_version"`
	DisclosureDigest      string                     `json:"disclosure_digest"`
	Phase                 string                     `json:"phase"`
	CancelRequestedAt     *time.Time                 `json:"cancel_requested_at,omitempty"`
	CreatedAt             time.Time                  `json:"created_at"`
	RunningAt             *time.Time                 `json:"running_at,omitempty"`
	TerminalAt            *time.Time                 `json:"terminal_at,omitempty"`
	Result                *DirectReviewResult        `json:"result,omitempty"`
}

type DirectReviewTaskContext struct {
	ContextID          string    `json:"context_id"`
	RepositoryID       string    `json:"repository_id"`
	TaskID             string    `json:"task_id"`
	AttemptID          string    `json:"attempt_id"`
	CredentialID       string    `json:"credential_id"`
	BaseOID            string    `json:"base_oid"`
	HeadOID            string    `json:"head_oid"`
	PullRequestNumber  int64     `json:"pull_request_number,omitempty"`
	ObservedSourceOID  string    `json:"observed_source_oid,omitempty"`
	ObservedTargetOID  string    `json:"observed_target_oid,omitempty"`
	RegistrationDigest string    `json:"registration_digest"`
	CreatedAt          time.Time `json:"created_at"`
}

type DirectReviewRequest struct {
	Sequence                int64                        `json:"sequence"`
	RequestID               string                       `json:"request_id"`
	RepositoryID            string                       `json:"repository_id"`
	TriggerKind             string                       `json:"trigger_kind"`
	SourceEventKey          string                       `json:"source_event_key,omitempty"`
	PullRequestNumber       int64                        `json:"pull_request_number,omitempty"`
	TaskID                  string                       `json:"task_id,omitempty"`
	AttemptID               string                       `json:"attempt_id,omitempty"`
	ObservedSourceOID       string                       `json:"observed_source_oid,omitempty"`
	ObservedTargetOID       string                       `json:"observed_target_oid,omitempty"`
	EffectiveBaseOID        string                       `json:"effective_base_oid,omitempty"`
	EffectiveHeadOID        string                       `json:"effective_head_oid,omitempty"`
	DiffMode                string                       `json:"diff_mode,omitempty"`
	ConfigurationVersion    int64                        `json:"configuration_version"`
	ConnectionVersion       int64                        `json:"connection_version"`
	ConnectionFingerprint   string                       `json:"connection_fingerprint"`
	Protocol                string                       `json:"protocol"`
	Endpoint                string                       `json:"endpoint"`
	Model                   string                       `json:"model"`
	AuthenticationMode      string                       `json:"authentication_mode"`
	ProviderLimits          DirectReviewProviderLimits   `json:"provider_limits"`
	RepositoryLimits        DirectReviewRepositoryLimits `json:"repository_limits"`
	InstructionVersion      string                       `json:"instruction_version"`
	ConsentVersion          string                       `json:"consent_version,omitempty"`
	ConsentDigest           string                       `json:"consent_digest,omitempty"`
	DisclosureVersion       string                       `json:"disclosure_version"`
	DisclosureDigest        string                       `json:"disclosure_digest"`
	RegistrationDigest      string                       `json:"registration_digest"`
	Phase                   string                       `json:"phase"`
	CancelRequestedAt       *time.Time                   `json:"cancel_requested_at,omitempty"`
	CreatedAt               time.Time                    `json:"created_at"`
	PreparingAt             *time.Time                   `json:"preparing_at,omitempty"`
	RunningAt               *time.Time                   `json:"running_at,omitempty"`
	TerminalAt              *time.Time                   `json:"terminal_at,omitempty"`
	InitialContextBytes     int64                        `json:"initial_context_bytes"`
	InitialContextTruncated bool                         `json:"initial_context_truncated"`
	Result                  *DirectReviewResult          `json:"result,omitempty"`
}

type DirectReviewFinding struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
}

type DirectReviewUsage struct {
	Request                  int    `json:"request"`
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	TotalTokens              *int64 `json:"total_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
}

type DirectReviewCoverage struct {
	Complete   bool     `json:"complete"`
	Reasons    []string `json:"reasons,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	ToolRounds int      `json:"tool_rounds"`
	ToolCalls  int      `json:"tool_calls"`
}

type DirectReviewToolCall struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Arguments   string   `json:"arguments,omitempty"`
	Invoked     bool     `json:"invoked"`
	Outcome     string   `json:"outcome"`
	Paths       []string `json:"paths,omitempty"`
	OutputBytes int64    `json:"output_bytes"`
}

type DirectReviewResult struct {
	Outcome                 string                 `json:"outcome"`
	Reason                  string                 `json:"reason,omitempty"`
	RequestedModel          string                 `json:"requested_model,omitempty"`
	ReportedModels          []string               `json:"reported_models,omitempty"`
	Findings                []DirectReviewFinding  `json:"findings,omitempty"`
	Usage                   []DirectReviewUsage    `json:"usage,omitempty"`
	Coverage                DirectReviewCoverage   `json:"coverage"`
	ToolCalls               []DirectReviewToolCall `json:"tool_calls,omitempty"`
	Requests                int                    `json:"requests"`
	ContextBytes            int64                  `json:"context_bytes"`
	HTTPStatus              int                    `json:"http_status,omitempty"`
	InitialContextBytes     int64                  `json:"initial_context_bytes"`
	InitialContextTruncated bool                   `json:"initial_context_truncated"`
	EvidenceTruncated       bool                   `json:"evidence_truncated"`
	ErrorCode               string                 `json:"error_code,omitempty"`
}

func validateDirectReviewSettings(settings DirectReviewSettings, requireTimes bool) error {
	if settings.RepositoryID == "" || settings.ConfigurationVersion <= 0 || settings.ConnectionVersion < 0 || !validDirectReviewID(settings.AuthorityEpoch) {
		return errors.New("invalid direct review settings identity")
	}
	if !validDirectReviewProtocol(settings.Protocol) || !validDirectReviewEndpoint(settings.Endpoint) || !validDirectReviewText(settings.Model, MaximumDirectReviewModelBytes, false) {
		return errors.New("invalid direct review provider configuration")
	}
	if settings.AuthenticationMode != DirectReviewAuthenticationStored && settings.AuthenticationMode != DirectReviewAuthenticationNone {
		return errors.New("invalid direct review authentication mode")
	}
	if settings.AuthenticationMode == DirectReviewAuthenticationNone && settings.CredentialID != "" {
		return errors.New("credential-free direct review settings contain an attachment")
	}
	if settings.CredentialID != "" && !validDirectReviewID(settings.CredentialID) {
		return errors.New("invalid direct review credential attachment")
	}
	if settings.CredentialAttached != (settings.CredentialID != "") {
		return errors.New("inconsistent direct review credential attachment")
	}
	if err := validateDirectReviewLimits(settings.ProviderLimits, settings.RepositoryLimits); err != nil {
		return err
	}
	if !validDirectReviewText(settings.InstructionVersion, MaximumDirectReviewInstructionVersionBytes, false) {
		return errors.New("invalid direct review instruction version")
	}
	if settings.ProbeFingerprint != "" && (!validDirectReviewDigest(settings.ProbeFingerprint) || !validDirectReviewID(settings.ProbeRequestID) || settings.ProbeUpdatedAt == nil || settings.ProbeUpdatedAt.IsZero()) {
		return errors.New("invalid direct review probe authority")
	}
	if settings.ProbeFingerprint == "" && (settings.ProbeRequestID != "" || settings.ProbeUpdatedAt != nil) {
		return errors.New("incomplete direct review probe authority")
	}
	if settings.AutomaticPREnabled || settings.AutomaticTaskEnabled || settings.AutomaticConsent || settings.AutomaticConsentVer != 0 || settings.AutomaticConsentHash != "" {
		return errors.New("automatic direct review authority is not enabled in this checkpoint")
	}
	if requireTimes && (settings.CreatedAt.IsZero() || settings.UpdatedAt.IsZero() || settings.UpdatedAt.Before(settings.CreatedAt)) {
		return errors.New("invalid direct review settings time")
	}
	return nil
}

func validateDirectReviewLimits(provider DirectReviewProviderLimits, repository DirectReviewRepositoryLimits) error {
	if provider.MaxRequestBytes <= 0 || provider.MaxRequestBytes > MaximumDirectReviewProviderRequestBytes ||
		provider.MaxContextBytes <= 0 || provider.MaxContextBytes > MaximumDirectReviewProviderContextBytes ||
		provider.MaxContextBytes > provider.MaxRequestBytes ||
		provider.MaxResponseBytes <= 0 || provider.MaxResponseBytes > MaximumDirectReviewProviderResponseBytes ||
		provider.MaxToolOutputBytes <= 0 || provider.MaxToolOutputBytes > MaximumDirectReviewProviderToolOutputBytes ||
		provider.MaxToolRounds <= 0 || provider.MaxToolRounds > MaximumDirectReviewProviderToolRounds ||
		provider.MaxToolCalls <= 0 || provider.MaxToolCalls > MaximumDirectReviewProviderToolCalls ||
		provider.MaxToolCalls < provider.MaxToolRounds ||
		provider.MaxOutputTokens <= 0 || provider.MaxOutputTokens > MaximumDirectReviewProviderOutputTokens ||
		provider.MaxDurationMS <= 0 || provider.MaxDurationMS > MaximumDirectReviewProviderDurationMS {
		return errors.New("invalid direct review provider limits")
	}
	if repository.MaxInitialContextBytes <= 0 || repository.MaxInitialContextBytes > MaximumDirectReviewInitialContextBytes ||
		repository.MaxInitialContextBytes > provider.MaxContextBytes ||
		repository.MaxDirectoryBytes <= 0 || repository.MaxDirectoryBytes > MaximumDirectReviewDirectoryBytes ||
		repository.MaxFileChunkBytes < utf8.UTFMax || repository.MaxFileChunkBytes > MaximumDirectReviewFileChunkBytes ||
		repository.MaxFilePrefixBytes <= 0 || repository.MaxFilePrefixBytes > MaximumDirectReviewFilePrefixBytes ||
		repository.MaxFileChunkBytes > repository.MaxFilePrefixBytes ||
		repository.MaxToolOutputBytes <= 0 || repository.MaxToolOutputBytes > MaximumDirectReviewRepositoryToolOutputBytes ||
		repository.MaxToolOutputBytes > provider.MaxToolOutputBytes ||
		repository.MaxOperationDurationMS <= 0 || repository.MaxOperationDurationMS > MaximumDirectReviewOperationDurationMS ||
		repository.MaxOperationDurationMS > provider.MaxDurationMS {
		return errors.New("invalid direct review repository limits")
	}
	return nil
}

func validDirectReviewProtocol(protocol string) bool {
	switch protocol {
	case DirectReviewProtocolOpenAIResponses, DirectReviewProtocolAnthropic, DirectReviewProtocolCompatibleChat:
		return true
	default:
		return false
	}
}

func validDirectReviewEndpoint(value string) bool {
	if !validDirectReviewText(value, MaximumDirectReviewEndpointBytes, false) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && !strings.ContainsAny(parsed.Host, "\x00\r\n \t")
}

func validDirectReviewCredential(value string) bool {
	if value == "" || len(value) > MaximumDirectReviewCredentialBytes || !utf8.ValidString(value) {
		return false
	}
	for _, item := range []byte(value) {
		if item < 0x21 || item == 0x7f {
			return false
		}
	}
	return true
}

func validDirectReviewID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func validDirectReviewDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validDirectReviewText(value string, maximum int, allowLineBreaks bool) bool {
	if strings.TrimSpace(value) == "" || len(value) > maximum || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	if !allowLineBreaks && (value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n")) {
		return false
	}
	return true
}

func directReviewDigest(value any) (string, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

// DirectReviewConnectionFingerprint binds consent to the local connection
// authority as well as its public destination. The local epoch is replaced on
// restore and is never included in portable state.
func DirectReviewConnectionFingerprint(settings DirectReviewSettings) string {
	digest, _ := directReviewDigest(struct {
		RepositoryID       string `json:"repository_id"`
		Protocol           string `json:"protocol"`
		Endpoint           string `json:"endpoint"`
		Model              string `json:"model"`
		AuthenticationMode string `json:"authentication_mode"`
		ConnectionVersion  int64  `json:"connection_version"`
		AuthorityEpoch     string `json:"authority_epoch"`
	}{settings.RepositoryID, settings.Protocol, settings.Endpoint, settings.Model, settings.AuthenticationMode, settings.ConnectionVersion, settings.AuthorityEpoch})
	return digest
}

func marshalDirectReviewLimits(provider DirectReviewProviderLimits, repository DirectReviewRepositoryLimits) (string, string, error) {
	if err := validateDirectReviewLimits(provider, repository); err != nil {
		return "", "", err
	}
	providerJSON, err := json.Marshal(provider)
	if err != nil || len(providerJSON) > MaximumDirectReviewLimitsJSONBytes {
		return "", "", errors.New("direct review provider limits are too large")
	}
	repositoryJSON, err := json.Marshal(repository)
	if err != nil || len(repositoryJSON) > MaximumDirectReviewLimitsJSONBytes {
		return "", "", errors.New("direct review repository limits are too large")
	}
	return string(providerJSON), string(repositoryJSON), nil
}

func unmarshalDirectReviewLimits(providerJSON, repositoryJSON string) (DirectReviewProviderLimits, DirectReviewRepositoryLimits, error) {
	if len(providerJSON) > MaximumDirectReviewLimitsJSONBytes || len(repositoryJSON) > MaximumDirectReviewLimitsJSONBytes {
		return DirectReviewProviderLimits{}, DirectReviewRepositoryLimits{}, errors.New("stored direct review limits are too large")
	}
	var provider DirectReviewProviderLimits
	if err := decodeDirectReviewJSON([]byte(providerJSON), &provider); err != nil {
		return DirectReviewProviderLimits{}, DirectReviewRepositoryLimits{}, fmt.Errorf("decode direct review provider limits: %w", err)
	}
	var repository DirectReviewRepositoryLimits
	if err := decodeDirectReviewJSON([]byte(repositoryJSON), &repository); err != nil {
		return DirectReviewProviderLimits{}, DirectReviewRepositoryLimits{}, fmt.Errorf("decode direct review repository limits: %w", err)
	}
	if err := validateDirectReviewLimits(provider, repository); err != nil {
		return DirectReviewProviderLimits{}, DirectReviewRepositoryLimits{}, err
	}
	return provider, repository, nil
}

func marshalDirectReviewResult(result DirectReviewResult) (string, error) {
	if err := validateDirectReviewResult(result); err != nil {
		return "", err
	}
	content, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if len(content) > MaximumDirectReviewResultJSONBytes {
		return "", errors.New("direct review result exceeds its storage limit")
	}
	return string(content), nil
}

func unmarshalDirectReviewResult(content string) (*DirectReviewResult, error) {
	if content == "" {
		return nil, nil
	}
	if len(content) > MaximumDirectReviewResultJSONBytes {
		return nil, errors.New("stored direct review result is too large")
	}
	var result DirectReviewResult
	if err := decodeDirectReviewJSON([]byte(content), &result); err != nil {
		return nil, err
	}
	if err := validateDirectReviewResult(result); err != nil {
		return nil, err
	}
	return &result, nil
}

func decodeDirectReviewJSON(content []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func validateDirectReviewResult(result DirectReviewResult) error {
	if !validDirectReviewOutcome(result.Outcome) || len(result.Reason) > 100 || !utf8.ValidString(result.Reason) || strings.ContainsAny(result.Reason, "\x00\r\n") {
		return errors.New("invalid direct review result outcome")
	}
	if result.RequestedModel != "" && !validDirectReviewText(result.RequestedModel, MaximumDirectReviewModelBytes, false) {
		return errors.New("invalid direct review requested model")
	}
	if len(result.ReportedModels) > 32 || len(result.Findings) > MaximumDirectReviewFindings || len(result.Usage) > MaximumDirectReviewProviderToolRounds+1 || len(result.ToolCalls) > MaximumDirectReviewProviderToolCalls {
		return errors.New("direct review result contains too many records")
	}
	for _, model := range result.ReportedModels {
		if !validDirectReviewText(model, MaximumDirectReviewModelBytes, false) {
			return errors.New("invalid direct review reported model")
		}
	}
	for _, finding := range result.Findings {
		if !validDirectReviewText(finding.Title, MaximumDirectReviewFindingTitleBytes, true) || !validDirectReviewText(finding.Detail, MaximumDirectReviewFindingDetailBytes, true) || finding.Line < 0 || len(finding.Path) > MaximumDirectReviewPathBytes || !utf8.ValidString(finding.Path) || strings.ContainsRune(finding.Path, 0) {
			return errors.New("invalid direct review finding")
		}
	}
	for _, usage := range result.Usage {
		if usage.Request <= 0 || usage.Request > MaximumDirectReviewProviderToolRounds+1 || usage.Request > result.Requests {
			return errors.New("invalid direct review usage request")
		}
		for _, value := range []*int64{usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens} {
			if value != nil && *value < 0 {
				return errors.New("invalid direct review usage value")
			}
		}
	}
	if result.Coverage.ToolRounds < 0 || result.Coverage.ToolRounds > MaximumDirectReviewProviderToolRounds || result.Coverage.ToolCalls < 0 || result.Coverage.ToolCalls > MaximumDirectReviewProviderToolCalls || len(result.Coverage.Reasons) > 32 || len(result.Coverage.Paths) > MaximumDirectReviewCoveragePaths {
		return errors.New("invalid direct review coverage")
	}
	for _, reason := range result.Coverage.Reasons {
		if !validDirectReviewText(reason, 100, false) {
			return errors.New("invalid direct review coverage reason")
		}
	}
	for _, path := range result.Coverage.Paths {
		if len(path) > MaximumDirectReviewPathBytes || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
			return errors.New("invalid direct review coverage path")
		}
	}
	for _, call := range result.ToolCalls {
		if !validDirectReviewText(call.ID, 200, false) || !validDirectReviewText(call.Name, 64, false) || len(call.Arguments) > MaximumDirectReviewToolArgumentBytes || !utf8.ValidString(call.Arguments) || strings.ContainsRune(call.Arguments, 0) || !validDirectReviewText(call.Outcome, 100, false) || call.OutputBytes < 0 || len(call.Paths) > MaximumDirectReviewCoveragePaths {
			return errors.New("invalid direct review tool call")
		}
		for _, path := range call.Paths {
			if len(path) > MaximumDirectReviewPathBytes || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
				return errors.New("invalid direct review tool path")
			}
		}
	}
	if result.Requests < 0 || result.Requests > MaximumDirectReviewProviderToolRounds+1 || result.ContextBytes < 0 || result.ContextBytes > MaximumDirectReviewProviderContextBytes || result.InitialContextBytes < 0 || result.InitialContextBytes > MaximumDirectReviewInitialContextBytes || (result.HTTPStatus != 0 && (result.HTTPStatus < 100 || result.HTTPStatus > 599)) {
		return errors.New("invalid direct review result measurements")
	}
	switch result.Outcome {
	case DirectReviewOutcomeContextUnavailable, DirectReviewOutcomeCancelledBeforeSubmission, DirectReviewOutcomeInterruptedBeforeSubmission, DirectReviewOutcomeCapabilityUnavailable, DirectReviewOutcomeCapacityUnavailable:
		if result.Requests != 0 {
			return errors.New("unsubmitted direct review result contains provider requests")
		}
	}
	if result.Requests == 0 && (result.HTTPStatus != 0 || len(result.ReportedModels) != 0 || len(result.Findings) != 0 || len(result.Usage) != 0 || len(result.ToolCalls) != 0) {
		return errors.New("unsubmitted direct review result contains provider evidence")
	}
	if result.ErrorCode != "" && !validDirectReviewText(result.ErrorCode, 100, false) {
		return errors.New("invalid direct review error code")
	}
	return nil
}

func validDirectReviewOutcome(outcome string) bool {
	switch outcome {
	case "completed", "incomplete", "refused", "empty_output", "missing_output", "malformed_output", "truncated", "cancelled", "transport_failure", "provider_failure",
		DirectReviewOutcomeContextUnavailable, DirectReviewOutcomeCancelledBeforeSubmission, DirectReviewOutcomeInterruptedBeforeSubmission, DirectReviewOutcomeInterruptedMayHaveSubmitted, DirectReviewOutcomeCapabilityUnavailable, DirectReviewOutcomeCapacityUnavailable,
		"supported", "inconclusive", "invalid_response", "provider_rejected":
		return true
	default:
		return false
	}
}

func validZeroRequestProbeLimit(probe DirectReviewProbe, result DirectReviewResult) bool {
	if !validZeroRequestLimitResult(probe.Model, probe.ProviderLimits, result) {
		return false
	}
	return !result.Coverage.Complete && len(result.Coverage.Reasons) == 0 && len(result.Coverage.Paths) == 0 &&
		result.Coverage.ToolRounds == 0 && result.Coverage.ToolCalls == 0 &&
		result.InitialContextBytes == 0 && !result.InitialContextTruncated
}

func validZeroRequestReviewLimit(request DirectReviewRequest, result DirectReviewResult) bool {
	if !validZeroRequestLimitResult(request.Model, request.ProviderLimits, result) {
		return false
	}
	return !result.Coverage.Complete && len(result.Coverage.Reasons) == 1 && result.Coverage.Reasons[0] == result.Reason &&
		len(result.Coverage.Paths) == 0 && result.Coverage.ToolRounds == 0 && result.Coverage.ToolCalls == 0
}

func validZeroRequestLimitResult(model string, limits DirectReviewProviderLimits, result DirectReviewResult) bool {
	if result.Requests != 0 || result.RequestedModel != model || result.ContextBytes <= 0 || result.HTTPStatus != 0 ||
		len(result.ReportedModels) != 0 || len(result.Findings) != 0 || len(result.Usage) != 0 || len(result.ToolCalls) != 0 || result.EvidenceTruncated {
		return false
	}
	switch result.Reason {
	case "context_limit":
		return result.ContextBytes > limits.MaxContextBytes && result.ErrorCode == ""
	case "request_limit":
		return result.ContextBytes <= limits.MaxContextBytes && result.ErrorCode == "invalid_request"
	default:
		return false
	}
}

func validateDirectReviewProbeResult(probe DirectReviewProbe) error {
	result := probe.Result
	if result == nil {
		return errors.New("terminal direct review probe has no result")
	}
	if err := validateDirectReviewResult(*result); err != nil {
		return err
	}
	switch result.Outcome {
	case "inconclusive":
		if probe.RunningAt == nil || result.Requests == 0 && !validZeroRequestProbeLimit(probe, *result) {
			return errors.New("submitted direct review probe result has no provider request history")
		}
	case "supported", "refused", "transport_failure", "provider_rejected", "invalid_response":
		if probe.RunningAt == nil || result.Requests == 0 {
			return errors.New("submitted direct review probe result has no provider request history")
		}
	case "cancelled", DirectReviewOutcomeInterruptedMayHaveSubmitted:
		if probe.RunningAt == nil {
			return errors.New("submitted direct review probe result has no running history")
		}
	case DirectReviewOutcomeCancelledBeforeSubmission:
		if probe.CancelRequestedAt == nil {
			return errors.New("cancelled direct review probe has no cancellation history")
		}
		// A running marker can precede the final cancellation check. Retain that
		// durable history even when the client was known not to have been called.
	case DirectReviewOutcomeInterruptedBeforeSubmission:
		if probe.CancelRequestedAt != nil {
			return errors.New("interrupted direct review probe contains cancellation history")
		}
		// A running marker can precede the final interruption check. Retain that
		// durable history even when the client was known not to have been called.
	default:
		return errors.New("invalid direct review probe outcome")
	}
	if len(result.Findings) != 0 || result.Coverage.Complete || len(result.Coverage.Reasons) != 0 || len(result.Coverage.Paths) != 0 ||
		result.Coverage.ToolRounds != 0 || result.Coverage.ToolCalls != 0 || len(result.ToolCalls) != 0 ||
		result.InitialContextBytes != 0 || result.InitialContextTruncated {
		return errors.New("direct review probe result contains review evidence")
	}
	return nil
}

func validateDirectReviewRequestResult(request DirectReviewRequest) error {
	result := request.Result
	if result == nil {
		return errors.New("terminal direct review request has no result")
	}
	if err := validateDirectReviewResult(*result); err != nil {
		return err
	}
	switch result.Outcome {
	case "incomplete":
		if request.RunningAt == nil || result.Requests == 0 && !validZeroRequestReviewLimit(request, *result) {
			return errors.New("submitted direct review result has no provider request history")
		}
	case "completed", "refused", "empty_output", "missing_output", "malformed_output", "truncated", "transport_failure", "provider_failure":
		if request.RunningAt == nil || result.Requests == 0 {
			return errors.New("submitted direct review result has no provider request history")
		}
	case "cancelled", DirectReviewOutcomeInterruptedMayHaveSubmitted:
		if request.RunningAt == nil {
			return errors.New("submitted direct review result has no running history")
		}
	case DirectReviewOutcomeContextUnavailable, DirectReviewOutcomeCapabilityUnavailable, DirectReviewOutcomeCapacityUnavailable:
		if request.RunningAt != nil {
			return errors.New("unsubmitted direct review result contains running history")
		}
	case DirectReviewOutcomeCancelledBeforeSubmission:
		if request.CancelRequestedAt == nil {
			return errors.New("cancelled direct review request has no cancellation history")
		}
		// A running marker can precede the final cancellation check. Retain that
		// durable history even when the client was known not to have been called.
	case DirectReviewOutcomeInterruptedBeforeSubmission:
		if request.CancelRequestedAt != nil {
			return errors.New("interrupted direct review request contains cancellation history")
		}
		// A running marker can precede the final interruption check. Retain that
		// durable history even when the client was known not to have been called.
	default:
		return errors.New("invalid direct review request outcome")
	}
	if result.InitialContextBytes != request.InitialContextBytes || result.InitialContextTruncated != request.InitialContextTruncated {
		return errors.New("direct review result context metadata does not match the request")
	}
	return nil
}

type directReviewProbeDigestFacts struct {
	RequestID             string                     `json:"request_id"`
	RepositoryID          string                     `json:"repository_id"`
	CapabilityFingerprint string                     `json:"capability_fingerprint"`
	ConfigurationVersion  int64                      `json:"configuration_version"`
	ConnectionVersion     int64                      `json:"connection_version"`
	ConnectionFingerprint string                     `json:"connection_fingerprint"`
	Protocol              string                     `json:"protocol"`
	Endpoint              string                     `json:"endpoint"`
	Model                 string                     `json:"model"`
	AuthenticationMode    string                     `json:"authentication_mode"`
	ProviderLimits        DirectReviewProviderLimits `json:"provider_limits"`
	DisclosureVersion     string                     `json:"disclosure_version"`
	DisclosureDigest      string                     `json:"disclosure_digest"`
}

func DirectReviewProbeDigest(probe DirectReviewProbe) (string, error) {
	return directReviewDigest(directReviewProbeDigestFacts{
		RequestID: probe.RequestID, RepositoryID: probe.RepositoryID, CapabilityFingerprint: probe.CapabilityFingerprint,
		ConfigurationVersion: probe.ConfigurationVersion, ConnectionVersion: probe.ConnectionVersion,
		ConnectionFingerprint: probe.ConnectionFingerprint, Protocol: probe.Protocol, Endpoint: probe.Endpoint, Model: probe.Model,
		AuthenticationMode: probe.AuthenticationMode, ProviderLimits: probe.ProviderLimits,
		DisclosureVersion: probe.DisclosureVersion, DisclosureDigest: probe.DisclosureDigest,
	})
}

// ValidateDirectReviewProbeRegistration checks caller-supplied immutable facts
// without consulting current settings or credentials.
func ValidateDirectReviewProbeRegistration(probe DirectReviewProbe) error {
	probe.CreatedAt = time.Unix(1, 0).UTC()
	probe.Phase = DirectReviewPhasePreparing
	probe.CancelRequestedAt = nil
	probe.RunningAt = nil
	probe.TerminalAt = nil
	probe.Result = nil
	return validateDirectReviewProbe(probe, false)
}

func validateDirectReviewProbe(probe DirectReviewProbe, stored bool) error {
	if !validDirectReviewID(probe.RequestID) || probe.RepositoryID == "" || !validDirectReviewDigest(probe.CapabilityFingerprint) ||
		probe.ConfigurationVersion <= 0 || probe.ConnectionVersion < 0 || !validDirectReviewDigest(probe.ConnectionFingerprint) ||
		!validDirectReviewProtocol(probe.Protocol) || !validDirectReviewEndpoint(probe.Endpoint) || !validDirectReviewText(probe.Model, MaximumDirectReviewModelBytes, false) ||
		(probe.AuthenticationMode != DirectReviewAuthenticationStored && probe.AuthenticationMode != DirectReviewAuthenticationNone) ||
		!validDirectReviewText(probe.DisclosureVersion, 100, false) || !validDirectReviewDigest(probe.DisclosureDigest) || probe.CreatedAt.IsZero() {
		return errors.New("invalid direct review probe")
	}
	if err := validateDirectReviewLimits(probe.ProviderLimits, minimumDirectReviewRepositoryLimits(probe.ProviderLimits)); err != nil {
		return err
	}
	digest, err := DirectReviewProbeDigest(probe)
	if err != nil || probe.RegistrationDigest != digest {
		return errors.New("invalid direct review probe registration digest")
	}
	if probe.CancelRequestedAt != nil && (probe.CancelRequestedAt.IsZero() || probe.CancelRequestedAt.Before(probe.CreatedAt) ||
		probe.RunningAt != nil && probe.CancelRequestedAt.Before(*probe.RunningAt)) {
		return errors.New("invalid direct review probe cancellation time")
	}
	if probe.RunningAt != nil && (probe.RunningAt.IsZero() || probe.RunningAt.Before(probe.CreatedAt)) {
		return errors.New("invalid direct review probe running time")
	}
	switch probe.Phase {
	case DirectReviewPhasePreparing:
		if probe.RunningAt != nil || probe.TerminalAt != nil || probe.Result != nil {
			return errors.New("preparing direct review probe contains terminal evidence")
		}
	case DirectReviewPhaseRunning:
		if probe.RunningAt == nil || probe.TerminalAt != nil || probe.Result != nil {
			return errors.New("invalid running direct review probe")
		}
	case DirectReviewPhaseTerminal:
		if probe.TerminalAt == nil || probe.TerminalAt.IsZero() || probe.TerminalAt.Before(probe.CreatedAt) ||
			probe.RunningAt != nil && probe.TerminalAt.Before(*probe.RunningAt) ||
			probe.CancelRequestedAt != nil && probe.TerminalAt.Before(*probe.CancelRequestedAt) || probe.Result == nil {
			return errors.New("invalid terminal direct review probe")
		}
		if err := validateDirectReviewProbeResult(probe); err != nil {
			return err
		}
	default:
		return errors.New("invalid direct review probe phase")
	}
	_ = stored
	return nil
}

type directReviewRequestDigestFacts struct {
	RequestID             string                       `json:"request_id"`
	RepositoryID          string                       `json:"repository_id"`
	TriggerKind           string                       `json:"trigger_kind"`
	SourceEventKey        string                       `json:"source_event_key"`
	PullRequestNumber     int64                        `json:"pull_request_number"`
	TaskID                string                       `json:"task_id"`
	AttemptID             string                       `json:"attempt_id"`
	ObservedSourceOID     string                       `json:"observed_source_oid"`
	ObservedTargetOID     string                       `json:"observed_target_oid"`
	EffectiveBaseOID      string                       `json:"effective_base_oid"`
	EffectiveHeadOID      string                       `json:"effective_head_oid"`
	DiffMode              string                       `json:"diff_mode"`
	ConfigurationVersion  int64                        `json:"configuration_version"`
	ConnectionVersion     int64                        `json:"connection_version"`
	ConnectionFingerprint string                       `json:"connection_fingerprint"`
	Protocol              string                       `json:"protocol"`
	Endpoint              string                       `json:"endpoint"`
	Model                 string                       `json:"model"`
	AuthenticationMode    string                       `json:"authentication_mode"`
	ProviderLimits        DirectReviewProviderLimits   `json:"provider_limits"`
	RepositoryLimits      DirectReviewRepositoryLimits `json:"repository_limits"`
	InstructionVersion    string                       `json:"instruction_version"`
	ConsentVersion        string                       `json:"consent_version"`
	ConsentDigest         string                       `json:"consent_digest"`
	DisclosureVersion     string                       `json:"disclosure_version"`
	DisclosureDigest      string                       `json:"disclosure_digest"`
}

func DirectReviewRequestDigest(request DirectReviewRequest) (string, error) {
	return directReviewDigest(directReviewRequestDigestFacts{
		RequestID: request.RequestID, RepositoryID: request.RepositoryID, TriggerKind: request.TriggerKind, SourceEventKey: request.SourceEventKey,
		PullRequestNumber: request.PullRequestNumber, TaskID: request.TaskID, AttemptID: request.AttemptID,
		ObservedSourceOID: request.ObservedSourceOID, ObservedTargetOID: request.ObservedTargetOID,
		EffectiveBaseOID: request.EffectiveBaseOID, EffectiveHeadOID: request.EffectiveHeadOID, DiffMode: request.DiffMode,
		ConfigurationVersion: request.ConfigurationVersion, ConnectionVersion: request.ConnectionVersion,
		ConnectionFingerprint: request.ConnectionFingerprint, Protocol: request.Protocol, Endpoint: request.Endpoint, Model: request.Model,
		AuthenticationMode: request.AuthenticationMode, ProviderLimits: request.ProviderLimits, RepositoryLimits: request.RepositoryLimits,
		InstructionVersion: request.InstructionVersion, ConsentVersion: request.ConsentVersion, ConsentDigest: request.ConsentDigest,
		DisclosureVersion: request.DisclosureVersion, DisclosureDigest: request.DisclosureDigest,
	})
}

// ValidateDirectReviewRequestRegistration checks caller-supplied immutable
// facts without consulting current branches, settings, credentials, or capacity.
func ValidateDirectReviewRequestRegistration(request DirectReviewRequest) error {
	created := time.Unix(1, 0).UTC()
	request.Sequence = 0
	request.CreatedAt = created
	request.PreparingAt = &created
	request.RunningAt = nil
	request.TerminalAt = nil
	request.CancelRequestedAt = nil
	request.Result = nil
	request.Phase = DirectReviewPhasePreparing
	return validateDirectReviewRequest(request, false)
}

func validateDirectReviewRequest(request DirectReviewRequest, stored bool) error {
	if !validDirectReviewID(request.RequestID) || request.RepositoryID == "" || request.ConfigurationVersion <= 0 || request.ConnectionVersion < 0 ||
		!validDirectReviewDigest(request.ConnectionFingerprint) || !validDirectReviewProtocol(request.Protocol) || !validDirectReviewEndpoint(request.Endpoint) ||
		!validDirectReviewText(request.Model, MaximumDirectReviewModelBytes, false) ||
		(request.AuthenticationMode != DirectReviewAuthenticationStored && request.AuthenticationMode != DirectReviewAuthenticationNone) ||
		!validDirectReviewText(request.InstructionVersion, MaximumDirectReviewInstructionVersionBytes, false) ||
		!validDirectReviewText(request.DisclosureVersion, 100, false) || !validDirectReviewDigest(request.DisclosureDigest) || request.CreatedAt.IsZero() {
		return errors.New("invalid direct review request")
	}
	if stored && request.Sequence <= 0 {
		return errors.New("stored direct review request has no sequence")
	}
	if !stored && request.Sequence != 0 {
		return errors.New("new direct review request already has a sequence")
	}
	if err := validateDirectReviewLimits(request.ProviderLimits, request.RepositoryLimits); err != nil {
		return err
	}
	if request.SourceEventKey != "" && !validDirectReviewText(request.SourceEventKey, 200, false) {
		return errors.New("invalid direct review source event identity")
	}
	switch request.TriggerKind {
	case DirectReviewTriggerManual:
		if request.SourceEventKey != "" || request.PullRequestNumber <= 0 || request.TaskID != "" || request.AttemptID != "" ||
			!validObjectID(request.ObservedSourceOID) || !validObjectID(request.ObservedTargetOID) ||
			request.EffectiveBaseOID != request.ObservedTargetOID || request.EffectiveHeadOID != request.ObservedSourceOID || request.DiffMode != DirectReviewDiffTwoCommit {
			return errors.New("invalid manual direct review binding")
		}
	case DirectReviewTriggerAutomaticPR:
		if request.SourceEventKey == "" || request.PullRequestNumber <= 0 || request.TaskID != "" || request.AttemptID != "" ||
			!validObjectID(request.ObservedSourceOID) || !validObjectID(request.ObservedTargetOID) ||
			request.EffectiveBaseOID != request.ObservedTargetOID || request.EffectiveHeadOID != request.ObservedSourceOID || request.DiffMode != DirectReviewDiffTwoCommit {
			return errors.New("invalid automatic pull request review binding")
		}
	case DirectReviewTriggerAutomaticTask:
		if request.SourceEventKey == "" || request.TaskID == "" || !validAttemptID(request.AttemptID) {
			return errors.New("invalid automatic task review binding")
		}
		if (request.EffectiveBaseOID == "") != (request.EffectiveHeadOID == "") ||
			(request.EffectiveBaseOID == "" && request.DiffMode != "") ||
			(request.EffectiveBaseOID != "" && (!validObjectID(request.EffectiveBaseOID) || !validObjectID(request.EffectiveHeadOID) || request.DiffMode != DirectReviewDiffTwoCommit)) {
			return errors.New("invalid automatic task review commit binding")
		}
		if request.PullRequestNumber == 0 {
			if request.ObservedSourceOID != "" || request.ObservedTargetOID != "" {
				return errors.New("non-PR automatic task review contains a PR binding")
			}
		} else if !validObjectID(request.ObservedSourceOID) || !validObjectID(request.ObservedTargetOID) ||
			request.EffectiveHeadOID != "" && (request.EffectiveHeadOID != request.ObservedSourceOID || request.EffectiveBaseOID != request.ObservedTargetOID) {
			return errors.New("invalid automatic task PR binding")
		}
	default:
		return errors.New("invalid direct review trigger")
	}
	if !validDirectReviewText(request.ConsentVersion, 100, false) {
		return errors.New("invalid direct review consent version")
	}
	if !validDirectReviewDigest(request.ConsentDigest) {
		return errors.New("invalid direct review consent digest")
	}
	digest, err := DirectReviewRequestDigest(request)
	if err != nil || request.RegistrationDigest != digest {
		return errors.New("invalid direct review request registration digest")
	}
	if request.CancelRequestedAt != nil && (request.CancelRequestedAt.IsZero() || request.CancelRequestedAt.Before(request.CreatedAt) ||
		request.PreparingAt != nil && request.CancelRequestedAt.Before(*request.PreparingAt) ||
		request.RunningAt != nil && request.CancelRequestedAt.Before(*request.RunningAt)) {
		return errors.New("invalid direct review cancellation time")
	}
	if request.PreparingAt != nil && (request.PreparingAt.IsZero() || request.PreparingAt.Before(request.CreatedAt)) {
		return errors.New("invalid direct review preparing time")
	}
	if request.RunningAt != nil && (request.RunningAt.IsZero() || request.PreparingAt == nil || request.RunningAt.Before(*request.PreparingAt)) {
		return errors.New("invalid direct review running time")
	}
	if request.InitialContextBytes < 0 || request.InitialContextBytes > request.RepositoryLimits.MaxInitialContextBytes {
		return errors.New("invalid direct review initial context measurement")
	}
	switch request.Phase {
	case DirectReviewPhaseObserved:
		if request.TriggerKind == DirectReviewTriggerManual || request.PreparingAt != nil || request.RunningAt != nil || request.TerminalAt != nil || request.Result != nil {
			return errors.New("invalid observed direct review request")
		}
	case DirectReviewPhasePreparing:
		if request.PreparingAt == nil || request.RunningAt != nil || request.TerminalAt != nil || request.Result != nil {
			return errors.New("invalid preparing direct review request")
		}
	case DirectReviewPhaseRunning:
		if request.PreparingAt == nil || request.RunningAt == nil || request.TerminalAt != nil || request.Result != nil {
			return errors.New("invalid running direct review request")
		}
	case DirectReviewPhaseTerminal:
		if request.TerminalAt == nil || request.TerminalAt.IsZero() || request.TerminalAt.Before(request.CreatedAt) ||
			request.PreparingAt != nil && request.TerminalAt.Before(*request.PreparingAt) ||
			request.RunningAt != nil && request.TerminalAt.Before(*request.RunningAt) ||
			request.CancelRequestedAt != nil && request.TerminalAt.Before(*request.CancelRequestedAt) || request.Result == nil ||
			request.TriggerKind == DirectReviewTriggerManual && request.PreparingAt == nil {
			return errors.New("invalid terminal direct review request")
		}
		if err := validateDirectReviewRequestResult(request); err != nil {
			return err
		}
	default:
		return errors.New("invalid direct review request phase")
	}
	return nil
}

type directReviewTaskContextDigestFacts struct {
	ContextID         string `json:"context_id"`
	RepositoryID      string `json:"repository_id"`
	TaskID            string `json:"task_id"`
	AttemptID         string `json:"attempt_id"`
	CredentialID      string `json:"credential_id"`
	BaseOID           string `json:"base_oid"`
	HeadOID           string `json:"head_oid"`
	PullRequestNumber int64  `json:"pull_request_number"`
	ObservedSourceOID string `json:"observed_source_oid"`
	ObservedTargetOID string `json:"observed_target_oid"`
}

func DirectReviewTaskContextDigest(contextRecord DirectReviewTaskContext) (string, error) {
	return directReviewDigest(directReviewTaskContextDigestFacts{
		ContextID: contextRecord.ContextID, RepositoryID: contextRecord.RepositoryID, TaskID: contextRecord.TaskID,
		AttemptID: contextRecord.AttemptID, CredentialID: contextRecord.CredentialID, BaseOID: contextRecord.BaseOID,
		HeadOID: contextRecord.HeadOID, PullRequestNumber: contextRecord.PullRequestNumber,
		ObservedSourceOID: contextRecord.ObservedSourceOID, ObservedTargetOID: contextRecord.ObservedTargetOID,
	})
}

func validateDirectReviewTaskContext(contextRecord DirectReviewTaskContext) error {
	if !validDirectReviewID(contextRecord.ContextID) || contextRecord.RepositoryID == "" || contextRecord.TaskID == "" ||
		!validAttemptID(contextRecord.AttemptID) || !validDirectReviewID(contextRecord.CredentialID) ||
		!validObjectID(contextRecord.BaseOID) || !validObjectID(contextRecord.HeadOID) || contextRecord.CreatedAt.IsZero() {
		return errors.New("invalid direct review task context")
	}
	if contextRecord.PullRequestNumber == 0 {
		if contextRecord.ObservedSourceOID != "" || contextRecord.ObservedTargetOID != "" {
			return errors.New("non-PR task context contains a PR binding")
		}
	} else if contextRecord.PullRequestNumber < 0 || contextRecord.ObservedSourceOID != contextRecord.HeadOID || contextRecord.ObservedTargetOID != contextRecord.BaseOID {
		return errors.New("invalid direct review task PR binding")
	}
	digest, err := DirectReviewTaskContextDigest(contextRecord)
	if err != nil || contextRecord.RegistrationDigest != digest {
		return errors.New("invalid direct review task context digest")
	}
	return nil
}
