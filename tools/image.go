package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// Describer turns an image into text. vision.Describer implements it.
type Describer interface {
	Describe(ctx context.Context, path, question string) (string, error)
	Model() string
}

// ImageConfig configures the describe_image tool.
type ImageConfig struct {
	Root      string
	Describer Describer
}

type imageArgs struct {
	Path     string `json:"path" jsonschema:"description=Image file path relative to the workspace root."`
	Question string `json:"question,omitempty" jsonschema:"description=What you need to know about the image. Be specific: a targeted question yields a useful answer where a generic one yields a generic caption."`
}

type imageTool struct {
	cfg ImageConfig
}

// NewDescribeImage returns a tool that reads an image through a vision model.
func NewDescribeImage(cfg ImageConfig) agent.Tool {
	return &imageTool{cfg: cfg}
}

func (t *imageTool) Name() string { return "describe_image" }

func (t *imageTool) Description() string {
	return strings.Join([]string{
		"Look at an image in the workspace and get back a description in text.",
		"",
		"Use this for screenshots, design mockups, diagrams and photographs of screens — anything the read tool refuses because it is not text.",
		"",
		"Ask a specific question. \"What error does this show, and at which file and line?\" or \"What components does this mockup use, and how are they spaced?\" produce something you can act on; asking nothing produces a generic caption that usually is not.",
		"",
		"Accepts png, jpg, gif and webp. Text in the image is transcribed verbatim, so error messages, paths and identifiers come back exactly as written.",
		"",
		"Does not return: the image itself, pixel data, or anything about files it was not pointed at. The description is a reading by another model — treat an odd detail as possibly misread rather than as fact.",
	}, "\n")
}

func (t *imageTool) InputSchema() any { return imageArgs{} }

func (t *imageTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args imageArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if args.Path == "" {
		return failure("path is required")
	}
	if t.cfg.Describer == nil {
		return failure("no vision model is configured; start the agent with -vision-model to describe images")
	}

	abs, err := resolveWithinRoot(t.cfg.Root, args.Path)
	if err != nil {
		return failure("%v", err)
	}

	description, err := t.cfg.Describer.Describe(ctx, abs, args.Question)
	if err != nil {
		return failure("%v", err)
	}

	return agent.ToolResult{
		Content: description,
		Metadata: map[string]any{
			"ok":    true,
			"path":  abs,
			"model": t.cfg.Describer.Model(),
		},
	}, nil
}
