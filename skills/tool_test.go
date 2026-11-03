package skills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUseSkillListsThemAllAndOpensOne(t *testing.T) {
	set := Set{
		"seo-audit": {Name: "seo-audit", Description: "Check a site's ranking and its answers in AI engines.", Body: "1. Search each keyword.\n2. Read the top pages."},
		"review":    {Name: "review", Description: "Review a change.", Body: "Read the diff first."},
	}
	tool := NewTool(set)
	desc := tool.Description()
	if !strings.Contains(desc, "- review: Review a change.") || !strings.Contains(desc, "- seo-audit: Check a site's ranking") || strings.Contains(desc, "Read the diff") {
		t.Fatalf("the description lists names and descriptions, never bodies:\n%s", desc)
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"/SEO-Audit"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, `"seo-audit" skill`) || !strings.Contains(res.Content, "2. Read the top pages.") {
		t.Fatalf("opened:\n%s", res.Content)
	}

	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"name":"deploy"}`))
	if ok, _ := res.Metadata["ok"].(bool); ok || !strings.Contains(res.Content, "review, seo-audit") {
		t.Fatalf("an unknown skill must name the known ones: %+v", res)
	}
	if NewTool(Set{}) != nil {
		t.Fatal("no skills, no tool")
	}
}
