package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// kebabRe validates a generated display name: lowercase letters, digits, hyphens only.
var kebabRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,48}[a-z0-9]$`)

func newAutoNameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auto-name [<session>...]",
		Short: "Generate human-readable display names for sessions using LLM",
		Long: `auto-name uses a fast LLM (claude haiku) to generate a short kebab-case
display name for one or more sessions based on the conversation content.
The display name is stored in session metadata and shown in the TUI instead
of the raw session ID (e.g. "opnsense-bgp-cutover" instead of "configs-6d383f1d").

The original session name (used for resume, fork, etc.) is not changed.

Examples:
  clotilde auto-name configs-6d383f1d       # name one session
  clotilde auto-name --all                  # name all sessions without a display name
  clotilde auto-name --all --force          # regenerate all display names
  clotilde auto-name --all --dry-run        # preview without saving`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: sessionNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			force, _ := cmd.Flags().GetBool("force")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			model, _ := cmd.Flags().GetString("model")
			if model == "" {
				model = "haiku"
			}

			store, err := globalStore()
			if err != nil {
				return err
			}

			var targets []*session.Session

			if all {
				list, listErr := store.List()
				if listErr != nil {
					return fmt.Errorf("failed to list sessions: %w", listErr)
				}
				for _, s := range list {
					if s.Metadata.IsIncognito {
						continue
					}
					if !force && s.Metadata.DisplayName != "" {
						continue
					}
					targets = append(targets, s)
				}
			} else {
				if len(args) == 0 {
					return fmt.Errorf("provide session name(s) or use --all")
				}
				for _, name := range args {
					s, getErr := store.Get(name)
					if getErr != nil {
						return fmt.Errorf("session '%s' not found", name)
					}
					targets = append(targets, s)
				}
			}

			if len(targets) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions to rename.")
				return nil
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Generating display names for %d session(s)...\n", len(targets))

			succeeded := 0
			for _, sess := range targets {
				name, genErr := generateDisplayName(nil, sess, "", nil, model)
				if genErr != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  SKIP %s: %v\n", sess.Name, genErr)
					continue
				}

				if dryRun {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  DRY  %s  =>  %s\n", sess.Name, name)
					continue
				}

				sess.Metadata.DisplayName = name
				if updateErr := store.Update(sess); updateErr != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  FAIL %s: %v\n", sess.Name, updateErr)
					continue
				}

				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  OK   %s  =>  %s\n", sess.Name, name)
				succeeded++
			}

			if !dryRun {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nDone. %d/%d sessions named.\n", succeeded, len(targets))
			}
			return nil
		},
	}

	cmd.Flags().Bool("all", false, "Process all sessions (skips those already named unless --force)")
	cmd.Flags().Bool("force", false, "Regenerate display names even if already set")
	cmd.Flags().Bool("dry-run", false, "Print generated names without saving")
	cmd.Flags().String("model", "haiku", "Claude model for name generation")
	return cmd
}

// generateDisplayName loads the session transcript and calls claude to produce a
// short kebab-case display name. Returns an error if transcript is missing or LLM fails.
// homeDir and clotildeRootCache are unused but kept for future extension.
func generateDisplayName(
	_ interface{},
	sess *session.Session,
	_ string,
	_ map[string]string,
	model string,
) (string, error) {
	if sess.Metadata.TranscriptPath == "" {
		return "", fmt.Errorf("no transcript path")
	}
	if _, statErr := os.Stat(sess.Metadata.TranscriptPath); statErr != nil {
		return "", fmt.Errorf("transcript not found")
	}

	// Load up to 20 messages for naming context.
	f, err := os.Open(sess.Metadata.TranscriptPath)
	if err != nil {
		return "", fmt.Errorf("open transcript: %w", err)
	}
	messages, parseErr := transcript.Parse(f)
	_ = f.Close()
	if parseErr != nil {
		return "", fmt.Errorf("parse transcript: %w", parseErr)
	}
	if len(messages) == 0 {
		return "", fmt.Errorf("transcript has no messages")
	}

	// Take first 20 messages for context (intro sets the tone better than recent).
	sample := messages
	if len(sample) > 20 {
		sample = sample[:20]
	}

	var lines []string
	for _, msg := range sample {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		text := msg.Text
		if len(text) > 300 {
			text = text[:300] + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", role, text))
	}

	prompt := `Based on these messages from a coding session, generate a SHORT human-readable name in kebab-case (3 to 5 words, all lowercase, hyphens only, no numbers). The name must describe the MAIN topic or task. Output ONLY the kebab-case name. Nothing else. No punctuation. No explanation.

Good examples: opnsense-bgp-cutover, clotilde-search-pipeline, tack-node-model-refactor, mwan-firewall-rules-cleanup

Messages:
` + strings.Join(lines, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	claudeCmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, prompt)
	output, err := claudeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude -p failed: %w", err)
	}

	generated := strings.TrimSpace(string(output))
	generated = strings.ToLower(generated)
	// Strip any surrounding quotes or punctuation the model might add.
	generated = strings.Trim(generated, `"'` + "`.,;:!?")
	// Collapse spaces to hyphens in case model adds them.
	generated = strings.ReplaceAll(generated, " ", "-")

	if !kebabRe.MatchString(generated) {
		return "", fmt.Errorf("LLM returned invalid name %q", generated)
	}

	return generated, nil
}
