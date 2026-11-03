package main

import (
	"agent-core/internal/benchmark"
	"agent-core/internal/workflow"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func cliSkills(args []string) error {
	set := flag.NewFlagSet("skills", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	name := set.String("name", "", "skill name to load")
	if err := set.Parse(args); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	if *name != "" {
		return printJSON(client.do(http.MethodPost, "/v1/skills/load", map[string]string{"name": *name}))
	}
	return printJSON(client.do(http.MethodGet, "/v1/skills", nil))
}

func cliWorkflows(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workflows action: list, show, validate, resume, cancel")
	}
	set := flag.NewFlagSet("workflows "+args[0], flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	id := set.String("id", "", "workflow run ID")
	file := set.String("file", "", "workflow YAML/JSON file")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	switch args[0] {
	case "list":
		return printJSON(client.do(http.MethodGet, "/v1/workflows", nil))
	case "show":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return printJSON(client.do(http.MethodGet, "/v1/workflows/"+*id, nil))
	case "resume", "cancel":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return printJSON(client.do(http.MethodPost, "/v1/workflows/"+*id+"/"+args[0], map[string]any{}))
	case "validate":
		definition, err := readWorkflowDefinition(*file)
		if err != nil {
			return err
		}
		return printJSON(client.do(http.MethodPost, "/v1/workflows/validate", map[string]any{"definition": definition}))
	default:
		return fmt.Errorf("unknown workflows action %q", args[0])
	}
}

func cliSnapshots(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("snapshots action: list, create, restore")
	}
	set := flag.NewFlagSet("snapshots "+args[0], flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	id := set.String("id", "", "snapshot ID")
	allow := set.Bool("allow-changes", false, "allow restoring changed files")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	switch args[0] {
	case "list":
		return printJSON(client.do(http.MethodGet, "/v1/snapshots", nil))
	case "create":
		return printJSON(client.do(http.MethodPost, "/v1/snapshots", map[string]any{}))
	case "restore":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return printJSON(client.do(http.MethodPost, "/v1/snapshots/"+*id+"/restore", map[string]any{"allow_changes": *allow}))
	default:
		return fmt.Errorf("unknown snapshots action %q", args[0])
	}
}

func cliTrust(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("trust action: review, grant, revoke")
	}
	set := flag.NewFlagSet("trust "+args[0], flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	digest := set.String("digest", "", "bundle digest")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	switch args[0] {
	case "review":
		return printJSON(client.do(http.MethodPost, "/v1/trust/review", map[string]any{}))
	case "grant", "revoke":
		if *digest == "" {
			return fmt.Errorf("--digest is required")
		}
		return printJSON(client.do(http.MethodPost, "/v1/trust/"+args[0], map[string]string{"bundle_digest": *digest}))
	default:
		return fmt.Errorf("unknown trust action %q", args[0])
	}
}

func cliBenchmarks(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("benchmarks action: start, show, cancel")
	}
	set := flag.NewFlagSet("benchmarks "+args[0], flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	id := set.String("id", "", "benchmark plan ID")
	file := set.String("file", "", "benchmark manifest YAML/JSON")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	switch args[0] {
	case "start":
		if *file == "" {
			return fmt.Errorf("--file is required")
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var manifest benchmark.Manifest
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			return err
		}
		return printJSON(client.do(http.MethodPost, "/v1/benchmarks", manifest))
	case "show":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return printJSON(client.do(http.MethodGet, "/v1/benchmarks/"+*id, nil))
	case "cancel":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return printJSON(client.do(http.MethodPost, "/v1/benchmarks/"+*id+"/cancel", map[string]any{}))
	default:
		return fmt.Errorf("unknown benchmarks action %q", args[0])
	}
}

func readWorkflowDefinition(path string) (workflow.Definition, error) {
	if strings.TrimSpace(path) == "" {
		return workflow.Definition{}, fmt.Errorf("--file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return workflow.Definition{}, err
	}
	var definition workflow.Definition
	if err := yaml.Unmarshal(data, &definition); err != nil {
		return definition, err
	}
	return definition, nil
}
