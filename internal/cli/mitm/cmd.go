// Package mitm implements the `clyde mitm` cobra subcommand. It
// drives the per-upstream MITM capture harness (CLYDE-125) and
// related snapshot/diff workflows.
package mitm

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
	mitmpkg "goodkind.io/clyde/internal/mitm"
)

// isV2Snapshot returns true when the reference TOML uses the v2
// per-flavor schema. Detection sniffs for the [[flavors]] table.
func isV2Snapshot(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "[[flavors]]")
}

// NewCmd returns the cobra command for `clyde mitm`.
func NewCmd(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mitm",
		Short: "Capture and validate upstream wire traffic for parity tracking",
		Long: `clyde mitm runs the per-upstream MITM capture harness so the
adapter's outbound shape can be diffed against ground-truth references
(CLYDE-125).

Subcommands:
  capture   --upstream <name>  Spawn the upstream client through the
                               proxy and write a JSONL transcript.
  snapshot  <transcript>       Convert a JSONL transcript into a
                               typed reference.toml.
  diff      <ref> <candidate>  Diff two reference snapshots.

Supported upstreams: claude-code, claude-desktop, codex-cli, codex-desktop.`,
	}
	cmd.AddCommand(newCaptureCmd(f))
	cmd.AddCommand(newLaunchCmd(f))
	cmd.AddCommand(newSnapshotCmd(f))
	cmd.AddCommand(newDiffCmd(f))
	cmd.AddCommand(newCodegenCmd(f))
	cmd.AddCommand(newDriftCheckCmd(f))
	return cmd
}

func newCodegenCmd(f *cli.Factory) *cobra.Command {
	var (
		pkg       string
		outputDir string
	)
	cmd := &cobra.Command{
		Use:   "codegen <reference.toml>",
		Short: "Generate wire_constants_gen.go (v1) or wire_flavors_gen.go (v2) from a committed reference snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Auto-detect v2 by sniffing the file. v2 has a top-level
			// [[flavors]] table; v1 has top-level [body] / [constants].
			if isV2Snapshot(args[0]) {
				snap, err := mitmpkg.LoadSnapshotV2TOML(args[0])
				if err != nil {
					return fmt.Errorf("load v2 reference: %w", err)
				}
				out, err := mitmpkg.GenerateWireFlavors(snap, mitmpkg.CodegenOptions{
					PackageName: pkg,
					OutputDir:   outputDir,
					UpstreamRef: args[0],
				})
				if err != nil {
					return err
				}
				fmt.Fprintln(out2(f), "generated:", out)
				return nil
			}
			snap, err := mitmpkg.LoadSnapshotTOML(args[0])
			if err != nil {
				return fmt.Errorf("load reference: %w", err)
			}
			out, err := mitmpkg.GenerateWireConstants(snap, mitmpkg.CodegenOptions{
				PackageName: pkg,
				OutputDir:   outputDir,
				UpstreamRef: args[0],
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(out2(f), "generated:", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&pkg, "package", "", "Go package the generated file belongs to (e.g. codex, anthropic)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "directory for the generated file (default internal/adapter/<package>/)")
	_ = cmd.MarkFlagRequired("package")
	return cmd
}

// newDriftCheckCmd performs a single capture/snapshot/diff cycle for
// one upstream and exits non-zero on divergence. Suitable for CI or
// for a scheduled cron task to surface upstream wire drift.
func newDriftCheckCmd(f *cli.Factory) *cobra.Command {
	var (
		upstream      string
		referencePath string
		captureRoot   string
		caCert        string
		driftLogPath  string
		includeUA     []string
		excludeUA     []string
		requireKeys   []string
		forbidKeys    []string
	)
	cmd := &cobra.Command{
		Use:   "drift-check",
		Short: "Capture, snapshot, diff, append to drift log, exit non-zero on drift",
		Long: `Capture one upstream session, extract a snapshot, diff against
the committed reference, append the structured outcome to the drift
log, and exit non-zero on divergence. Suitable for cron / CI.

Auto-detects v1 vs v2 reference shape. Body-key and User-Agent
filters apply only to v2.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if captureRoot == "" {
				captureRoot = defaultCaptureRoot()
			}
			if caCert == "" {
				caCert = defaultCACert()
			}
			if driftLogPath == "" {
				driftLogPath = defaultDriftLogPath(upstream)
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			outcome, err := mitmpkg.RunDriftCheck(ctx, mitmpkg.DriftCheckOptions{
				Upstream:        upstream,
				Reference:       referencePath,
				CaptureRoot:     captureRoot,
				CACertPath:      caCert,
				DriftLogPath:    driftLogPath,
				IncludeUA:       includeUA,
				ExcludeUA:       excludeUA,
				RequireBodyKeys: requireKeys,
				ForbidBodyKeys:  forbidKeys,
				Log:             f.Logger,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(out2(f), outcome.Summary)
			if outcome.Diverged {
				return fmt.Errorf("wire shape drift detected for %s", upstream)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream name")
	cmd.Flags().StringVar(&referencePath, "reference", "", "path to the committed reference.toml")
	cmd.Flags().StringVar(&captureRoot, "capture-dir", "", "root directory for transcripts")
	cmd.Flags().StringVar(&caCert, "ca-cert", "", "path to mitmproxy CA cert")
	cmd.Flags().StringVar(&driftLogPath, "drift-log", "", "path for the structured drift JSONL log (default: ~/.local/state/clyde/mitm-drift/<upstream>.jsonl)")
	cmd.Flags().StringSliceVar(&includeUA, "include-ua", nil, "v2 only: include records whose User-Agent contains one of these substrings")
	cmd.Flags().StringSliceVar(&excludeUA, "exclude-ua", nil, "v2 only: drop records whose User-Agent contains one of these substrings")
	cmd.Flags().StringSliceVar(&requireKeys, "require-body-key", nil, "v2 only: require these top-level body keys")
	cmd.Flags().StringSliceVar(&forbidKeys, "forbid-body-key", nil, "v2 only: drop records that contain any of these keys")
	_ = cmd.MarkFlagRequired("upstream")
	_ = cmd.MarkFlagRequired("reference")
	return cmd
}

// defaultDriftLogPath returns ~/.local/state/clyde/mitm-drift/<upstream>.jsonl.
// One file per upstream keeps each log small enough to grep, tail, or hand
// to a smaller LLM context without truncation. Cross-upstream patterns are
// still recoverable by globbing or jq across files.
func defaultDriftLogPath(upstream string) string {
	name := strings.TrimSpace(upstream)
	if name == "" {
		name = "default"
	}
	if base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); base != "" {
		return filepath.Join(base, "clyde", "mitm-drift", name+".jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("mitm-drift", name+".jsonl")
	}
	return filepath.Join(home, ".local", "state", "clyde", "mitm-drift", name+".jsonl")
}

func out2(f *cli.Factory) io.Writer {
	return out(f)
}

// newLaunchCmd is the dock-pinnable variant of capture: it ensures
// the proxy is up, spawns the upstream client with LaunchProfile env
// + Chromium flags, then returns immediately. The child runs
// detached. Suitable for a wrapper .app whose dock click invokes
// `clyde mitm launch codex-desktop`.
func newLaunchCmd(f *cli.Factory) *cobra.Command {
	var (
		upstream string
		caCert   string
	)
	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Spawn an upstream client through the MITM proxy and detach (dock-pinnable)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := mitmpkg.LookupLaunchProfile(upstream)
			if err != nil {
				return err
			}
			if caCert == "" {
				caCert = defaultCACert()
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			if err := mitmpkg.LaunchUpstream(ctx, mitmpkg.LaunchUpstreamOptions{
				Profile:    profile,
				CACertPath: caCert,
				Log:        f.Logger,
			}); err != nil {
				return err
			}
			fmt.Fprintln(out(f), "launched:", upstream)
			return nil
		},
	}
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream name (claude-desktop|codex-desktop|vscode|claude-code|codex-cli)")
	cmd.Flags().StringVar(&caCert, "ca-cert", "", "path to mitmproxy CA cert (default ~/.mitmproxy/mitmproxy-ca-cert.pem)")
	_ = cmd.MarkFlagRequired("upstream")
	return cmd
}

func newCaptureCmd(f *cli.Factory) *cobra.Command {
	var (
		upstream   string
		captureDir string
		caCert     string
		proxyAddr  string
	)
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Spawn an upstream client through the proxy and capture its wire traffic",
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := mitmpkg.LookupLaunchProfile(upstream)
			if err != nil {
				return err
			}
			if captureDir == "" {
				captureDir = defaultCaptureRoot()
			}
			if caCert == "" {
				caCert = defaultCACert()
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			result, err := mitmpkg.RunCaptureSession(ctx, mitmpkg.CaptureSessionOptions{
				Profile:     profile,
				CaptureRoot: captureDir,
				CACertPath:  caCert,
				ProxyHost:   proxyAddr,
				Log:         f.Logger,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(out(f), "transcript:", result.TranscriptPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream name (claude-code|claude-desktop|codex-cli|codex-desktop)")
	cmd.Flags().StringVar(&captureDir, "capture-dir", "", "root directory for transcripts (default ~/.local/state/clyde/mitm)")
	cmd.Flags().StringVar(&caCert, "ca-cert", "", "path to mitmproxy CA cert (default ~/.mitmproxy/mitmproxy-ca-cert.pem)")
	cmd.Flags().StringVar(&proxyAddr, "proxy-addr", "127.0.0.1:0", "host:port the proxy listens on")
	_ = cmd.MarkFlagRequired("upstream")
	return cmd
}

func newSnapshotCmd(f *cli.Factory) *cobra.Command {
	var (
		upstream    string
		version     string
		outputDir   string
		useV2       bool
		includeUA   []string
		excludeUA   []string
		requireKeys []string
		forbidKeys  []string
	)
	cmd := &cobra.Command{
		Use:   "snapshot <transcript>",
		Short: "Convert a JSONL transcript into a reference.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputDir == "" {
				outputDir = filepath.Dir(args[0])
			}
			if useV2 {
				snap, err := mitmpkg.ExtractSnapshotV2(args[0], mitmpkg.SnapshotV2Options{
					UpstreamName:               upstream,
					UpstreamVersion:            version,
					IncludeUserAgentSubstrings: includeUA,
					ExcludeUserAgentSubstrings: excludeUA,
					RequireBodyKeys:            requireKeys,
					ForbidBodyKeys:             forbidKeys,
				})
				if err != nil {
					return err
				}
				path, err := mitmpkg.WriteSnapshotV2TOML(snap, outputDir)
				if err != nil {
					return err
				}
				fmt.Fprintln(out(f), "reference (v2):", path)
				fmt.Fprintln(out(f), "  flavors observed:", len(snap.Flavors))
				for _, fl := range snap.Flavors {
					fmt.Fprintln(out(f), "  -", fl.Slug, fmt.Sprintf("(%d records, %d headers, %d body fields)",
						fl.RecordCount, len(fl.Headers), len(fl.Body.Fields)))
				}
				return nil
			}
			snap, err := mitmpkg.ExtractSnapshot(args[0], mitmpkg.SnapshotOptions{
				UpstreamName:    upstream,
				UpstreamVersion: version,
			})
			if err != nil {
				return err
			}
			path, err := mitmpkg.WriteSnapshotTOML(snap, outputDir)
			if err != nil {
				return err
			}
			fmt.Fprintln(out(f), "reference:", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream name to record in the snapshot")
	cmd.Flags().StringVar(&version, "version", "", "upstream version string to record in the snapshot")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "directory to write reference.toml (default: transcript dir)")
	cmd.Flags().BoolVar(&useV2, "v2", false, "emit per-flavor SnapshotV2 with classified headers and nested body shapes")
	cmd.Flags().StringSliceVar(&includeUA, "include-ua", nil, "only include records whose User-Agent contains one of these substrings (v2 only)")
	cmd.Flags().StringSliceVar(&excludeUA, "exclude-ua", nil, "drop records whose User-Agent contains one of these substrings (v2 only)")
	cmd.Flags().StringSliceVar(&requireKeys, "require-body-key", nil, "only include records whose top-level body has all listed keys (v2 only)")
	cmd.Flags().StringSliceVar(&forbidKeys, "forbid-body-key", nil, "drop records whose top-level body contains any listed key (v2 only)")
	_ = cmd.MarkFlagRequired("upstream")
	return cmd
}

func newDiffCmd(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <reference.toml> <candidate.toml>",
		Short: "Diff a candidate snapshot against a committed reference",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := mitmpkg.LoadSnapshotTOML(args[0])
			if err != nil {
				return fmt.Errorf("load reference: %w", err)
			}
			cand, err := mitmpkg.LoadSnapshotTOML(args[1])
			if err != nil {
				return fmt.Errorf("load candidate: %w", err)
			}
			report := mitmpkg.DiffSnapshots(ref, cand)
			fmt.Fprintln(out(f), report.SummaryString())
			if report.HasDiverged() {
				return fmt.Errorf("snapshot parity drift detected")
			}
			return nil
		},
	}
	return cmd
}

func defaultCaptureRoot() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "clyde", "mitm")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".clyde/mitm"
	}
	return filepath.Join(home, ".local", "state", "clyde", "mitm")
}

func defaultCACert() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
}

func out(f *cli.Factory) io.Writer {
	if f == nil || f.IOStreams == nil || f.IOStreams.Out == nil {
		return os.Stdout
	}
	return f.IOStreams.Out
}
