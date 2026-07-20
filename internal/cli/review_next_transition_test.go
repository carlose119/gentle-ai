package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-ai/internal/reviewtransaction"
)

func TestValidatingEvidenceCollectionUnblocksFinalizeAndPreCommit(t *testing.T) {
	repo, started, _, record, _ := capturedArtifact(t)
	finalize := []string{"--contract", ReviewIntegrationContractV1, "--next-transition", "--cwd", repo, "--lineage", started.LineageID, "--captured-results"}
	var first bytes.Buffer
	if err := RunReviewFacadeFinalize(finalize, &first); err != nil {
		t.Fatal(err)
	}
	var repeated bytes.Buffer
	if err := RunReviewFacadeFinalize(finalize, &repeated); err != nil {
		t.Fatal(err)
	}
	var repeatedResult ReviewIntegrationFinalizeResult
	decodeStrictReviewJSON(t, decodeReviewOperationEnvelope(t, repeated.Bytes()).Result, &repeatedResult)
	if repeatedResult.State != reviewtransaction.StateValidating || repeatedResult.NextTransition == nil || repeatedResult.NextTransition.Kind != reviewNextTransitionCollect || repeatedResult.NextTransition.ReasonCode != "verification_evidence_required" {
		t.Fatalf("repeated finalize made no-progress recommendation = %#v", repeatedResult)
	}

	statusArgs := []string{"status", "--contract", ReviewIntegrationContractV1, "--next-transition", "--cwd", repo, "--lineage", started.LineageID}
	var waiting bytes.Buffer
	if err := RunReview(statusArgs, &waiting); err != nil {
		t.Fatal(err)
	}
	var status ReviewTargetStatusResult
	decodeStrictReviewJSON(t, waiting.Bytes(), &status)
	if status.NextTransition == nil || status.NextTransition.Kind != reviewNextTransitionCollect || status.NextTransition.Collect == nil || len(status.NextTransition.Collect.Inputs) != 1 || status.NextTransition.Collect.Inputs[0].CaptureOperation != "review.capture-evidence" {
		t.Fatalf("validating status = %#v", status.NextTransition)
	}
	evidence := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(evidence, []byte("verification passed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunReview([]string{"capture-evidence", "--cwd", repo, "--lineage", started.LineageID, "--target", record.State.InitialSnapshot.Identity, "--expected-revision", status.Authority.Revision, "--input", evidence}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var ready bytes.Buffer
	if err := RunReview(statusArgs, &ready); err != nil {
		t.Fatal(err)
	}
	decodeStrictReviewJSON(t, ready.Bytes(), &status)
	if status.NextTransition == nil || status.NextTransition.Kind != reviewNextTransitionExecute || status.NextTransition.Execute == nil || status.NextTransition.Execute.Operation != "review.finalize" {
		t.Fatalf("evidence-ready status = %#v", status.NextTransition)
	}
	var terminal bytes.Buffer
	if err := RunReviewFacadeFinalize([]string{"--contract", ReviewIntegrationContractV1, "--next-transition", "--cwd", repo, "--lineage", started.LineageID, "--captured-evidence"}, &terminal); err != nil {
		t.Fatal(err)
	}
	runReviewCLIGit(t, repo, "add", "tracked.txt")
	if err := RunReview([]string{"validate", "--cwd", repo, "--lineage", started.LineageID, "--gate", string(reviewtransaction.GatePreCommit)}, &bytes.Buffer{}); err != nil {
		t.Fatalf("pre-commit after captured evidence: %v", err)
	}
}

func TestNegotiatedNextTransitionDiscoversCapturedArtifactsAndAdvances(t *testing.T) {
	repo, started, _, record, _ := capturedArtifact(t)
	args := []string{"status", "--contract", ReviewIntegrationContractV1, "--next-transition", "--cwd", repo, "--lineage", started.LineageID}
	var first, replay bytes.Buffer
	if err := RunReview(args, &first); err != nil {
		t.Fatal(err)
	}
	if err := RunReview(args, &replay); err != nil {
		t.Fatal(err)
	}
	if first.String() != replay.String() {
		t.Fatalf("next transition changed after restart:\n%s\n%s", first.String(), replay.String())
	}
	var status ReviewTargetStatusResult
	if err := json.Unmarshal(first.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if err := status.Validate(); err != nil {
		t.Fatal(err)
	}
	transition := status.NextTransition
	if transition == nil || transition.Kind != reviewNextTransitionExecute || transition.Execute == nil || transition.Execute.Operation != "review.finalize" ||
		len(transition.Execute.Artifacts) != len(record.State.SelectedLenses) || strings.Contains(first.String(), "reviewer-results") || strings.Contains(first.String(), repo) {
		t.Fatalf("captured result transition = %#v\n%s", transition, first.String())
	}
	var finalized bytes.Buffer
	if err := RunReviewFacadeFinalize([]string{"--contract", ReviewIntegrationContractV1, "--next-transition", "--cwd", repo, "--lineage", started.LineageID, "--captured-results"}, &finalized); err != nil {
		t.Fatal(err)
	}
	result := decodeReviewOperationEnvelope(t, finalized.Bytes())
	var public ReviewIntegrationFinalizeResult
	decodeStrictReviewJSON(t, result.Result, &public)
	if public.NextTransition == nil || public.NextTransition.Kind != reviewNextTransitionCollect || public.NextTransition.ReasonCode != "verification_evidence_required" {
		t.Fatalf("finalize transition = %#v\n%s", public.NextTransition, finalized.String())
	}
}

func TestReviewNextTransitionStateTable(t *testing.T) {
	status := func(applicability reviewtransaction.TargetApplicability, state reviewtransaction.State, action reviewtransaction.TargetStatusAction, replayability reviewtransaction.Replayability) ReviewTargetStatusResult {
		return ReviewTargetStatusResult{
			Applicability: applicability, Action: action, Replayability: replayability,
			TargetIdentity: "sha256:" + strings.Repeat("b", 64), Candidates: []string{"first", "second"},
			Authority:  &ReviewTargetStatusAuthority{LineageID: "review-next-transition", Revision: "sha256:" + strings.Repeat("a", 64), State: state},
			Frozen:     &ReviewTargetStatusFrozen{Tier: reviewtransaction.RiskMedium},
			Projection: ReviewTargetStatusProjection{Projection: reviewtransaction.ProjectionWorkspace, BaseTree: strings.Repeat("c", 40), CurrentCandidateTree: strings.Repeat("d", 40)},
		}
	}
	all := []ReviewTransitionArtifact{{Schema: reviewResultArtifactSchema, Capability: reviewResultArtifactCapability, SHA256: "sha256:" + strings.Repeat("c", 64), LineageID: "review-next-transition", TargetIdentity: "sha256:" + strings.Repeat("b", 64), Lens: reviewtransaction.LensReliability, SelectedOrder: 0}}
	for _, tt := range []struct {
		name          string
		status        ReviewTargetStatusResult
		lenses        []string
		artifacts     []ReviewTransitionArtifact
		wantKind      string
		wantOperation string
	}{
		{"unreviewed workspace", status(reviewtransaction.TargetApplicabilityUnrelated, "", reviewtransaction.TargetStatusActionStart, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionExecute, "review.start"},
		{"unreviewed staged", status(reviewtransaction.TargetApplicabilityUnrelated, "", reviewtransaction.TargetStatusActionStart, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionExecute, "review.start"},
		{"unreviewed base ref", status(reviewtransaction.TargetApplicabilityUnrelated, "", reviewtransaction.TargetStatusActionStart, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionExecute, "review.start"},
		{"unreviewed overlay", status(reviewtransaction.TargetApplicabilityUnrelated, "", reviewtransaction.TargetStatusActionStart, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionExecute, "review.start"},
		{"reviewing low partial", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateReviewing, reviewtransaction.TargetStatusActionFinalize, reviewtransaction.ReplayabilityNotReplayable), []string{reviewtransaction.LensReliability}, nil, reviewNextTransitionCollect, ""},
		{"reviewing medium all captured", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateReviewing, reviewtransaction.TargetStatusActionFinalize, reviewtransaction.ReplayabilityNotReplayable), []string{reviewtransaction.LensReliability}, all, reviewNextTransitionExecute, "review.finalize"},
		{"reviewing high partial", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateReviewing, reviewtransaction.TargetStatusActionFinalize, reviewtransaction.ReplayabilityNotReplayable), []string{reviewtransaction.LensReliability}, nil, reviewNextTransitionCollect, ""},
		{"correction required", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateCorrectionRequired, reviewtransaction.TargetStatusActionFinalize, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionCollect, ""},
		{"unchanged corrected authority", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateCorrectionRequired, reviewtransaction.TargetStatusActionStop, reviewtransaction.ReplayabilityManualActionRequired), nil, nil, reviewNextTransitionStop, ""},
		{"validating", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateValidating, reviewtransaction.TargetStatusActionFinalize, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionCollect, ""},
		{"pending finalize journal", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateReviewing, reviewtransaction.TargetStatusActionReconcileFinalize, reviewtransaction.ReplayabilityStatusRequired), nil, nil, reviewNextTransitionStop, ""},
		{"approved", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateApproved, reviewtransaction.TargetStatusActionValidate, reviewtransaction.ReplayabilityNotReplayable), nil, nil, reviewNextTransitionExecute, "review.validate"},
		{"invalidated", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateInvalidated, reviewtransaction.TargetStatusActionRecover, reviewtransaction.ReplayabilityManualActionRequired), nil, nil, reviewNextTransitionExecute, "review.recover"},
		{"escalated unchanged", status(reviewtransaction.TargetApplicabilityCurrent, reviewtransaction.StateEscalated, reviewtransaction.TargetStatusActionStop, reviewtransaction.ReplayabilityManualActionRequired), nil, nil, reviewNextTransitionStop, ""},
		{"ambiguous", status(reviewtransaction.TargetApplicabilityAmbiguous, "", reviewtransaction.TargetStatusActionSelectLineage, reviewtransaction.ReplayabilityStatusRequired), nil, nil, reviewNextTransitionCollect, ""},
		{"corrupt", status(reviewtransaction.TargetApplicabilityCorrupted, "", reviewtransaction.TargetStatusActionRepairAuthority, reviewtransaction.ReplayabilityManualActionRequired), nil, nil, reviewNextTransitionStop, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := reviewNextTransitionInput{}
			if tt.status.Authority.State == reviewtransaction.StateApproved {
				tt.status.Receipt.Status = ReviewReceiptPresent
			}
			if tt.status.Action == reviewtransaction.TargetStatusActionRecover {
				input = reviewNextTransitionInput{Successor: "review-next-successor", Reason: "authorized recovery", Actor: "maintainer"}
				input.Authorization = "gentle-ai.review-recovery-authorization/v1\npredecessor_lineage=" + tt.status.Authority.LineageID + "\npredecessor_revision=" + tt.status.Authority.Revision + "\ntarget_identity=" + tt.status.TargetIdentity + "\nactor=" + input.Actor + "\nreason=" + input.Reason
			}
			got := newReviewNextTransition(tt.status, tt.lenses, tt.artifacts, false, nil, input)
			if got.Kind != tt.wantKind || got.Execute != nil && got.Execute.Operation != tt.wantOperation {
				t.Fatalf("next transition = %#v", got)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
			if got.Kind == reviewNextTransitionStop && (got.Execute != nil || got.Collect != nil) {
				t.Fatalf("stop exposed a command or template: %#v", got)
			}
		})
	}
}

func TestReviewNextTransitionRefusesTargetDriftAndUnverifiableCaptures(t *testing.T) {
	status := ReviewTargetStatusResult{
		Applicability: reviewtransaction.TargetApplicabilityCurrent, Action: reviewtransaction.TargetStatusActionFinalize,
		Authority:      &ReviewTargetStatusAuthority{LineageID: "target-drift", Revision: "sha256:" + strings.Repeat("a", 64), State: reviewtransaction.StateReviewing},
		TargetIdentity: "sha256:" + strings.Repeat("b", 64), Frozen: &ReviewTargetStatusFrozen{Tier: reviewtransaction.RiskHigh},
	}
	got := newReviewNextTransition(status, []string{reviewtransaction.LensRisk}, nil, false, errors.New("tampered capture"), reviewNextTransitionInput{})
	if got.Kind != reviewNextTransitionStop || got.ReasonCode != "captured_artifacts_unverifiable" || got.Execute != nil || got.Collect != nil {
		t.Fatalf("target drift transition = %#v", got)
	}
}
