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

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
	mitmpkg "goodkind.io/clyde/internal/mitm"
)

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
		Short: "Generate wire_constants_gen.go from a committed reference snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
	)
	cmd := &cobra.Command{
		Use:   "drift-check",
		Short: "Capture, snapshot, diff, and exit non-zero on drift (suitable for CI/cron)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := mitmpkg.LookupLaunchProfile(upstream)
			if err != nil {
				return err
			}
			if captureRoot == "" {
				captureRoot = defaultCaptureRoot()
			}
			if caCert == "" {
				caCert = defaultCACert()
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			result, err := mitmpkg.RunCaptureSession(ctx, mitmpkg.CaptureSessionOptions{
				Profile:     profile,
				CaptureRoot: captureRoot,
				CACertPath:  caCert,
				Log:         f.Logger,
			})
			if err != nil {
				return fmt.Errorf("capture: %w", err)
			}
			snap, err := mitmpkg.ExtractSnapshot(result.TranscriptPath, mitmpkg.SnapshotOptions{
				UpstreamName: upstream,
				UpstreamVersion: "live-" + result.StartedAt.Format("20060102T150405"),
			})
			if err != nil {
				return fmt.Errorf("extract: %w", err)
			}
			ref, err := mitmpkg.LoadSnapshotTOML(referencePath)
			if err != nil {
				return fmt.Errorf("load reference: %w", err)
			}
			report := mitmpkg.DiffSnapshots(ref, snap)
			fmt.Fprintln(out2(f), report.SummaryString())
			if report.HasDiverged() {
				return fmt.Errorf("wire shape drift detected for %s", upstream)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream name")
	cmd.Flags().StringVar(&referencePath, "reference", "", "path to the committed reference.toml")
	cmd.Flags().StringVar(&captureRoot, "capture-dir", "", "root directory for transcripts")
	cmd.Flags().StringVar(&caCert, "ca-cert", "", "path to mitmproxy CA cert")
	_ = cmd.MarkFlagRequired("upstream")
	_ = cmd.MarkFlagRequired("reference")
	return cmd
}

func out2(f *cli.Factory) io.Writer {
	return out(f)
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
		upstream   string
		version    string
		outputDir  string
		useV2      bool
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
					UpstreamName:    upstream,
					UpstreamVersion: version,
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
