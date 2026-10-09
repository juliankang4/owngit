package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/statepath"
)

func workflowSecretsFixture(t *testing.T, count int) *Store {
	t.Helper()
	store := openTestStore(t)
	for _, id := range []string{"project", "other"} {
		noErr(t, store.AddRepository(t.Context(), Repository{ID: id, Name: id, CreatedAt: testImportNow()}))
	}
	if count > 0 {
		secrets := make([]workflowSecret, count)
		for index := range secrets {
			secrets[index] = workflowSecret{WorkflowSecretInfo: WorkflowSecretInfo{
				Name: fmt.Sprintf("SECRET_%03d", index), UpdatedAt: testImportNow(), Actor: Actor{Kind: ActorAdministrator},
			}, Value: fmt.Sprintf("synthetic-value-%03d", index)}
		}
		noErr(t, store.saveWorkflowSecrets(t.Context(), "project", secrets))
	}
	return store
}

func TestSetWorkflowSecret(t *testing.T) {
	for _, test := range []struct {
		name  string
		count int
	}{{"add", 0}, {"replace", 1}, {"capacity", MaxWorkflowSecrets}, {"concurrent", 0}} {
		t.Run(test.name, func(t *testing.T) {
			store := workflowSecretsFixture(t, test.count)
			ctx, now := t.Context(), testImportNow().Add(time.Minute)
			actor := Actor{Kind: ActorAdministrator}
			if test.name == "concurrent" {
				var workers sync.WaitGroup
				start, failures := make(chan struct{}), make(chan error, 8)
				for index := range 8 {
					workers.Go(func() {
						<-start
						_, err := store.SetWorkflowSecret(ctx, "project", fmt.Sprintf("NEW_%d", index), "synthetic", actor, now)
						failures <- err
					})
				}
				close(start)
				workers.Wait()
				close(failures)
				for err := range failures {
					noErr(t, err)
				}
				infos, err := store.ListWorkflowSecrets(ctx, "project")
				noErr(t, err)
				if len(infos) != 8 {
					t.Fatalf("concurrent writers kept %d names", len(infos))
				}
			} else {
				info, err := store.SetWorkflowSecret(ctx, "project", "secret_000", "replacement\n한글", actor, now)
				noErr(t, err)
				if !reflect.DeepEqual(info, WorkflowSecretInfo{Name: "SECRET_000", UpdatedAt: now, Actor: actor}) {
					t.Fatalf("metadata=%+v", info)
				}
				values, err := store.ReadWorkflowSecrets(ctx, "project", []string{"SeCrEt_000"})
				noErr(t, err)
				if values["SECRET_000"] != "replacement\n한글" {
					t.Fatal("stored value changed")
				}
				infos, err := store.ListWorkflowSecrets(ctx, "project")
				noErr(t, err)
				if len(infos) != max(1, test.count) {
					t.Fatalf("replacement changed the name count: %d", len(infos))
				}
				if test.name == "capacity" {
					if _, err := store.SetWorkflowSecret(ctx, "project", "NEW", "synthetic", actor, now); err == nil {
						t.Fatal("accepted the 101st name")
					}
				}
			}
			path, err := store.workflowSecretPath("project")
			noErr(t, err)
			assertWorkflowSecretsPrivate(t, path)
			entries, err := os.ReadDir(filepath.Dir(path))
			noErr(t, err)
			if len(entries) != 1 || entries[0].Name() != "project.json" {
				t.Fatal("publication left temporary files")
			}
		})
	}
}

func TestWorkflowSecretValidation(t *testing.T) {
	for _, test := range []struct {
		name, value string
		valid       bool
	}{
		{"_mixed9", "", true}, {"lower", "first\nsecond\r\n한글", true},
		{"MAX", strings.Repeat("x", MaxWorkflowSecretValueBytes), true},
		{"", "synthetic", false}, {"1START", "synthetic", false}, {"HAS-DASH", "synthetic", false},
		{" HAS_SPACE", "synthetic", false}, {"한글", "synthetic", false}, {"bad\x00name", "synthetic", false},
		{"github_token", "synthetic", false}, {"GITHUB", "synthetic", true},
		{"BAD_UTF8", string([]byte{0xff}), false}, {"NUL", "synthetic\x00value", false},
		{"TOO_BIG", strings.Repeat("x", MaxWorkflowSecretValueBytes+1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := workflowSecretsFixture(t, 0)
			_, err := store.SetWorkflowSecret(t.Context(), "project", test.name, test.value, Actor{}, testImportNow())
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if err != nil && test.value != "" && strings.Contains(err.Error(), test.value) {
				t.Fatal("validation error contains a value")
			}
		})
	}
	store := workflowSecretsFixture(t, 0)
	for _, test := range []struct {
		repository string
		actor      Actor
		now        time.Time
	}{{"../project", Actor{}, testImportNow()}, {"missing", Actor{}, testImportNow()},
		{"project", Actor{Kind: "invalid"}, testImportNow()}, {"project", Actor{}, time.Time{}}} {
		if _, err := store.SetWorkflowSecret(t.Context(), test.repository, "VALID", "synthetic", test.actor, test.now); err == nil {
			t.Fatal("accepted an invalid repository or update metadata")
		}
	}
}

func TestRemoveWorkflowSecret(t *testing.T) {
	for _, test := range []struct {
		name      string
		count     int
		want      int
		temporary bool
	}{{"secret_000", 2, 1, false}, {"secret_000", 1, 0, false}, {"ABSENT", 1, 1, false}, {"ABSENT", 0, 0, false}, {"GITHUB_NO", 1, 1, false},
		{"secret_000", 1, 0, true}, {"ABSENT", 0, 0, true}} {
		t.Run(fmt.Sprintf("%s/%d/temporary=%v", test.name, test.count, test.temporary), func(t *testing.T) {
			store := workflowSecretsFixture(t, test.count)
			temporary := filepath.Join(store.Dir(), statepath.WorkflowSecrets, ".project.tmp-0001020304050607")
			if test.temporary {
				noErr(t, os.MkdirAll(filepath.Dir(temporary), 0o700))
				noErr(t, os.WriteFile(temporary, []byte("synthetic interrupted write"), 0o600))
			}
			err := store.RemoveWorkflowSecret(t.Context(), "project", test.name)
			if (err != nil) != (test.name == "GITHUB_NO") {
				t.Fatalf("remove error=%v", err)
			}
			infos, err := store.ListWorkflowSecrets(t.Context(), "project")
			noErr(t, err)
			if len(infos) != test.want {
				t.Fatalf("remaining names=%d want=%d", len(infos), test.want)
			}
			if test.temporary {
				if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("removal left a secret temporary: %v", err)
				}
			}
			if test.want == 0 {
				path, err := store.workflowSecretPath("project")
				noErr(t, err)
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("empty secrets file remains: %v", err)
				}
			}
		})
	}
}

func TestListWorkflowSecrets(t *testing.T) {
	for _, test := range []struct {
		repository string
		count      int
	}{{"project", 0}, {"project", 2}, {"other", 2}, {"missing", 0}} {
		t.Run(fmt.Sprintf("%s/%d", test.repository, test.count), func(t *testing.T) {
			store := workflowSecretsFixture(t, test.count)
			infos, err := store.ListWorkflowSecrets(t.Context(), test.repository)
			if test.repository == "missing" {
				if !errors.Is(err, ErrRepositoryNotFound) {
					t.Fatalf("missing repository error=%v", err)
				}
				return
			}
			noErr(t, err)
			want := test.count
			if test.repository == "other" {
				want = 0
			}
			if infos == nil || len(infos) != want {
				t.Fatalf("listed names=%d want=%d", len(infos), want)
			}
			for index, info := range infos {
				if info.Name != fmt.Sprintf("SECRET_%03d", index) || !info.UpdatedAt.Equal(testImportNow()) {
					t.Fatalf("metadata=%+v", info)
				}
			}
			content, err := json.Marshal(infos)
			noErr(t, err)
			if strings.Contains(string(content), "synthetic-value") || strings.Contains(string(content), `"value"`) {
				t.Fatal("list returned values")
			}
		})
	}
}

func TestReadWorkflowSecrets(t *testing.T) {
	for _, test := range []struct {
		name, repository string
		names            []string
		want             map[string]string
	}{
		{"selected", "project", []string{"secret_000", "MISSING"}, map[string]string{"SECRET_000": "synthetic-value-000", "MISSING": ""}},
		{"no names", "project", nil, map[string]string{}},
		{"other repository", "other", []string{"SECRET_000"}, map[string]string{"SECRET_000": ""}},
		{"fresh", "project", []string{"SECRET_000"}, map[string]string{"SECRET_000": "fresh-synthetic"}},
		{"invalid JSON", "project", []string{"SECRET_000"}, nil},
		{"invalid record", "project", nil, nil},
		{"duplicate names", "project", nil, nil},
		{"invalid UTF-8", "project", nil, nil},
		{"too many records", "project", nil, nil},
		{"directory", "project", nil, nil},
		{"cancelled", "project", nil, nil},
		{"invalid name", "project", []string{"GITHUB_TOKEN"}, nil},
		{"missing repository", "missing", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := workflowSecretsFixture(t, 2)
			ctx := t.Context()
			switch test.name {
			case "fresh":
				_, err := store.ReadWorkflowSecrets(ctx, "project", test.names)
				noErr(t, err)
				_, err = store.SetWorkflowSecret(ctx, "project", "SECRET_000", "fresh-synthetic", Actor{}, testImportNow())
				noErr(t, err)
			case "invalid JSON", "invalid record", "invalid UTF-8", "duplicate names":
				path, err := store.workflowSecretPath("project")
				noErr(t, err)
				content := `[{"name":"SECRET_000","value":"synthetic-value-000","updated_at":"synthetic-value-000"}]`
				if test.name == "invalid record" {
					content = `[{"name":"github_token","value":"synthetic-value-000"}]`
				}
				if test.name == "invalid UTF-8" {
					content = string([]byte{0xff})
				} else if test.name == "duplicate names" {
					secrets, err := store.loadWorkflowSecrets(ctx, "project")
					noErr(t, err)
					secrets = append(secrets, secrets[0])
					encoded, err := json.Marshal(secrets)
					noErr(t, err)
					content = string(encoded)
				}
				noErr(t, os.WriteFile(path, []byte(content), 0o600))
			case "too many records":
				secrets := make([]workflowSecret, MaxWorkflowSecrets+1)
				noErr(t, store.saveWorkflowSecrets(ctx, "project", secrets))
			case "directory":
				path, err := store.workflowSecretPath("project")
				noErr(t, err)
				noErr(t, os.Rename(path, filepath.Join(t.TempDir(), "original.json")))
				noErr(t, os.Mkdir(path, 0o700))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			values, err := store.ReadWorkflowSecrets(ctx, test.repository, test.names)
			if (err != nil) != (test.want == nil) || !reflect.DeepEqual(values, test.want) {
				t.Fatalf("read error=%v requested keys=%d returned keys=%d", err, len(test.names), len(values))
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-value-000") {
				t.Fatal("read error contains a value")
			}
			if test.name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("read lost cancellation")
			}
			if test.want == nil && test.name != "invalid name" {
				_, listErr := store.ListWorkflowSecrets(ctx, test.repository)
				_, setErr := store.SetWorkflowSecret(ctx, test.repository, "VALID", "synthetic", Actor{}, testImportNow())
				removeErr := store.RemoveWorkflowSecret(ctx, test.repository, "VALID")
				for _, failure := range []error{listErr, setErr, removeErr} {
					if failure == nil || strings.Contains(failure.Error(), "synthetic-value-000") {
						t.Fatal("a failed read became success or exposed a value on another entry point")
					}
				}
			}
		})
	}
}

func TestWorkflowSecretsDeletion(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{{"delete files", RepositoryDeletionDeleteFiles}, {"keep files", RepositoryDeletionKeepFiles},
		{"retry", RepositoryDeletionDeleteFiles}, {"blocked cleanup", RepositoryDeletionDeleteFiles},
		{"newer repository", RepositoryDeletionDeleteFiles}, {"temporary separator", RepositoryDeletionDeleteFiles},
		{"blocked temporary", RepositoryDeletionDeleteFiles}, {"linked temporary", RepositoryDeletionDeleteFiles}} {
		t.Run(test.name, func(t *testing.T) {
			store := workflowSecretsFixture(t, 1)
			ctx := t.Context()
			id := "project"
			if test.name == "temporary separator" {
				id = "project.tmp-other.restore-other"
				noErr(t, store.AddRepository(ctx, Repository{ID: id, Name: id, CreatedAt: testImportNow()}))
				_, err := store.SetWorkflowSecret(ctx, id, "PROBE", "synthetic", Actor{}, testImportNow())
				noErr(t, err)
			}
			path, err := store.workflowSecretPath(id)
			noErr(t, err)
			var temporaries []string
			for _, operation := range []string{statepath.CredentialWrite, statepath.CredentialRestore} {
				temporary := filepath.Join(filepath.Dir(path), statepath.CredentialTemporary(id, operation, []byte{0, 1, 2, 3, 4, 5, 6, 7}))
				noErr(t, os.WriteFile(temporary, []byte("synthetic interrupted write"), 0o600))
				temporaries = append(temporaries, temporary)
			}
			var unrelated []string
			for _, name := range []string{".other.tmp-0001020304050607", "." + id + "-other.tmp-0001020304050607", "." + id + ".tmp-invalid", id + ".tmp-0001020304050607"} {
				file := filepath.Join(filepath.Dir(path), name)
				noErr(t, os.WriteFile(file, []byte("kept"), 0o600))
				unrelated = append(unrelated, file)
			}
			blocked := ""
			switch test.name {
			case "blocked cleanup":
				blocked = path
			case "blocked temporary":
				blocked = temporaries[0]
			case "linked temporary":
				blocked = temporaries[0]
				noErr(t, os.Link(blocked, filepath.Join(t.TempDir(), "outside")))
			}
			if blocked != "" && test.name != "linked temporary" {
				noErr(t, os.Rename(blocked, filepath.Join(t.TempDir(), "original")))
				noErr(t, os.Mkdir(blocked, 0o700))
				noErr(t, os.WriteFile(filepath.Join(blocked, "blocked"), []byte("fixture"), 0o600))
			}
			noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion(id, test.mode)))
			if blocked != "" {
				if err := store.FinishRepositoryDeletion(ctx, id); err == nil {
					t.Fatal("blocked secret cleanup dropped the deletion intent")
				}
				if _, exists, err := store.RepositoryDeletion(ctx, id); err != nil || !exists {
					t.Fatalf("cleanup failure lost its intent: exists=%v err=%v", exists, err)
				}
				noErr(t, os.Rename(blocked, filepath.Join(t.TempDir(), "blocked")))
				noErr(t, store.FinishRepositoryDeletion(ctx, id))
			}
			for _, file := range append([]string{path}, temporaries...) {
				if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deletion left a secret file: %v", err)
				}
			}
			if _, err := store.SetWorkflowSecret(ctx, id, "NEW", "synthetic", Actor{}, testImportNow()); !errors.Is(err, ErrRepositoryNotFound) {
				t.Fatalf("deleting repository accepted a secret: %v", err)
			}
			if test.name == "retry" || test.name == "newer repository" {
				noErr(t, store.saveWorkflowSecrets(ctx, id, []workflowSecret{}))
				noErr(t, os.WriteFile(temporaries[0], []byte("synthetic interrupted write"), 0o600))
			}
			if test.name == "newer repository" {
				noErr(t, store.Exec(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES(?,?,?,?)`, id, id, "", testImportNow().Unix()))
			}
			noErr(t, store.FinishRepositoryDeletion(ctx, id))
			for _, file := range []string{path, temporaries[0]} {
				_, err = os.Stat(file)
				if (err == nil) != (test.name == "newer repository") {
					t.Fatalf("finished deletion file state=%v", err)
				}
			}
			for _, file := range unrelated {
				content, err := os.ReadFile(file)
				if err != nil || string(content) != "kept" {
					t.Fatalf("unrelated file changed: %v", err)
				}
			}
			if statepath.WorkflowSecrets != filepath.Base(filepath.Dir(path)) {
				t.Fatal("secret file is outside the managed secret directory")
			}
		})
	}
}
