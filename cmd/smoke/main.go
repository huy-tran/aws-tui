// cmd/smoke is a one-shot verification harness for the internal/aws package.
// It lists profiles discovered from ~/.aws/{config,credentials} and attempts
// to load credentials for the first profile in ap-southeast-2. Useful as a
// manual sanity check; not part of the main binary.
package main

import (
	"context"
	"fmt"

	"github.com/huy-tran/aws-tui/internal/aws"
)

func main() {
	profiles, err := aws.ListProfiles()
	if err != nil {
		fmt.Println("list profiles error:", err)
		return
	}
	for _, p := range profiles {
		fmt.Printf("%s (%s) region=%s\n", p.Name, p.Source, p.Region)
	}

	if len(profiles) > 0 {
		ctx := aws.NewContext(profiles[0].Name)
		ctx.SetRegion("ap-southeast-2")
		if err := ctx.Load(context.Background()); err != nil {
			fmt.Println("load error:", err)
		} else {
			fmt.Println("loaded OK")
		}
	}
}
