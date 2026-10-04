package main

import (
	"context"
	"errors"
	"fmt"

	circuitclient "github.com/hgayan7/circuit/sdk/go"
)

func main() {
	c, err := circuitclient.FromEnvironment()
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	request := func(operation string) circuitclient.ActionRequest {
		r := circuitclient.NewActionRequest(operation, map[string]interface{}{"sku": "go"})
		r.SetCustomTool("inventory")
		return *r
	}
	read, err := c.Execute(ctx, request("lookup"), "go-read")
	if err != nil || read.State != "succeeded" {
		panic(fmt.Sprint("read failed: ", err))
	}
	pending, err := c.Submit(ctx, request("reserve"), "go-write")
	if err != nil || pending.State != "pending" {
		panic(fmt.Sprint("pending failed: ", err))
	}
	write, err := c.Execute(ctx, request("reserve"), "go-write")
	if err != nil || write.Id != pending.Id {
		panic(fmt.Sprint("approval failed: ", err))
	}
	for _, test := range []struct{ operation, key, state string }{{"outside_scope", "go-denied", "denied"}, {"lose", "go-uncertain", "uncertain"}, {"lose", "go-uncertain", "uncertain"}} {
		_, err := c.Execute(ctx, request(test.operation), test.key)
		var stopped *circuitclient.ActionStopped
		if !errors.As(err, &stopped) || stopped.Action.State != test.state {
			panic(fmt.Sprint("unsafe outcome: ", err))
		}
	}
	fmt.Println("PASS Go: verified TLS, read, exact approval, denial and uncertain no-replay")
}
