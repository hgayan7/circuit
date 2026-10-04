// Disposable SDK rehearsal configuration; never a production configuration tool.
package main

import (
	"flag"
	"os"

	"github.com/hgayan7/circuit/pkg/gateway"
	"gopkg.in/yaml.v3"
)

func main() {
	path := flag.String("config", "", "Disposable SDK fixture config")
	flag.Parse()
	c, err := gateway.LoadConfig(*path)
	if err != nil {
		panic(err)
	}
	for i := range c.Limits {
		if c.Limits[i].ID == "writes-per-hour" {
			c.Limits[i].MaxCalls = 10
		}
	}
	if err := yaml.NewEncoder(os.Stdout).Encode(c); err != nil {
		panic(err)
	}
}
