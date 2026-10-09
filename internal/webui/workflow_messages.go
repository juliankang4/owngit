package webui

import (
	"html/template"
	"regexp"
	"strconv"
)

// WorkflowMessage is one workflow refusal or note as the backend recorded it:
// a stable code, the key path and line it names, the backend's English
// detail, and the values its placeholders take.
type WorkflowMessage struct {
	Code   string
	Path   string
	Line   int
	Detail string
	Args   map[string]string
}

var workflowTexts = map[string]struct{ en, ko string }{
	"workflow.yaml":               {"Line {line} is not valid YAML: {detail}", "{line}번째 줄이 올바른 YAML이 아닙니다: {detail}"},
	"workflow.yaml_feature":       {"Line {line} uses {feature}, which OwnGit does not read in workflow files. Write the values out in full.", "{line}번째 줄에 OwnGit이 워크플로 파일에서 읽지 않는 {feature} 기능이 있습니다. 값을 직접 풀어 쓰세요."},
	"workflow.duplicate_key":      {"{key} appears twice in {path} (lines {a} and {b}). Keep one.", "{path}에 {key}가 두 번 있습니다({a}번째 줄과 {b}번째 줄). 하나만 남기세요."},
	"workflow.unknown_key":        {"{path} (line {line}) is not a workflow key OwnGit knows. Check the spelling against GitHub's workflow syntax, or remove it.", "{path}({line}번째 줄)는 OwnGit이 아는 워크플로 키가 아닙니다. GitHub 워크플로 문법과 철자를 비교하거나 이 키를 지우세요."},
	"workflow.wrong_type":         {"{path} (line {line}) must be {expected}.", "{path}({line}번째 줄)에는 {expected} 값을 써야 합니다."},
	"workflow.limit":              {"{what} is over OwnGit's limit of {limit}.", "{what}: OwnGit의 한도({limit})를 넘었습니다."},
	"workflow.action":             {"OwnGit does not download actions, so {action} does not run. Replace the step with a run step that does the same work, and install the tool in the administrator's container image or on the runner.", "OwnGit은 액션을 내려받지 않으므로 {action}은 실행되지 않습니다. 같은 일을 하는 run 단계로 바꾸고, 필요한 도구는 관리자의 컨테이너 이미지나 러너에 설치하세요."},
	"workflow.local_action":       {"Local actions such as {action} do not run. Call the action's script from a run step.", "{action} 같은 로컬 액션은 실행되지 않습니다. 액션의 스크립트를 run 단계에서 직접 실행하세요."},
	"workflow.docker_action":      {"Container actions such as {action} do not run, because the administrator's policy chooses the only container. Run the tool in a run step.", "{action} 같은 컨테이너 액션은 실행되지 않습니다. 컨테이너는 관리자의 설정이 정합니다. 도구를 run 단계에서 실행하세요."},
	"workflow.reusable":           {"Reusable workflows such as {workflow} do not run. Copy its jobs into this file.", "{workflow} 같은 재사용 워크플로는 실행되지 않습니다. 그 작업들을 이 파일에 옮겨 적으세요."},
	"workflow.container":          {"container is not used, because the administrator's policy chooses where jobs run. Remove it. For a container, the administrator sets the container executor and image.", "작업이 실행될 곳은 관리자의 설정이 정하므로 container는 쓰이지 않습니다. 이 키를 지우세요. 컨테이너가 필요하면 관리자가 컨테이너 실행과 이미지를 설정합니다."},
	"workflow.services":           {"Service containers do not run. Use a service the job can reach over the network the check policy allows, or start the service and use it inside one run script.", "서비스 컨테이너는 실행되지 않습니다. 체크 설정이 허용하는 네트워크로 접속할 수 있는 서비스를 쓰거나, 하나의 run 스크립트 안에서 서비스를 시작하고 사용하세요."},
	"workflow.environment":        {"OwnGit has no deployment environments or approval gates, and anyone with general access can start a workflow by hand, so a manual start is not an approval. Remove environment only if running this job without approval is acceptable.", "OwnGit에는 배포 환경과 승인 절차가 없고, 일반 접근 권한이 있으면 누구나 워크플로를 직접 실행할 수 있으므로 직접 실행은 승인이 아닙니다. 승인 없이 이 작업을 실행해도 될 때만 environment를 지우세요."},
	"workflow.snapshot":           {"snapshot builds a GitHub runner image, which OwnGit does not do. Keep this workflow on GitHub, or remove the key.", "snapshot은 GitHub 러너 이미지를 만드는 키이며 OwnGit은 이미지를 만들지 않습니다. 이 워크플로는 GitHub에서 실행하거나 이 키를 지우세요."},
	"workflow.background":         {"Background and parallel steps ({key}) do not run in OwnGit. Start the process and use it inside one run script.", "백그라운드 단계와 병렬 단계({key})는 OwnGit에서 실행되지 않습니다. 하나의 run 스크립트 안에서 프로세스를 시작하고 사용하세요."},
	"workflow.secret_ref":         {"Name each secret directly, as secrets.NAME, and do not use secrets in if. To test a secret, put it in env and test the variable, for example if: env.TOKEN != ''.", "시크릿은 secrets.NAME처럼 이름을 직접 쓰고 if에는 쓰지 마세요. 시크릿이 있는지 확인하려면 env에 넣고 그 변수를 확인하세요. 예: if: env.TOKEN != ''"},
	"workflow.token":              {"OwnGit gives workflows no GitHub token. Remove {expr}, or store a token of your own as a repository secret under another name.", "OwnGit은 워크플로에 GitHub 토큰을 주지 않습니다. {expr}를 지우거나, 직접 만든 토큰을 다른 이름의 저장소 시크릿으로 저장하세요."},
	"workflow.context":            {"{context} is not available in {key} in OwnGit workflows. {hint}", "OwnGit 워크플로의 {key}에서는 {context}를 쓸 수 없습니다. {hint}"},
	"hint.outputs":                {"OwnGit does not pass outputs between jobs. Compute the value in the job that uses it, or merge the two jobs.", "OwnGit은 작업 사이에 출력값을 넘기지 않습니다. 값이 필요한 작업에서 직접 계산하거나 두 작업을 합치세요."},
	"hint.vars":                   {"Use env in the workflow, or a secret.", "워크플로의 env나 시크릿을 쓰세요."},
	"hint.event":                  {"OwnGit fills only the event fields listed in its workflow documentation.", "OwnGit은 워크플로 문서에 적힌 이벤트 값만 채웁니다."},
	"workflow.function":           {"{function}() is not supported. {hint}", "{function}()은 지원하지 않습니다. {hint}"},
	"hint.hashFiles":              {"Compute the hash in a run step, for example with sha256sum.", "run 단계에서 sha256sum 등으로 해시를 계산하세요."},
	"workflow.event":              {"OwnGit does not run workflows on {event}. It runs push, pull_request, workflow_dispatch and schedule.", "OwnGit은 {event} 이벤트로 워크플로를 실행하지 않습니다. push, pull_request, workflow_dispatch, schedule만 실행합니다."},
	"workflow.event_pr_target":    {"pull_request_target does not run. Use pull_request; OwnGit runs a pull request from its own commit.", "pull_request_target은 실행되지 않습니다. pull_request를 쓰세요. OwnGit은 풀 리퀘스트를 그 풀 리퀘스트의 커밋으로 실행합니다."},
	"workflow.tags":               {"OwnGit does not run workflows for tag pushes, so this push trigger never starts a run.", "OwnGit은 태그 푸시로 워크플로를 실행하지 않으므로 이 푸시 조건으로는 실행되지 않습니다."},
	"workflow.timezone":           {"OwnGit runs schedules in UTC only. Remove timezone and write the cron time in UTC.", "OwnGit은 일정을 UTC로만 실행합니다. timezone을 지우고 cron 시각을 UTC로 쓰세요."},
	"workflow.cron_never":         {"Schedule {cron} never matches a date. Check its day and month fields.", "{cron} 일정은 어떤 날짜와도 맞지 않습니다. 날짜와 월 값을 확인하세요."},
	"workflow.too_many_jobs":      {"This workflow would start {count} jobs; OwnGit starts at most 16 for one run. Make the matrix smaller (an OS axis repeats the same work here), combine jobs, or move jobs to another workflow file.", "이 워크플로는 작업 {count}개를 시작하게 됩니다. OwnGit은 실행 하나에 작업을 16개까지만 시작합니다. 매트릭스를 줄이거나(OS 축은 여기서 같은 일을 반복합니다), 작업을 합치거나, 일부 작업을 다른 워크플로 파일로 옮기세요."},
	"workflow.never_fits":         {"This workflow needs {count} jobs at once, but the check policy's queue holds at most {limit}. Raise queue_limit, or make the matrix smaller.", "이 워크플로는 작업 {count}개가 한 번에 필요하지만 체크 설정의 대기열은 최대 {limit}개입니다. queue_limit을 늘리거나 매트릭스를 줄이세요."},
	"workflow.env_name":           {"Variable name {name} is not supported. Use letters, digits and underscores, and do not start with a digit.", "{name} 변수 이름은 지원하지 않습니다. 영문자, 숫자, 밑줄만 쓰고 숫자로 시작하지 마세요."},
	"workflow.env_too_long":       {"Variable {name} is longer than {limit}, the most this computer allows for one environment variable. Pass the value in a file instead.", "{name} 변수가 이 컴퓨터에서 환경 변수 하나에 허용하는 길이({limit})보다 깁니다. 값을 파일로 전달하세요."},
	"workflow.shell":              {"Shell {shell} is not valid. Use bash, sh, pwsh, powershell, cmd, python, or a command with {0} where the script file goes.", "{shell} 셸은 올바르지 않습니다. bash, sh, pwsh, powershell, cmd, python 중 하나나, 스크립트 파일 자리에 {0}을 넣은 명령을 쓰세요."},
	"workflow.shell_unavailable":  {"Shell {shell} is not available on this {os} computer. Install it, or choose another shell.", "이 {os} 컴퓨터에는 {shell} 셸이 없습니다. 설치하거나 다른 셸을 고르세요."},
	"workflow.step_timeout":       {"This step stopped after {limit}: the check policy's max_timeout_ms limits each step. An administrator can raise it under Automatic checks or with owngit check-policy set.", "이 단계는 {limit} 뒤에 멈췄습니다. 체크 설정의 max_timeout_ms가 단계마다 시간을 제한합니다. 관리자가 자동 체크 화면이나 owngit check-policy set으로 늘릴 수 있습니다."},
	"workflow.checkout_input":     {"actions/checkout input {input} is not supported. OwnGit prepares only the files of commit {sha}: no other ref or repository, no submodules and no Git LFS content. Remove the input, or fetch what you need in a run step.", "actions/checkout의 {input} 입력은 지원하지 않습니다. OwnGit은 커밋 {sha}의 파일만 준비하며 다른 ref나 저장소, 서브모듈, Git LFS 내용은 준비하지 않습니다. 이 입력을 지우거나 필요한 것을 run 단계에서 받아 오세요."},
	"workflow.artifact_download":  {"actions/download-artifact does not run, because OwnGit keeps no artifacts and later steps would miss the files. Build the files in the same job.", "OwnGit은 산출물을 보관하지 않아 다음 단계에서 파일이 없으므로 actions/download-artifact는 실행되지 않습니다. 파일을 같은 작업 안에서 만드세요."},
	"workflow.run_too_long":       {"The run script of this step is longer than 24000 bytes. Put it in a file in the repository and run that file.", "이 단계의 run 스크립트가 24000바이트보다 깁니다. 저장소에 파일로 넣고 그 파일을 실행하세요."},
	"workflow.not_run_queue":      {"Not run: the queue holds {waiting} of {limit} jobs and this run needs {needed}. Rerun it later, or raise queue_limit.", "실행하지 않음: 대기열에 작업이 {limit}개 중 {waiting}개 있고 이 실행에는 {needed}개가 필요합니다. 나중에 다시 실행하거나 queue_limit을 늘리세요."},
	"workflow.off":                {"Workflows are off for this repository. Review the workflow files below, then turn them on (administrator password).", "이 저장소의 워크플로가 꺼져 있습니다. 아래 워크플로 파일을 확인한 뒤 켜세요(관리자 비밀번호 필요)."},
	"workflow.event_off":          {"The check policy does not allow {event}. An administrator can allow it under Automatic checks.", "체크 설정에서 {event} 이벤트를 허용하지 않습니다. 관리자가 자동 체크 화면에서 허용할 수 있습니다."},
	"workflow.dispatch_input":     {"Input {input} is not valid: {reason}. The workflow declares its inputs under on.workflow_dispatch.inputs.", "{input} 입력이 올바르지 않습니다: {reason}. 워크플로는 on.workflow_dispatch.inputs에 입력을 정의합니다."},
	"workflow.moved":              {"Branch {branch} moved to {oid} after this form was opened. Check the new commit and run it again.", "이 양식을 연 뒤 {branch} 브랜치가 {oid}로 바뀌었습니다. 새 커밋을 확인하고 다시 실행하세요."},
	"workflow.secrets_unreadable": {"The secrets of this repository could not be read, so this job did not start. An administrator can check them on the Secrets page.", "이 저장소의 시크릿을 읽을 수 없어 이 작업을 시작하지 않았습니다. 관리자가 시크릿 화면에서 확인할 수 있습니다."},
	"note.checkout":               {"Built in: commit {sha} is in the workspace without a .git folder, so Git commands do not work. Git LFS files are pointer files.", "기본 제공: 커밋 {sha}이 작업 공간에 있지만 .git 폴더가 없어 Git 명령은 동작하지 않습니다. Git LFS 파일은 포인터 파일입니다."},
	"note.setup":                  {"Not run: OwnGit does not install, check or select {tool} {version}. This job uses the {tool} already on the computer or in the image; a version matrix runs that same tool each time.", "실행하지 않음: OwnGit은 {tool} {version}을 설치하거나 확인하거나 고르지 않습니다. 이 작업은 컴퓨터나 이미지에 이미 있는 {tool}을 쓰며, 버전 매트릭스도 매번 같은 도구로 실행합니다."},
	"note.cache":                  {"Not run: OwnGit keeps no cache, so later steps start without restored files.", "실행하지 않음: OwnGit은 캐시를 보관하지 않으므로 다음 단계는 복원된 파일 없이 시작합니다."},
	"note.artifact":               {"Not run: OwnGit keeps no artifacts. The files stay only in this job's workspace.", "실행하지 않음: OwnGit은 산출물을 보관하지 않습니다. 파일은 이 작업의 작업 공간에만 남습니다."},
	"note.runs_on":                {"runs-on: {label} is shown only. This job ran {where}.", "runs-on: {label}은 표시만 합니다. 이 작업은 {where}에서 실행되었습니다."},
	"note.not_applied":            {"{key} is not applied: {effect}.", "{key}는 적용되지 않습니다: {effect}."},
	"note.nothing_ran":            {"Nothing ran: every step was skipped or built in.", "실행된 단계 없음: 모든 단계를 건너뛰었거나 기본 제공 단계였습니다."},
	"note.tolerated":              {"Failed, but continue-on-error lets the work continue.", "실패했지만 continue-on-error 때문에 계속 진행합니다."},
	"note.paths_unknown":          {"Not run: OwnGit could not list the changed files ({reason}), so the paths filter could not be decided. Rerun to run it anyway.", "실행하지 않음: 바뀐 파일 목록을 읽지 못해({reason}) paths 조건을 판단할 수 없었습니다. 그래도 실행하려면 다시 실행하세요."},
	"note.concurrency_wait":       {"Waiting for run #{n} in concurrency group {group}.", "동시 실행 그룹 {group}의 실행 #{n}을 기다리는 중입니다."},
	"note.concurrency_cancel":     {"Cancelled by run #{n} in concurrency group {group}.", "동시 실행 그룹 {group}의 실행 #{n} 때문에 취소되었습니다."},
	"note.max_parallel":           {"Waiting: max-parallel allows {n} jobs of this matrix at a time.", "대기 중: max-parallel이 이 매트릭스의 작업을 한 번에 {n}개까지만 허용합니다."},
	"note.fail_fast":              {"Cancelled because {job} failed and fail-fast is on (GitHub's default). Set fail-fast: false to run every combination.", "{job}이 실패했고 fail-fast가 켜져 있어(GitHub 기본값) 취소되었습니다. 모든 조합을 실행하려면 fail-fast: false로 설정하세요."},
	"note.uncertain":              {"Execution of {job} is uncertain: OwnGit stopped, or lost contact with the runner, before the job finished. Jobs that need it were skipped. Rerun the run when that is safe.", "{job}의 실행 결과를 알 수 없습니다. 작업이 끝나기 전에 OwnGit이 멈췄거나 러너와 연결이 끊겼습니다. 이 작업이 필요한 작업은 건너뛰었습니다. 안전할 때 다시 실행하세요."},
	"note.schedule_often":         {"This schedule asks for runs less than 5 minutes apart. OwnGit starts it at most every 5 minutes and skips the times between.", "이 일정은 5분보다 짧은 간격을 요청합니다. OwnGit은 최소 5분 간격으로만 시작하고 그 사이 시각은 건너뜁니다."},
	"note.missed":                 {"Scheduled for {slot}, started {time}. OwnGit was not running, or the previous run had not finished. Missed times run once.", "{slot}에 예정된 실행을 {time}에 시작했습니다. OwnGit이 꺼져 있었거나 이전 실행이 끝나지 않았습니다. 놓친 시각은 한 번만 실행합니다."},
	"note.schedule_paused":        {"Paused: the last scheduled run's execution is uncertain. Rerun that run or start the workflow by hand to resume.", "일시 중지됨: 마지막 예약 실행의 결과를 알 수 없습니다. 그 실행을 다시 실행하거나 워크플로를 직접 실행하면 다시 시작합니다."},
	"note.runner_old":             {"Waiting for a runner that supports workflows (OwnGit 1.1.8 or later).", "워크플로를 지원하는 러너(OwnGit 1.1.8 이상)를 기다리는 중입니다."},
	"note.secret_missing":         {"Secret {name} is not set for this repository, so it is empty.", "이 저장소에 {name} 시크릿이 설정되어 있지 않아 빈 값으로 실행합니다."},
	"note.plain_http":             {"This runner reaches OwnGit without HTTPS. Unless the network encrypts the connection, as Tailscale does, its secrets cross the network unencrypted.", "이 러너는 HTTPS 없이 OwnGit에 접속합니다. Tailscale처럼 연결을 암호화하는 네트워크가 아니면 시크릿이 암호화되지 않은 채 네트워크를 지납니다."},
	"note.open_dispatch":          {"OwnGit is in open access mode: anyone who can reach it can start this workflow, and the workflow can read this repository's secrets.", "OwnGit이 공개 접근 모드입니다. 접속할 수 있는 사람은 누구나 이 워크플로를 실행할 수 있고, 워크플로는 이 저장소의 시크릿을 읽을 수 있습니다."},
	"note.ignored_env":            {"{name} cannot be set through GITHUB_ENV, so it was ignored.", "{name}은 GITHUB_ENV로 설정할 수 없어 무시했습니다."},
	"note.mask_limit":             {"This job registered more than 256 masked values, so OwnGit stopped it and withheld the rest of its output.", "이 작업이 가릴 값을 256개보다 많이 등록해 OwnGit이 작업을 멈추고 나머지 출력을 보관하지 않았습니다."},
	"note.stopped":                {"The job was stopped, so later steps did not run, including steps with if: always().", "작업이 중단되어 이후 단계는 if: always() 단계를 포함해 실행되지 않았습니다."},
	"note.summary":                {"OwnGit does not show the job summary this step wrote to $GITHUB_STEP_SUMMARY.", "이 단계가 $GITHUB_STEP_SUMMARY에 쓴 작업 요약은 OwnGit에 표시되지 않습니다."},
}

var workflowAliases = map[string]string{"note.secret_unset": "note.secret_missing"}

var workflowNamedPlaceholder = regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9_]*\}`)

func workflowMessageText(m WorkflowMessage) (string, string) {
	code := m.Code
	if alias, ok := workflowAliases[code]; ok {
		code = alias
	}
	if text, ok := workflowTexts[code]; ok {
		en, enFilled := fillWorkflowText(text.en, m, func(t struct{ en, ko string }) string { return t.en })
		ko, koFilled := fillWorkflowText(text.ko, m, func(t struct{ en, ko string }) string { return t.ko })
		if enFilled && koFilled {
			return en, ko
		}
	}
	if Has(MessageCode(m.Code)) {
		return Text(LangEN, MessageCode(m.Code)), Text(LangKO, MessageCode(m.Code))
	}
	detail := m.Detail
	if detail == "" {
		detail = m.Code
	}
	return detail, detail
}

func fillWorkflowText(text string, m WorkflowMessage, pick func(struct{ en, ko string }) string) (string, bool) {
	filled := true
	result := workflowNamedPlaceholder.ReplaceAllStringFunc(text, func(placeholder string) string {
		name := placeholder[1 : len(placeholder)-1]
		if m.Args == nil {
			filled = false
			return placeholder
		}
		switch {
		case name == "path" && m.Path != "":
			return m.Path
		case name == "line" && m.Line > 0:
			return strconv.Itoa(m.Line)
		}
		value, ok := m.Args[name]
		if !ok || value == "" {
			filled = false
			return placeholder
		}
		if name == "line" || m.Code == "workflow.duplicate_key" && (name == "a" || name == "b") {
			line, err := strconv.Atoi(value)
			if err != nil || line <= 0 {
				filled = false
				return placeholder
			}
		}
		if name == "hint" && (m.Code == "workflow.context" || m.Code == "workflow.function") {
			if hint, known := workflowTexts["hint."+value]; known {
				return pick(hint)
			}
			if key, known := workflowHintKeys[value]; known {
				return pick(workflowTexts[key])
			}
		}
		if effect, known := workflowEffectTexts[value]; known && m.Code == "note.not_applied" && name == "effect" {
			return pick(effect)
		}
		if ko, known := workflowArgTexts[m.Code+"."+name][value]; known {
			return pick(struct{ en, ko string }{value, ko})
		}
		return value
	})
	return result, filled
}

func biWorkflowMessage(lang Lang, m WorkflowMessage) template.HTML {
	en, ko := workflowMessageText(m)
	return biText(lang, en, ko)
}
