package firstrun

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"owngit/internal/server"
	"owngit/internal/webui"
)

// flow asks the setup questions in the order of the web setup page and saves
// the answers through the same server code.
type flow struct {
	ctx     context.Context
	lang    webui.Lang
	screen  *screen
	console *console
	app     *server.App
	// origin is the owner-facing address, such as http://127.0.0.1:7654.
	origin string
	// listen is the address OwnGit listens on; network is true when other
	// devices can connect to it.
	listen  string
	network bool
	// otherDevices is the address other devices use, when it differs from
	// origin and OwnGit accepts it.
	otherDevices string
	suggested    string
	openBrowser  func(string) error
	stateDir     string
	tailscale    <-chan Tailscale
	found        *Tailscale

	folder   string
	access   string
	shared   string
	admin    string
	insecure bool
}

// errRestart returns to the first question, after "Start over".
var errRestart = errors.New("start over")

func (f *flow) say(key string, replacements ...string) string {
	return say(f.lang, key, replacements...)
}

func (f *flow) text(code webui.MessageCode) string { return webui.Text(f.lang, code) }

func (f *flow) toggleLanguage() {
	if f.lang == webui.LangKO {
		f.lang = webui.LangEN
	} else {
		f.lang = webui.LangKO
	}
}

func (f *flow) port() string {
	_, port, err := net.SplitHostPort(f.listen)
	if err != nil {
		return "7654"
	}
	return port
}

// run asks the questions until setup is complete. It returns errStopped
// when the owner stops, after printing the stop card, and nil when setup was
// completed by any path.
func (f *flow) run() error {
	err := f.questions()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errFinishedElsewhere):
		return f.finishedElsewhere()
	case errors.Is(err, errStopped):
		f.stopped()
		return errStopped
	}
	return err
}

func (f *flow) questions() error {
	f.screen.banner(f.say("subtitle"), f.origin+"/")
	if err := f.language(); err != nil {
		return err
	}
	for {
		choice, err := f.choose(func() choiceCard {
			return choiceCard{
				title: f.say("start_title"), help: f.say("start_help"),
				options: [][2]string{{f.say("opt_term"), f.say("opt_term_help")}, {f.say("opt_web"), f.say("opt_web_help")}},
			}
		})
		if err != nil {
			return err
		}
		if choice == 0 {
			return f.terminal()
		}
		return f.web()
	}
}

func (f *flow) language() error {
	items := []item{optionItem("1", "English", ""), optionItem("2", "한국어", ""), blankItem(),
		textItem(languageHint[0], roleMuted), textItem(languageHint[1], roleMuted)}
	f.screen.card(languageTitle, items, cardStyle{})
	fallback := "1"
	if f.lang == webui.LangKO {
		fallback = "2"
	}
	for {
		f.screen.prompt(languagePrompt, fallback, "")
		answer, err := f.console.readLine(false)
		if err != nil {
			return err
		}
		answer = normalizeKey(strings.TrimSpace(answer))
		if answer == "" {
			answer = fallback
		}
		switch answer {
		case "1":
			f.lang = webui.LangEN
			return nil
		case "2":
			f.lang = webui.LangKO
			return nil
		}
		f.screen.notice("err", languageBad)
	}
}

// choiceCard is a card with numbered options answered with Enter. L switches
// the language and draws the card again.
type choiceCard struct {
	title, help string
	step        string
	pairs       []pair
	options     [][2]string // label and help
	fallback    int
}

// choose shows the card that build returns and reads the chosen option. The
// card is built again after L switches the language.
func (f *flow) choose(build func() choiceCard) (int, error) {
	for {
		c := build()
		items := []item{}
		if c.help != "" {
			items = append(items, textItem(c.help, roleMuted))
		}
		if len(c.pairs) != 0 {
			if c.help != "" {
				items = append(items, blankItem())
			}
			items = append(items, pairsItem(c.pairs))
		}
		items = append(items, blankItem())
		anyHelp := false
		for _, option := range c.options {
			anyHelp = anyHelp || option[1] != ""
		}
		for i, option := range c.options {
			if i > 0 && anyHelp {
				items = append(items, blankItem())
			}
			items = append(items, optionItem(strconv.Itoa(i+1), option[0], option[1]))
		}
		items = append(items, blankItem(), optionItem("L", f.say("lang_other"), ""))
		f.screen.card(c.title, items, cardStyle{right: c.step})
		fallback := strconv.Itoa(c.fallback + 1)
		switched := false
		for !switched {
			f.screen.prompt(f.say("choice"), fallback, f.say("suggested"))
			answer, err := f.console.readLine(false)
			if err != nil {
				return 0, err
			}
			answer = normalizeKey(strings.TrimSpace(answer))
			if answer == "" {
				answer = fallback
			}
			if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(c.options) {
				return n - 1, nil
			}
			if answer == "l" {
				f.toggleLanguage()
				switched = true
				continue
			}
			f.screen.notice("err", f.say("choice_bad", "n", strconv.Itoa(len(c.options))))
		}
	}
}

// question is a card answered by typing. The card is shown once; a refused
// answer shows only the problem and the prompt again.
type question struct {
	title, help, sub, step string
	field                  string
	fallback               string
	hidden                 bool
}

func (f *flow) questionCard(q question) {
	items := []item{textItem(q.help, roleMuted)}
	if q.sub != "" {
		items = append(items, blankItem(), textItem(q.sub, rolePlain))
	}
	f.screen.card(q.title, items, cardStyle{right: q.step})
}

func (f *flow) answer(q question, problem string) (string, error) {
	if problem != "" {
		f.screen.notice("err", problem)
	}
	f.screen.prompt(q.field, q.fallback, f.say("suggested"))
	value, err := f.console.readLine(q.hidden)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" && q.fallback != "" {
		return q.fallback, nil
	}
	return value, nil
}

// steps lists the terminal steps; the plain HTTP step appears only when
// other devices can connect.
func (f *flow) steps() []func(string) error {
	steps := []func(string) error{f.askFolder, f.askAccess, f.askAdmin, f.showDevices}
	if f.network {
		steps = append(steps, f.askConnection)
	}
	return append(steps, f.review)
}

// terminal asks every question in the terminal and saves the answers.
func (f *flow) terminal() error {
	for {
		steps := f.steps()
		restart := false
		for i := 0; i < len(steps) && !restart; i++ {
			err := steps[i](stepLabel(i+1, len(steps)))
			if errors.Is(err, errRestart) {
				restart = true
				break
			}
			if err != nil {
				return err
			}
		}
		if restart {
			continue
		}
		retry, err := f.save()
		if err != nil || !retry {
			return err
		}
	}
}

func (f *flow) askFolder(step string) error {
	q := question{
		title: f.text(webui.MsgSetupStorageLabel), help: f.say("storage_help"), sub: f.say("storage_default"),
		step: step, field: f.say("storage_field"), fallback: f.suggested,
	}
	if f.folder != "" {
		q.fallback = f.folder
	}
	f.questionCard(q)
	problem := ""
	for {
		value, err := f.answer(q, problem)
		if err != nil {
			return err
		}
		path := expandHome(strings.TrimSpace(value))
		if code := f.app.CheckRepositoryFolder(path); code != "" {
			problem = f.text(code)
			continue
		}
		f.folder = filepath.Clean(path)
		f.screen.notice("ok", f.say("storage_ok"))
		return nil
	}
}

// expandHome expands a leading ~ to the home directory, as a shell would.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

func (f *flow) askAccess(step string) error {
	build := func() choiceCard {
		fallback := 0
		if f.access == "password" {
			fallback = 1
		}
		return choiceCard{
			title: f.text(webui.MsgSetupAccessLabel), help: f.text(webui.MsgSetupAccessHelp), step: step,
			options: [][2]string{
				{f.text(webui.MsgSetupAccessOpen), f.text(webui.MsgSetupAccessOpenHelp)},
				{f.text(webui.MsgSetupAccessPassword), f.text(webui.MsgSetupAccessPassHelp)},
			},
			fallback: fallback,
		}
	}
	choice, err := f.choose(build)
	if err != nil {
		return err
	}
	if choice == 0 {
		f.access, f.shared = "open", ""
		return nil
	}
	f.access = "password"
	shared, err := f.askPassword(step, false)
	if err != nil {
		return err
	}
	f.shared = shared
	return nil
}

func (f *flow) askAdmin(step string) error {
	admin, err := f.askPassword(step, true)
	if err != nil {
		return err
	}
	f.admin = admin
	return nil
}

// askPassword reads a password twice without showing it and applies the
// server's password rules.
func (f *flow) askPassword(step string, admin bool) (string, error) {
	q := question{
		title: f.say("shared_title"), help: f.text(webui.MsgSetupAccessPassHelp), sub: f.say("pw_rule"),
		step: step, field: f.say("shared_title"), hidden: true,
	}
	if admin {
		q.title, q.help, q.field = f.text(webui.MsgSetupAdminLabel), f.text(webui.MsgSetupAdminHelp), f.text(webui.MsgSetupAdminLabel)
	}
	f.questionCard(q)
	problem := ""
	for {
		first, err := f.answer(q, problem)
		if err != nil {
			return "", err
		}
		var code webui.MessageCode
		if admin {
			code = server.AdminPasswordProblem(first, f.access, f.shared)
		} else {
			code = server.AccessPasswordProblem(first)
		}
		if code != "" {
			problem = f.text(code)
			continue
		}
		second, err := f.answer(question{field: f.say("again_field"), hidden: true}, "")
		if err != nil {
			return "", err
		}
		if first != second {
			problem = f.say("pw_mismatch")
			continue
		}
		if admin {
			f.screen.notice("ok", f.text(webui.MsgSetupAdminSaved))
		} else {
			f.screen.notice("ok", f.text(webui.MsgSetupAccessPassSaved))
		}
		return first, nil
	}
}

func (f *flow) showDevices(step string) error {
	if f.found == nil {
		select {
		case found := <-f.tailscale:
			f.found = &found
		case <-f.ctx.Done():
			return errStopped
		}
	}
	found := *f.found
	items := []item{}
	switch found.State {
	case TailscaleRunning:
		items = append(items, tagItem("ok", f.say("dev_found")), blankItem())
		pairs := []pair{{label: f.say("dev_addr"), value: found.IPv4}}
		if found.Name != "" {
			pairs = append(pairs, pair{label: f.say("dev_name"), value: found.Name})
		}
		items = append(items, pairsItem(pairs), blankItem(), textItem(f.say("dev_how"), rolePlain),
			blankItem(), textItem(f.say("dev_enc"), rolePlain))
	case TailscaleStopped:
		items = append(items, tagItem("note", f.say("dev_stopped")), blankItem(), textItem(f.say("dev_docs"), rolePlain))
	default:
		items = append(items, tagItem("note", f.say("dev_missing")), blankItem(), textItem(f.say("dev_docs"), rolePlain))
	}
	f.screen.card(f.say("dev_title"), items, cardStyle{right: step})
	if found.State == TailscaleRunning {
		// The command sits alone on its line at column 0, so a triple-click
		// or drag copies exactly the command even when the terminal wraps it.
		_, _, limit := f.screen.dims()
		f.screen.blankLine()
		f.screen.emit(colored("  "+f.say("dev_cmd"), roleMuted))
		f.screen.blankLine()
		f.screen.write(f.screen.painter.paint(rolePlain, tailscaleCommand(found, f.port(), f.stateDir), true) + "\n")
		f.screen.blankLine()
		for _, line := range wrapText(f.say("dev_service"), limit-2) {
			f.screen.emit(plain("  "), colored(line, roleMuted))
		}
		f.screen.blankLine()
	}
	f.screen.prompt(f.say("dev_continue"), "", "")
	_, err := f.console.readLine(false)
	return err
}

func (f *flow) askConnection(step string) error {
	items := []item{textItem(f.say("conn_listen", "addr", displayValue(f.listen)), roleWarn), blankItem(),
		textItem(f.text(webui.MsgSetupInsecureHelp), rolePlain)}
	f.screen.card(f.say("conn_label"), items, cardStyle{right: step, titleRole: roleWarn, marker: "[!]", markerRole: roleWarn})
	for {
		yes, err := f.confirm(f.say("conn_q"))
		if err != nil {
			return err
		}
		if yes {
			f.insecure = true
			return nil
		}
		f.screen.notice("err", f.say("conn_need", "port", f.port()))
	}
}

// confirm asks a [y/N] question until it gets y or n.
func (f *flow) confirm(question string) (bool, error) {
	for {
		f.screen.prompt(question+" [y/N]", "", "")
		value, err := f.console.readLine(false)
		if err != nil {
			return false, err
		}
		if yes, ok := yesNo(value); ok {
			return yes, nil
		}
		f.screen.notice("err", f.say("yn_bad"))
	}
}

func (f *flow) review(step string) error {
	build := func() choiceCard {
		accessLabel := f.text(webui.MsgSetupAccessOpen)
		if f.access == "password" {
			accessLabel = f.text(webui.MsgSetupAccessPassword)
		}
		pairs := []pair{{label: f.text(webui.MsgSetupStorageLabel), value: f.folder}, {label: f.say("row_access"), value: accessLabel}}
		if f.access == "password" {
			pairs = append(pairs, pair{label: f.say("shared_title"), value: f.say("val_entered")})
		}
		pairs = append(pairs, pair{label: f.text(webui.MsgSetupAdminLabel), value: f.say("val_entered")})
		if f.network {
			pairs = append(pairs, pair{label: f.say("row_conn"), value: f.say("val_plain"), role: roleWarn})
		} else {
			pairs = append(pairs, pair{label: f.say("row_conn"), value: f.say("val_local")})
		}
		return choiceCard{
			title: f.say("review_title"), help: f.say("review_help"), step: step, pairs: pairs,
			options: [][2]string{{f.text(webui.MsgSetupSubmit), ""}, {f.say("start_over"), ""}},
		}
	}
	choice, err := f.choose(build)
	if err != nil {
		return err
	}
	if choice == 1 {
		return errRestart
	}
	return nil
}

// save completes setup with the answers. It returns retry when the answers
// were refused and the questions start again.
func (f *flow) save() (bool, error) {
	answers := server.SetupAnswers{
		StoragePath: f.folder, AccessMode: f.access, AccessPassword: f.shared, AdminPassword: f.admin,
		InsecureAccepted: f.insecure,
	}
	notices, err := f.app.CompleteSetup(f.ctx, answers, f.network)
	switch {
	case len(notices) != 0:
		for _, notice := range notices {
			f.screen.notice("err", f.text(notice.Code))
		}
		return true, nil
	case errors.Is(err, server.ErrSetupNotSaved):
		return false, f.finishedElsewhere()
	case errors.Is(err, server.ErrSetupUnavailable):
		f.screen.notice("err", f.text(webui.MsgSetupFailed))
		return true, nil
	case errors.Is(err, server.ErrSetupCleanup):
		// Setup is saved; the leftover setup file is reported in the log and
		// removed at the next capability issue.
	case err != nil:
		return false, err
	}
	f.shared, f.admin = "", ""
	f.done()
	return false, nil
}

func (f *flow) done() {
	pairs := []pair{{label: f.say("done_dash"), value: f.origin + "/"}}
	if f.otherDevices != "" {
		pairs = append(pairs, pair{label: f.say("done_other"), value: f.otherDevices})
	}
	f.screen.card(f.say("done_title"), []item{pairsItem(pairs), blankItem(), textItem(f.say("done_next"), rolePlain)},
		cardStyle{titleRole: roleOK, marker: "[ok]", markerRole: roleOK})
	f.screen.emit(colored("  "+f.say("done_log"), roleMuted))
	f.screen.blankLine()
}

// finishedElsewhere reports setup completed by a browser: what it saved,
// then the completion card.
func (f *flow) finishedElsewhere() error {
	settings, err := f.app.Store.Settings(context.WithoutCancel(f.ctx))
	if err != nil || !settings.Initialized {
		f.screen.notice("ok", f.say("done_elsewhere"))
		f.done()
		return nil
	}
	f.screen.blankLine()
	f.screen.emit(plain("  " + f.say("web_progress")))
	access := f.text(webui.MsgSetupAccessOpen)
	if settings.AccessMode == "password" {
		access = f.text(webui.MsgSetupAccessPassword)
	}
	rows := []pair{
		{label: f.text(webui.MsgSetupStorageLabel), value: settings.RepositoryRoot},
		{label: f.say("row_access"), value: access},
		{label: f.text(webui.MsgSetupAdminLabel), value: f.say("val_entered")},
	}
	if f.network && settings.InsecureHTTPAccepted {
		rows = append(rows, pair{label: f.say("row_conn"), value: f.say("val_plain"), role: roleWarn})
	}
	labelWidth := 0
	for _, label := range []string{f.text(webui.MsgSetupStorageLabel), f.say("row_access"), f.text(webui.MsgSetupAdminLabel), f.say("row_conn")} {
		labelWidth = max(labelWidth, textWidth(label))
	}
	labelWidth += 3
	lead := 2 + 5 + labelWidth
	_, _, limit := f.screen.dims()
	for _, row := range rows {
		if limit-lead < 32 {
			f.screen.emit(plain("  "), strong("[ok]", roleOK), plain(" "), colored(row.label, roleMuted))
			for _, value := range valueLines(row.value, limit-9) {
				f.screen.emit(spaces(9), colored(value, row.role))
			}
			continue
		}
		values := valueLines(row.value, limit-lead)
		f.screen.emit(plain("  "), strong("[ok]", roleOK), plain(" "), colored(row.label, roleMuted),
			spaces(labelWidth-textWidth(row.label)), colored(values[0], row.role))
		for _, value := range values[1:] {
			f.screen.emit(spaces(lead), colored(value, row.role))
		}
	}
	f.done()
	return nil
}

func (f *flow) stopped() {
	f.screen.blankLine()
	f.screen.card(f.say("stop_title"), []item{textItem(f.say("stop_body"), rolePlain), textItem(f.say("stop_resume"), rolePlain)},
		cardStyle{titleRole: roleWarn, marker: "[!]", markerRole: roleWarn})
	f.screen.blankLine()
}

// web lets a browser continue setup after the owner approves it here.
func (f *flow) web() error {
	approvals := f.app.Approvals
	approvals.Open()
	address := f.origin + "/setup"
	help := f.say("web_visit", "url", displayValue(address))
	if f.openBrowser != nil {
		help = f.say("web_open", "url", displayValue(address))
	}
	f.screen.card(f.say("web_title"), []item{textItem(help, rolePlain)}, cardStyle{})
	if f.openBrowser != nil {
		if err := f.openBrowser(address); err != nil {
			f.screen.notice("note", f.say("web_visit", "url", displayValue(address)))
		}
	}
	approved := false
	for {
		f.screen.blankLine()
		if approved {
			f.screen.emit(plain("  " + f.say("web_finishing")))
		} else {
			f.screen.emit(plain("  " + f.say("web_wait")))
		}
		f.screen.emit(plain("  "), strong(f.say("web_switch"), rolePlain))
		switchHere, err := f.waitForBrowser()
		if err != nil {
			return err
		}
		if switchHere {
			if err := f.app.EndBrowserSetup(f.ctx); err != nil {
				return err
			}
			return f.terminal()
		}
		request, ok := approvals.Pending()
		if !ok {
			continue
		}
		yes, err := f.approvalCard(request, approved)
		if err != nil {
			return err
		}
		if err := approvals.Decide(request.ID, yes); err != nil {
			f.screen.notice("note", f.say("web_gone"))
			continue
		}
		if yes {
			f.screen.notice("ok", f.say("web_approved"))
			approved = true
		} else {
			f.screen.notice("note", f.say("web_rejected"))
		}
	}
}

// waitForBrowser waits for a browser request, for setup to finish in the
// approved browser, or for T. A new request is shown even after another
// browser was approved, since that browser may have given up or its
// approval may have expired; approving the new one ends the other's setup
// session. Held server log lines are shown meanwhile, since no answer is
// being typed.
func (f *flow) waitForBrowser() (bool, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		f.screen.flushLogs()
		if _, ok := f.app.Approvals.Pending(); ok {
			return false, nil
		}
		k, err := f.console.readKey(f.app.Approvals.Changed(), ticker.C)
		switch {
		case errors.Is(err, errWoken), errors.Is(err, errInputTimeout):
			continue
		case err != nil:
			return false, err
		case k.kind == keyInterrupt:
			return false, errStopped
		case k.kind == keyRune && normalizeKey(string(k.r)) == "t":
			return true, nil
		}
	}
}

// approvalCard shows a browser request and asks whether to approve it.
// replaces says that another browser was approved before, whose setup
// session approving this one ends.
func (f *flow) approvalCard(request server.ApprovalRequest, replaces bool) (bool, error) {
	from := displayValue(request.Address) + " (" + f.say("web_this") + ")"
	style := cardStyle{}
	var items []item
	if !request.Loopback {
		from = displayValue(request.Address) + " (" + f.say("web_other_dev") + ")"
		style = cardStyle{titleRole: roleWarn, marker: "[!]", markerRole: roleWarn}
		items = append(items, textItem(f.say("web_remote"), roleWarn), blankItem())
	}
	items = append(items, codeItem(request.Code), blankItem(),
		pairsItem([]pair{{label: f.say("web_from"), value: from}}), blankItem(), textItem(f.say("web_compare"), rolePlain))
	if replaces {
		items = append(items, blankItem(), textItem(f.say("web_replaces"), roleWarn))
	}
	f.screen.card(f.say("web_req"), items, style)
	return f.confirm(f.say("web_q"))
}
