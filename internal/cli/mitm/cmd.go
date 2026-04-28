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
		upstream   string
		version    string
		outputDir  string
	)
	cmd := &cobra.Command{
		Use:   "snapshot <transcript>",
		Short: "Convert a JSONL transcript into a reference.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snap, err := mitmpkg.ExtractSnapshot(args[0], mitmpkg.SnapshotOptions{
				UpstreamName:    upstream,
				UpstreamVersion: version,
			})
			if err != nil {
				return err
			}
			if outputDir == "" {
				outputDir = filepath.Dir(args[0])
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
