package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-ai/internal/reviewtransaction"
)

// TestRunReviewAcquireResult drives the exactly-once pre-launch acquisition
// CLI surface directly: a first acquisition over a valid binding succeeds and
// is durable, a stale binding is refused before any write, and a repeated
// acquisition over the identical binding — modeling a duplicate or post-crash
// retry — always refuses.
func TestRunReviewAcquireResult(t *testing.T) {
	repo, started, store, record := newArtifactReview(t, false)
	target, lens := record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0]
	args := func(target, lens string, order int) []string {
		return []string{
			"--cwd", repo, "--lineage", started.LineageID, "--target", target,
			"--lens", lens, "--order", fmt.Sprint(order),
		}
	}
	for _, bad := range [][]string{
		args("sha256:"+strings.Repeat("0", 64), lens, 0),
		args(target, "review-risk", 0),
		args(target, lens, 1),
	} {
		if err := RunReviewAcquireResult(bad, io.Discard); err == nil {
			t.Fatal("acquire-result over a stale binding was accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(store.Dir, reviewtransaction.CompactReviewerAcquisitionsDir)); !os.IsNotExist(err) {
		t.Fatalf("stale acquire-result attempts wrote durable state: %v", err)
	}
	var output bytes.Buffer
	if err := RunReviewAcquireResult(args(target, lens, 0), &output); err != nil {
		t.Fatalf("acquire-result over a valid binding failed: %v", err)
	}
	var first ReviewAcquireResultResult
	decodeStrictReviewJSON(t, output.Bytes(), &first)
	if first.Operation != "review/acquire-result" || first.Acquisition.ID == "" ||
		first.Acquisition.LineageID != started.LineageID || first.Acquisition.TargetIdentity != target ||
		first.Acquisition.Lens != lens || first.Acquisition.SelectedOrder != 0 ||
		first.Acquisition.State != reviewtransaction.CompactResultAcquisitionUnknownOutcome {
		t.Fatalf("acquire-result result = %#v", first)
	}
	// A duplicate or post-crash retry of the identical binding must never
	// authorize a second launch.
	if err := RunReviewAcquireResult(args(target, lens, 0), io.Discard); err == nil {
		t.Fatal("repeated acquire-result over the identical binding was accepted")
	}
	if _, err := store.ReadResultAcquisition(started.LineageID, target, lens, 0, first.Acquisition.ID); err != nil {
		t.Fatalf("original acquisition unreadable after a rejected repeat: %v", err)
	}
}

func TestReviewCaptureResultStrictBindingReplayAndFinalize(t *testing.T) {
	repo, started, _, record := newArtifactReview(t, false)
	input := filepath.Join(t.TempDir(), "result.json")
	args := func(lineage, target, lens, order string) []string {
		return []string{"--cwd", repo, "--lineage", lineage, "--target", target, "--lens", lens, "--order", order, "--input", input}
	}
	acquisitionID := acquireResultForTest(t, repo, started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], 0)
	validArgs := append(args(started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], "0"), "--acquisition", acquisitionID)
	for _, payload := range []string{"prose", `{}`, `{"findings":[],"evidence":[]} {}`, `{"findings":[],"evidence":[],"unknown":true}`} {
		if err := os.WriteFile(input, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RunReviewCaptureResult(validArgs, io.Discard); err == nil {
			t.Fatalf("invalid payload accepted: %s", payload)
		}
	}
	if err := os.WriteFile(input, []byte(`{"findings":[],"evidence":["checked exact target"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{
		args("wrong-lineage", record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], "0"),
		args(started.LineageID, "sha256:"+strings.Repeat("0", 64), record.State.SelectedLenses[0], "0"),
		args(started.LineageID, record.State.InitialSnapshot.Identity, "review-risk", "0"),
		args(started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], "1"),
	} {
		if err := RunReviewCaptureResult(bad, io.Discard); err == nil {
			t.Fatal("wrong capture binding accepted")
		}
	}
	var first, replay bytes.Buffer
	if err := RunReviewCaptureResult(validArgs, &first); err != nil {
		t.Fatal(err)
	}
	if err := RunReviewCaptureResult(validArgs, &replay); err != nil || first.String() != replay.String() {
		t.Fatalf("exact replay changed: %v", err)
	}
	var artifact reviewResultArtifact
	decodeStrictReviewJSON(t, first.Bytes(), &artifact)
	if err := os.WriteFile(input, []byte(`{"findings":[],"evidence":["different"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunReviewCaptureResult(validArgs, io.Discard); err == nil {
		t.Fatal("mismatched replay accepted")
	}
	manifest := strings.TrimSpace(first.String())
	for _, finalize := range [][]string{
		{"--cwd", repo, "--lineage", started.LineageID, "--result-artifact", manifest, "--result", input},
		{"--cwd", repo, "--lineage", started.LineageID, "--result-artifact", manifest, "--result-artifact", manifest},
	} {
		if err := RunReviewFacadeFinalize(finalize, io.Discard); err == nil {
			t.Fatal("mixed or duplicate artifact accepted")
		}
	}
	if err := RunReviewFacadeFinalize([]string{"--cwd", repo, "--lineage", started.LineageID, "--result-artifact", manifest}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestReviewCaptureResultWaitsForMaintenanceBeforePublication(t *testing.T) {
	repo, started, store, record := newArtifactReview(t, false)
	input := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(input, []byte(`{"findings":[],"evidence":["checked exact target"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	// Acquire before the maintenance lock is held: AcquireReviewerResult takes
	// the same store lock CaptureReviewerResult waits on below.
	acquisitionID := acquireResultForTest(t, repo, started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	held, err := reviewtransaction.AcquireReviewMaintenanceExclusive(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--cwd", repo, "--lineage", started.LineageID, "--target", record.State.InitialSnapshot.Identity, "--lens", record.State.SelectedLenses[0], "--order", "0", "--input", input, "--acquisition", acquisitionID}
	if err := RunReviewCaptureResult(args, io.Discard); !errors.Is(err, reviewtransaction.ErrAuthorityLockTimeout) {
		t.Fatalf("capture while maintenance held = %v", err)
	}
	after, err := os.ReadFile(store.StatePath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("authority changed while capture blocked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, reviewtransaction.CompactReviewerResultsDir)); !os.IsNotExist(err) {
		t.Fatalf("capture published while maintenance held: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := RunReviewCaptureResult(args, io.Discard); err != nil {
		t.Fatalf("capture after maintenance release: %v", err)
	}
}
func TestReviewArtifactSubstitutionFailsBeforeMutation(t *testing.T) {
	mutations := []struct {
		name string
		run  func(*reviewResultArtifact)
	}{
		{"lineage", func(a *reviewResultArtifact) { a.LineageID = "wrong" }},
		{"target", func(a *reviewResultArtifact) { a.TargetIdentity = "sha256:" + strings.Repeat("0", 64) }},
		{"lens", func(a *reviewResultArtifact) { a.Lens = "review-risk" }},
		{"order", func(a *reviewResultArtifact) { a.SelectedOrder = 1 }},
		{"hash", func(a *reviewResultArtifact) { a.SHA256 = "sha256:" + strings.Repeat("0", 64) }},
		{"path", func(a *reviewResultArtifact) { a.Path = filepath.Join(t.TempDir(), "result.json") }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			repo, started, store, record, artifact := capturedArtifact(t)
			tt.run(&artifact)
			assertArtifactFinalizeUnchanged(t, repo, started.LineageID, store, record.Revision, artifact)
		})
	}
	for _, kind := range []string{"directory", "symlink", "mode", "bytes", "race"} {
		t.Run(kind, func(t *testing.T) {
			repo, started, store, record, artifact := capturedArtifact(t)
			original, _ := os.ReadFile(artifact.Path)
			replacement := filepath.Join(t.TempDir(), "replacement.json")
			if err := os.WriteFile(replacement, original, 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				_ = os.Remove(artifact.Path)
				_ = os.Mkdir(artifact.Path, 0o700)
			case "symlink":
				_ = os.Remove(artifact.Path)
				_ = os.Symlink(filepath.Join(t.TempDir(), "missing"), artifact.Path)
			case "mode":
				if runtime.GOOS == "windows" {
					t.Skip("Windows ACLs do not map to Unix mode bits")
				}
				_ = os.Chmod(artifact.Path, 0o644)
			case "bytes":
				_ = os.WriteFile(artifact.Path, []byte("replacement"), 0o600)
			case "race":
				reviewArtifactAfterLstat = func() {
					reviewArtifactAfterLstat = func() {}
					if err := os.Rename(artifact.Path, artifact.Path+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, artifact.Path); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { reviewArtifactAfterLstat = func() {} })
			}
			assertArtifactFinalizeUnchanged(t, repo, started.LineageID, store, record.Revision, artifact)
		})
	}
	if !reviewArtifactModeSafeForOS(0o666, false, "windows") || reviewArtifactModeSafeForOS(0o666, false, "linux") {
		t.Fatal("platform permission semantics changed")
	}
}
func TestReviewCaptureResultConcurrentSelectedLenses(t *testing.T) {
	repo, started, store, record := newArtifactReview(t, true)
	manifests := make([]string, len(record.State.SelectedLenses))
	acquisitions := make([]string, len(record.State.SelectedLenses))
	for order, lens := range record.State.SelectedLenses {
		acquisitions[order] = acquireResultForTest(t, repo, started.LineageID, record.State.InitialSnapshot.Identity, lens, order)
	}
	var wg sync.WaitGroup
	for order, lens := range record.State.SelectedLenses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			input := filepath.Join(t.TempDir(), fmt.Sprintf("%d.json", order))
			_ = os.WriteFile(input, []byte(`{"findings":[],"evidence":["checked exact target"]}`), 0o600)
			var output bytes.Buffer
			err := RunReviewCaptureResult([]string{"--cwd", repo, "--lineage", started.LineageID, "--target", record.State.InitialSnapshot.Identity, "--lens", lens, "--order", fmt.Sprint(order), "--input", input, "--acquisition", acquisitions[order]}, &output)
			if err != nil {
				t.Errorf("capture %s: %v", lens, err)
				return
			}
			manifests[order] = strings.TrimSpace(output.String())
		}()
	}
	wg.Wait()
	if _, err := readFacadeReviewerArtifacts(manifests, store.Dir, record.State); err != nil {
		t.Fatal(err)
	}
}
func TestCaptureReviewerArtifactDirectorySync(t *testing.T) {
	originalGOOS, originalSync := reviewArtifactRuntimeGOOS, syncReviewerArtifactDirectory
	t.Cleanup(func() { reviewArtifactRuntimeGOOS, syncReviewerArtifactDirectory = originalGOOS, originalSync })
	warning := []byte(`{"findings":[{"severity":"WARNING"}],"evidence":["unchanged"]}` + "\n")
	cases := []struct {
		name, goos string
		err        error
		wantOK     bool
	}{
		{"fatal", "linux", errors.New("disk sync failed"), false},
		{"invalid", "linux", syscall.EINVAL, true},
		{"unsupported", "linux", errors.ErrUnsupported, true},
		{"windows permission", "windows", os.ErrPermission, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := t.TempDir()
			state := reviewtransaction.CompactState{LineageID: "lineage", InitialSnapshot: reviewtransaction.Snapshot{Identity: "target"}, SelectedLenses: []string{"review-correctness"}}
			reviewArtifactRuntimeGOOS = func() string { return tt.goos }
			syncReviewerArtifactDirectory = func(string) error { return tt.err }
			artifact, err := captureReviewerArtifact(storeDir, state, 0, warning)
			path := filepath.Join(storeDir, "reviewer-results", "00-review-correctness.json")
			if !tt.wantOK {
				if _, statErr := os.Stat(path); err == nil || artifact != (reviewResultArtifact{}) || !os.IsNotExist(statErr) {
					t.Fatalf("fatal sync returned artifact or retained publication: artifact=%+v err=%v", artifact, err)
				}
				return
			}
			got, readErr := os.ReadFile(path)
			if err != nil || readErr != nil || !bytes.Equal(got, warning) {
				t.Fatalf("compatible sync changed WARNING result: capture=%v read=%v got=%q", err, readErr, got)
			}
		})
	}
}

// testAcquisitionID is a syntactically valid but unbound placeholder
// acquisition ID. review preserve-result durably saves evidence even from a
// repository that cannot resolve the live compact store the acquisition was
// published in (for example a nested-worktree misconfiguration), so it only
// format-validates --acquisition; this placeholder exercises that path
// without needing a real, resolvable acquisition record.
func testAcquisitionID() string {
	return "acq-" + strings.Repeat("0", 64)
}

// acquireResultForTest durably acquires the exact pre-launch slot for the
// given binding, mirroring what `review acquire-result` (called by the
// plugin's tool.execute.before hook) does before a bound reviewer task
// launches. Tests exercising a real capture-result or preserve-result success
// path must present this ID via --acquisition.
func acquireResultForTest(t *testing.T, repo, lineage, target, lens string, order int) string {
	t.Helper()
	store, err := reviewtransaction.CompactAuthoritativeStore(context.Background(), repo, lineage)
	if err != nil {
		t.Fatal(err)
	}
	acquisition, err := store.AcquireReviewerResult(context.Background(), target, lens, order)
	if err != nil {
		t.Fatal(err)
	}
	return acquisition.ID
}

func newArtifactReview(t *testing.T, high bool) (string, ReviewFacadeStartResult, reviewtransaction.CompactStore, reviewtransaction.CompactRecord) {
	t.Helper()
	repo := initReviewCLIRepo(t)
	name := "tracked.txt"
	if high {
		name = "service-token.ts"
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := startFacadeReview(t, repo)
	store, _ := reviewtransaction.CompactAuthoritativeStore(context.Background(), repo, started.LineageID)
	record, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return repo, started, store, record
}
func capturedArtifact(t *testing.T) (string, ReviewFacadeStartResult, reviewtransaction.CompactStore, reviewtransaction.CompactRecord, reviewResultArtifact) {
	t.Helper()
	repo, started, store, record := newArtifactReview(t, false)
	input := filepath.Join(t.TempDir(), "result.json")
	_ = os.WriteFile(input, []byte(`{"findings":[],"evidence":["checked exact target"]}`), 0o600)
	acquisitionID := acquireResultForTest(t, repo, started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], 0)
	var output bytes.Buffer
	if err := RunReviewCaptureResult([]string{"--cwd", repo, "--lineage", started.LineageID, "--target", record.State.InitialSnapshot.Identity, "--lens", record.State.SelectedLenses[0], "--order", "0", "--input", input, "--acquisition", acquisitionID}, &output); err != nil {
		t.Fatal(err)
	}
	var artifact reviewResultArtifact
	decodeStrictReviewJSON(t, output.Bytes(), &artifact)
	return repo, started, store, record, artifact
}
func assertArtifactFinalizeUnchanged(t *testing.T, repo, lineage string, store reviewtransaction.CompactStore, revision string, artifact reviewResultArtifact) {
	t.Helper()
	payload, _ := json.Marshal(artifact)
	if err := RunReviewFacadeFinalize([]string{"--cwd", repo, "--lineage", lineage, "--result-artifact", string(payload)}, io.Discard); err == nil {
		t.Fatal("substituted artifact accepted")
	}
	after, _ := store.Load()
	if after.Revision != revision {
		t.Fatal("artifact mismatch mutated authority")
	}
}

func TestValidateReviewerResultPayloadEmptyResult(t *testing.T) {
	for _, payload := range [][]byte{
		nil,
		{},
		[]byte("   "),
		[]byte("\n\t\r\n"),
	} {
		err := validateReviewerResultPayload(payload)
		if err == nil {
			t.Fatalf("expected error for empty payload %q, got nil", payload)
		}
		var payloadErr *ReviewerResultPayloadError
		if !errors.As(err, &payloadErr) {
			t.Fatalf("expected ReviewerResultPayloadError, got %T: %v", err, err)
		}
		if payloadErr.Code != "empty_result" {
			t.Fatalf("expected code empty_result, got %q", payloadErr.Code)
		}
	}
}

func TestValidateReviewerResultPayloadNestedEnvelope(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte("<task_result>\n{\"findings\":[]}\n</task_result>"),
		[]byte("some prefix <task_result> content </task_result> suffix"),
		[]byte("</task_result>"),
		[]byte("<task_result>"),
	} {
		err := validateReviewerResultPayload(payload)
		if err == nil {
			t.Fatalf("expected error for nested envelope payload %q, got nil", payload)
		}
		var payloadErr *ReviewerResultPayloadError
		if !errors.As(err, &payloadErr) {
			t.Fatalf("expected ReviewerResultPayloadError, got %T: %v", err, err)
		}
		if payloadErr.Code != "nested_envelope" {
			t.Fatalf("expected code nested_envelope, got %q", payloadErr.Code)
		}
	}
}

func TestValidateReviewerResultPayloadDistinguishesErrorCodes(t *testing.T) {
	// Empty and nested envelope must produce distinct, non-overlapping codes.
	emptyErr := validateReviewerResultPayload([]byte(""))
	nestedErr := validateReviewerResultPayload([]byte("<task_result>{}</task_result>"))
	validErr := validateReviewerResultPayload([]byte(`{"findings":[],"evidence":["ok"]}`))
	validWithTagsErr := validateReviewerResultPayload([]byte(`{"findings":[],"evidence":["<task_result>"]}`))

	if emptyErr == nil || nestedErr == nil {
		t.Fatal("both failure modes must return errors")
	}
	if validErr != nil || validWithTagsErr != nil {
		t.Fatalf("valid payloads must not error, got: %v, %v", validErr, validWithTagsErr)
	}
	var emptyPayloadErr, nestedPayloadErr *ReviewerResultPayloadError
	if !errors.As(emptyErr, &emptyPayloadErr) || !errors.As(nestedErr, &nestedPayloadErr) {
		t.Fatal("both errors must be ReviewerResultPayloadError")
	}
	if emptyPayloadErr.Code == nestedPayloadErr.Code {
		t.Fatalf("error codes must differ: both are %q", emptyPayloadErr.Code)
	}
}

func TestReviewCaptureResultRejectsEmptyPayload(t *testing.T) {
	repo, started, _, record := newArtifactReview(t, false)
	input := filepath.Join(t.TempDir(), "result.json")
	acquisitionID := acquireResultForTest(t, repo, started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], 0)
	validArgs := []string{"--cwd", repo, "--lineage", started.LineageID, "--target",
		record.State.InitialSnapshot.Identity, "--lens", record.State.SelectedLenses[0], "--order", "0", "--input", input,
		"--acquisition", acquisitionID}

	for _, payload := range []string{"", "   ", "\n\t"} {
		if err := os.WriteFile(input, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		err := RunReviewCaptureResult(validArgs, io.Discard)
		if err == nil {
			t.Fatalf("empty payload %q must be rejected", payload)
		}
		if !strings.Contains(err.Error(), "empty_result") && !strings.Contains(err.Error(), "empty") {
			t.Fatalf("expected empty_result in error, got: %v", err)
		}
	}
}

func TestReviewCaptureResultRejectsNestedEnvelope(t *testing.T) {
	repo, started, _, record := newArtifactReview(t, false)
	input := filepath.Join(t.TempDir(), "result.json")
	acquisitionID := acquireResultForTest(t, repo, started.LineageID, record.State.InitialSnapshot.Identity, record.State.SelectedLenses[0], 0)
	validArgs := []string{"--cwd", repo, "--lineage", started.LineageID, "--target",
		record.State.InitialSnapshot.Identity, "--lens", record.State.SelectedLenses[0], "--order", "0", "--input", input,
		"--acquisition", acquisitionID}

	payload := "<task_result>\n{\"findings\":[],\"evidence\":[\"checked\"]}\n</task_result>"
	if err := os.WriteFile(input, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RunReviewCaptureResult(validArgs, io.Discard)
	if err == nil {
		t.Fatal("nested envelope payload must be rejected")
	}
	if !strings.Contains(err.Error(), "nested_envelope") && !strings.Contains(err.Error(), "envelope") {
		t.Fatalf("expected nested_envelope in error, got: %v", err)
	}
}
