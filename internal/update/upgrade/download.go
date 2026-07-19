package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gentleman-programming/gentle-ai/internal/system"
	"github.com/gentleman-programming/gentle-ai/internal/update"
)

// httpClient is the HTTP client used for asset downloads.
// Package-level var for testability.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

// lookPathFn resolves the binary path. Package-level var for testability.
var lookPathFn = exec.LookPath

// resolveAssetURLFn and resolveChecksumURLFn build download URLs.
// Package-level vars for testability.
var resolveAssetURLFn = resolveAssetURL
var resolveChecksumURLFn = resolveChecksumURL

// Download downloads the GitHub release binary for the given tool, verifies its
// SHA256 checksum against the release's checksums.txt, and replaces the installed
// binary atomically.
//
// Checksum verification is mandatory: the install fails if checksums.txt is
// unavailable, if the archive is not listed, or if the digest does not match.
//
// This function is not called on Windows — callers (strategy.go) gate it via
// platform check and return a manual fallback error instead.
func Download(ctx context.Context, r update.UpdateResult, profile system.PlatformProfile) error {
	if profile.OS == "windows" {
		hint := r.UpdateHint
		if hint == "" {
			hint = fmt.Sprintf("Download from https://github.com/%s/%s/releases", r.Tool.Owner, r.Tool.Repo)
		}
		return fmt.Errorf("upgrade %q on Windows requires manual update — %s", r.Tool.Name, hint)
	}

	// Resolve the current binary path from PATH.
	binaryPath, err := lookPathFn(r.Tool.Name)
	if err != nil {
		return fmt.Errorf("locate %q binary: %w", r.Tool.Name, err)
	}

	archiveName := resolveArchiveName(r.Tool.Repo, r.LatestVersion, profile.OS, runtime.GOARCH)
	assetURL := resolveAssetURLFn(r.Tool.Owner, r.Tool.Repo, r.LatestVersion, profile.OS, runtime.GOARCH)
	checksumURL := resolveChecksumURLFn(r.Tool.Owner, r.Tool.Repo, r.LatestVersion)

	// Download archive to a temp directory so we can verify before extracting.
	tmpDir, err := os.MkdirTemp("", "gentle-ai-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, archiveName)
	actualDigest, err := downloadToFile(ctx, assetURL, archivePath)
	if err != nil {
		return fmt.Errorf("download %s: %w", r.Tool.Name, err)
	}

	// Verify checksum — fail closed if checksums.txt is unavailable or mismatched.
	checksumsContent, err := fetchChecksums(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("checksum verification failed: checksums.txt unavailable: %w", err)
	}
	expectedDigest, err := expectedChecksumFor(checksumsContent, archiveName)
	if err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}
	if actualDigest != expectedDigest {
		return fmt.Errorf("checksum mismatch for %s:\n  expected: %s\n  got:      %s",
			archiveName, expectedDigest, actualDigest)
	}

	// Extract the verified binary.
	tmpBinaryPath := binaryPath + ".new"
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	if err := extractBinaryFromTarGz(f, r.Tool.Name, tmpBinaryPath); err != nil {
		_ = os.Remove(tmpBinaryPath)
		return fmt.Errorf("extract %s: %w", r.Tool.Name, err)
	}

	// Atomic replace.
	if err := atomicReplace(tmpBinaryPath, binaryPath); err != nil {
		_ = os.Remove(tmpBinaryPath)
		return fmt.Errorf("replace %q: %w", binaryPath, err)
	}

	return nil
}

// resolveArchiveName returns the GoReleaser archive filename for the given
// repo/version/os/arch combination.
//
// Convention: {repo}_{version}_{os}_{arch}.tar.gz
func resolveArchiveName(repo, version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", repo, version, goos, goarch)
}

// resolveAssetURL constructs the GitHub Releases asset download URL.
func resolveAssetURL(owner, repo, version, goos, goarch string) string {
	filename := resolveArchiveName(repo, version, goos, goarch)
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/v%s/%s",
		owner, repo, version, filename)
}

// resolveChecksumURL constructs the GitHub Releases URL for checksums.txt.
func resolveChecksumURL(owner, repo, version string) string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/v%s/checksums.txt",
		owner, repo, version)
}

// downloadToFile downloads the resource at url to outPath and returns the
// SHA256 hex digest of the downloaded content.
func downloadToFile(ctx context.Context, url string, outPath string) (hexDigest string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", fmt.Errorf("write %s: %w", outPath, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchChecksums downloads checksums.txt from url and returns its content.
// Returns an error if the file cannot be fetched or the server returns non-200.
func fetchChecksums(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch checksums.txt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksums.txt: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read checksums.txt: %w", err)
	}
	return string(data), nil
}

// expectedChecksumFor parses checksums.txt content and returns the SHA256 hex
// digest for filename. Returns an error if the filename is not listed.
//
// GoReleaser produces BSD-style checksums.txt: "<digest>  <filename>" per line.
func expectedChecksumFor(content, filename string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%q not listed in checksums.txt", filename)
}

// downloadBinary fetches the asset at url, extracts the binary named binaryName
// from the .tar.gz, and writes it to outPath with executable permissions.
//
// Note: this function does not verify checksums. Use Download for a complete,
// checksum-verified upgrade flow.
func downloadBinary(ctx context.Context, url string, binaryName string, outPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	return extractBinaryFromTarGz(resp.Body, binaryName, outPath)
}

// extractBinaryFromTarGz reads a .tar.gz stream and extracts the first file
// whose base name matches binaryName, writing it to outPath.
func extractBinaryFromTarGz(r io.Reader, binaryName string, outPath string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Match by base name (handles subdirectory layouts like tool_1.0_os_arch/tool).
		// Only accept regular files — skip symlinks, hardlinks, and special files.
		if filepath.Base(hdr.Name) == binaryName &&
			(hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) {
			if err := writeExecutable(tr, outPath); err != nil {
				return err
			}
			return nil
		}
	}

	return fmt.Errorf("binary %q not found in archive", binaryName)
}

// writeExecutable writes the content from r to outPath with executable permissions.
func writeExecutable(r io.Reader, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	return nil
}

// atomicReplace moves src to dst atomically using os.Rename.
// This is safe on Unix (same-filesystem rename) but NOT safe on Windows
// when the binary is running. The caller must guard against Windows before calling.
func atomicReplace(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
	}
	return nil
}

// --- Release signature verification (slice A: helper only) ---

// MinisignPublicKey is the ed25519 public key (hex) used to verify signed
// release artifacts. It must be exactly 64 hex characters (32 bytes).
//
// The value below is a placeholder. The maintainer replaces it with the real
// gentle-ai release key before the first signed release ships.
var MinisignPublicKey = "0000000000000000000000000000000000000000000000000000000000000000"

// MinisignGraceCutoffVersion is the release version at and above which an
// artifact MUST be signed. Releases at or after this version fail the install
// if they are not signed or the signature does not verify.
//
// Releases strictly before this version are tolerated as unsigned (grace window
// for already-published artifacts).
var MinisignGraceCutoffVersion = "v1.23.0"

// Sentinel errors emitted by release signature verification.
var (
	// ErrSignatureVerificationFailed: the signature was present and parsed
	// successfully, but ed25519.Verify rejected it against MinisignPublicKey.
	ErrSignatureVerificationFailed = errors.New("signature verification failed")

	// ErrSignatureMissing: the release artifacts are at or after
	// MinisignGraceCutoffVersion, but no .minisig file is published alongside
	// them. Install must fail closed.
	ErrSignatureMissing = errors.New("signature file missing")

	// ErrSignatureFetchFailed: the .minisig file URL was unreachable or
	// returned non-200. Treated as a hard failure during the grace window
	// to preserve fail-closed semantics.
	ErrSignatureFetchFailed = errors.New("signature file could not be fetched")

	// ErrSignatureBlobMalformed: the .minisig content cannot be parsed
	// (wrong line count, bad prefix, bad base64, wrong decoded length, or
	// the hex key embedded in the constant is itself malformed).
	ErrSignatureBlobMalformed = errors.New("signature blob malformed")
)

// parseMinisign parses a minisign-format .minisig blob into an ed25519 public
// key. It also decodes the caller-supplied hexPubKey to validate the caller's
// input; the returned key is the one embedded in line 4 of the minisig file.
//
// A minisig file has exactly 4 lines:
//
//	line 1: "untrusted comment: <free text>"
//	line 2: base64(2-byte signum || 8-byte keynum || 64-byte ed25519 signature)
//	line 3: "trusted comment: <free text>"
//	line 4: base64(2-byte signum || 8-byte keynum || 32-byte ed25519 public key)
//
// The function does NOT call ed25519.Verify — that is the caller's job in
// the wired flow (slice B). This helper exists purely to surface malformed
// blobs cheaply before any signature math runs.
//
// Returns ErrSignatureBlobMalformed (wrapped with details) for any structural
// problem: wrong line count, unexpected prefix, invalid base64, hex key
// invalid, or unexpected decoded length.
func parseMinisign(hexPubKey string, minisigBlob []byte) (ed25519.PublicKey, error) {
	// Decode and validate the caller's hex-encoded public key. This
	// guarantees the caller's constant is at least well-formed before any
	// downstream signature verification runs.
	rawKey, err := hex.DecodeString(hexPubKey)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid hex public key: %v", ErrSignatureBlobMalformed, err)
	}
	if len(rawKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: hex public key length: got %d, want %d",
			ErrSignatureBlobMalformed, len(rawKey), ed25519.PublicKeySize)
	}

	// Split the .minisig content into exactly 4 lines. Allow a single
	// trailing CR/LF/CRLF, but no other stray whitespace.
	text := strings.TrimRight(string(minisigBlob), "\r\n")
	lines := strings.Split(text, "\n")
	if len(lines) != 4 {
		return nil, fmt.Errorf("%w: expected 4 lines, got %d",
			ErrSignatureBlobMalformed, len(lines))
	}

	if !strings.HasPrefix(lines[0], "untrusted comment: ") {
		return nil, fmt.Errorf("%w: line 1 must start with %q",
			ErrSignatureBlobMalformed, "untrusted comment: ")
	}
	if !strings.HasPrefix(lines[2], "trusted comment: ") {
		return nil, fmt.Errorf("%w: line 3 must start with %q",
			ErrSignatureBlobMalformed, "trusted comment: ")
	}

	// Line 2: signature blob = 2 signum + 8 keynum + 64 signature bytes = 74 bytes.
	const sigLineLen = 2 + 8 + ed25519.SignatureSize
	sigBin, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid signature base64: %v", ErrSignatureBlobMalformed, err)
	}
	if len(sigBin) != sigLineLen {
		return nil, fmt.Errorf("%w: signature line decoded length: got %d, want %d",
			ErrSignatureBlobMalformed, len(sigBin), sigLineLen)
	}

	// Line 4: key blob = 2 signum + 8 keynum + 32 pubkey bytes = 42 bytes.
	const keyLineLen = 2 + 8 + ed25519.PublicKeySize
	keyBin, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid pubkey base64: %v", ErrSignatureBlobMalformed, err)
	}
	if len(keyBin) != keyLineLen {
		return nil, fmt.Errorf("%w: pubkey line decoded length: got %d, want %d",
			ErrSignatureBlobMalformed, len(keyBin), keyLineLen)
	}

	// Skip the 10-byte header (signum + keynum) and return the 32-byte key.
	return ed25519.PublicKey(keyBin[10:]), nil
}
