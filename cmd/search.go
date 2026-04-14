package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/search"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/ui"
)

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <name> <query>",
		Short: "Search a session's conversation history",
		Long: `Search through a session's conversation using an LLM to find
where a specific topic was discussed. Returns the matching messages.

Configure the search backend in ~/.config/clotilde/config.toml:

  [search]
  backend = "local"   # or "claude"

  [search.local]
  url   = "http://localhost:1234"
  model = "qwen3-coder-next"`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: sessionNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := globalStore()
			if err != nil {
				return err
			}

			// Resolve session name (supports fuzzy match)
			name := args[0]
			sess, err := resolveSessionForResume(cmd, store, name)
			if err != nil {
				return err
			}
			if sess == nil {
				return fmt.Errorf("session '%s' not found", name)
			}

			// Get query: from args or interactive input
			var query string
			if len(args) >= 2 {
				query = args[1]
			} else if isatty.IsTerminal(os.Stdout.Fd()) {
				query, err = ui.RunInput(ui.NewInput(fmt.Sprintf("Search '%s' for:", sess.Name)))
				if err != nil || query == "" {
					return nil
				}
			} else {
				return fmt.Errorf("query required: clotilde search <name> <query>")
			}

			// Load messages
			messages, loadErr := loadSessionMessages(sess)
			if loadErr != nil {
				return fmt.Errorf("failed to load conversation: %w", loadErr)
			}
			if len(messages) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No conversation messages found.")
				return nil
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Searching %d messages for: %s\n", len(messages), query)

			// Search
			depth, _ := cmd.Flags().GetString("depth")
			if depth == "" {
				depth = "normal"
			}
			cfg, _ := config.LoadGlobalOrDefault()
			results, searchErr := search.SearchWithDepth(context.Background(), messages, query, cfg.Search, depth)
			if searchErr != nil {
				return fmt.Errorf("search failed: %w", searchErr)
			}

			if len(results) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No matching messages found.")
				return nil
			}

			// Collect and display results
			var allMatched []transcript.Message
			for _, r := range results {
				if r.Summary != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Found: %s\n", r.Summary)
				}
				allMatched = append(allMatched, r.Messages...)
			}

			text := transcript.RenderPlainText(allMatched)

			// TUI viewer if interactive, plain text if piped
			if isatty.IsTerminal(os.Stdout.Fd()) {
				viewer := ui.NewViewer(fmt.Sprintf("Search results: %q in %s", query, sess.Name), text)
				return ui.RunViewer(viewer)
			}

			_, _ = fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		},
	}
	cmd.Flags().String("depth", "normal", "Search depth: quick, normal, or deep")
	return cmd
}
