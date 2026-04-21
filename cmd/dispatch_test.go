package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            []string
		wantMode        InvocationMode
		wantRewritten   []string
		rewrittenIsNil  bool
	}{
		{
			name:          "empty",
			args:          nil,
			wantMode:      ModeClyde,
			wantRewritten: nil,
		},
		{
			name:          "bare_resume_subcommand",
			args:          []string{"resume"},
			wantMode:      ModeResumeNoArgDashboard,
			rewrittenIsNil: true,
		},
		{
			name:          "resume_with_target",
			args:          []string{"resume", "my-session"},
			wantMode:      ModeClyde,
			wantRewritten: []string{"resume", "my-session"},
		},
		{
			name:          "bare_r_flag",
			args:          []string{"-r"},
			wantMode:      ModeResumeNoArgDashboard,
			rewrittenIsNil: true,
		},
		{
			name:          "bare_resume_long_flag",
			args:          []string{"--resume"},
			wantMode:      ModeResumeNoArgDashboard,
			rewrittenIsNil: true,
		},
		{
			name:          "resume_flag_with_uuid",
			args:          []string{"-r", "uuid-here"},
			wantMode:      ModeResumeFlag,
			wantRewritten: []string{"resume", "uuid-here"},
		},
		{
			name:          "resume_long_flag_with_name",
			args:          []string{"--resume", "foo"},
			wantMode:      ModeResumeFlag,
			wantRewritten: []string{"resume", "foo"},
		},
		{
			name:          "compact_subcommand",
			args:          []string{"compact", "x"},
			wantMode:      ModeClyde,
			wantRewritten: []string{"compact", "x"},
		},
		{
			name:          "passthrough_exec",
			args:          []string{"exec"},
			wantMode:      ModePassthrough,
			wantRewritten: []string{"exec"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mode, rewritten := ClassifyArgs(tt.args)
			require.Equal(t, tt.wantMode, mode, "mode")
			if tt.rewrittenIsNil {
				require.Nil(t, rewritten, "rewritten")
			} else {
				require.Equal(t, tt.wantRewritten, rewritten, "rewritten")
			}
		})
	}
}
