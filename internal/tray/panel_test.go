package tray

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/server"
	"owngit/internal/webui"
)

// The panel that other programs draw is the view with its words, in plain
// JSON field names, and names no address to open.
func TestPanelOfARunningServer(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	status := &server.TrayStatus{
		OK: true, State: "running", Version: "1.1.3", DashboardURL: "http://127.0.0.1:7654", CloneAddress: "http://127.0.0.1:7654/git/",
		Pushes: []server.TrayPush{{RepositoryID: "notes", Repository: "notes", Ref: "refs/heads/main", Branch: "main", PushedAt: now.Add(-12 * time.Minute)}},
	}
	panel := NewPanel(Report{Condition: Running, Status: status, Dashboard: "http://127.0.0.1:7654"}, webui.LangKO, now)
	if panel.Condition != "running" || panel.State != "실행 중" || !panel.CanOpen || panel.CloneAddress != status.CloneAddress ||
		!reflect.DeepEqual(panel.Pushes, []PanelPush{{Repository: "notes", Branch: "main", When: "오늘 14:48"}}) ||
		panel.Labels.Hide != "패널에서 숨기기" || panel.Labels.CopyClone != "클론 주소 복사" || panel.Labels.Open != "대시보드 열기" {
		t.Fatalf("panel %+v", panel)
	}
	encoded, err := json.Marshal(panel)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"condition", "state", "tooltip", "subtitle", "notice", "command_intro", "command", "clone_address", "pushes", "no_pushes", "can_open", "labels"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("the panel has no %q: %s", key, encoded)
		}
	}
	if strings.Contains(string(encoded), status.DashboardURL+`"`) {
		t.Errorf("the panel names the dashboard's address: %s", encoded)
	}
}

func TestPanelWithoutAnAnswer(t *testing.T) {
	panel := NewPanel(Report{Condition: Stopped, Message: "OwnGit is not running.", Repair: "owngit service start"}, webui.LangEN, time.Now())
	if panel.Condition != "stopped" || panel.CanOpen || panel.Command != "owngit service start" || panel.Pushes == nil || len(panel.Pushes) != 0 || panel.Notice == nil {
		t.Fatalf("stopped panel %+v", panel)
	}
	if NewPanel(Report{Condition: Unavailable}, webui.LangEN, time.Now()).Condition != "unavailable" {
		t.Error("an unavailable status is not unavailable")
	}
}

func TestDesktopLanguage(t *testing.T) {
	for _, check := range []struct {
		all, messages, lang string
		want                webui.Lang
	}{
		{"", "", "ko_KR.UTF-8", webui.LangKO},
		{"", "en_US.UTF-8", "ko_KR.UTF-8", webui.LangEN},
		{"ko_KR.UTF-8", "en_US.UTF-8", "en_US.UTF-8", webui.LangKO},
		{"", "", "", webui.LangEN},
	} {
		t.Setenv("LC_ALL", check.all)
		t.Setenv("LC_MESSAGES", check.messages)
		t.Setenv("LANG", check.lang)
		if got := DesktopLanguage(); got != check.want {
			t.Errorf("LC_ALL=%q LC_MESSAGES=%q LANG=%q: %s, want %s", check.all, check.messages, check.lang, got, check.want)
		}
	}
}

// Desktop bars recolor a symbolic icon by replacing the fill of its shapes,
// so every icon is well-formed SVG drawn from fills alone, and the Omarchy
// bar widget ships the same drawings.
func TestIconsAreSymbolicSVG(t *testing.T) {
	drawings := map[string]string{"tile": TileSVG()}
	for condition, name := range conditionNames {
		drawings["owngit-"+name+"-symbolic"] = GlyphSVG(condition)
		drawings["owngit-state-"+name+"-symbolic"] = SymbolSVG(condition)
	}
	for name, drawing := range drawings {
		decoder := xml.NewDecoder(strings.NewReader(drawing))
		for {
			token, err := decoder.Token()
			if err != nil {
				if err.Error() != "EOF" {
					t.Errorf("%s is not well-formed: %v", name, err)
				}
				break
			}
			if element, ok := token.(xml.StartElement); ok {
				for _, attribute := range element.Attr {
					if attribute.Name.Local == "stroke" {
						t.Errorf("%s draws a stroke, which desktops do not recolor", name)
					}
				}
			}
		}
		if name == "tile" {
			continue
		}
		shipped, err := os.ReadFile(filepath.Join("..", "..", "integrations", "omarchy", "owngit.status", "assets", name+".svg"))
		if err != nil || string(shipped) != drawing {
			t.Errorf("the Omarchy widget's %s.svg differs from the icon (%v)", name, err)
		}
	}
	// Every condition has its own drawing.
	seen := map[string]string{}
	for name, drawing := range drawings {
		if other, ok := seen[drawing]; ok {
			t.Errorf("%s and %s are the same drawing", name, other)
		}
		seen[drawing] = name
	}
}

// outline draws every part clockwise, so the nonzero rule fills their union
// instead of cutting holes where they overlap.
func TestOutlinePartsRunClockwise(t *testing.T) {
	corners := []point{{0, 0}, {0, 1}, {1, 1}, {1, 0}}
	if area(corners) >= 0 {
		t.Fatalf("a counterclockwise square has area %v", area(corners))
	}
	for _, s := range append(append([]stroke{}, logo...), stroke{points: []point{{28.5, 35.5}, {35.5, 28.5}}, w: 2.4}) {
		path := outline([]stroke{s}, nil)
		for _, part := range strings.Split(path, "Z")[:strings.Count(path, "Z")] {
			if !strings.Contains(part, "L") {
				if !strings.Contains(part, " 0 1 1 ") {
					t.Errorf("a round end runs counterclockwise: %s", part)
				}
				continue
			}
			var quad []point
			for _, pair := range strings.FieldsFunc(part, func(r rune) bool { return r == 'M' || r == 'L' }) {
				var p point
				if _, err := fmt.Sscan(pair, &p.x, &p.y); err != nil {
					t.Fatalf("read %q: %v", pair, err)
				}
				quad = append(quad, p)
			}
			if area(quad) <= 0 {
				t.Errorf("a segment runs counterclockwise: %s", part)
			}
		}
	}
}
