package server

import (
	"agent-core/internal/benchmark"
	"agent-core/internal/commands"
	"agent-core/internal/project"
	"agent-core/internal/skills"
	"agent-core/internal/subagents"
	"agent-core/internal/telemetry"
	"agent-core/internal/trust"
	"agent-core/internal/workflow"
	"agent-core/internal/workspace"
)

// Services contains the shared extension and IDE control-plane dependencies.
type Services struct {
	Workspace *workspace.Service
	Trust     *trust.Manager
	Skills    *skills.Loader
	Commands  *commands.Loader
	Subagents *subagents.Manager
	Workflow  *workflow.Engine
	Recorder  *telemetry.Recorder
	Benchmark *benchmark.Runner
	Project   project.Report
}

// SetServices wires optional capabilities without changing the core chat constructor.
func (s *Server) SetServices(services Services) { s.services = services }
