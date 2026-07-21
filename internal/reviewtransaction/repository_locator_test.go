package reviewtransaction

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestReviewRepositoryLocatorPublishesPrivateImmutableRecord(t *testing.T) {
	repo, locator := reviewRepositoryLocatorFixture(t, "locator-private")
	if err := PublishReviewRepositoryLocators(context.Background(), repo, []ReviewRepositoryLocator{locator}); err != nil {
		t.Fatal(err)
	}
	path := reviewRepositoryLocatorTestPath(t, locator, repo)
	if err := PublishReviewRepositoryLocators(context.Background(), repo, []ReviewRepositoryLocator{locator}); err != nil {
		t.Fatalf("retry publication: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("locator file = %v, %v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("locator mode = %o, want 600", info.Mode().Perm())
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil || dir.Mode().Perm() != 0o700 {
			t.Fatalf("bucket mode = %v, %v; want 700", dir, err)
		}
	}
}

func TestReviewRepositoryLocatorRejectsUnsafeOrMalformedExistingRecord(t *testing.T) {
	repo, locator := reviewRepositoryLocatorFixture(t, "locator-reject")
	path := reviewRepositoryLocatorTestPath(t, locator, repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":"gentle-ai.review-repository-locator/v1","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishReviewRepositoryLocators(context.Background(), repo, []ReviewRepositoryLocator{locator}); err == nil {
		t.Fatal("malformed existing locator was accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, path); err != nil {
		t.Fatal(err)
	}
	if err := PublishReviewRepositoryLocators(context.Background(), repo, []ReviewRepositoryLocator{locator}); err == nil {
		t.Fatal("symlinked locator was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	bucket := filepath.Dir(path)
	if err := os.Remove(bucket); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, bucket); err != nil {
		t.Fatal(err)
	}
	if err := PublishReviewRepositoryLocators(context.Background(), repo, []ReviewRepositoryLocator{locator}); err == nil {
		t.Fatal("symlinked locator bucket was accepted")
	}
}

func TestReviewRepositoryLocatorRejectsOversizedAndSymlinkedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locator.json")
	if err := os.WriteFile(path, make([]byte, reviewRepositoryLocatorMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readReviewRepositoryLocator(path); err == nil {
		t.Fatal("oversized locator was accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(t.TempDir(), "locator-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readReviewRepositoryLocator(link); err == nil {
		t.Fatal("symlinked locator read was accepted")
	}
}

func TestReviewRepositoryLocatorConcurrentPublicationConverges(t *testing.T) {
	repo, locator := reviewRepositoryLocatorFixture(t, "locator-concurrent")
	const writers = 8
	errs := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- PublishReviewRepositoryLocators(context.Background(), repo, []ReviewRepositoryLocator{locator})
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent publication: %v", err)
		}
	}
}

func TestReviewRepositoryLocatorUsesDistinctWorktreeIdentities(t *testing.T) {
	locator := ReviewRepositoryLocator{Lineage: "locator-worktree", Target: "sha256:" + strings.Repeat("a", 64), Lens: LensRisk}
	first, err := reviewRepositoryLocatorPath(locator, "/worktree/one", "/repository/common")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reviewRepositoryLocatorPath(locator, "/worktree/two", "/repository/common")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("worktrees sharing a common directory produced the same locator name")
	}
}

func reviewRepositoryLocatorFixture(t *testing.T, lineage string) (string, ReviewRepositoryLocator) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "tracked.txt", "base\nreviewed change\n")
	record, _ := pristineReviewingFixture(t, repo, lineage)
	return repo, ReviewRepositoryLocator{Lineage: record.State.LineageID, Target: record.State.InitialSnapshot.Identity,
		Lens: record.State.SelectedLenses[0], Order: 0}
}

func reviewRepositoryLocatorTestPath(t *testing.T, locator ReviewRepositoryLocator, repo string) string {
	t.Helper()
	_, common, err := reviewRepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	path, err := reviewRepositoryLocatorPath(locator, repo, common)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
