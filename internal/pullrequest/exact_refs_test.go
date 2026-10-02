package pullrequest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"owngit/internal/repository"
)

func TestAmbiguousBranchAdviceRespectsLegacyInputLimit(t *testing.T) {
	for _, size := range []int{233, 234, 244} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.commitFile("file.txt", "base\n", "base")
			fixture.push("HEAD:refs/heads/main")
			suffix := strings.Repeat("a", size)
			shortRef := "refs/heads/" + suffix
			longRef := "refs/heads/" + shortRef
			fixture.push("HEAD:" + shortRef)
			fixture.commitFile("file.txt", "feature\n", "feature")
			fixture.push("HEAD:" + longRef)
			input := CreateInput{Repository: fixture.repositoryID, Title: "Size-aware advice", SourceBranch: shortRef, TargetBranch: "main"}
			_, err := fixture.service.Create(fixture.ctx, input)
			var ambiguous *repository.AmbiguousBranchError
			if problemCode(err) != "ambiguous_branch" || !errors.As(err, &ambiguous) || len(ambiguous.Values) != 2 || ambiguous.Values[0] != suffix {
				t.Fatalf("ambiguous input lost its choices: %v", err)
			}
			create := fixture.service.Create
			input.SourceBranch = longRef
			if size == 233 {
				if ambiguous.Values[1] != longRef || !strings.Contains(err.Error(), longRef+": "+longRef) {
					t.Fatalf("valid 255-byte API operand was omitted: %v", err)
				}
			} else {
				if ambiguous.Values[1] != "" || !strings.Contains(err.Error(), "browser") || !strings.Contains(err.Error(), longRef) || strings.Contains(err.Error(), longRef+": "+longRef) {
					t.Fatalf("advice offered an oversized API operand: %v", err)
				}
				if _, err := fixture.service.Create(fixture.ctx, input); problemCode(err) != "invalid_branch" {
					t.Fatalf("legacy input limit changed: %v", err)
				}
				create = fixture.service.CreateExact
				input.TargetBranch = "refs/heads/main"
			}
			view, err := create(fixture.ctx, input)
			noErr(t, err)
			if view.Source.Branch != shortRef {
				t.Fatalf("advised route chose %s instead of %s", view.Source.Branch, shortRef)
			}
		})
	}
}

func TestCreateExactPreservesBranchNameLengthLimit(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.commitFile("file.txt", "feature\n", "feature")
	branch := strings.Repeat("a", 128) + "/" + strings.Repeat("b", 126)
	fixture.push("HEAD:refs/heads/" + branch)
	input := CreateInput{Repository: fixture.repositoryID, Title: "Long branch", SourceBranch: branch, TargetBranch: "main"}
	legacy, err := fixture.service.Create(fixture.ctx, input)
	noErr(t, err)
	_, err = fixture.service.Close(fixture.ctx, fixture.repositoryID, legacy.Number)
	noErr(t, err)
	input.SourceBranch, input.TargetBranch = "refs/heads/"+branch, "refs/heads/main"
	exact, err := fixture.service.CreateExact(fixture.ctx, input)
	noErr(t, err)
	if exact.Source.Branch != branch || exact.Source.OID != legacy.Source.OID {
		t.Fatalf("exact input changed the branch: %+v", exact.Source)
	}
	if _, err := fixture.service.Create(fixture.ctx, input); problemCode(err) != "invalid_branch" {
		t.Fatalf("legacy input limit changed: %v", err)
	}
	input.SourceBranch += "a"
	if _, err := fixture.service.CreateExact(fixture.ctx, input); problemCode(err) != "invalid_branch" {
		t.Fatalf("exact branch-name limit changed: %v", err)
	}
}

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
