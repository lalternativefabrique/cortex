package pricing

// Published is what the endpoints this talks to charge, per million tokens.
//
// A run that cannot price itself says nothing rather than guessing, so a model
// missing here costs an unknown amount — not zero.
var Published = Table{
	// Scaleway Generative APIs, read from its published pricing page. Only
	// deepseek-v4-flash publishes a cached-input rate; the others have none,
	// so a cached token costs what a fresh one does.
	"deepseek-v4-flash-0731":              {Input: 0.40, Output: 0.80, Cached: 0.08},
	"glm-5.2":                             {Input: 1.80, Output: 5.50},
	"qwen3.5-397b-a17b":                   {Input: 0.60, Output: 3.60},
	"qwen3.6-35b-a3b":                     {Input: 0.25, Output: 1.50},
	"qwen3-235b-a22b-instruct-2507":       {Input: 0.75, Output: 2.25},
	"qwen3-coder-30b-a3b-instruct":        {Input: 0.20, Output: 0.80},
	"mistral-small-3.2-24b-instruct-2506": {Input: 0.15, Output: 0.35},
	"mistral-medium-3.5-128b":             {Input: 1.50, Output: 7.50},
	"llama-3.3-70b-instruct":              {Input: 0.90, Output: 0.90},
	"gemma-4-26b-a4b-it":                  {Input: 0.25, Output: 0.50},
	"gpt-oss-120b":                        {Input: 0.15, Output: 0.60},
	"pixtral-12b-2409":                    {Input: 0.20, Output: 0.20},

	// OVH AI Endpoints.
	"gpt-oss-20b":                      {Input: 0.04, Output: 0.15},
	"qwen3-coder-30b-a3b-instruct-ovh": {Input: 0.06, Output: 0.22},
	"qwen3-32b":                        {Input: 0.08, Output: 0.23},
	"deepseek-r1-distill-llama-70b":    {Input: 0.67, Output: 0.67},
	"mistral-nemo-instruct-2407":       {Input: 0.13, Output: 0.13},
}
