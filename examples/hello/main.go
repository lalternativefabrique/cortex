package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httptest"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/client"
	"github.com/lalternativefabrique/cortex/host"
)

func main() {
	ctx := context.Background()
	provider := agent.Provider{BaseURL: "http://localhost:8000/v1", Model: "m"}
	h, err := host.New(ctx, host.Config{Agent: host.Agent{Name: "hello"}, Provider: provider, Token: "tok"})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	text, _, err := client.Say(ctx, client.Agent{URL: srv.URL, Token: "tok"}, client.Request{Text: "hello"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(text)
}
