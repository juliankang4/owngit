package webui

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompareNamesFollowsTheReadersAlphabet(t *testing.T) {
	for _, test := range []struct {
		lang       Lang
		sorted     []string
		equalPairs [][2]string
	}{
		{LangEN, []string{"_scratch", "2024-notes", "api-server", "Blog", "project-2", "project-10", "project10", "site", "가계부", "블로그"}, [][2]string{{"Site", "site"}, {"v007", "v7"}}},
		{LangKO, []string{"_scratch", "2024-notes", "가계부", "블로그", "api-server", "Blog", "project-2", "project-10", "project10", "site"}, [][2]string{{"Site", "site"}}},
	} {
		for index := 1; index < len(test.sorted); index++ {
			a, b := test.sorted[index-1], test.sorted[index]
			if compareNames(a, b, test.lang) >= 0 || compareNames(b, a, test.lang) <= 0 {
				t.Errorf("%s: %q does not come before %q", test.lang, a, b)
			}
		}
		for _, pair := range test.equalPairs {
			if compareNames(pair[0], pair[1], test.lang) != 0 {
				t.Errorf("%s: %q and %q differ in name order", test.lang, pair[0], pair[1])
			}
		}
	}
}

func TestOrderListOffersFourStableOrders(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	items := []NavRepository{
		{ID: "empty-b", Name: "empty-b"},
		{ID: "project-10", Name: "project-10", LastActivity: day(3)},
		{ID: "notes", Name: "notes", LastActivity: day(5)},
		{ID: "site-2", Name: "Site", LastActivity: day(3).Add(400 * time.Millisecond)},
		{ID: "site-1", Name: "site", LastActivity: day(1)},
		{ID: "project-2", Name: "project-2", LastActivity: day(3)},
		{ID: "empty-a", Name: "empty-a"},
	}
	names := func(order ListOrder) []string {
		list := slices.Clone(items)
		OrderNav(list, order, LangEN)
		var ids []string
		for _, item := range list {
			ids = append(ids, item.ID)
		}
		return ids
	}
	byName := []string{"empty-a", "empty-b", "notes", "project-2", "project-10", "site-1", "site-2"}
	byNameDesc := slices.Clone(byName)
	slices.Reverse(byNameDesc)
	for order, want := range map[ListOrder][]string{
		// Equal times, to the second, follow name order; undated rows come
		// last in name order in both time directions.
		OrderUpdatedNewest: {"notes", "project-2", "project-10", "site-2", "site-1", "empty-a", "empty-b"},
		OrderUpdatedOldest: {"site-1", "project-2", "project-10", "site-2", "notes", "empty-a", "empty-b"},
		// Names equal apart from case keep their ID order.
		OrderNameAsc:  byName,
		OrderNameDesc: byNameDesc,
	} {
		if got := names(order); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", order, got, want)
		}
		// Rows that arrive in another order still come out the same.
		reversed := slices.Clone(items)
		slices.Reverse(reversed)
		OrderNav(reversed, order, LangEN)
		for index, item := range reversed {
			if item.ID != want[index] {
				t.Errorf("%s: the order depends on the input order", order)
				break
			}
		}
	}
}

func TestOrderListRanksNamesInBothLanguages(t *testing.T) {
	items := []RepositorySummary{
		{ID: "b", Name: "블로그", Empty: true},
		{ID: "a", Name: "api-server", Empty: true},
		{ID: "g", Name: "가계부", Empty: true},
	}
	OrderRepositories(items, OrderNameAsc, LangKO)
	var got []string
	for _, item := range items {
		got = append(got, item.Name)
	}
	if want := []string{"가계부", "블로그", "api-server"}; !slices.Equal(got, want) {
		t.Fatalf("Korean name order is %v, want %v", got, want)
	}
	// The ranks let the script reorder in place when the language changes:
	// English readers see Latin names first.
	rank := map[string]NameRank{}
	for _, item := range items {
		rank[item.Name] = item.Rank
	}
	if rank["api-server"] != (NameRank{EN: 0, KO: 2}) || rank["가계부"] != (NameRank{EN: 1, KO: 0}) || rank["블로그"] != (NameRank{EN: 2, KO: 1}) {
		t.Errorf("name ranks are %v", rank)
	}
}

func TestParseListOrderRejectsUnknownValues(t *testing.T) {
	for _, order := range ListOrders() {
		if parsed, ok := ParseListOrder(string(order)); !ok || parsed != order {
			t.Errorf("%s is not accepted", order)
		}
	}
	for _, value := range []string{"", "name", "updated", "NAME-ASC", "name-asc "} {
		if parsed, ok := ParseListOrder(value); ok || parsed != DefaultListOrder {
			t.Errorf("%q is accepted as %s", value, parsed)
		}
	}
}

// The dashboard's order control is a form that works without the script,
// names the order in words, and the rows carry what the script needs to
// reorder them in place.
func TestDashboardRendersTheOrderControlAndRowKeys(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		chrome := fullChrome(lang)
		chrome.Nav.Order = OrderNameAsc
		out := render(t, r, OverviewPage{Chrome: chrome, Activity: sampleGraph(), TotalCount: 2, Repositories: []RepositorySummary{
			{ID: "r1", Name: "forge-cli", URL: "/repositories/r1", DefaultBranch: "main", Head: CommitSummary{Subject: "Fix", AuthorDate: testNow}, Rank: NameRank{EN: 1, KO: 1}},
			{ID: "r2", Name: "cedar-config", URL: "/repositories/r2", Empty: true},
		}})
		for _, want := range []string{
			`<form class="sortctl" method="get" action="/" data-order-form>`,
			`<select id="list-order" name="sort" class="sortctl__sel" data-order-select>`,
			`<option value="name-asc" data-en="Name, A to Z" data-ko="이름순 (가나다, ABC)" selected>` + Text(lang, MsgOrderNameAsc) + `</option>`,
			`<button type="submit" class="btn btn--sm" data-order-apply>`,
			`<div class="rows" data-order-list data-order="name-asc">`,
			`data-order-item data-updated="` + strconv.FormatInt(testNow.Unix(), 10) + `" data-rank-en="1" data-rank-ko="1"`,
			// An empty repository shows no time and sorts as undated.
			`<a class="row row--repo" href="/repositories/r2" data-order-item data-updated="" data-rank-en="0" data-rank-ko="0">`,
			`<div class="rowhead" aria-hidden="true">`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s dashboard lacks %s", lang, want)
			}
		}
		menu := sidebarOfOutput(t, out)
		if !strings.Contains(menu, `data-order-list data-order="name-asc"`) ||
			!strings.Contains(menu, `<span data-order-name data-en="Name, A to Z" data-ko="이름순 (가나다, ABC)">`+Text(lang, MsgOrderNameAsc)+`</span>`) {
			t.Errorf("%s sidebar does not state the order in words:\n%s", lang, menu)
		}
	}
}

var (
	orderListTag = regexp.MustCompile(`<div[^>]*data-order-list[^>]*>`)
	orderRowTag  = regexp.MustCompile(`<a[^>]*data-order-item[^>]*>`)
)

// renderedLists returns every ordered list on the page as rendered.
func renderedLists(out string) []renderedList {
	var lists []renderedList
	for _, at := range orderListTag.FindAllStringIndex(out, -1) {
		section := out[at[1]:]
		section = section[:strings.Index(section, "</div>")]
		lists = append(lists, renderedList{Tag: out[at[0]:at[1]], Rows: orderRowTag.FindAllString(section, -1)})
	}
	return lists
}

// The shipped script reorders the dashboard list and the sidebar in place
// when the language changes, and lands on the order the server gives for
// that language, in every order. Korean display names are synthetic here:
// OwnGit cannot create them yet.
func TestLanguageSwitchReordersListsLikeTheServer(t *testing.T) {
	r := newRenderer(t)
	same := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	repositories := []RepositorySummary{
		{ID: "ledger", Name: "가계부", DefaultBranch: "main", Head: CommitSummary{AuthorDate: same}},
		{ID: "api", Name: "api-server", DefaultBranch: "main", Head: CommitSummary{AuthorDate: same}},
		{ID: "blog", Name: "블로그", DefaultBranch: "main", Head: CommitSummary{AuthorDate: same.AddDate(0, -1, 0)}},
		{ID: "p10", Name: "project-10", Empty: true},
		{ID: "p2", Name: "project-2", Empty: true},
	}
	for index := range repositories {
		repositories[index].URL = "/repositories/" + repositories[index].ID
	}
	for _, order := range ListOrders() {
		english := slices.Clone(repositories)
		OrderRepositories(english, order, LangEN)
		korean := slices.Clone(repositories)
		OrderRepositories(korean, order, LangKO)
		chrome := fullChrome(LangEN)
		chrome.Nav.Order = order
		chrome.Nav.Repositories = nil
		for _, item := range english {
			chrome.Nav.Repositories = append(chrome.Nav.Repositories, NavRepository{ID: item.ID, Name: item.Name, URL: item.URL, LastActivity: item.Updated()})
		}
		OrderNav(chrome.Nav.Repositories, order, LangEN)
		out := render(t, r, OverviewPage{Chrome: chrome, Activity: sampleGraph(), TotalCount: len(english), Repositories: english})

		lists := renderedLists(out)
		if len(lists) != 2 {
			t.Fatalf("%s: the page has %d ordered lists, want the sidebar and the dashboard", order, len(lists))
		}
		var want []string
		for _, item := range korean {
			want = append(want, item.URL)
		}
		result := runLanguageClick(t, out, "/", nil, lists)
		for index, got := range result.Lists {
			if !slices.Equal(got, want) {
				t.Errorf("%s: list %d after switching to Korean is %v, want %v", order, index, got, want)
			}
		}
	}
}
