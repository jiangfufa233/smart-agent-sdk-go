package tool_test

import (
	"context"
	"fmt"
	"log"

	"github.com/jiangfufa233/openai-agent-sdk-go/tool"
)

// ExampleNewFunction shows the reflection-based function tool adapter: the
// input struct becomes the JSON Schema sent to the model, the `desc` tag
// becomes the parameter description.
func ExampleNewFunction() {
	weather, err := tool.NewFunction("get_weather", "Return the current weather for a city.",
		func(ctx context.Context, in struct {
			City string `json:"city" desc:"City name"`
		}) (string, error) {
			return `{"temp_c":21}`, nil
		})
	if err != nil {
		log.Fatal(err)
	}

	out, err := weather.Run(context.Background(), `{"city":"Beijing"}`)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
	// Output: {"temp_c":21}
}
