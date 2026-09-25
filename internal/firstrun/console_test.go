package firstrun

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func scriptedConsole(chunks ...string) (*console, *bytes.Buffer) {
	input := make(chan []byte, len(chunks))
	for _, chunk := range chunks {
		input <- []byte(chunk)
	}
	close(input)
	var echo bytes.Buffer
	return &console{ctx: context.Background(), input: input, finished: make(chan struct{}),
		echo: func(s string) { echo.WriteString(s) }}, &echo
}

func TestHiddenAnswersEchoNothing(t *testing.T) {
	c, echo := scriptedConsole("secret 한글\x7f\x7f글\r")
	got, err := c.readLine(true)
	if err != nil || got != "secret 글" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if echo.String() != "\n" {
		t.Fatalf("hidden input echoed %q", echo.String())
	}
}

func TestVisibleAnswersSupportEditing(t *testing.T) {
	c, echo := scriptedConsole("/tmp/가x\x7f", "\x15/srv/git\r")
	got, err := c.readLine(false)
	if err != nil || got != "/srv/git" {
		t.Fatalf("got %q err=%v", got, err)
	}
	// Backspace over a wide character erases two columns.
	if !strings.HasPrefix(echo.String(), "/tmp/가x\b \b\b\b  \b\b") {
		t.Fatalf("echo %q", echo.String())
	}
}

func TestEscapeSequencesNeverBecomeText(t *testing.T) {
	c, _ := scriptedConsole("a\x1b[A\x1b]11;rgb:0000/0000/0000\x07\x1b[?62;22cb\x1bOPc\r")
	got, err := c.readLine(false)
	if err != nil || got != "abc" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// A hidden answer keeps every character the web form keeps, so a pasted
// password is saved exactly as the browser would send it. Nothing is
// normalized: a decomposed and a composed é stay different, as on the web.
func TestHiddenAnswersKeepWhatTheWebFormKeeps(t *testing.T) {
	for _, password := range []string{
		"Shared\u00a0PW\u200bzq81x",                      // no-break space, zero-width space
		"cafe\u0301 e\u0301\u0327 \u00e9",                // combining marks next to a composed letter
		"\U0001F510\U0001F468\u200d\U0001F4BB\ufe0f key", // emoji, joiner, variation selector
		"tab\there\x01\x00\u202eend\u0085",               // controls and a direction override
		" spaces around ",
	} {
		c, echo := scriptedConsole(password + "\r")
		got, err := c.readLine(true)
		if err != nil || got != password {
			t.Fatalf("got %q, want %q (err=%v)", got, password, err)
		}
		if echo.String() != "\n" {
			t.Fatalf("hidden input echoed %q", echo.String())
		}
	}
}

// A visible answer keeps controls and format characters too, but echoes
// only what can be shown, so typing cannot move the cursor or reorder text.
func TestVisibleAnswersEchoOnlyGraphicCharacters(t *testing.T) {
	c, echo := scriptedConsole("a\tb\u202ec\u00a0d\x01\x7f\x7f\x7f\r")
	got, err := c.readLine(false)
	if err != nil || got != "a\tb\u202ec" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if echo.String() != "abc\u00a0d\b \b\b \b\n" {
		t.Fatalf("echo %q", echo.String())
	}
}

func TestCtrlCAndClosedInputStop(t *testing.T) {
	c, _ := scriptedConsole("abc\x03")
	if _, err := c.readLine(true); err != errStopped {
		t.Fatalf("Ctrl-C err=%v", err)
	}
	c, _ = scriptedConsole("abc")
	if _, err := c.readLine(false); err != errStopped {
		t.Fatalf("closed input err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ = scriptedConsole()
	c.ctx = ctx
	if _, err := c.readLine(false); err != errStopped {
		t.Fatalf("stopped server err=%v", err)
	}
}

func TestSetupFinishedElsewhereInterruptsAPrompt(t *testing.T) {
	finished := make(chan struct{})
	close(finished)
	c := &console{ctx: context.Background(), input: make(chan []byte), finished: finished, echo: func(string) {}}
	if _, err := c.readLine(true); err != errFinishedElsewhere {
		t.Fatalf("err=%v", err)
	}
}

func TestAnswerLengthIsBounded(t *testing.T) {
	c, _ := scriptedConsole(strings.Repeat("a", maxAnswerRunes+10) + "\r")
	got, err := c.readLine(true)
	if err != nil || len(got) != maxAnswerRunes {
		t.Fatalf("length %d err=%v", len(got), err)
	}
}

func TestYesNoAndKeys(t *testing.T) {
	for value, want := range map[string]bool{"y": true, "Y": true, "yes": true, "ㅛ": true, "예": true, "": false, "n": false, "ㅜ": false, "아니요": false} {
		got, ok := yesNo(value)
		if !ok || got != want {
			t.Errorf("%q: %v %v", value, got, ok)
		}
	}
	if _, ok := yesNo("maybe"); ok {
		t.Error("an unknown answer was accepted")
	}
	if normalizeKey("ㅅ") != "t" || normalizeKey("L") != "l" || normalizeKey("ㅣ") != "l" {
		t.Error("jamo or case not normalized")
	}
}

func TestLogGateHoldsLinesUntilFlushedAndIsBounded(t *testing.T) {
	var target bytes.Buffer
	gate := NewLogGate(&target)
	gate.Write([]byte("before\n"))
	gate.Hold()
	gate.Write([]byte("held one\n"))
	gate.Write([]byte("held two\n"))
	if target.String() != "before\n" {
		t.Fatalf("a held line was written: %q", target.String())
	}
	gate.Flush()
	if target.String() != "before\nheld one\nheld two\n" {
		t.Fatalf("flush wrote %q", target.String())
	}
	target.Reset()
	for i := 0; i < heldLogLines+5; i++ {
		gate.Write([]byte("line\n"))
	}
	gate.Write([]byte(strings.Repeat("x", heldLogLine*2) + "\n"))
	gate.Release()
	lines := strings.Split(strings.TrimSuffix(target.String(), "\n"), "\n")
	if !strings.HasPrefix(lines[0], "(6 earlier log lines were dropped") || len(lines) != heldLogLines+1 {
		t.Fatalf("first=%q lines=%d", lines[0], len(lines))
	}
	if last := lines[len(lines)-1]; len(last) > heldLogLine || !strings.HasSuffix(last, " ...") {
		t.Fatalf("a long line was not shortened: %d", len(last))
	}
	target.Reset()
	gate.Write([]byte("after\n"))
	if target.String() != "after\n" {
		t.Fatal("the gate still holds after Release")
	}
}

// Pipes, files and redirected output are not terminals, so services and CI
// keep the setup file flow.
func TestPipesAreNotInteractive(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if Interactive(reader, writer) {
		t.Fatal("a pipe was taken for a terminal")
	}
	file, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if Interactive(file, file) {
		t.Fatal("a file was taken for a terminal")
	}
}

func TestLocaleChoosesTheDefaultLanguage(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	if localeLanguage(env(map[string]string{"LANG": "ko_KR.UTF-8"})) != "ko" {
		t.Error("Korean locale")
	}
	if localeLanguage(env(map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "ko_KR.UTF-8"})) != "en" {
		t.Error("LC_ALL wins")
	}
	if localeLanguage(env(nil)) != "en" {
		t.Error("default")
	}
}
