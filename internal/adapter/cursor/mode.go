package cursor

type Mode string

const (
	ModeAgent Mode = "agent"
	ModePlan  Mode = "plan"
)

func DetectMode(req Request) Mode {
	if req.Mode != "" {
		return req.Mode
	}
	return ModeAgent
}
