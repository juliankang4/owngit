package pullrequest

import "testing"

func TestCreateRefCollision(t *testing.T) {
	for _, field := range []string{"source", "target"} {
		t.Run(field, func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.commitFile("file.txt", "base\n", "base")
			fixture.push("HEAD:refs/heads/main")
			fixture.push("HEAD:refs/heads/x")
			fixture.commitFile("file.txt", "feature\n", "feature")
			fixture.push("HEAD:refs/heads/refs/heads/x")
			input := CreateInput{Repository: fixture.repositoryID, Title: "Distinct branches", SourceBranch: "refs/heads/x", TargetBranch: "main"}
			if field == "target" {
				input.SourceBranch, input.TargetBranch = input.TargetBranch, input.SourceBranch
			}
			if _, err := fixture.service.Create(fixture.ctx, input); problemCode(err) != "ambiguous_branch" {
				t.Fatalf("ambiguous %s input error=%v, want ambiguous_branch", field, err)
			}
			input.SourceBranch, input.TargetBranch = "x", "refs/heads/refs/heads/x"
			wantSource, wantTarget := "x", "refs/heads/x"
			if field == "target" {
				input.SourceBranch, input.TargetBranch = input.TargetBranch, input.SourceBranch
				wantSource, wantTarget = wantTarget, wantSource
			}
			view, err := fixture.service.Create(fixture.ctx, input)
			noErr(t, err)
			if view.Number != 1 || view.Source.Branch != wantSource || view.Target.Branch != wantTarget {
				t.Fatalf("unambiguous pair was reinterpreted: %+v", view)
			}
		})
	}
}
