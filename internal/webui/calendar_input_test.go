package webui

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCalendarKeyboardAndComposingEscape(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the calendar and input check")
	}
	type calendar struct {
		HTML    string
		Initial int
		Last    int
	}
	var calendars []calendar
	r := newRenderer(t)
	for _, row := range []struct {
		year, today, selected, initial int
	}{
		{2026, 282, 0, 282},
		{2026, 282, 227, 227},
		{2026, 282, 365, 365},
		{2024, 366, 0, 366},
		{2027, 0, 0, 1},
	} {
		for _, links := range []bool{true, false} {
			g := ActivityGraph{Year: row.year, Available: true, Complete: true}
			for d := time.Date(row.year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == row.year; d = d.AddDate(0, 0, 1) {
				day := ActivityDay{Date: d, Future: d.YearDay() > row.today}
				if links {
					day.URL = "/activity?date=" + d.Format("2006-01-02")
				}
				g.Days = append(g.Days, day)
				if d.YearDay() == row.selected {
					g.SelectedDate = d
				}
			}
			calendars = append(calendars, calendar{render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: g}), row.initial, len(g.Days)})
		}
	}
	input, err := json.Marshal(calendars)
	noErr(t, err)
	script := scriptSource(t)
	graph := section(t, script, "  all('[data-graph]')", "  /* 4. setup link")
	sidebar, err := assetFS.ReadFile("assets/sidebar.js")
	noErr(t, err)
	const check = `
const assert = require('node:assert/strict');
const vm = require('node:vm');
for (const sample of JSON.parse(require('node:fs').readFileSync(0, 'utf8'))) {
  let active = null;
  const handlers = {};
  const scroll = {scrollWidth:800,clientWidth:334,scrollLeft:0,getBoundingClientRect:()=>({left:0,right:334})};
  const cells = [...sample.HTML.matchAll(/<(?:a|span) class="hm__cell"[^>]+>/g)].map(([tag]) => {
    const attrs = Object.fromEntries([...tag.matchAll(/([\w-]+)="([^"]*)"/g)].map(m=>[m[1],m[2]]));
    const index = Number(attrs['data-index']);
    return {getAttribute:n=>attrs[n],setAttribute:(n,v)=>{attrs[n]=v;},focus(){active=this;},closest(){return this;},getBoundingClientRect:()=>({left:index*2-scroll.scrollLeft,right:index*2+10-scroll.scrollLeft})};
  });
  const originalOrder = cells.map(c=>c.getAttribute('data-index'));
  const graph = {getAttribute:()=>'',querySelector:s=>s==='.hm__scroll'?scroll:s.includes('aria-current')?cells.find(c=>c.getAttribute('aria-current')==='true'):null,addEventListener:(n,f)=>{handlers[n]=f;}};
  vm.runInNewContext(process.argv[1], {all:s=>s==='[data-graph]'?[graph]:cells.slice(),root:{getAttribute:()=> 'en'}});
  const stop = () => cells.filter(c=>c.getAttribute('tabindex')==='0');
  assert.equal(stop().length,1);
  assert.equal(Number(stop()[0].getAttribute('data-index')),sample.Initial);
  assert.equal(active,null,'initialization must not move focus');
  assert.ok(stop()[0].getBoundingClientRect().right<=334,'initial day must be visible');
  assert.deepEqual(cells.map(c=>c.getAttribute('data-index')),originalOrder,'keep weekday DOM order');
  for (const [key,index] of [['Home',1],['ArrowRight',8],['ArrowDown',9],['ArrowUp',8],['ArrowLeft',1],['End',sample.Last]]) {
    let prevented = false;
    handlers.keydown({target:stop()[0],key,preventDefault(){prevented=true;}});
    assert.equal(Number(active.getAttribute('data-index')),index,key);
    assert.equal(stop().length,1);
    assert.equal(prevented,true);
  }
  for (const key of ['ArrowDown','ArrowRight','Enter',' ']) {
    const before = active;
    handlers.keydown({target:active,key,preventDefault(){assert.fail('boundary or link key intercepted');}});
    assert.equal(active,before);
  }
}
for (const event of [{key:'Escape',isComposing:true},{key:'Escape',keyCode:229},{key:'Escape'},{key:'Enter'}]) {
  for (const visible of [true,false]) {
    let open=true,focused=false;
    const handlers={};
    const toggle={offsetParent:visible?{}:null,setAttribute(){},addEventListener(){},focus(){focused=true;}};
    const sidebar={querySelector:()=>toggle,classList:{add(){},contains:()=>open,toggle:(name,value)=>{open=value;}},addEventListener:(name,handler)=>{handlers[name]=handler;}};
    const document={documentElement:{classList:{add(name){assert.equal(name,'sidebar-ready');}}},addEventListener:(name,handler)=>{handlers[name]=handler;}};
    vm.runInNewContext(process.argv[2],{document});
    handlers.keydown({...event,target:{closest:()=>sidebar}});
    const closes=visible && event.key==='Escape' && !event.isComposing && event.keyCode!==229;
    assert.equal(open,!closes); assert.equal(focused,closes);
  }
}
`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", check, graph, string(sidebar))
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("calendar and composing Escape: %v\n%s", err, output)
	}
}
