package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	// A GIT_DIR/GIT_INDEX_FILE leaked in from the invoking shell or a git hook
	// would route this suite's fixture `git init`/`git config`/`git commit`
	// into that repository, and the discovery under test would follow it too
	// (gastownhall/beads#7706). A test that needs a location sets it with
	// t.Setenv.
	if err := testutil.UnsetGitRepoLocationEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		return 1
	}
	return m.Run()
}

// decoySnapshot is the state of a decoy repository that a leaked git location
// would let a fixture rewrite: its history, its index and its config.
type decoySnapshot struct {
	head, count   string
	index, config []byte
}

// decoyGit runs git in dir with the location keys scrubbed, so it always acts
// on the decoy itself whatever the test process has in its environment.
func decoyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(append([]string{}, noAutoMaintenance...), args...)...)
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newDecoyRepo builds an outer repository holding one real commit, the shape
// of the checkout a git hook's children would inherit GIT_DIR from.
func newDecoyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	decoyGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoyGit(t, dir, "add", ".")
	decoyGit(t, dir, "-c", "user.name=Me", "-c", "user.email=me@x", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=", "commit", "-qm", "real work")
	return dir
}

// snapshotDecoy reads the decoy's state without refreshing its index.
func snapshotDecoy(t *testing.T, dir string) decoySnapshot {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	return decoySnapshot{
		head:   decoyGit(t, dir, "rev-parse", "HEAD"),
		count:  decoyGit(t, dir, "rev-list", "--count", "HEAD"),
		index:  index,
		config: config,
	}
}

// assertDecoyUnchanged fails the test when a fixture wrote into the decoy.
func assertDecoyUnchanged(t *testing.T, dir string, before decoySnapshot) {
	t.Helper()
	after := snapshotDecoy(t, dir)
	if after.head != before.head || after.count != before.count {
		t.Errorf("decoy history moved from %s (%s commits) to %s (%s commits):\n%s",
			before.head, before.count, after.head, after.count,
			decoyGit(t, dir, "log", "--format=%h %an %s", "-3"))
	}
	if !bytes.Equal(after.index, before.index) {
		t.Errorf("decoy index file was rewritten")
	}
	if !bytes.Equal(after.config, before.config) {
		t.Errorf("decoy .git/config was rewritten:\n%s", after.config)
	}
}

// TestSuiteIgnoresInheritedGitLocation starts this test binary the way a git
// hook's child would run it, with GIT_DIR and GIT_INDEX_FILE naming an outer
// repository, and checks that a fixture-building test still passes and leaves
// that repository untouched (gastownhall/beads#7706). The leak has to be in the
// process environment at start: Bazel's strict action env would drop one
// exported around `bazel test`, and a t.Setenv here is too late for TestMain.
func TestSuiteIgnoresInheritedGitLocation(t *testing.T) {
	const probe = "TestPinNoRepositoryUnderForTestingSurvivesResetCaches"
	cases := []struct {
		name string
		env  func(decoy string) []string
	}{
		{"hook_pair", func(decoy string) []string {
			return []string{
				"GIT_DIR=" + filepath.Join(decoy, ".git"),
				"GIT_INDEX_FILE=" + filepath.Join(decoy, ".git", "index"),
			}
		}},
		// What a hook in a linked worktree hands its children.
		{"linked_worktree_shape", func(decoy string) []string {
			return []string{
				"GIT_DIR=" + filepath.Join(decoy, ".git"),
				"GIT_INDEX_FILE=" + filepath.Join(decoy, ".git", "index"),
				"GIT_WORK_TREE=" + decoy,
				"GIT_COMMON_DIR=" + filepath.Join(decoy, ".git"),
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decoy := newDecoyRepo(t)
			before := snapshotDecoy(t, decoy)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+probe+"$", "-test.count=1", "-test.v")
			// rules_go turns --test_filter into TESTBRIDGE_TEST_ONLY, which
			// would override -test.run and re-run this test in the child.
			env := testutil.WithoutGitRepoLocationEnv(bazeltest.ShardFreeEnv(os.Environ()))
			var childEnv []string
			for _, kv := range env {
				if !strings.HasPrefix(kv, "TESTBRIDGE_TEST_ONLY=") {
					childEnv = append(childEnv, kv)
				}
			}
			child.Env = append(childEnv, tc.env(decoy)...)
			out, err := child.CombinedOutput()
			if err != nil {
				t.Errorf("%s under an inherited git location: %v\n%s", probe, err, out)
			} else if !strings.Contains(string(out), "--- PASS: "+probe) {
				t.Errorf("child did not run %s:\n%s", probe, out)
			}
			assertDecoyUnchanged(t, decoy, before)
		})
	}
}
