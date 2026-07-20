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
	"syscall"

	"github.com/gentleman-programming/gentle-ai/internal/reviewtransaction"
)

const (
	reviewResultArtifactSchema     = "gentle-ai.review-result-artifact/v1"
	reviewResultArtifactCapability = "review.native_result_artifact"
	reviewResultArtifactLimit      = 4 << 20
	reviewFinalEvidenceDir         = "final-evidence"
	reviewFinalEvidenceFile        = "verification.txt"
)

func RunReviewCaptureEvidence(args []string, stdout io.Writer) error {
	flags := newReviewFlagSet("review capture-evidence", stdout, "Capture final verification evidence bound to one validating compact authority.")
	cwd := flags.String("cwd", ".", "repository path")
	lineage := flags.String("lineage", "", "exact review lineage identifier")
	target := flags.String("target", "", "exact frozen target identity")
	revision := flags.String("expected-revision", "", "exact validating authority revision")
	input := flags.String("input", "", "final verification evidence file or - for stdin")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if reviewHelpRequested(args) {
		return nil
	}
	if flags.NArg() != 0 || strings.TrimSpace(*lineage) == "" || strings.TrimSpace(*target) == "" || strings.TrimSpace(*revision) == "" || strings.TrimSpace(*input) == "" {
		return reviewPreflightError(errors.New("review capture-evidence requires exact --cwd, --lineage, --target, --expected-revision, and --input"))
	}
	ctx := context.Background()
	root, err := (reviewtransaction.SnapshotBuilder{Repo: *cwd}).ResolveRepositoryRoot(ctx)
	if err != nil {
		return err
	}
	store, record, err := discoverCompactFacadeReview(ctx, root, *lineage, false)
	if err != nil {
		return reviewPreflightError(err)
	}
	state := record.State
	if state.State != reviewtransaction.StateValidating || state.InitialSnapshot.Identity != *target || record.Revision != *revision {
		return reviewPreflightError(errors.New("final evidence binding does not match the current validating authority"))
	}
	payload, err := readFacadeBytes(*input)
	if err != nil || len(payload) == 0 || len(payload) > reviewResultArtifactLimit {
		return reviewPreflightError(errors.New("final verification evidence is required"))
	}
	dir := filepath.Join(store.Dir, reviewFinalEvidenceDir)
	if err := ensureReviewerArtifactDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, reviewFinalEvidenceFile)
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(existing, payload) {
			return reviewPreflightError(errors.New("captured final evidence already exists with different bytes"))
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	} else {
		temp, createErr := os.CreateTemp(dir, ".capture-*")
		if createErr != nil {
			return createErr
		}
		owned, _ := temp.Stat()
		defer removeOwnedArtifact(temp.Name(), owned)
		if err := temp.Chmod(0o600); err != nil {
			return err
		}
		if _, err := temp.Write(payload); err != nil {
			return err
		}
		if err := temp.Sync(); err != nil {
			return err
		}
		if err := temp.Close(); err != nil {
			return err
		}
		if err := reviewtransaction.PublishFileNoReplace(temp.Name(), path); err != nil {
			if existing, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(existing, payload) {
				return err
			}
		}
		if err := syncReviewerArtifactDirectoryCompatible(dir); err != nil {
			return err
		}
	}
	return encodeReviewJSON(stdout, map[string]any{"schema": "gentle-ai.review-verification-evidence/v1", "capability": "review.native_final_evidence", "sha256": facadePayloadHash(payload), "lineage_id": state.LineageID, "target_identity": state.InitialSnapshot.Identity, "revision": record.Revision})
}

func readCapturedFinalEvidence(storeDir string, state reviewtransaction.CompactState, revision string) ([]byte, error) {
	path := filepath.Join(storeDir, reviewFinalEvidenceDir, reviewFinalEvidenceFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !reviewArtifactModeSafe(info.Mode(), false) {
		return nil, errors.New("captured final evidence is unavailable or unsafe")
	}
	payload, err := os.ReadFile(path)
	if err != nil || len(payload) == 0 || len(payload) > reviewResultArtifactLimit {
		return nil, errors.New("captured final evidence is invalid")
	}
	return payload, nil
}

type reviewResultArtifact struct {
	Schema         string `json:"schema"`
	Capability     string `json:"capability"`
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	LineageID      string `json:"lineage_id"`
	TargetIdentity string `json:"target_identity"`
	Lens           string `json:"lens"`
	SelectedOrder  int    `json:"selected_order"`
}

var reviewArtifactAfterLstat = func() {}
var reviewArtifactRuntimeGOOS = func() string { return runtime.GOOS }
var syncReviewerArtifactDirectory = func(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func RunReviewCaptureResult(args []string, stdout io.Writer) error {
	flags := newReviewFlagSet("review capture-result", stdout, "Capture one strict reviewer result in native authority and emit its bound manifest.")
	cwd := flags.String("cwd", ".", "repository path")
	lineage := flags.String("lineage", "", "exact review lineage identifier")
	target := flags.String("target", "", "exact frozen target identity")
	lens := flags.String("lens", "", "exact selected lens")
	order := flags.Int("order", -1, "zero-based selected lens order")
	revision := flags.String("expected-revision", "", "exact reviewing authority revision")
	input := flags.String("input", "", "raw reviewer result JSON file or - for stdin")
	preflight := flags.Bool("preflight", false, "verify the capture binding against the current reviewing authority without reading or persisting any result")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if reviewHelpRequested(args) {
		return nil
	}
	if flags.NArg() != 0 || strings.TrimSpace(*lineage) == "" || strings.TrimSpace(*target) == "" ||
		strings.TrimSpace(*lens) == "" || *order < 0 || (!*preflight && strings.TrimSpace(*input) == "") {
		return reviewPreflightError(errors.New("review capture-result requires exact --cwd, --lineage, --target, --lens, --order, and --input (or --preflight)"))
	}
	if *preflight && strings.TrimSpace(*input) != "" {
		return reviewPreflightError(errors.New("review capture-result --preflight verifies the binding only and does not accept --input"))
	}
	ctx := context.Background()
	root, err := (reviewtransaction.SnapshotBuilder{Repo: *cwd}).ResolveRepositoryRoot(ctx)
	if err != nil {
		return fmt.Errorf("resolve review repository root: %w", err)
	}
	store, record, err := discoverCompactFacadeReview(ctx, root, *lineage, false)
	if err != nil {
		return reviewPreflightError(fmt.Errorf("resolve reviewing authority for lineage %q under repository %q: %w; if the review was started in a different repository (for example a nested one), re-run with --cwd set to that repository", *lineage, root, err))
	}
	state := record.State
	if state.State != reviewtransaction.StateReviewing || state.LineageID != *lineage || state.InitialSnapshot.Identity != *target ||
		(strings.TrimSpace(*revision) != "" && record.Revision != *revision) || *order >= len(state.SelectedLenses) || state.SelectedLenses[*order] != *lens {
		return reviewPreflightError(fmt.Errorf("capture binding does not match the current reviewing authority under repository %q; verify the frozen lineage, target, lens, and order for that repository, or re-run with --cwd set to the repository where the review was started", root))
	}
	if *preflight {
		return encodeReviewJSON(stdout, reviewCapturePreflightResult{
			Schema: reviewCapturePreflightSchema, Capability: reviewCapturePreflightCapability, RepositoryRoot: root,
			LineageID: state.LineageID, TargetIdentity: state.InitialSnapshot.Identity, Lens: *lens, SelectedOrder: *order,
		})
	}
	payload, err := readFacadeBytes(*input)
	if err != nil {
		return reviewPreflightError(fmt.Errorf("read reviewer result: %w", err))
	}
	var result facadeReviewerResult
	if err := decodeFacadeJSONBytes(payload, &result); err != nil {
		return reviewPreflightError(fmt.Errorf("decode reviewer result: %w", err))
	}
	if result.Findings == nil || result.Evidence == nil {
		return reviewPreflightError(errors.New("reviewer result requires explicit findings and evidence arrays"))
	}
	if _, err := prepareCompactReviewerResults(reviewtransaction.CompactState{SelectedLenses: []string{*lens}}, []facadeReviewerResult{result}, facadeRefuterResult{}); err != nil {
		return reviewPreflightError(err)
	}
	canonical, err := json.Marshal(result)
	if err != nil {
		return err
	}
	canonical = append(canonical, '\n')
	var artifact reviewResultArtifact
	err = store.CaptureReviewerResult(*target, *lens, *order, func(current reviewtransaction.CompactState) error {
		var captureErr error
		artifact, captureErr = captureReviewerArtifact(store.Dir, current, *order, canonical)
		return captureErr
	})
	if err != nil {
		return reviewPreflightError(err)
	}
	return encodeReviewJSON(stdout, artifact)
}
func captureReviewerArtifact(storeDir string, state reviewtransaction.CompactState, order int, payload []byte) (reviewResultArtifact, error) {
	dir := filepath.Join(storeDir, reviewtransaction.CompactReviewerResultsDir)
	if err := ensureReviewerArtifactDir(dir); err != nil {
		return reviewResultArtifact{}, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%02d-%s.json", order, state.SelectedLenses[order]))
	artifact := reviewResultArtifact{
		Schema: reviewResultArtifactSchema, Capability: reviewResultArtifactCapability, Path: path,
		SHA256: facadePayloadHash(payload), LineageID: state.LineageID,
		TargetIdentity: state.InitialSnapshot.Identity, Lens: state.SelectedLenses[order], SelectedOrder: order,
	}
	if existing, err := readVerifiedReviewerArtifact(artifact, storeDir, state); err == nil {
		if !bytes.Equal(existing, payload) {
			return reviewResultArtifact{}, errors.New("captured reviewer result already exists with different canonical bytes")
		}
		return artifact, persistReviewerArtifactDigest(path, artifact.SHA256)
	} else if !os.IsNotExist(err) {
		return reviewResultArtifact{}, err
	}
	temp, err := os.CreateTemp(dir, ".capture-*")
	if err != nil {
		return reviewResultArtifact{}, fmt.Errorf("create reviewer result temporary file: %w", err)
	}
	owned, _ := temp.Stat()
	defer removeOwnedArtifact(temp.Name(), owned)
	if err := temp.Chmod(0o600); err != nil {
		return reviewResultArtifact{}, err
	}
	if _, err := temp.Write(payload); err != nil {
		return reviewResultArtifact{}, err
	}
	if err := temp.Sync(); err != nil {
		return reviewResultArtifact{}, err
	}
	if err := temp.Close(); err != nil {
		return reviewResultArtifact{}, err
	}
	if err := reviewtransaction.PublishFileNoReplace(temp.Name(), path); err != nil {
		if existing, readErr := readVerifiedReviewerArtifact(artifact, storeDir, state); readErr == nil && bytes.Equal(existing, payload) {
			return artifact, persistReviewerArtifactDigest(path, artifact.SHA256)
		}
		return reviewResultArtifact{}, fmt.Errorf("publish reviewer result atomically: %w", err)
	}
	if err := syncReviewerArtifactDirectoryCompatible(dir); err != nil {
		removeOwnedArtifact(path, owned)
		return reviewResultArtifact{}, fmt.Errorf("sync reviewer result directory: %w", err)
	}
	if _, err := readVerifiedReviewerArtifact(artifact, storeDir, state); err != nil {
		removeOwnedArtifact(path, owned)
		return reviewResultArtifact{}, fmt.Errorf("read back reviewer result: %w", err)
	}
	return artifact, persistReviewerArtifactDigest(path, artifact.SHA256)
}

func persistReviewerArtifactDigest(path, digest string) error {
	digestPath := path + ".sha256"
	if existing, err := os.ReadFile(digestPath); err == nil {
		if strings.TrimSpace(string(existing)) == digest {
			return nil
		}
		return errors.New("captured reviewer result digest already exists with different bytes")
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".capture-digest-*")
	if err != nil {
		return err
	}
	defer temp.Close()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temp.WriteString(digest + "\n"); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := reviewtransaction.PublishFileNoReplace(temp.Name(), digestPath); err != nil {
		if existing, readErr := os.ReadFile(digestPath); readErr == nil && strings.TrimSpace(string(existing)) == digest {
			return nil
		}
		return err
	}
	return syncReviewerArtifactDirectoryCompatible(dir)
}

func syncReviewerArtifactDirectoryCompatible(dir string) error {
	err := syncReviewerArtifactDirectory(dir)
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) || reviewArtifactRuntimeGOOS() == "windows" && errors.Is(err, os.ErrPermission) {
		return nil
	}
	return err
}
func ensureReviewerArtifactDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create reviewer result directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !reviewArtifactModeSafe(info.Mode(), true) {
		return errors.New("reviewer result directory is not a private native directory")
	}
	return nil
}
func readFacadeReviewerArtifacts(raw []string, storeDir string, state reviewtransaction.CompactState) ([]facadeReviewerResult, error) {
	if len(raw) != len(state.SelectedLenses) {
		return nil, fmt.Errorf("review finalize requires all %d original reviewer artifact(s)", len(state.SelectedLenses))
	}
	results := make([]facadeReviewerResult, len(raw))
	for index := range raw {
		var artifact reviewResultArtifact
		if err := decodeFacadeJSONBytes([]byte(raw[index]), &artifact); err != nil {
			return nil, fmt.Errorf("decode reviewer artifact %d: %w", index+1, err)
		}
		if artifact.SelectedOrder != index {
			return nil, fmt.Errorf("reviewer artifact %d is out of selected-lens order", index+1)
		}
		payload, err := readVerifiedReviewerArtifact(artifact, storeDir, state)
		if err != nil {
			return nil, fmt.Errorf("verify reviewer artifact %d: %w", index+1, err)
		}
		if err := decodeFacadeJSONBytes(payload, &results[index]); err != nil {
			return nil, fmt.Errorf("parse reviewer artifact %d: %w", index+1, err)
		}
		if results[index].Findings == nil || results[index].Evidence == nil {
			return nil, fmt.Errorf("reviewer artifact %d requires explicit findings and evidence arrays", index+1)
		}
	}
	return results, nil
}

// discoverCapturedReviewerArtifacts reads only the canonical native capture
// locations. It makes status restart-safe without exposing provider paths or
// asking a consumer to reconstruct result manifests.
func discoverCapturedReviewerArtifacts(storeDir string, state reviewtransaction.CompactState) ([]ReviewTransitionArtifact, error) {
	artifacts := make([]ReviewTransitionArtifact, 0, len(state.SelectedLenses))
	for order, lens := range state.SelectedLenses {
		path := filepath.Join(storeDir, reviewtransaction.CompactReviewerResultsDir, fmt.Sprintf("%02d-%s.json", order, lens))
		digest, err := os.ReadFile(path + ".sha256")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read captured reviewer result digest %d: %w", order, err)
		}
		artifact := reviewResultArtifact{
			Schema: reviewResultArtifactSchema, Capability: reviewResultArtifactCapability, Path: path, SHA256: strings.TrimSpace(string(digest)),
			LineageID: state.LineageID, TargetIdentity: state.InitialSnapshot.Identity, Lens: lens, SelectedOrder: order,
		}
		if _, err := readVerifiedReviewerArtifact(artifact, storeDir, state); err != nil {
			return nil, fmt.Errorf("verify captured reviewer result %d: %w", order, err)
		}
		artifacts = append(artifacts, ReviewTransitionArtifact{
			Schema: artifact.Schema, Capability: artifact.Capability, SHA256: artifact.SHA256, LineageID: artifact.LineageID,
			TargetIdentity: artifact.TargetIdentity, Lens: artifact.Lens, SelectedOrder: artifact.SelectedOrder,
		})
	}
	return artifacts, nil
}

func readCapturedReviewerResults(storeDir string, state reviewtransaction.CompactState) ([]facadeReviewerResult, error) {
	artifacts, err := discoverCapturedReviewerArtifacts(storeDir, state)
	if err != nil {
		return nil, err
	}
	if len(artifacts) != len(state.SelectedLenses) {
		return nil, fmt.Errorf("review finalize requires all %d captured reviewer result(s)", len(state.SelectedLenses))
	}
	results := make([]facadeReviewerResult, len(artifacts))
	for index, published := range artifacts {
		artifact := reviewResultArtifact{
			Schema: published.Schema, Capability: published.Capability,
			Path:   filepath.Join(storeDir, reviewtransaction.CompactReviewerResultsDir, fmt.Sprintf("%02d-%s.json", published.SelectedOrder, published.Lens)),
			SHA256: published.SHA256, LineageID: published.LineageID, TargetIdentity: published.TargetIdentity,
			Lens: published.Lens, SelectedOrder: published.SelectedOrder,
		}
		payload, err := readVerifiedReviewerArtifact(artifact, storeDir, state)
		if err != nil {
			return nil, err
		}
		if err := decodeFacadeJSONBytes(payload, &results[index]); err != nil {
			return nil, err
		}
	}
	return results, nil
}
func readVerifiedReviewerArtifact(artifact reviewResultArtifact, storeDir string, state reviewtransaction.CompactState) ([]byte, error) {
	if artifact.Schema != reviewResultArtifactSchema || artifact.Capability != reviewResultArtifactCapability ||
		artifact.LineageID != state.LineageID || artifact.TargetIdentity != state.InitialSnapshot.Identity ||
		artifact.SelectedOrder < 0 || artifact.SelectedOrder >= len(state.SelectedLenses) ||
		artifact.Lens != state.SelectedLenses[artifact.SelectedOrder] || !validReviewCapabilitySHA256(artifact.SHA256) {
		return nil, errors.New("artifact manifest does not match frozen lineage, target, lens, and order")
	}
	wantPath := filepath.Join(storeDir, reviewtransaction.CompactReviewerResultsDir, fmt.Sprintf("%02d-%s.json", artifact.SelectedOrder, artifact.Lens))
	if !filepath.IsAbs(artifact.Path) || filepath.Clean(artifact.Path) != artifact.Path || artifact.Path != wantPath {
		return nil, errors.New("artifact path is outside native transaction ownership")
	}
	pathInfo, err := os.Lstat(artifact.Path)
	if err != nil {
		return nil, err
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !reviewArtifactModeSafe(pathInfo.Mode(), false) {
		return nil, errors.New("artifact is not an owner-only regular file")
	}
	// Windows resolves file IDs lazily from the path; snapshot it before the path can be replaced.
	if !os.SameFile(pathInfo, pathInfo) {
		return nil, errors.New("artifact identity is unavailable")
	}
	reviewArtifactAfterLstat()
	file, err := os.Open(artifact.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, opened) {
		return nil, errors.New("artifact path changed before read")
	}
	payload, err := io.ReadAll(io.LimitReader(file, reviewResultArtifactLimit+1))
	if err != nil || len(payload) > reviewResultArtifactLimit {
		return nil, errors.New("artifact exceeds the native result size limit")
	}
	after, err := os.Lstat(artifact.Path)
	if err != nil || !os.SameFile(opened, after) {
		return nil, errors.New("artifact path changed during read")
	}
	if facadePayloadHash(payload) != artifact.SHA256 {
		return nil, errors.New("artifact SHA-256 mismatch")
	}
	return payload, nil
}
func decodeFacadeJSONBytes(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("input contains multiple JSON values")
	}
	return nil
}
func reviewArtifactModeSafe(mode os.FileMode, directory bool) bool {
	return reviewArtifactModeSafeForOS(mode, directory, runtime.GOOS)
}
func reviewArtifactModeSafeForOS(mode os.FileMode, directory bool, goos string) bool {
	return goos == "windows" || mode.Perm()&0o077 == 0 && (!directory || mode.Perm()&0o700 == 0o700)
}
func removeOwnedArtifact(path string, owned os.FileInfo) {
	if owned == nil {
		return
	}
	if current, err := os.Lstat(path); err == nil && os.SameFile(current, owned) {
		_ = os.Remove(path)
	}
}
