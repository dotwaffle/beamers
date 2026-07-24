//go:build ignore

package main

import (
	"log"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

func main() {
	err := entc.Generate("./schema", &gen.Config{
		Features: []gen.Feature{
			gen.FeatureExecQuery,
			gen.FeatureSnapshot,
		},
	})
	if err != nil {
		log.Fatalf("generate Ent code: %v", err)
	}
}
