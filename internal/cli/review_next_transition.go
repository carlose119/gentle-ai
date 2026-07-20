package cli

import (
	"fmt"
	"strings"

	"github.com/gentleman-programming/gentle-ai/internal/reviewtransaction"
)

const (
	reviewNextTransitionExecute = "execute"
	reviewNextTransitionCollect = "collect"
	reviewNextTransitionStop    = "stop"
)

// ReviewNextTransition is the sole negotiated routing decision. Its execute
// form is complete, its collect form identifies one externally supplied input,
// and its stop form intentionally contains no command-shaped data.
type ReviewNextTransition struct {
	Kind       string                      `json:"kind"`
	ReasonCode string                      `json:"reason_code"`
	Execute    *ReviewTransitionExecution  `json:"execute,omitempty"`
	Collect    *ReviewTransitionCollection `json:"collect,omitempty"`
}

type ReviewTransitionExecution struct {
	Operation     string                     `json:"operation"`
	Arguments     []ReviewTransitionArgument `json:"arguments"`
	Preconditions []ReviewTransitionArgument `json:"preconditions"`
	Binding       ReviewTransitionBinding    `json:"binding"`
	Artifacts     []ReviewTransitionArtifact `json:"artifacts,omitempty"`
}

type ReviewTransitionCollection struct {
	Inputs []ReviewTransitionInput `json:"inputs"`
}

type ReviewTransitionInput struct {
	Name             string                     `json:"name"`
	Schema           string                     `json:"schema"`
	CaptureOperation string                     `json:"capture_operation"`
	Arguments        []ReviewTransitionArgument `json:"arguments"`
}

type ReviewTransitionArgument struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ReviewTransitionBinding struct {
	LineageID      string `json:"lineage_id,omitempty"`
	Revision       string `json:"revision,omitempty"`
	TargetIdentity string `json:"target_identity"`
}

// ReviewTransitionArtifact deliberately excludes the provider-owned path. The
// native finalize command discovers the immutable captured bytes itself.
type ReviewTransitionArtifact struct {
	Schema         string `json:"schema"`
	Capability     string `json:"capability"`
	SHA256         string `json:"sha256"`
	LineageID      string `json:"lineage_id"`
	TargetIdentity string `json:"target_identity"`
	Lens           string `json:"lens"`
	SelectedOrder  int    `json:"selected_order"`
}

func newReviewNextTransition(status ReviewTargetStatusResult, selectedLenses []string, artifacts []ReviewTransitionArtifact, evidenceAvailable bool, artifactErr error, input reviewNextTransitionInput) ReviewNextTransition {
	if status.Applicability != reviewtransaction.TargetApplicabilityCurrent {
		switch status.Applicability {
		case reviewtransaction.TargetApplicabilityUnrelated:
			return reviewExecuteTransition("fresh_target_ready", "review.start", []ReviewTransitionArgument{}, []ReviewTransitionArgument{{Name: "target_identity", Value: status.TargetIdentity}}, ReviewTransitionBinding{TargetIdentity: status.TargetIdentity}, nil)
		case reviewtransaction.TargetApplicabilityAmbiguous:
			return reviewCollectTransition("lineage_selection_required", ReviewTransitionInput{
				Name: "lineage_selection", Schema: "gentle-ai.review-lineage-selection/v1", CaptureOperation: "external.select_lineage",
				Arguments: append(reviewTargetArguments(status), ReviewTransitionArgument{Name: "candidates", Value: strings.Join(status.Candidates, ",")}),
			})
		default:
			return reviewStopTransition("corrupted_or_unverifiable_authority")
		}
	}
	if status.Authority == nil {
		return reviewStopTransition("missing_authority_binding")
	}
	binding := reviewTransitionBinding(status.Authority, status.TargetIdentity)
	if status.Action == reviewtransaction.TargetStatusActionReconcileFinalize {
		return reviewStopTransition("original_finalize_request_required")
	}
	if status.Action == reviewtransaction.TargetStatusActionStop {
		if status.Authority.State == reviewtransaction.StateCorrectionRequired {
			return reviewStopTransition("unchanged_or_unverified_authority")
		}
		return reviewStopTransition("native_stop_required")
	}
	switch status.Authority.State {
	case reviewtransaction.StateReviewing:
		if artifactErr != nil {
			return reviewStopTransition("captured_artifacts_unverifiable")
		}
		if len(artifacts) != len(selectedLenses) {
			return reviewMissingCaptureTransition(binding, selectedLenses, artifacts)
		}
		return reviewExecuteTransition("captured_results_ready", "review.finalize", []ReviewTransitionArgument{
			{Name: "lineage", Value: binding.LineageID}, {Name: "captured_results", Value: "true"},
		}, []ReviewTransitionArgument{{Name: "state", Value: "reviewing"}, {Name: "captured_artifacts", Value: "complete"}}, binding, artifacts)
	case reviewtransaction.StateCorrectionRequired:
		if status.Action == reviewtransaction.TargetStatusActionRecover {
			return reviewRecoveryCollection(status, binding, input)
		}
		return reviewCollectTransition("correction_plan_required", ReviewTransitionInput{
			Name: "correction_lines", Schema: "gentle-ai.review-correction-plan/v1", CaptureOperation: "external.plan_correction",
			Arguments: reviewBindingArguments(binding),
		})
	case reviewtransaction.StateValidating:
		if evidenceAvailable {
			return reviewExecuteTransition("captured_verification_evidence_ready", "review.finalize", []ReviewTransitionArgument{{Name: "lineage", Value: binding.LineageID}, {Name: "captured_evidence", Value: "true"}}, []ReviewTransitionArgument{{Name: "state", Value: "validating"}, {Name: "verification_evidence", Value: "captured"}}, binding, nil)
		}
		if status.Frozen != nil && status.Frozen.Tier == reviewtransaction.RiskLow {
			return reviewExecuteTransition("native_low_risk_verification", "review.finalize", []ReviewTransitionArgument{{Name: "lineage", Value: binding.LineageID}}, []ReviewTransitionArgument{{Name: "state", Value: "validating"}, {Name: "risk_level", Value: "low"}}, binding, nil)
		}
		return reviewCollectTransition("verification_evidence_required", ReviewTransitionInput{
			Name: "evidence", Schema: "gentle-ai.review-verification-evidence/v1", CaptureOperation: "review.capture-evidence",
			Arguments: reviewBindingArguments(binding),
		})
	case reviewtransaction.StateInvalidated:
		return reviewRecoveryCollection(status, binding, input)
	case reviewtransaction.StateApproved:
		if status.Receipt.Status == ReviewReceiptPresent {
			return reviewExecuteTransition("approved_receipt_ready", "review.validate", []ReviewTransitionArgument{{Name: "lineage", Value: binding.LineageID}, {Name: "gate", Value: string(input.gate())}}, []ReviewTransitionArgument{{Name: "state", Value: "approved"}, {Name: "receipt", Value: "present"}}, binding, nil)
		}
		if status.Replayability == reviewtransaction.ReplayabilityExactReplaySafe {
			return reviewExecuteTransition("exact_receipt_replay", "review.finalize", []ReviewTransitionArgument{{Name: "lineage", Value: binding.LineageID}}, []ReviewTransitionArgument{{Name: "state", Value: "approved"}, {Name: "receipt", Value: "publication_pending"}}, binding, nil)
		}
		return reviewCollectTransition("delivery_gate_required", ReviewTransitionInput{
			Name: "gate", Schema: "gentle-ai.review-gate-selection/v1", CaptureOperation: "external.select_gate",
			Arguments: reviewBindingArguments(binding),
		})
	case reviewtransaction.StateEscalated:
		return reviewStopTransition("escalated_authority")
	default:
		if status.Action == reviewtransaction.TargetStatusActionReconcileFinalize {
			return reviewStopTransition("original_finalize_request_required")
		}
		return reviewStopTransition("manual_intervention_required")
	}
}

func reviewFinalizeNextTransition(state reviewtransaction.CompactState, revision string, artifacts []ReviewTransitionArtifact, artifactErr error) ReviewNextTransition {
	status := ReviewTargetStatusResult{
		Applicability:  reviewtransaction.TargetApplicabilityCurrent,
		Authority:      &ReviewTargetStatusAuthority{LineageID: state.LineageID, Revision: revision, State: state.State},
		TargetIdentity: state.InitialSnapshot.Identity,
		Frozen:         &ReviewTargetStatusFrozen{Tier: state.RiskLevel},
	}
	if state.State == reviewtransaction.StateReviewing && artifactErr == nil && len(artifacts) != len(state.SelectedLenses) {
		return reviewMissingCaptureTransition(reviewTransitionBinding(status.Authority, status.TargetIdentity), state.SelectedLenses, artifacts)
	}
	if state.State == reviewtransaction.StateReviewing && artifactErr == nil {
		return reviewExecuteTransition("captured_results_ready", "review.finalize", []ReviewTransitionArgument{{Name: "lineage", Value: state.LineageID}, {Name: "captured_results", Value: "true"}}, []ReviewTransitionArgument{{Name: "state", Value: "reviewing"}, {Name: "captured_artifacts", Value: "complete"}}, reviewTransitionBinding(status.Authority, status.TargetIdentity), artifacts)
	}
	return newReviewNextTransition(status, state.SelectedLenses, artifacts, false, artifactErr, reviewNextTransitionInput{})
}

func reviewMissingCaptureTransition(binding ReviewTransitionBinding, selectedLenses []string, artifacts []ReviewTransitionArtifact) ReviewNextTransition {
	captured := make(map[int]bool, len(artifacts))
	for _, artifact := range artifacts {
		captured[artifact.SelectedOrder] = true
	}
	inputs := make([]ReviewTransitionInput, 0)
	for order, lens := range selectedLenses {
		if !captured[order] {
			inputs = append(inputs, reviewCaptureInput(binding, lens, order))
		}
	}
	if len(inputs) == 0 {
		return reviewStopTransition("captured_result_selection_unavailable")
	}
	return reviewCollectTransition("reviewer_results_required", inputs...)
}

func reviewCaptureInput(binding ReviewTransitionBinding, lens string, order int) ReviewTransitionInput {
	return ReviewTransitionInput{
		Name: "reviewer_result", Schema: reviewReviewerSchemaID, CaptureOperation: "review.capture-result",
		Arguments: append(reviewBindingArguments(binding), ReviewTransitionArgument{Name: "lens", Value: lens}, ReviewTransitionArgument{Name: "order", Value: fmt.Sprint(order)}),
	}
}

type reviewNextTransitionInput struct {
	Gate                                    reviewtransaction.GateKind
	Successor, Reason, Actor, Authorization string
}

func (input reviewNextTransitionInput) gate() reviewtransaction.GateKind {
	if validReviewIntegrationGate(input.Gate) {
		return input.Gate
	}
	return reviewtransaction.GatePreCommit
}

func reviewRecoveryCollection(status ReviewTargetStatusResult, binding ReviewTransitionBinding, input reviewNextTransitionInput) ReviewNextTransition {
	disposition := status.ActionDisposition
	if disposition == "" {
		disposition = reviewtransaction.RecoveryInvalidated
	}
	if input.recoveryAuthorized(binding) {
		return reviewExecuteTransition("recovery_authorized", "review.recover", []ReviewTransitionArgument{{Name: "predecessor-lineage", Value: binding.LineageID}, {Name: "expected-predecessor-revision", Value: binding.Revision}, {Name: "successor-lineage", Value: input.Successor}, {Name: "disposition", Value: string(disposition)}, {Name: "reason", Value: input.Reason}, {Name: "actor", Value: input.Actor}, {Name: "maintainer-authorization", Value: input.Authorization}}, []ReviewTransitionArgument{{Name: "state", Value: string(status.Authority.State)}, {Name: "recovery_authorization", Value: "provided"}}, binding, nil)
	}
	return reviewCollectTransition("recovery_authorization_required", ReviewTransitionInput{
		Name: "recovery_authorization", Schema: "gentle-ai.review-recovery-authorization/v1", CaptureOperation: "external.authorize_recovery",
		Arguments: append(reviewBindingArguments(binding), ReviewTransitionArgument{Name: "disposition", Value: string(disposition)}),
	})
}

func (input reviewNextTransitionInput) recoveryAuthorized(binding ReviewTransitionBinding) bool {
	return input.Successor != "" && input.Reason != "" && input.Actor != "" && input.Authorization == "gentle-ai.review-recovery-authorization/v1\npredecessor_lineage="+binding.LineageID+"\npredecessor_revision="+binding.Revision+"\ntarget_identity="+binding.TargetIdentity+"\nactor="+input.Actor+"\nreason="+input.Reason
}

func reviewTargetArguments(status ReviewTargetStatusResult) []ReviewTransitionArgument {
	return []ReviewTransitionArgument{
		{Name: "target_identity", Value: status.TargetIdentity},
		{Name: "projection", Value: string(status.Projection.Projection)},
		{Name: "base_tree", Value: status.Projection.BaseTree},
		{Name: "candidate_tree", Value: status.Projection.CurrentCandidateTree},
	}
}

func reviewBindingArguments(binding ReviewTransitionBinding) []ReviewTransitionArgument {
	return []ReviewTransitionArgument{{Name: "lineage", Value: binding.LineageID}, {Name: "expected-revision", Value: binding.Revision}, {Name: "target", Value: binding.TargetIdentity}}
}

func reviewTransitionBinding(authority *ReviewTargetStatusAuthority, target string) ReviewTransitionBinding {
	return ReviewTransitionBinding{LineageID: authority.LineageID, Revision: authority.Revision, TargetIdentity: target}
}

func reviewExecuteTransition(reason, operation string, arguments, preconditions []ReviewTransitionArgument, binding ReviewTransitionBinding, artifacts []ReviewTransitionArtifact) ReviewNextTransition {
	return ReviewNextTransition{Kind: reviewNextTransitionExecute, ReasonCode: reason, Execute: &ReviewTransitionExecution{Operation: operation, Arguments: arguments, Preconditions: preconditions, Binding: binding, Artifacts: artifacts}}
}

func reviewCollectTransition(reason string, inputs ...ReviewTransitionInput) ReviewNextTransition {
	return ReviewNextTransition{Kind: reviewNextTransitionCollect, ReasonCode: reason, Collect: &ReviewTransitionCollection{Inputs: inputs}}
}

func reviewStopTransition(reason string) ReviewNextTransition {
	return ReviewNextTransition{Kind: reviewNextTransitionStop, ReasonCode: reason}
}
