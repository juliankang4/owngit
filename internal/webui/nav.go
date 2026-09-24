package webui

// Sidebar is what the shared sidebar shows for one page.
//
// Outside a repository it lists places and repositories, and Place names the
// place the page is. Inside a repository it lists that repository's sections,
// taken from the page's own RepoTabs so the sidebar and the page never
// disagree about which sections exist.
type Sidebar struct {
	// InRepo selects the repository sidebar.
	InRepo   bool
	RepoName string
	Tabs     RepoTabs
	// Place is "home", "activity", "settings", "new", or "import" outside a
	// repository, and "" when the page is none of them.
	Place string
}

// Current names the place or section for the narrow-window menu button.
func (s Sidebar) Current() MessageCode {
	if s.InRepo {
		switch s.Tabs.Active {
		case RepoTabOverview:
			return MsgRepoTabOver
		case RepoTabCode:
			return MsgRepoTabCode
		case RepoTabCommits:
			return MsgRepoTabCommits
		case RepoTabPullRequests:
			return MsgPRTabLabel
		case RepoTabChecks:
			return MsgTasksTabLabel
		case RepoTabImport:
			return MsgImportTab
		case RepoTabSettings:
			return MsgNavRepoSettings
		case RepoTabDelete:
			return MsgRepoDeleteTab
		}
		return ""
	}
	switch s.Place {
	case "home":
		return MsgNavHome
	case "activity":
		return MsgActivityTitle
	case "settings":
		return MsgSettingsTitle
	case "new":
		return MsgRepoNewTitle
	case "import":
		return MsgImportListLink
	}
	return ""
}

// sidebarOf reads the sidebar context from a page. A repository page whose
// caller supplied no section addresses falls back to the general sidebar
// instead of drawing an empty repository menu.
func sidebarOf(page Page) Sidebar {
	inRepo := func(repo RepositoryHeader, tabs RepoTabs) Sidebar {
		if tabs.OverviewURL == "" {
			return Sidebar{}
		}
		return Sidebar{InRepo: true, RepoName: repo.Name, Tabs: tabs}
	}
	switch p := page.(type) {
	case OverviewPage, *OverviewPage:
		return Sidebar{Place: "home"}
	case ActivityPage, *ActivityPage:
		return Sidebar{Place: "activity"}
	case SettingsPage, *SettingsPage:
		return Sidebar{Place: "settings"}
	case NewRepositoryPage, *NewRepositoryPage:
		return Sidebar{Place: "new"}
	case NewImportPage, *NewImportPage:
		return Sidebar{Place: "import"}
	case RepositoryPage:
		return inRepo(p.Repo, repoTabsOf(p))
	case *RepositoryPage:
		return inRepo(p.Repo, repoTabsOf(*p))
	case RestorePage:
		return inRepo(p.Repo, p.Tabs)
	case *RestorePage:
		return inRepo(p.Repo, p.Tabs)
	case ImportPage:
		return inRepo(p.Repo, p.Tabs)
	case *ImportPage:
		return inRepo(p.Repo, p.Tabs)
	case PullRequestsPage:
		return inRepo(p.Repo, p.Tabs)
	case *PullRequestsPage:
		return inRepo(p.Repo, p.Tabs)
	case NewPullRequestPage:
		return inRepo(p.Repo, p.Tabs)
	case *NewPullRequestPage:
		return inRepo(p.Repo, p.Tabs)
	case PullRequestPage:
		return inRepo(p.Repo, p.Tabs)
	case *PullRequestPage:
		return inRepo(p.Repo, p.Tabs)
	case TasksPage:
		return inRepo(p.Repo, p.Tabs)
	case *TasksPage:
		return inRepo(p.Repo, p.Tabs)
	case HelperCredentialsPage:
		return inRepo(p.Repo, p.Tabs)
	case *HelperCredentialsPage:
		return inRepo(p.Repo, p.Tabs)
	case ConfiguredChecksPage:
		return inRepo(p.Repo, p.Tabs)
	case *ConfiguredChecksPage:
		return inRepo(p.Repo, p.Tabs)
	case RunnerCredentialsPage:
		return inRepo(p.Repo, p.Tabs)
	case *RunnerCredentialsPage:
		return inRepo(p.Repo, p.Tabs)
	case RepositorySettingsPage:
		return inRepo(p.Repo, p.Tabs)
	case *RepositorySettingsPage:
		return inRepo(p.Repo, p.Tabs)
	case RepositoryDeletePage:
		return inRepo(p.Repo, p.Tabs)
	case *RepositoryDeletePage:
		return inRepo(p.Repo, p.Tabs)
	}
	return Sidebar{}
}

// wideOf reports whether a page shows code or diffs, which read better with
// the whole width of a wide window.
func wideOf(page Page) bool {
	switch p := page.(type) {
	case RepositoryPage:
		return p.Tab == RepoTabCode || p.Tab == RepoTabCommits
	case *RepositoryPage:
		return p.Tab == RepoTabCode || p.Tab == RepoTabCommits
	case PullRequestPage, *PullRequestPage:
		return true
	}
	return false
}
