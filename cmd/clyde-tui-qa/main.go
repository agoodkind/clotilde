// Command clyde-tui-qa drives the real clyde TUI for agent iteration (tmux, PTY, iTerm).
package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/tuiqa"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var (
		driverName     string
		clydeBin       string
		cols           int
		rows           int
		sessionName    string
		itermSessionID string
		isolatedRoot   string
		seedSessions   bool
		disableDaemon  bool
	)
	root := &cobra.Command{
		Use:   "clyde-tui-qa",
		Short: "Drive the clyde TUI for agent QA (tmux, pty, iterm)",
	}
	root.PersistentFlags().StringVar(&driverName, "driver", "tmux", "tmux, pty, or iterm")
	root.PersistentFlags().StringVar(&clydeBin, "clyde", envOr("CLYDE_BINARY", "./dist/clyde"), "path to clyde binary")
	root.PersistentFlags().IntVar(&cols, "cols", 120, "terminal width")
	root.PersistentFlags().IntVar(&rows, "rows", 40, "terminal height")
	root.PersistentFlags().StringVar(&sessionName, "session", "", "tmux session name (default auto)")
	root.PersistentFlags().StringVar(&itermSessionID, "iterm-session-id", envOr("CLYDE_TUIQA_ITERM_ID", ""), "iTerm session id from session-start (multi-invocation)")
	root.PersistentFlags().StringVar(&isolatedRoot, "isolated", "", "hermetic root for XDG and HOME (recommended)")
	root.PersistentFlags().BoolVar(&seedSessions, "seed", false, "with --isolated, write one demo session for a stable table")
	root.PersistentFlags().BoolVar(&disableDaemon, "disable-daemon", true, "set CLYDE_DISABLE_DAEMON=1")

	envPrint := &cobra.Command{
		Use:   "env-print",
		Short: "Print export lines for an isolated tree (source from a shell)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if isolatedRoot == "" {
				return fmt.Errorf("--isolated is required")
			}
			if err := tuiqa.PrepareIsolatedRoot(isolatedRoot); err != nil {
				return err
			}
			for _, line := range tuiqa.IsolatedXDG(isolatedRoot) {
				fmt.Printf("export %s\n", line)
			}
			return nil
		},
	}
	envPrint.Flags().StringVar(&isolatedRoot, "isolated", "", "hermetic root directory")
	_ = envPrint.MarkFlagRequired("isolated")

	seed := &cobra.Command{
		Use:   "seed",
		Short: "Write demo session metadata under isolated XDG data home",
		RunE: func(cmd *cobra.Command, args []string) error {
			if isolatedRoot == "" {
				return fmt.Errorf("--isolated is required")
			}
			dataHome := filepath.Join(isolatedRoot, "data")
			if err := tuiqa.PrepareIsolatedRoot(isolatedRoot); err != nil {
				return err
			}
			return tuiqa.WriteSeedSessions(dataHome)
		},
	}
	seed.Flags().StringVar(&isolatedRoot, "isolated", "", "hermetic root directory")
	_ = seed.MarkFlagRequired("isolated")

	repl := &cobra.Command{
		Use:   "repl",
		Short: "Interactive line loop: capture, send, raw, sleep, quit",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRepl(driverName, clydeBin, cols, rows, sessionName, isolatedRoot, seedSessions, disableDaemon)
		},
	}

	sessStart := &cobra.Command{
		Use:   "session-start",
		Short: "Start clyde in tmux or iterm (prints tmux session name or iTerm session id to stdout)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if driverName == "pty" {
				return fmt.Errorf("session-start is for tmux or iterm; use repl for pty")
			}
			return cmdSessionStart(driverName, clydeBin, cols, rows, sessionName, isolatedRoot, seedSessions, disableDaemon)
		},
	}

	sessCapture := &cobra.Command{
		Use:   "session-capture",
		Short: "Capture tmux or iTerm pane text",
		RunE: func(cmd *cobra.Command, args []string) error {
			if driverName == "pty" {
				return fmt.Errorf("use repl for pty")
			}
			if driverName == "iterm" && itermSessionID == "" {
				return fmt.Errorf("--iterm-session-id is required for iTerm multi-invocation")
			}
			if driverName == "tmux" && sessionName == "" {
				return fmt.Errorf("--session is required for tmux multi-invocation")
			}
			return cmdSessionCapture(driverName, sessionName, itermSessionID)
		},
	}

	sessSend := &cobra.Command{
		Use:   "session-send TOKEN [TOKEN...]",
		Short: "Send tmux-style tokens to tmux or iTerm",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if driverName == "pty" {
				return fmt.Errorf("use repl for pty")
			}
			if driverName == "iterm" && itermSessionID == "" {
				return fmt.Errorf("--iterm-session-id is required for iTerm multi-invocation")
			}
			if driverName == "tmux" && sessionName == "" {
				return fmt.Errorf("--session is required for tmux multi-invocation")
			}
			return cmdSessionSend(driverName, sessionName, itermSessionID, args)
		},
	}

	sessStop := &cobra.Command{
		Use:   "session-stop",
		Short: "Kill tmux session or close iTerm window",
		RunE: func(cmd *cobra.Command, args []string) error {
			if driverName == "pty" {
				return fmt.Errorf("use repl for pty")
			}
			if driverName == "iterm" && itermSessionID == "" {
				return fmt.Errorf("--iterm-session-id is required for iTerm multi-invocation")
			}
			if driverName == "tmux" && sessionName == "" {
				return fmt.Errorf("--session is required for tmux multi-invocation")
			}
			return cmdSessionStop(driverName, sessionName, itermSessionID)
		},
	}

	root.AddCommand(envPrint, seed, repl, sessStart, sessCapture, sessSend, sessStop)
	return root
}

func envOr(key, fallback string) string {
	v := os.Getenv(key)
	if v != "" {
		return v
	}
	return fallback
}

func buildEnv(isolatedRoot string, seedSessions bool, disableDaemon bool) ([]string, error) {
	var env []string
	if isolatedRoot != "" {
		if err := tuiqa.PrepareIsolatedRoot(isolatedRoot); err != nil {
			return nil, err
		}
		env = append(env, tuiqa.IsolatedXDG(isolatedRoot)...)
		dataHome := filepath.Join(isolatedRoot, "data")
		if seedSessions {
			if err := tuiqa.WriteSeedSessions(dataHome); err != nil {
				return nil, err
			}
		}
	}
	if disableDaemon {
		env = append(env, "CLYDE_DISABLE_DAEMON=1")
	}
	return env, nil
}

func cmdSessionStart(driverName, clydeBin string, cols, rows int, sessionName, isolatedRoot string, seedSessions, disableDaemon bool) error {
	d, err := tuiqa.New(driverName)
	if err != nil {
		return err
	}
	tuiqa.ConfigureSessionName(d, sessionName)
	env, err := buildEnv(isolatedRoot, seedSessions, disableDaemon)
	if err != nil {
		return err
	}
	absBin, err := filepath.Abs(clydeBin)
	if err != nil {
		return err
	}
	if err := d.Start(absBin, env, cols, rows); err != nil {
		return err
	}
	if driverName == "iterm" {
		fmt.Println(tuiqa.ITermSessionID(d))
		return nil
	}
	if sn := tuiqa.TmuxSessionName(d); sn != "" {
		fmt.Println(sn)
		return nil
	}
	fmt.Println(sessionName)
	return nil
}

func cmdSessionCapture(driverName, sessionName, itermSID string) error {
	d, err := driverForSession(driverName, sessionName, itermSID)
	if err != nil {
		return err
	}
	s, err := d.Capture()
	if err != nil {
		return err
	}
	fmt.Print(s)
	return nil
}

func cmdSessionSend(driverName, sessionName, itermSID string, tokens []string) error {
	d, err := driverForSession(driverName, sessionName, itermSID)
	if err != nil {
		return err
	}
	return d.SendKey(tokens)
}

func cmdSessionStop(driverName, sessionName, itermSID string) error {
	d, err := driverForSession(driverName, sessionName, itermSID)
	if err != nil {
		return err
	}
	return d.Kill()
}

func driverForSession(driverName, sessionName, itermSID string) (tuiqa.Driver, error) {
	if driverName == "iterm" {
		d := tuiqa.AttachITermSession(itermSID)
		if d == nil {
			return nil, fmt.Errorf("iTerm driver requires macOS")
		}
		return d, nil
	}
	d, err := tuiqa.New(driverName)
	if err != nil {
		return nil, err
	}
	tuiqa.ConfigureSessionName(d, sessionName)
	return d, nil
}

func runRepl(
	driverName, clydeBin string,
	cols, rows int,
	sessionName, isolatedRoot string,
	seedSessions, disableDaemon bool,
) error {
	d, err := tuiqa.New(driverName)
	if err != nil {
		return err
	}
	tuiqa.ConfigureSessionName(d, sessionName)
	env, err := buildEnv(isolatedRoot, seedSessions, disableDaemon)
	if err != nil {
		return err
	}
	absBin, err := filepath.Abs(clydeBin)
	if err != nil {
		return err
	}
	if err := d.Start(absBin, env, cols, rows); err != nil {
		return err
	}
	defer func() { _ = d.Kill() }()

	fmt.Fprintf(os.Stderr, "clyde-tui-qa repl driver=%s (capture|send ...|raw HEX|sleep MS|quit)\n", d.Name())
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "quit" || line == "exit" {
			return nil
		}
		if line == "capture" {
			s, err := d.Capture()
			if err != nil {
				fmt.Fprintf(os.Stderr, "capture: %v\n", err)
				continue
			}
			fmt.Print(s)
			if !strings.HasSuffix(s, "\n") {
				fmt.Println()
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "sleep "); ok {
			msStr := strings.TrimSpace(rest)
			ms, err := strconv.Atoi(msStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "sleep: %v\n", err)
				continue
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
			continue
		}
		if rest, ok := strings.CutPrefix(line, "raw "); ok {
			hexStr := strings.TrimSpace(rest)
			b, err := hex.DecodeString(strings.ReplaceAll(hexStr, " ", ""))
			if err != nil {
				fmt.Fprintf(os.Stderr, "raw: %v\n", err)
				continue
			}
			if err := d.PasteRaw(b); err != nil {
				fmt.Fprintf(os.Stderr, "raw: %v\n", err)
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "send "); ok {
			toks := tuiqa.ParseSendTokens(rest)
			if err := d.SendKey(toks); err != nil {
				fmt.Fprintf(os.Stderr, "send: %v\n", err)
			}
			continue
		}
		fmt.Fprintf(os.Stderr, "unknown command (try capture, send, raw, sleep, quit)\n")
	}
	return sc.Err()
}
